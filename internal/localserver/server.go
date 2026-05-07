package localserver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"cs-cloud/internal/config"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/runtime"
	"cs-cloud/internal/terminal"
)

type PrewarmTracker interface {
	MarkStarted(dir string)
	MarkCompleted(dir string, err error)
}

type prewarmState struct {
	Status     string     `json:"status"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
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

	prewarmMu   sync.Mutex
	prewarmMap  map[string]*prewarmState
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

	api.HandleFunc("GET /openapi.json", s.handleOpenAPISpec)
	api.HandleFunc("GET /docs", s.handleSwaggerUI)
	api.HandleFunc("GET /docs/", s.handleSwaggerUI)

	api.HandleFunc("GET /agents", s.handleListAgents)
	api.HandleFunc("GET /agents/health", s.handleAgentHealth)
	api.HandleFunc("GET /agents/models", s.handleProxy)
	api.HandleFunc("GET /agents/session-modes", s.handleProxy)
	api.HandleFunc("GET /agents/commands", s.handleCommands)
	api.HandleFunc("GET /agents/mcp", s.handleProxy)
	api.HandleFunc("GET /agents/lsp", s.handleProxy)

	api.HandleFunc("POST /conversations", s.handleProxy)
	api.HandleFunc("GET /conversations", s.handleProxy)
	api.HandleFunc("GET /conversations/status", s.handleProxy)
	api.HandleFunc("GET /conversations/{id}", s.handleProxy)
	api.HandleFunc("PATCH /conversations/{id}", s.handleProxy)
	api.HandleFunc("DELETE /conversations/{id}", s.handleProxy)
	api.HandleFunc("POST /conversations/{id}/prompt", s.handleProxy)
	api.HandleFunc("POST /conversations/{id}/prompt/async", s.handleProxy)
	api.HandleFunc("POST /conversations/{id}/abort", s.handleProxy)
	api.HandleFunc("GET /conversations/{id}/messages", s.handleProxy)
	api.HandleFunc("GET /conversations/{id}/todo", s.handleProxy)
	api.HandleFunc("GET /conversations/{id}/diff", s.handleConversationDiffDeprecated)
	api.HandleFunc("POST /conversations/{id}/shell", s.handleProxy)
	api.HandleFunc("POST /conversations/{id}/command", s.handleProxy)
	api.HandleFunc("POST /conversations/{id}/command/async", s.handleProxy)
	// api.HandleFunc("GET /conversations/{id}/children", s.handleProxy)
	// api.HandleFunc("POST /conversations/{id}/fork", s.handleProxy)
	api.HandleFunc("POST /conversations/{id}/revert", s.handleProxy)
	api.HandleFunc("POST /conversations/{id}/summarize", s.handleProxy)
	// api.HandleFunc("POST /conversations/{id}/unrevert", s.handleProxy)
	// api.HandleFunc("GET /config/providers", s.handleProxy)
	// api.HandleFunc("GET /file", s.handleProxy)
	// api.HandleFunc("GET /file/status", s.handleProxy)
	api.HandleFunc("GET /events", s.handleProxy)

	api.HandleFunc("GET /agents/favorites", s.handleFavoriteList)
	api.HandleFunc("POST /agents/favorites/{id}/load", s.handleFavoriteLoad)
	api.HandleFunc("POST /agents/favorites/{id}/unload", s.handleFavoriteUnload)

	api.HandleFunc("GET /permissions", s.handleProxy)
	api.HandleFunc("POST /permissions/{id}/reply", s.handleProxy)

	api.HandleFunc("GET /questions", s.handleProxy)
	api.HandleFunc("POST /questions/{id}/reply", s.handleProxy)
	api.HandleFunc("POST /questions/{id}/reject", s.handleProxy)

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
	s.url = "http://" + ln.Addr().String()
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
	} else {
		st.Status = "completed"
	}
	st.FinishedAt = &now
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

func (s *Server) prewarmDir(ctx context.Context, dir string) {
	base := s.manager.Endpoint()
	if base == "" {
		s.MarkCompleted(dir, fmt.Errorf("agent endpoint not available"))
		return
	}

	s.prewarmRequest(ctx, &http.Client{Timeout: 30 * time.Second}, base, "/session", dir)

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
