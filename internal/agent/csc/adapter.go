package csc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// pendingTTL defines how long a pending permission/question entry lives without
// receiving a corresponding reply event. This prevents memory leaks when the
// CSC backend never emits session.permission_replied / session.question_replied
// (e.g. due to dropped SSE connection or "always allow" optimisations).
const pendingTTL = 5 * time.Minute

// pendingEntry wraps the payload with metadata for TTL-based lazy cleanup.
type pendingEntry struct {
	sessionID string
	createdAt time.Time
	data      map[string]any
}

type AdapterServer struct {
	upstream      *url.URL
	http          *http.Server
	ln            net.Listener
	url           string
	mu            sync.Mutex
	closed        bool
	pendingFiles  sync.Map
	sessionModels sync.Map // sessionID -> {modelID, providerID}
	sessionAgents sync.Map // sessionID -> agentName

	// In-memory tracking of pending permission/question requests
	// so GET /permission and GET /question return correct data even
	// when the raw CSC backend does not expose these endpoints.
	// Values are *pendingEntry; entries are lazily evicted after pendingTTL
	// on read, and eagerly deleted on session.permission_replied /
	// session.question_replied / session.deleted.
	pendingPerms    sync.Map // requestID -> *pendingEntry
	pendingQs       sync.Map // requestID -> *pendingEntry
}

type sseFrame struct {
	event string
	data  string
}

type blockState struct {
	blockType string
	text      string
	toolID    string
	toolName  string
}

type toolPartMeta struct {
	partID   string
	msgID    string
	toolName string
	input    map[string]any
}

type streamingState struct {
	active       bool
	sessionID    string
	msgID        string
	parentMsgID  string
	turnParentID string
	partSeq      uint64
	stepStarted  bool
	blocks       map[int]*blockState
	toolUseParts map[string]*toolPartMeta
	modelID      string
	providerID   string
	agent        string
}

var eventSeq uint64

func NewAdapterServer(rawEndpoint string) (*AdapterServer, error) {
	targetURL, err := url.Parse(rawEndpoint)
	if err != nil {
		return nil, err
	}

	a := &AdapterServer{upstream: targetURL}
	mux := http.NewServeMux()
	mux.HandleFunc("/agents/lsp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	})
	mux.HandleFunc("/global/dispose", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/permission", a.handlePermissionList)
	mux.HandleFunc("/question", a.handleQuestionList)
	mux.HandleFunc("/", a.handleProxy)

	a.http = &http.Server{Handler: mux}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	a.ln = ln
	a.url = "http://" + ln.Addr().String()
	go func() {
		_ = a.http.Serve(ln)
	}()
	return a, nil
}

func (a *AdapterServer) URL() string { return a.url }

func (a *AdapterServer) getSessionAgent(sessionID string) string {
	if v, ok := a.sessionAgents.Load(sessionID); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return "build"
}

func (a *AdapterServer) Close(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	return a.http.Shutdown(ctx)
}

func (a *AdapterServer) handleProxy(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/global/health" {
		path = "/health"
	}

	if r.Method == http.MethodPost {
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()

		if strings.HasSuffix(path, "/prompt_async") || strings.HasSuffix(path, "/prompt") {
			sessionID := extractPathSegment(path, -2)
			if sessionID != "" {
				var parsed map[string]any
				if json.Unmarshal(body, &parsed) == nil {
					if agent, ok := parsed["agent"].(string); ok && agent != "" {
						a.sessionAgents.Store(sessionID, agent)
						if parts, ok := parsed["parts"].([]any); ok {
							hasAgentPart := false
							for _, p := range parts {
								if pm, ok := p.(map[string]any); ok {
									if pm["type"] == "agent" {
										hasAgentPart = true
										break
									}
								}
							}
							if !hasAgentPart {
								parsed["parts"] = append(parts, map[string]any{
									"type": "agent",
									"name": agent,
								})
							}
						}
						delete(parsed, "agent")
						newBody, err := json.Marshal(parsed)
						if err == nil {
							body = newBody
						}
					}
					if parts, ok := parsed["parts"].([]any); ok {
						var fileParts []map[string]any
						for _, p := range parts {
							pm, ok := p.(map[string]any)
							if !ok {
								continue
							}
							t, _ := pm["type"].(string)
							if t == "file" {
								fileParts = append(fileParts, pm)
							}
						}
						if len(fileParts) > 0 {
							a.pendingFiles.Store(sessionID, fileParts)
						}
					}
				}
			}
		} else if len(bytes.TrimSpace(body)) == 0 {
			body = []byte("{}")
		}

		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Header.Set("Content-Length", strconv.Itoa(len(body)))
		r.Header.Set("Content-Type", "application/json")
	}

	proxy := httputil.NewSingleHostReverseProxy(a.upstream)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = path
		req.URL.RawPath = ""
		req.Host = a.upstream.Host
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		return a.adaptResponse(r.URL.Path, resp)
	}
	proxy.ServeHTTP(w, r)
}

