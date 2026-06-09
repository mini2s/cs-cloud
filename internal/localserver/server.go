package localserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"cs-cloud/internal/config"
	"cs-cloud/internal/filewatcher"
	"cs-cloud/internal/gitwatcher"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/runtime"
	"cs-cloud/internal/terminal"
	"cs-cloud/internal/updater"
)

type TunnelStatus struct {
	Connected   bool       `json:"connected"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
}

type TunnelStatusProvider interface {
	TunnelStatus() TunnelStatus
}

type PrewarmTracker interface {
	MarkStarted(dir string)
	MarkCompleted(dir string, err error)
}

type prewarmState struct {
	Status        string     `json:"status"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	Error         string     `json:"error,omitempty"`
	SessionID     string     `json:"session_id,omitempty"`
	SessionStatus string     `json:"session_status,omitempty"`
	Consumed      bool       `json:"consumed,omitempty"`
}

type Server struct {
	http    *http.Server
	ln      net.Listener
	url     string
	version string

	manager    *runtime.AgentManager
	eventBus   *runtime.EventBus
	termMgr    *terminal.TerminalManager
	termH      *terminal.Handlers
	inputWsH   *terminal.InputWsHandler
	runtimeCfg config.RuntimeConfig
	cfg        *config.Config
	rootDir    string
	recentMu   sync.Mutex

	findFilesMu     sync.Mutex
	findFilesCache  map[string]*fileSearchIndex
	findFilesBuilds map[string]*fileSearchBuild

	dispatcher *CommandDispatcher

	tunnelStatus TunnelStatusProvider

	prewarmMu  sync.Mutex
	prewarmMap map[string]*prewarmState

	// Host event watchers
	fileWatcher *filewatcher.Watcher
	gitWatcher  *gitwatcher.Watcher

	updateChecker *updater.Checker
}

