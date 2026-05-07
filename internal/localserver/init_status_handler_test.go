package localserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func resolveTestDir(dir string) string {
	abs, _ := filepath.Abs(filepath.Clean(dir))
	return abs
}

func newTestServerForInitStatus() *Server {
	return New(WithVersion("test"))
}

func TestInitStatusNoDirectory(t *testing.T) {
	srv := newTestServerForInitStatus()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/init-status", nil)
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	var resp envelope
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Errorf("ok=%v, want true", resp.OK)
	}
	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("data type=%T", resp.Data)
	}
	if _, has := data["directories"]; !has {
		t.Error("missing directories field")
	}
}

func TestInitStatusWithDirectoryQueryParam(t *testing.T) {
	srv := newTestServerForInitStatus()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/init-status?directory=.", nil)
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	var resp envelope
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("data type=%T", resp.Data)
	}
	if _, has := data["directory"]; !has {
		t.Error("missing directory field")
	}
	if _, has := data["ready"]; !has {
		t.Error("missing ready field")
	}
	if _, has := data["agent"]; !has {
		t.Error("missing agent field")
	}
	if _, has := data["prewarm"]; !has {
		t.Error("missing prewarm field")
	}
}

func TestInitStatusWithHeader(t *testing.T) {
	srv := newTestServerForInitStatus()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/init-status", nil)
	req.Header.Set("X-Workspace-Directory", ".")
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
}

func TestInitStatusPrewarmTracking(t *testing.T) {
	srv := newTestServerForInitStatus()
	dir := resolveTestDir("/tmp/test-workspace")

	srv.MarkStarted(dir)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/init-status?directory="+dir, nil)
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	var resp envelope
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]any)
	prewarm := data["prewarm"].(map[string]any)

	if prewarm["status"] != "in_progress" {
		t.Errorf("prewarm.status=%v, want in_progress", prewarm["status"])
	}
	if prewarm["started_at"] == nil || prewarm["started_at"] == "" {
		t.Error("expected started_at to be set")
	}

	srv.MarkCompleted(dir, nil)

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/init-status?directory="+dir, nil)
	w2 := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w2, req2)

	var resp2 envelope
	json.NewDecoder(w2.Body).Decode(&resp2)
	data2 := resp2.Data.(map[string]any)
	prewarm2 := data2["prewarm"].(map[string]any)

	if prewarm2["status"] != "completed" {
		t.Errorf("prewarm.status=%v, want completed", prewarm2["status"])
	}
	if prewarm2["finished_at"] == nil || prewarm2["finished_at"] == "" {
		t.Error("expected finished_at to be set")
	}
}

func TestInitStatusPrewarmFailed(t *testing.T) {
	srv := newTestServerForInitStatus()
	dir := resolveTestDir("/tmp/test-fail")

	srv.MarkStarted(dir)
	srv.MarkCompleted(dir, errors.New("connection refused"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/init-status?directory="+dir, nil)
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, req)

	var resp envelope
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]any)
	prewarm := data["prewarm"].(map[string]any)

	if prewarm["status"] != "failed" {
		t.Errorf("prewarm.status=%v, want failed", prewarm["status"])
	}
	if prewarm["error"] != "connection refused" {
		t.Errorf("prewarm.error=%v, want 'connection refused'", prewarm["error"])
	}
}

func TestInitStatusUnknownDirectory(t *testing.T) {
	srv := newTestServerForInitStatus()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/init-status?directory=/tmp/nonexistent-dir", nil)
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	var resp envelope
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]any)
	prewarm := data["prewarm"].(map[string]any)

	if prewarm["status"] != "in_progress" {
		t.Errorf("prewarm.status=%v, want in_progress for first-time dir", prewarm["status"])
	}
}

func TestInitStatusAgentNotRunning(t *testing.T) {
	srv := newTestServerForInitStatus()
	dir := resolveTestDir("/tmp/test-no-agent")

	srv.MarkCompleted(dir, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/init-status?directory="+dir, nil)
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, req)

	var resp envelope
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]any)
	agentInfo := data["agent"].(map[string]any)

	if agentInfo["state"] != "none" {
		t.Errorf("agent.state=%v, want none", agentInfo["state"])
	}
	if agentInfo["healthy"] != false {
		t.Errorf("agent.healthy=%v, want false", agentInfo["healthy"])
	}
	if data["ready"] != false {
		t.Errorf("ready=%v, want false (no agent)", data["ready"])
	}
}

func TestPrewarmTrackerConcurrent(t *testing.T) {
	srv := newTestServerForInitStatus()
	dir := resolveTestDir("/tmp/concurrent")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			srv.MarkStarted(dir)
		}
	}()

	for i := 0; i < 100; i++ {
		srv.GetPrewarmState(dir)
	}
	<-done

	st := srv.GetPrewarmState(dir)
	if st == nil {
		t.Fatal("expected state to exist")
	}
	if st.Status != "in_progress" {
		t.Errorf("status=%v, want in_progress", st.Status)
	}
}

func TestAllPrewarmStates(t *testing.T) {
	srv := newTestServerForInitStatus()
	a := resolveTestDir("/tmp/a")
	b := resolveTestDir("/tmp/b")
	srv.MarkStarted(a)
	srv.MarkCompleted(a, nil)
	srv.MarkStarted(b)

	all := srv.AllPrewarmStates()
	if len(all) != 2 {
		t.Fatalf("expected 2 states, got %d", len(all))
	}
	if all[a].Status != "completed" {
		t.Errorf("a.status=%v, want completed", all[a].Status)
	}
	if all[b].Status != "in_progress" {
		t.Errorf("b.status=%v, want in_progress", all[b].Status)
	}
}