func (a *AdapterServer) adaptResponse(path string, resp *http.Response) error {
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/event-stream") && path == "/event" {
		resp.Body = a.wrapEventStream(resp.Body)
		resp.Header.Del("Content-Length")
		return nil
	}
	if !strings.Contains(contentType, "application/json") {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	adapted, changed, err := a.adaptJSON(path, body)
	if err != nil || !changed {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}
	resp.Body = io.NopCloser(bytes.NewReader(adapted))
	resp.ContentLength = int64(len(adapted))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(adapted)))
	return nil
}

func frame(typeName string, properties map[string]any) sseFrame {
	envelope := struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
	}{Type: typeName, Properties: properties}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return sseFrame{data: `{"type":"","properties":null}`}
	}
	return sseFrame{data: string(encoded)}
}

// handlePermissionList returns in-memory tracked pending permissions.
// This avoids depending on the raw CSC backend exposing a GET /permission
// endpoint which may not exist or return data in a different format.
// Returns a flat JSON array to match the opencode SDK expected format
// where response.data is PermissionRequest[].
// Performs lazy eviction: entries older than pendingTTL are removed.
func (a *AdapterServer) handlePermissionList(w http.ResponseWriter, _ *http.Request) {
	var permissions []map[string]any
	var expired []string
	now := time.Now()
	a.pendingPerms.Range(func(key, value any) bool {
		entry, ok := value.(*pendingEntry)
		if !ok {
			expired = append(expired, key.(string))
			return true
		}
		if now.After(entry.createdAt.Add(pendingTTL)) {
			expired = append(expired, key.(string))
			return true
		}
		permissions = append(permissions, entry.data)
		return true
	})
	for _, k := range expired {
		a.pendingPerms.Delete(k)
	}
	if permissions == nil {
		permissions = []map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	out, _ := json.Marshal(permissions)
	_, _ = w.Write(out)
}

// handleQuestionList returns in-memory tracked pending questions.
// Returns a flat JSON array to match the opencode SDK expected format
// where response.data is QuestionRequest[].
// Performs lazy eviction: entries older than pendingTTL are removed.
func (a *AdapterServer) handleQuestionList(w http.ResponseWriter, _ *http.Request) {
	var questions []map[string]any
	var expired []string
	now := time.Now()
	a.pendingQs.Range(func(key, value any) bool {
		entry, ok := value.(*pendingEntry)
		if !ok {
			expired = append(expired, key.(string))
			return true
		}
		if now.After(entry.createdAt.Add(pendingTTL)) {
			expired = append(expired, key.(string))
			return true
		}
		questions = append(questions, entry.data)
		return true
	})
	for _, k := range expired {
		a.pendingQs.Delete(k)
	}
	if questions == nil {
		questions = []map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	out, _ := json.Marshal(questions)
	_, _ = w.Write(out)
}

// cleanupPendingForSession removes all pending permission and question entries
// that belong to the given session ID. Called when a session is deleted.
func (a *AdapterServer) cleanupPendingForSession(sessionID string) {
	clean := func(m *sync.Map) {
		var keys []string
		m.Range(func(key, value any) bool {
			if entry, ok := value.(*pendingEntry); ok && entry.sessionID == sessionID {
				keys = append(keys, key.(string))
			}
			return true
		})
		for _, k := range keys {
			m.Delete(k)
		}
	}
	clean(&a.pendingPerms)
	clean(&a.pendingQs)
}