func New(opts ...Option) *Server {
	initStartTime()

	s := &Server{
		eventBus:   runtime.NewEventBus(),
		runtimeCfg: defaultRuntimeConfig(),
		prewarmMap: make(map[string]*prewarmState),
	}
	for _, o := range opts {
		o(s)
	}
	s.manager = runtime.NewAgentManager(s.eventBus)

	// Initialize host event watchers
	s.fileWatcher = filewatcher.New(s.eventBus)
	s.gitWatcher = gitwatcher.New(s.eventBus)

	s.termMgr = terminal.NewManager(terminal.WithConfig(s.cfg))
	s.termH = terminal.NewHandlers(s.termMgr)
	s.inputWsH = terminal.NewInputWsHandler(s.termMgr)

	mux := http.NewServeMux()
	api := http.NewServeMux()
	mux.Handle("/api/v1/", corsMiddleware(http.StripPrefix("/api/v1", api)))

	// CORS-friendly 404 for paths outside /api/v1/ (e.g. wrong baseUrl)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w.Header(), r.Header.Get("Origin"))
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	})

	api.HandleFunc("GET /runtime/health", s.handleHealth)
	api.HandleFunc("GET /runtime/config", s.handleRuntimeConfig)
	api.HandleFunc("GET /runtime/files", s.handleFileList)
	api.HandleFunc("GET /runtime/files/meta", s.handleFileMeta)
	api.HandleFunc("GET /runtime/files/content", s.handleFileContent)
	api.HandleFunc("PUT /runtime/files/content", s.handleFileWrite)
	api.HandleFunc("GET /runtime/find/file", s.handleFindFiles)
	api.HandleFunc("GET /runtime/path", s.handlePath)
	api.HandleFunc("GET /runtime/vcs", s.handleVcs)
	api.HandleFunc("GET /runtime/diff", s.handleDiff)
	api.HandleFunc("GET /runtime/diff/content", s.handleDiffContent)
	api.HandleFunc("POST /runtime/dispose", s.handleInstanceDispose)
	api.HandleFunc("GET /runtime/init-status", s.handleInitStatus)
	api.HandleFunc("GET /runtime/update/check", s.handleUpdateCheck)

	api.HandleFunc("GET /openapi.json", s.handleOpenAPISpec)
	api.HandleFunc("GET /docs", s.handleSwaggerUI)
	api.HandleFunc("GET /docs/", s.handleSwaggerUI)

	api.HandleFunc("GET /agents", s.handleListAgents)
	api.HandleFunc("GET /agents/health", s.handleAgentHealth)
	api.HandleFunc("GET /agents/models", s.handleAgentModels)
	api.HandleFunc("GET /agents/session-modes", s.handleAgentSessionModes)
	api.HandleFunc("GET /agents/commands", s.handleCommands)
	api.HandleFunc("GET /agents/mcp", s.handleAgentMCP)
	api.HandleFunc("GET /agents/lsp", s.handleAgentLSP)

	api.HandleFunc("POST /conversations", s.handleConversationCreate)
	api.HandleFunc("GET /conversations", s.handleConversationList)
	api.HandleFunc("GET /conversations/status", s.handleConversationStatus)
	api.HandleFunc("GET /conversations/{id}", s.handleConversationGet)
	api.HandleFunc("PATCH /conversations/{id}", s.handleConversationUpdate)
	api.HandleFunc("DELETE /conversations/{id}", s.handleConversationDelete)
	api.HandleFunc("POST /conversations/{id}/prompt", s.handleConversationPrompt)
	api.HandleFunc("POST /conversations/{id}/prompt/async", s.handleConversationPromptAsync)
	api.HandleFunc("POST /conversations/{id}/abort", s.handleConversationAbort)
	api.HandleFunc("GET /conversations/{id}/messages", s.handleConversationMessages)
	api.HandleFunc("GET /conversations/{id}/todo", s.handleConversationTodo)
	api.HandleFunc("GET /conversations/{id}/tasks", s.handleConversationTasks)
	api.HandleFunc("GET /conversations/{id}/diff", s.handleConversationDiffDeprecated)
	api.HandleFunc("POST /conversations/{id}/shell", s.handleConversationShell)
	api.HandleFunc("POST /conversations/{id}/command", s.handleConversationCommand)
	api.HandleFunc("POST /conversations/{id}/command/async", s.handleConversationCommandAsync)
	api.HandleFunc("POST /conversations/{id}/revert", s.handleProxy)
	api.HandleFunc("POST /conversations/{id}/summarize", s.handleProxy)

	api.HandleFunc("GET /events", s.handleEvents)

	api.HandleFunc("GET /agents/favorites", s.handleFavoriteList)
	api.HandleFunc("POST /agents/favorites/{id}/load", s.handleFavoriteLoad)
	api.HandleFunc("POST /agents/favorites/{id}/unload", s.handleFavoriteUnload)

	api.HandleFunc("GET /permissions", s.handlePermissionList)
	api.HandleFunc("POST /permissions/{id}/reply", s.handlePermissionReply)

	api.HandleFunc("GET /questions", s.handleQuestionList)
	api.HandleFunc("POST /questions/{id}/reply", s.handleQuestionReply)
	api.HandleFunc("POST /questions/{id}/reject", s.handleQuestionReject)

	api.HandleFunc("POST /terminal", s.termH.HandleCreate)
	api.HandleFunc("DELETE /terminal/{id}", s.termH.HandleKill)
	api.HandleFunc("POST /terminal/{id}/resize", s.termH.HandleResize)
	api.HandleFunc("POST /terminal/{id}/restart", s.termH.HandleRestart)
	api.HandleFunc("GET /terminal/{id}/stream", s.termH.HandleStream)
	api.HandleFunc("POST /terminal/{id}/input", s.termH.HandleInput)
	api.HandleFunc("GET /terminal/input-ws", s.inputWsH.ServeHTTP)

	api.HandleFunc("POST /commands", s.handleCommandDispatch)
	api.HandleFunc("GET /commands/status", s.handleCommandStatus)

	s.http = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

type Option func(*Server)

func WithVersion(v string) Option {
	return func(s *Server) { s.version = v }
}

func WithRuntimeConfig(cfg config.RuntimeConfig) Option {
	return func(s *Server) { s.runtimeCfg = cfg }
}

func WithConfig(cfg *config.Config) Option {
	return func(s *Server) { s.cfg = cfg }
}

