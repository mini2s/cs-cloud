package localserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConversationCreateConsumesPrewarmedSession(t *testing.T) {
	srv := newTestServerForInitStatus()
	dir := resolveTestDir("/tmp/prewarm-consume")
	sessionID := "warm-session-1"

	srv.MarkStarted(dir)
	srv.MarkPrewarmSession(dir, sessionID, "running")
	srv.MarkCompleted(dir, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/conversations", nil)
	req.Header.Set(workspaceDirHeader, dir)
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d, want 201, body=%s", w.Code, w.Body.String())
	}
	var payload map[string]any
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["session_id"] != sessionID {
		t.Fatalf("session_id=%v, want %s", payload["session_id"], sessionID)
	}
	if payload["prewarmed"] != true {
		t.Fatalf("prewarmed=%v, want true", payload["prewarmed"])
	}

	st := srv.GetPrewarmState(dir)
	if st == nil {
		t.Fatal("expected prewarm state")
	}
	if !st.Consumed {
		t.Fatal("expected prewarm session to be consumed")
	}
	if st.Status != "consumed" {
		t.Fatalf("status=%s, want consumed", st.Status)
	}
}

func TestPrewarmSessionCanOnlyBeConsumedOnce(t *testing.T) {
	srv := newTestServerForInitStatus()
	dir := resolveTestDir("/tmp/prewarm-consume-once")

	srv.MarkStarted(dir)
	srv.MarkPrewarmSession(dir, "warm-session-1", "running")
	srv.MarkCompleted(dir, nil)

	first := srv.ConsumePrewarmedSession(dir, 0)
	if first == nil || first.SessionID != "warm-session-1" {
		t.Fatalf("first consume=%+v", first)
	}
	second := srv.ConsumePrewarmedSession(dir, 0)
	if second != nil {
		t.Fatalf("second consume=%+v, want nil", second)
	}
}

func TestRefillPrewarmReplacesConsumedState(t *testing.T) {
	srv := newTestServerForInitStatus()
	dir := resolveTestDir("/tmp/prewarm-refill")

	srv.MarkStarted(dir)
	srv.MarkPrewarmSession(dir, "warm-session-1", "running")
	srv.MarkCompleted(dir, nil)
	if first := srv.ConsumePrewarmedSession(dir, 0); first == nil {
		t.Fatal("expected first consume")
	}

	srv.RefillPrewarm(dir)
	st := srv.GetPrewarmState(dir)
	if st == nil {
		t.Fatal("expected refill state")
	}
	if st.Status != "in_progress" {
		t.Fatalf("status=%s, want in_progress", st.Status)
	}
	if st.SessionID != "" {
		t.Fatalf("sessionID=%s, want empty before refill creates a session", st.SessionID)
	}
	if st.Consumed {
		t.Fatal("expected refill state to be unconsumed")
	}
}

func TestIsUnconsumedPrewarmSession(t *testing.T) {
	srv := newTestServerForInitStatus()
	dir := resolveTestDir("/tmp/prewarm-hidden")

	srv.MarkStarted(dir)
	srv.MarkPrewarmSession(dir, "warm-session-1", "running")
	srv.MarkCompleted(dir, nil)

	if !srv.IsUnconsumedPrewarmSession("warm-session-1") {
		t.Fatal("expected warm session to be hidden before consumption")
	}
	if consumed := srv.ConsumePrewarmedSession(dir, 0); consumed == nil {
		t.Fatal("expected consume")
	}
	if srv.IsUnconsumedPrewarmSession("warm-session-1") {
		t.Fatal("expected consumed warm session to be visible")
	}
}
