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
	"strings"
	"sync"
)

type AdapterServer struct {
	upstream      *url.URL
	http          *http.Server
	ln            net.Listener
	url           string
	mu            sync.Mutex
	closed        bool
	pendingFiles  sync.Map
	sessionModels sync.Map // sessionID -> {modelID, providerID}
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

	if r.Method == http.MethodPost && (strings.HasSuffix(path, "/prompt_async") || strings.HasSuffix(path, "/prompt")) {
		sessionID := extractPathSegment(path, -2)
		if sessionID != "" {
			body, _ := io.ReadAll(r.Body)
			r.Body.Close()
			var parsed map[string]any
			if json.Unmarshal(body, &parsed) == nil {
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
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
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