func WithRootDir(dir string) Option {
	return func(s *Server) { s.rootDir = dir }
}

func (s *Server) Manager() *runtime.AgentManager {
	return s.manager
}

func (s *Server) EventBus() *runtime.EventBus {
	return s.eventBus
}

func (s *Server) Start(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.ln = ln

	// Normalize the URL: wildcard addresses like 0.0.0.0 or :: are not
	// directly usable in a browser or API client. Replace them with
	// 127.0.0.1 so the reported URL is always accessible locally.
	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		s.url = "http://" + ln.Addr().String()
	} else if host == "0.0.0.0" || host == "::" {
		s.url = "http://127.0.0.1:" + port
	} else {
		s.url = "http://" + ln.Addr().String()
	}

	// Start host event watchers
	ctx := context.Background()
	if s.fileWatcher != nil {
		if err := s.fileWatcher.Start(ctx); err != nil {
			logger.Error("Failed to start file watcher: %v", err)
		}
	}
	if s.gitWatcher != nil {
		if err := s.gitWatcher.Start(ctx, s.fileWatcher); err != nil {
			logger.Error("Failed to start git watcher: %v", err)
		}
	}

	go func() {
		_ = s.http.Serve(ln)
	}()
	return nil
}

func (s *Server) URL() string {
	return s.url
}

func (s *Server) Port() int {
	if s.ln == nil {
		return 0
	}
	return s.ln.Addr().(*net.TCPAddr).Port
}

func (s *Server) Shutdown(ctx context.Context) error {
	// Stop host event watchers
	if s.gitWatcher != nil {
		s.gitWatcher.Stop()
	}
	if s.fileWatcher != nil {
		s.fileWatcher.Stop()
	}

	s.manager.KillAll()
	s.termMgr.CloseAll()
	return s.http.Shutdown(ctx)
}

func (s *Server) TerminalManager() *terminal.TerminalManager {
	return s.termMgr
}

func (s *Server) SetDispatcher(d *CommandDispatcher) {
	s.dispatcher = d
}

func (s *Server) Dispatcher() *CommandDispatcher {
	return s.dispatcher
}

func (s *Server) SetTunnelStatusProvider(p TunnelStatusProvider) {
	s.tunnelStatus = p
}

func (s *Server) MarkStarted(dir string) {
	s.prewarmMu.Lock()
	defer s.prewarmMu.Unlock()
	now := time.Now()
	st := s.prewarmMap[dir]
	if st == nil {
		st = &prewarmState{}
		s.prewarmMap[dir] = st
	}
	st.Status = "in_progress"
	st.StartedAt = &now
	st.FinishedAt = nil
	st.Error = ""
}

func (s *Server) MarkCompleted(dir string, err error) {
	s.prewarmMu.Lock()
	defer s.prewarmMu.Unlock()
	now := time.Now()
	st := s.prewarmMap[dir]
	if st == nil {
		st = &prewarmState{}
		s.prewarmMap[dir] = st
	}
	if err != nil {
		st.Status = "failed"
		st.Error = err.Error()
	} else if st.Consumed {
		st.Status = "consumed"
	} else {
		st.Status = "completed"
	}
	st.FinishedAt = &now
}

func (s *Server) MarkPrewarmSession(dir, sessionID, sessionStatus string) {
	s.prewarmMu.Lock()
	defer s.prewarmMu.Unlock()
	st := s.prewarmMap[dir]
	if st == nil {
		st = &prewarmState{}
		s.prewarmMap[dir] = st
	}
	st.SessionID = sessionID
	st.SessionStatus = sessionStatus
}

func (s *Server) ConsumePrewarmedSession(dir string, wait time.Duration) *prewarmState {
	deadline := time.Now().Add(wait)
	for {
		s.prewarmMu.Lock()
		st := s.prewarmMap[dir]
		if st != nil && !st.Consumed && st.SessionID != "" && (st.Status == "in_progress" || st.Status == "completed") {
			st.Consumed = true
			st.Status = "consumed"
			cp := *st
			if st.StartedAt != nil {
				t := *st.StartedAt
				cp.StartedAt = &t
			}
			if st.FinishedAt != nil {
				t := *st.FinishedAt
				cp.FinishedAt = &t
			}
			s.prewarmMu.Unlock()
			return &cp
		}
		s.prewarmMu.Unlock()
		if wait <= 0 || time.Now().After(deadline) {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (s *Server) GetPrewarmState(dir string) *prewarmState {
	s.prewarmMu.Lock()
	defer s.prewarmMu.Unlock()
	st := s.prewarmMap[dir]
	if st == nil {
		return nil
	}
	cp := *st
	if st.StartedAt != nil {
		t := *st.StartedAt
		cp.StartedAt = &t
	}
	if st.FinishedAt != nil {
		t := *st.FinishedAt
		cp.FinishedAt = &t
	}
	return &cp
}

func (s *Server) TriggerPrewarmIfNeeded(dir string) {
	s.prewarmMu.Lock()
	if _, exists := s.prewarmMap[dir]; exists {
		s.prewarmMu.Unlock()
		return
	}
	now := time.Now()
	s.prewarmMap[dir] = &prewarmState{
		Status:    "in_progress",
		StartedAt: &now,
	}
	s.prewarmMu.Unlock()

	go s.prewarmDir(context.Background(), dir)
}

func (s *Server) RefillPrewarm(dir string) {
	s.prewarmMu.Lock()
	st := s.prewarmMap[dir]
	if st != nil && st.Status == "in_progress" && !st.Consumed {
		s.prewarmMu.Unlock()
		return
	}
	now := time.Now()
	s.prewarmMap[dir] = &prewarmState{
		Status:    "in_progress",
		StartedAt: &now,
	}
	s.prewarmMu.Unlock()

	go s.prewarmDir(context.Background(), dir)
}

func (s *Server) IsUnconsumedPrewarmSession(sessionID string) bool {
	s.prewarmMu.Lock()
	defer s.prewarmMu.Unlock()
	for _, st := range s.prewarmMap {
		if st.SessionID == sessionID && !st.Consumed {
			return true
		}
	}
	return false
}

func (s *Server) prewarmDir(ctx context.Context, dir string) {
	base := s.manager.Endpoint()
	if base == "" {
		s.MarkCompleted(dir, fmt.Errorf("agent endpoint not available"))
		return
	}

	cli := &http.Client{Timeout: 30 * time.Second}
	sessionID, err := s.prewarmCreateSession(ctx, cli, base, dir)
	if err != nil {
		s.MarkCompleted(dir, err)
		return
	}
	if sessionID != "" {
		s.MarkPrewarmSession(dir, sessionID, "starting")
		if status, err := s.prewarmWaitSessionReady(ctx, cli, base, dir, sessionID, 30*time.Second); err == nil {
			s.MarkPrewarmSession(dir, sessionID, status)
		} else {
			logger.Warn("prewarm session %s not ready after wait: %v", sessionID, err)
			s.MarkPrewarmSession(dir, sessionID, "starting")
		}
	}

	paths := s.manager.PrewarmPaths()
	if len(paths) == 0 {
		s.MarkCompleted(dir, nil)
		return
	}

	var wg sync.WaitGroup
	for _, path := range paths {
		path := path
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.prewarmRequest(ctx, &http.Client{Timeout: 15 * time.Second}, base, path, dir)
		}()
	}
	wg.Wait()
	s.MarkCompleted(dir, nil)
}

func (s *Server) prewarmCreateSession(ctx context.Context, cli *http.Client, base string, dir string) (string, error) {
	begin := time.Now()
	body, err := json.Marshal(map[string]string{"cwd": dir})
	if err != nil {
		return "", fmt.Errorf("prewarm create session body encode failed: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/session", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("prewarm create session request build failed: %w", err)
	}
	if hdr := s.manager.WorkspaceHeaderName(); hdr != "" {
		req.Header.Set(hdr, dir)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := cli.Do(req)
	if err != nil {
		return "", fmt.Errorf("prewarm create session failed after %s: %w", time.Since(begin), err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("prewarm create session returned %d: %s", resp.StatusCode, string(respBody))
	}
	var payload map[string]any
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return "", fmt.Errorf("prewarm create session decode failed: %w", err)
	}
	sessionID, _ := payload["session_id"].(string)
	if sessionID == "" {
		sessionID, _ = payload["id"].(string)
	}
	if sessionID == "" {
		return "", fmt.Errorf("prewarm create session response missing session_id")
	}
	logger.Info("prewarm session created %s in %s", sessionID, time.Since(begin))
	return sessionID, nil
}

func (s *Server) prewarmWaitSessionReady(ctx context.Context, cli *http.Client, base, dir, sessionID string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		status, err := s.prewarmGetSessionStatus(ctx, cli, base, dir, sessionID)
		if err == nil && status == "running" {
			return status, nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return status, err
			}
			return status, fmt.Errorf("session status is %q", status)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (s *Server) prewarmGetSessionStatus(ctx context.Context, cli *http.Client, base, dir, sessionID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/session/"+sessionID, nil)
	if err != nil {
		return "", err
	}
	if hdr := s.manager.WorkspaceHeaderName(); hdr != "" {
		req.Header.Set(hdr, dir)
	}
	resp, err := cli.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("status returned %d: %s", resp.StatusCode, string(respBody))
	}
	var payload map[string]any
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return "", err
	}
	status, _ := payload["status"].(string)
	if status == "" {
		status, _ = payload["state"].(string)
	}
	return status, nil
}

func (s *Server) prewarmRequest(ctx context.Context, cli *http.Client, base string, path string, dir string) {
	begin := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		logger.Warn("prewarm request build failed (%s): %v", path, err)
		return
	}
	if hdr := s.manager.WorkspaceHeaderName(); hdr != "" {
		req.Header.Set(hdr, dir)
	}

	resp, err := cli.Do(req)
	if err != nil {
		logger.Warn("prewarm request failed (%s) after %s: %v", path, time.Since(begin), err)
		return
	}
	resp.Body.Close()

	cost := time.Since(begin)
	if resp.StatusCode >= http.StatusBadRequest {
		logger.Warn("prewarm request returned %d (%s) in %s", resp.StatusCode, path, cost)
		return
	}
	logger.Info("prewarm request ok (%s) in %s", path, cost)
}

func (s *Server) AllPrewarmStates() map[string]*prewarmState {
	s.prewarmMu.Lock()
	defer s.prewarmMu.Unlock()
	out := make(map[string]*prewarmState, len(s.prewarmMap))
	for dir, st := range s.prewarmMap {
		cp := *st
		if st.StartedAt != nil {
			t := *st.StartedAt
			cp.StartedAt = &t
		}
		if st.FinishedAt != nil {
			t := *st.FinishedAt
			cp.FinishedAt = &t
		}
		out[dir] = &cp
	}
	return out
}

// WatchDirectory starts watching a directory for file system changes
func (s *Server) WatchDirectory(dir string) error {
	if s.fileWatcher == nil {
		return fmt.Errorf("file watcher not initialized")
	}
	return s.fileWatcher.AddDirectory(dir)
}

// UnwatchDirectory stops watching a directory
func (s *Server) UnwatchDirectory(dir string) error {
	if s.fileWatcher == nil {
		return fmt.Errorf("file watcher not initialized")
	}
	return s.fileWatcher.RemoveDirectory(dir)
}

// WatchGitRepo starts watching a git repository
func (s *Server) WatchGitRepo(repoPath string) error {
	if s.gitWatcher == nil {
		return fmt.Errorf("git watcher not initialized")
	}
	return s.gitWatcher.AddRepository(repoPath)
}

// UnwatchGitRepo stops watching a git repository
func (s *Server) UnwatchGitRepo(repoPath string) error {
	if s.gitWatcher == nil {
		return fmt.Errorf("git watcher not initialized")
	}
	return s.gitWatcher.RemoveRepository(repoPath)
}

// WatchingDirectories returns the list of directories being watched
func (s *Server) WatchingDirectories() []string {
	if s.fileWatcher == nil {
		return nil
	}
	return s.fileWatcher.WatchingDirectories()
}

// WatchingGitRepos returns the list of git repositories being watched
func (s *Server) WatchingGitRepos() []string {
	if s.gitWatcher == nil {
		return nil
	}
	return s.gitWatcher.WatchingRepositories()
}
