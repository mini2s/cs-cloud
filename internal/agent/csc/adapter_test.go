package csc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// startTestUpstream creates a minimal HTTP server that acts as the "csc backend".
// It responds to /health with 200 OK and optionally handles other paths.
func startTestUpstream(t *testing.T, extraHandler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	if extraHandler != nil {
		mux.HandleFunc("/", extraHandler)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// mustGet performs a GET request and returns the response body.
func mustGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

// TestNewAdapterServer verifies the adapter starts and responds to health requests.
func TestNewAdapterServer(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}
	if adapter.URL() == "" {
		t.Fatal("adapter URL is empty")
	}
}

// TestPermissionList_Empty verifies GET /permission returns an empty array when no permissions are pending.
func TestPermissionList_Empty(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	resp := mustGet(t, adapter.URL()+"/permission")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var perms []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&perms); err != nil {
		t.Fatalf("decode /permission: %v", err)
	}
	if len(perms) != 0 {
		t.Fatalf("expected empty array, got %d items", len(perms))
	}
}

// TestQuestionList_Empty verifies GET /question returns an empty array when no questions are pending.
func TestQuestionList_Empty(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	resp := mustGet(t, adapter.URL()+"/question")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var questions []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&questions); err != nil {
		t.Fatalf("decode /question: %v", err)
	}
	if len(questions) != 0 {
		t.Fatalf("expected empty array, got %d items", len(questions))
	}
}

// TestPermissionList_AfterControlRequest verifies that after a control_request of
// subtype can_use_tool is processed, GET /permission returns the stored permission.
func TestPermissionList_AfterControlRequest(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	sessionID := "test-session-1"
	requestID := "req-001"
	payload := map[string]any{
		"session_id": sessionID,
		"request_id": requestID,
		"request": map[string]any{
			"subtype":    "can_use_tool",
			"tool_name":  "Read",
			"tool_use_id": "tool-call-1",
			"input": map[string]any{
				"file_path": "/home/test/file.go",
			},
		},
	}

	frames := adapter.adaptControlRequestEvent(sessionID, payload)
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}

	// Verify the frame type
	var parsed struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(frames[0].data), &parsed); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	if parsed.Type != "permission.asked" {
		t.Fatalf("expected permission.asked, got %s", parsed.Type)
	}

	// Now GET /permission should return 1 item
	resp := mustGet(t, adapter.URL()+"/permission")
	defer resp.Body.Close()

	var perms []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&perms); err != nil {
		t.Fatalf("decode /permission: %v", err)
	}
	if len(perms) != 1 {
		t.Fatalf("expected 1 permission, got %d", len(perms))
	}

	perm := perms[0]
	if perm["id"] != requestID {
		t.Fatalf("expected id %q, got %v", requestID, perm["id"])
	}
	if perm["sessionID"] != sessionID {
		t.Fatalf("expected sessionID %q, got %v", sessionID, perm["sessionID"])
	}
	if perm["permission"] != "read" {
		t.Fatalf("expected permission 'read', got %v", perm["permission"])
	}
	patterns, ok := perm["patterns"].([]any)
	if !ok || len(patterns) != 1 || patterns[0].(string) != "/home/test/file.go" {
		t.Fatalf("unexpected patterns: %v", perm["patterns"])
	}
}

// TestQuestionList_AfterControlRequest verifies that after a control_request of
// subtype elicitation is processed, GET /question returns the stored question.
func TestQuestionList_AfterControlRequest(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	sessionID := "test-session-2"
	requestID := "req-002"
	payload := map[string]any{
		"session_id": sessionID,
		"request_id": requestID,
		"request": map[string]any{
			"subtype":         "elicitation",
			"message":         "Which file would you like to edit?",
			"mcp_server_name": "filesystem",
			"requested_schema": map[string]any{
				"type": "string",
			},
		},
	}

	frames := adapter.adaptControlRequestEvent(sessionID, payload)
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}

	// Verify the frame type
	var parsed struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(frames[0].data), &parsed); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	if parsed.Type != "question.asked" {
		t.Fatalf("expected question.asked, got %s", parsed.Type)
	}

	// GET /question should return 1 item
	resp := mustGet(t, adapter.URL()+"/question")
	defer resp.Body.Close()

	var questions []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&questions); err != nil {
		t.Fatalf("decode /question: %v", err)
	}
	if len(questions) != 1 {
		t.Fatalf("expected 1 question, got %d", len(questions))
	}

	q := questions[0]
	if q["id"] != requestID {
		t.Fatalf("expected id %q, got %v", requestID, q["id"])
	}
	if q["sessionID"] != sessionID {
		t.Fatalf("expected sessionID %q, got %v", sessionID, q["sessionID"])
	}
}

// TestPermissionCleanedOnReply verifies that after a session.permission_replied event,
// the corresponding permission is removed from the in-memory store.
func TestPermissionCleanedOnReply(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	sessionID := "test-session-3"
	requestID := "req-003"

	// First, store a permission via control_request
	ctrlPayload := map[string]any{
		"session_id": sessionID,
		"request_id": requestID,
		"request": map[string]any{
			"subtype":    "can_use_tool",
			"tool_name":  "Edit",
			"tool_use_id": "tool-call-2",
			"input":      map[string]any{"file_path": "/test/a.go"},
		},
	}
	adapter.adaptControlRequestEvent(sessionID, ctrlPayload)

	// Verify it's stored
	resp := mustGet(t, adapter.URL()+"/permission")
	var permsBefore []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&permsBefore); err != nil {
		resp.Body.Close()
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if len(permsBefore) != 1 {
		t.Fatalf("expected 1 permission before reply, got %d", len(permsBefore))
	}

	// Simulate session.permission_replied via adaptEventMap
	replyPayload := map[string]any{
		"session_id": sessionID,
		"request_id": requestID,
	}
	adapter.adaptEventMap("session.permission_replied", replyPayload, &streamingState{
		blocks:       make(map[int]*blockState),
		toolUseParts: make(map[string]*toolPartMeta),
	})

	// Verify the permission is removed
	resp2 := mustGet(t, adapter.URL()+"/permission")
	var permsAfter []map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&permsAfter); err != nil {
		resp2.Body.Close()
		t.Fatalf("decode: %v", err)
	}
	resp2.Body.Close()
	if len(permsAfter) != 0 {
		t.Fatalf("expected 0 permissions after reply, got %d", len(permsAfter))
	}
}

// TestQuestionCleanedOnReply verifies that after a session.question_replied event,
// the corresponding question is removed from the in-memory store.
func TestQuestionCleanedOnReply(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	sessionID := "test-session-4"
	requestID := "req-004"

	// Store a question
	ctrlPayload := map[string]any{
		"session_id": sessionID,
		"request_id": requestID,
		"request": map[string]any{
			"subtype": "elicitation",
			"message": "Continue?",
		},
	}
	adapter.adaptControlRequestEvent(sessionID, ctrlPayload)

	resp := mustGet(t, adapter.URL()+"/question")
	var qsBefore []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&qsBefore); err != nil {
		resp.Body.Close()
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if len(qsBefore) != 1 {
		t.Fatalf("expected 1 question before reply, got %d", len(qsBefore))
	}

	// Simulate question_replied
	replyPayload := map[string]any{
		"session_id": sessionID,
		"request_id": requestID,
	}
	adapter.adaptEventMap("session.question_replied", replyPayload, &streamingState{
		blocks:       make(map[int]*blockState),
		toolUseParts: make(map[string]*toolPartMeta),
	})

	resp2 := mustGet(t, adapter.URL()+"/question")
	var qsAfter []map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&qsAfter); err != nil {
		resp2.Body.Close()
		t.Fatalf("decode: %v", err)
	}
	resp2.Body.Close()
	if len(qsAfter) != 0 {
		t.Fatalf("expected 0 questions after reply, got %d", len(qsAfter))
	}
}

// TestMultiplePermissionsAndQuestions verifies that multiple concurrent permission/question
// requests are all tracked and returned correctly.
func TestMultiplePermissionsAndQuestions(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	sessionID := "test-session-5"
	var wg sync.WaitGroup

	// Store 10 permissions concurrently
	for i := range 10 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			requestID := fmt.Sprintf("perm-req-%d", idx)
			payload := map[string]any{
				"session_id": sessionID,
				"request_id": requestID,
				"request": map[string]any{
					"subtype":    "can_use_tool",
					"tool_name":  "Read",
					"tool_use_id": fmt.Sprintf("tool-%d", idx),
					"input":      map[string]any{"file_path": fmt.Sprintf("/test/file%d.go", idx)},
				},
			}
			adapter.adaptControlRequestEvent(sessionID, payload)
		}(i)
	}
	wg.Wait()

	// Verify all 10 permissions are stored
	resp := mustGet(t, adapter.URL()+"/permission")
	var perms []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&perms); err != nil {
		resp.Body.Close()
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if len(perms) != 10 {
		t.Fatalf("expected 10 permissions, got %d", len(perms))
	}

	// Clean up all permissions
	for i := range 10 {
		requestID := fmt.Sprintf("perm-req-%d", i)
		replyPayload := map[string]any{
			"session_id": sessionID,
			"request_id": requestID,
		}
		adapter.adaptEventMap("session.permission_replied", replyPayload, &streamingState{
			blocks:       make(map[int]*blockState),
			toolUseParts: make(map[string]*toolPartMeta),
		})
	}

	resp = mustGet(t, adapter.URL()+"/permission")
	if err := json.NewDecoder(resp.Body).Decode(&perms); err != nil {
		resp.Body.Close()
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if len(perms) != 0 {
		t.Fatalf("expected 0 permissions after cleanup, got %d", len(perms))
	}
}

// TestControlRequest_DefaultBranchDoesNotStore checks that unknown subtypes
// are NOT stored in pendingPerms/pendingQs.
func TestControlRequest_DefaultBranchDoesNotStore(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	sessionID := "test-session-6"
	payload := map[string]any{
		"session_id": sessionID,
		"request_id": "req-unknown",
		"request": map[string]any{
			"subtype": "unknown_type",
			"message": "some data",
		},
	}

	frames := adapter.adaptControlRequestEvent(sessionID, payload)
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}

	// Verify no permissions or questions were stored
	resp := mustGet(t, adapter.URL()+"/permission")
	var perms []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&perms); err != nil {
		resp.Body.Close()
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if len(perms) != 0 {
		t.Fatalf("expected 0 permissions for unknown subtype, got %d", len(perms))
	}

	resp = mustGet(t, adapter.URL()+"/question")
	var qs []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&qs); err != nil {
		resp.Body.Close()
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if len(qs) != 0 {
		t.Fatalf("expected 0 questions for unknown subtype, got %d", len(qs))
	}
}

// TestSSEIntegration_PermissionLifecycle tests the full lifecycle of a permission
// via the SSE event stream, simulating what happens when real SSE data flows through.
func TestSSEIntegration_PermissionLifecycle(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	sessionID := "sse-session-1"
	requestID := "sse-req-1"

	// Simulate the SSE "session.control_request" event flowing through adaptEventMap
	payload := map[string]any{
		"session_id": sessionID,
		"request_id": requestID,
		"request": map[string]any{
			"subtype":    "can_use_tool",
			"tool_name":  "Bash",
			"tool_use_id": "bash-call-1",
			"input": map[string]any{
				"command": "ls -la",
			},
		},
	}

	ss := &streamingState{
		blocks:       make(map[int]*blockState),
		toolUseParts: make(map[string]*toolPartMeta),
	}
	_ = adapter.adaptEventMap("session.control_request", payload, ss)

	// Verify via HTTP
	resp := mustGet(t, adapter.URL()+"/permission")
	var perms []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&perms); err != nil {
		resp.Body.Close()
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if len(perms) != 1 {
		t.Fatalf("expected 1 permission, got %d", len(perms))
	}
	if perms[0]["permission"] != "bash" {
		t.Fatalf("expected bash permission, got %v", perms[0]["permission"])
	}

	// Now simulate the reply via SSE
	replyPayload := map[string]any{
		"session_id": sessionID,
		"request_id": requestID,
	}
	_ = adapter.adaptEventMap("session.permission_replied", replyPayload, ss)

	// Verify cleared
	resp = mustGet(t, adapter.URL()+"/permission")
	if err := json.NewDecoder(resp.Body).Decode(&perms); err != nil {
		resp.Body.Close()
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if len(perms) != 0 {
		t.Fatalf("expected 0 permissions after reply, got %d", len(perms))
	}
}

// TestQuestionFormat verifies the question body matches what the opencode SDK expects.
func TestQuestionFormat(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	sessionID := "test-session-7"
	requestID := "req-007"
	payload := map[string]any{
		"session_id": sessionID,
		"request_id": requestID,
		"request": map[string]any{
			"subtype":         "elicitation",
			"message":         "Which approach should I take?",
			"mcp_server_name": "my-server",
		},
	}

	adapter.adaptControlRequestEvent(sessionID, payload)

	resp := mustGet(t, adapter.URL()+"/question")
	var questions []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&questions); err != nil {
		resp.Body.Close()
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()

	if len(questions) != 1 {
		t.Fatalf("expected 1 question, got %d", len(questions))
	}

	q := questions[0]

	// Check nested questions array
	qs, ok := q["questions"].([]any)
	if !ok || len(qs) != 1 {
		t.Fatalf("expected questions array with 1 item, got %v", q["questions"])
	}
	firstQ := qs[0].(map[string]any)
	if firstQ["question"] != "Which approach should I take?" {
		t.Fatalf("unexpected question text: %v", firstQ["question"])
	}
	if firstQ["header"] != "my-server" {
		t.Fatalf("unexpected header: %v", firstQ["header"])
	}
	if firstQ["custom"] != true {
		t.Fatalf("expected custom=true, got %v", firstQ["custom"])
	}
	if firstQ["multiple"] != false {
		t.Fatalf("expected multiple=false, got %v", firstQ["multiple"])
	}

	// tool should be nil
	if q["tool"] != nil {
		t.Fatalf("expected tool=nil, got %v", q["tool"])
	}
}

// TestAdapterClose verifies the adapter can be shut down cleanly.
func TestAdapterClose(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := adapter.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Double close should be safe
	if err := adapter.Close(ctx); err != nil {
		t.Fatalf("double Close: %v", err)
	}
}

// TestEventMapDispatch_ControlRequest_InvalidRequest verifies that a nil request
// in the payload doesn't cause a panic.
func TestEventMapDispatch_ControlRequest_InvalidRequest(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	// Simulate a malformed control_request (nil request)
	payload := map[string]any{
		"session_id": "test-session-8",
		"request_id": "req-008",
	}

	// Should not panic
	frames := adapter.adaptControlRequestEvent("test-session-8", payload)
	if len(frames) != 1 {
		t.Fatalf("expected 1 fallback frame, got %d", len(frames))
	}
}

// TestPermissionAlwaysField verifies that the "always" field in the permission
// output is correctly set to an empty array.
func TestPermissionAlwaysField(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	sessionID := "test-session-9"
	requestID := "req-009"
	payload := map[string]any{
		"session_id": sessionID,
		"request_id": requestID,
		"request": map[string]any{
			"subtype":    "can_use_tool",
			"tool_name":  "Read",
			"tool_use_id": "tc-1",
			"input":      map[string]any{},
		},
	}

	adapter.adaptControlRequestEvent(sessionID, payload)

	resp := mustGet(t, adapter.URL()+"/permission")
	var perms []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&perms); err != nil {
		resp.Body.Close()
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()

	if len(perms) != 1 {
		t.Fatalf("expected 1 permission, got %d", len(perms))
	}

	always, ok := perms[0]["always"]
	if !ok {
		t.Fatal("expected 'always' field in permission")
	}
	alwaysArr, ok := always.([]any)
	if !ok || len(alwaysArr) != 0 {
		t.Fatalf("expected always to be empty array, got %v", always)
	}
}

// TestConcurrentReadAndWrite stresses the sync.Map under concurrent access.
func TestConcurrentReadAndWrite(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	var wg sync.WaitGroup

	// Writers: add permissions
	for i := range 20 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			requestID := fmt.Sprintf("conc-req-%d", idx)
			payload := map[string]any{
				"session_id": "conc-session",
				"request_id": requestID,
				"request": map[string]any{
					"subtype":    "can_use_tool",
					"tool_name":  "Read",
					"tool_use_id": fmt.Sprintf("tc-%d", idx),
					"input":      map[string]any{},
				},
			}
			adapter.adaptControlRequestEvent("conc-session", payload)
		}(i)
	}
	// Readers: GET /permission concurrently
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(adapter.URL() + "/permission")
			if err != nil {
				return
			}
			defer resp.Body.Close()
			var perms []map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&perms)
			// Just verify no panic, not checking exact count due to race
		}()
	}
	wg.Wait()
}

// TestPermissionMetadataInput verifies that the full "input" map is preserved in metadata.
func TestPermissionMetadataInput(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	sessionID := "test-session-10"
	requestID := "req-010"
	payload := map[string]any{
		"session_id": sessionID,
		"request_id": requestID,
		"request": map[string]any{
			"subtype":    "can_use_tool",
			"tool_name":  "Edit",
			"tool_use_id": "tc-edit",
			"input": map[string]any{
				"file_path": "/src/main.go",
				"old_string": "foo",
				"new_string": "bar",
			},
		},
	}

	adapter.adaptControlRequestEvent(sessionID, payload)

	resp := mustGet(t, adapter.URL()+"/permission")
	var perms []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&perms); err != nil {
		resp.Body.Close()
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()

	meta, ok := perms[0]["metadata"].(map[string]any)
	if !ok {
		t.Fatal("expected metadata field")
	}
	input, ok := meta["input"].(map[string]any)
	if !ok {
		t.Fatal("expected metadata.input field")
	}
	if input["file_path"] != "/src/main.go" {
		t.Fatalf("unexpected file_path: %v", input["file_path"])
	}
}

// TestStringHelpers are unit tests for the helper functions used in the package.
func TestPermissionKeyConversion(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Read", "read"},
		{"Edit", "edit"},
		{"Write", "edit"},
		{"Glob", "glob"},
		{"Grep", "grep"},
		{"LS", "list"},
		{"Bash", "bash"},
		{"PowerShell", "bash"},
		{"Agent", "task"},
		{"WebFetch", "webfetch"},
		{"WebSearch", "websearch"},
		{"TodoRead", "todoread"},
		{"TodoWrite", "todowrite"},
		{"UnknownTool", "unknowntool"},
		{"MyTool", "mytool"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := toPermissionKey(tc.input)
			if got != tc.expected {
				t.Fatalf("toPermissionKey(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestExtractPatterns(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		input     any
		expected  []string
		expectAny bool // if true, just check non-empty
	}{
		{
			name:     "file_path",
			input:    map[string]any{"file_path": "/tmp/test.go"},
			expected: []string{"/tmp/test.go"},
		},
		{
			name:     "path",
			input:    map[string]any{"path": "/tmp"},
			expected: []string{"/tmp"},
		},
		{
			name:     "pattern",
			input:    map[string]any{"pattern": "*.go"},
			expected: []string{"*.go"},
		},
		{
			name:     "glob",
			input:    map[string]any{"glob": "**/*.ts"},
			expected: []string{"**/*.ts"},
		},
		{
			name:     "command",
			input:    map[string]any{"command": "ls -la"},
			expected: []string{"ls -la"},
		},
		{
			name:     "multiple",
			input:    map[string]any{"file_path": "/a.go", "command": "go build"},
			expected: []string{"/a.go", "go build"},
		},
		{
			name:     "not a map",
			input:    "string input",
			expected: []string{},
		},
		{
			name:     "nil input",
			input:    nil,
			expected: []string{},
		},
		{
			name:     "empty values",
			input:    map[string]any{"file_path": "", "command": ""},
			expected: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractPatterns(tc.toolName, tc.input)
			if len(got) == 0 && len(tc.expected) == 0 {
				return
			}
			if len(got) != len(tc.expected) {
				t.Fatalf("extractPatterns(%q) = %v, want %v", tc.name, got, tc.expected)
			}
			for i := range got {
				if got[i] != tc.expected[i] {
					t.Fatalf("extractPatterns(%q)[%d] = %q, want %q", tc.name, i, got[i], tc.expected[i])
				}
			}
		})
	}
}

func TestAdapterString(t *testing.T) {
	tests := []struct {
		input    any
		expected string
		ok       bool
	}{
		{"hello", "hello", true},
		{"", "", false},
		{42, "", false},
		{nil, "", false},
		{false, "", false},
	}

	for _, tc := range tests {
		result, ok := adapterString(tc.input)
		if result != tc.expected || ok != tc.ok {
			t.Fatalf("adapterString(%v) = (%q, %v), want (%q, %v)",
				tc.input, result, ok, tc.expected, tc.ok)
		}
	}
}

func TestFrame(t *testing.T) {
	f := frame("test.event", map[string]any{
		"key": "value",
		"num": float64(42),
	})
	if f.data == "" {
		t.Fatal("frame data is empty")
	}
	var parsed struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(f.data), &parsed); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	if parsed.Type != "test.event" {
		t.Fatalf("expected type test.event, got %s", parsed.Type)
	}
	if parsed.Properties["key"] != "value" {
		t.Fatalf("expected key=value, got %v", parsed.Properties["key"])
	}
}

// TestAdaptEventMap_ControlRequest_UpdatesSessionAgent verifies that the
// session agent info is used when processing control_request events.
func TestAdaptEventMap_SessionReady_UpdatesAgent(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	ss := &streamingState{
		blocks:       make(map[int]*blockState),
		toolUseParts: make(map[string]*toolPartMeta),
	}

	// This tests that the method doesn't panic when called with various event types.
	payload := map[string]any{
		"id":      "test-session",
		"type":    "session",
		"details": "test",
	}

	frames := adapter.adaptEventMap("session.ready", payload, ss)
	if len(frames) == 0 {
		t.Fatal("expected at least 1 frame from session.ready")
	}
}

// TestHandlerEdgeCases ensures handlers work after adapter is partially initialized.
func TestHandlerEdgeCases(t *testing.T) {
	// NewAdapterServer doesn't connect to the upstream during creation,
	// it only starts a local HTTP listener. Verify it still works.
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}
	if adapter.URL() == "" {
		t.Fatal("expected non-empty URL")
	}
}

// TestSSERoundTripDataQuality validates that the JSON produced by the adapter
// for permission.asked events is well-formed and contains all expected fields.
func TestPermissionAskedJSONQuality(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	sessionID := "json-test-session"
	requestID := "json-req-1"
	payload := map[string]any{
		"session_id": sessionID,
		"request_id": requestID,
		"request": map[string]any{
			"subtype":    "can_use_tool",
			"tool_name":  "Edit",
			"tool_use_id": "json-tc-1",
			"input": map[string]any{
				"file_path": "/test/data.txt",
				"old_string": "before",
				"new_string": "after",
			},
		},
	}

	frames := adapter.adaptControlRequestEvent(sessionID, payload)

	// Unmarshal the frame data
	var envelope struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(frames[0].data), &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify specific fields in the SSE output
	props := envelope.Properties
	checkField := func(name string) {
		if _, exists := props[name]; !exists {
			t.Errorf("missing field %q in permission.asked properties", name)
		}
	}
	checkField("id")
	checkField("sessionID")
	checkField("permission")
	checkField("patterns")
	checkField("metadata")
	checkField("always")
	checkField("tool")

	// The tool field should have messageID and callID
	tool, ok := props["tool"].(map[string]any)
	if !ok {
		t.Fatal("tool field is not a map")
	}
	if _, exists := tool["messageID"]; !exists {
		t.Error("missing messageID in tool field")
	}
	if _, exists := tool["callID"]; !exists {
		t.Error("missing callID in tool field")
	}
}

// TestGetSetSessionAgent verifies that session agent tracking works correctly.
func TestGetSetSessionAgent(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	// Default agent should be "build"
	if agent := adapter.getSessionAgent("nonexistent"); agent != "build" {
		t.Fatalf("expected default agent 'build', got %q", agent)
	}
}

// TestNilPayloadsInEventMap tests that nil/missing expected fields don't panic.
func TestNilPayloadsInEventMap(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	ss := &streamingState{
		blocks:       make(map[int]*blockState),
		toolUseParts: make(map[string]*toolPartMeta),
	}

	testCases := []struct {
		name  string
		event string
		payload map[string]any
	}{
		{"empty session.created", "session.created", map[string]any{}},
		{"empty session.ready", "session.ready", map[string]any{}},
		{"empty session.deleted", "session.deleted", map[string]any{}},
		{"empty permission_replied", "session.permission_replied", map[string]any{}},
		{"empty question_replied", "session.question_replied", map[string]any{}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Should not panic
			frames := adapter.adaptEventMap(tc.event, tc.payload, ss)
			if frames == nil {
				t.Fatal("expected non-nil frames")
			}
		})
	}
}

// TestPendingPermissions_GetEndpointExplicitly uses the HTTP endpoint directly
// to test that the /permission handler works after Store.
func TestPendingPermissions_HTTPDirect(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	reqID := "direct-req-1"
	adapter.pendingPerms.Store(reqID, &pendingEntry{
		sessionID: "direct-session",
		createdAt: time.Now(),
		data: map[string]any{
			"id":         reqID,
			"sessionID":  "direct-session",
			"permission": "read",
			"patterns":   []string{"/test/file.go"},
		},
	})

	resp := mustGet(t, adapter.URL()+"/permission")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}

	var perms []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&perms); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(perms) != 1 {
		t.Fatalf("expected 1 permission, got %d", len(perms))
	}
	if perms[0]["id"] != reqID {
		t.Fatalf("expected id %q, got %v", reqID, perms[0]["id"])
	}
}

// TestPendingQuestions_GetEndpointExplicitly uses the HTTP endpoint directly
// to test that the /question handler works after Store.
func TestPendingQuestions_HTTPDirect(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	reqID := "direct-q-1"
	adapter.pendingQs.Store(reqID, &pendingEntry{
		sessionID: "direct-session",
		createdAt: time.Now(),
		data: map[string]any{
			"id":        reqID,
			"sessionID": "direct-session",
			"questions": []map[string]any{
				{"question": "test?", "custom": true},
			},
		},
	})

	resp := mustGet(t, adapter.URL()+"/question")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var questions []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&questions); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(questions) != 1 {
		t.Fatalf("expected 1 question, got %d", len(questions))
	}
	if questions[0]["id"] != reqID {
		t.Fatalf("expected id %q, got %v", reqID, questions[0]["id"])
	}
}

// TestTTLEviction verifies that entries older than pendingTTL are evicted
// lazily when the handler reads the map.
func TestTTLEviction(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	// Store a fresh permission
	adapter.pendingPerms.Store("fresh-perm", &pendingEntry{
		sessionID: "test-session",
		createdAt: time.Now(),
		data:      map[string]any{"id": "fresh-perm", "permission": "read"},
	})

	// Store an expired permission (created 10 minutes ago)
	adapter.pendingPerms.Store("stale-perm", &pendingEntry{
		sessionID: "test-session",
		createdAt: time.Now().Add(-10 * time.Minute),
		data:      map[string]any{"id": "stale-perm", "permission": "read"},
	})

	// Verify only the fresh one is returned
	resp := mustGet(t, adapter.URL()+"/permission")
	var perms []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&perms); err != nil {
		resp.Body.Close()
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()

	if len(perms) != 1 {
		t.Fatalf("expected 1 permission (fresh), got %d", len(perms))
	}
	if perms[0]["id"] != "fresh-perm" {
		t.Fatalf("expected 'fresh-perm', got %v", perms[0]["id"])
	}

	// Verify the stale entry was actually deleted from the map
	_, loaded := adapter.pendingPerms.Load("stale-perm")
	if loaded {
		t.Fatal("expected stale-perm to be deleted after lazy eviction")
	}
}

// TestTTLEviction_Questions verifies TTL eviction works for the /question endpoint.
func TestTTLEviction_Questions(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	adapter.pendingQs.Store("stale-q", &pendingEntry{
		sessionID: "test-session",
		createdAt: time.Now().Add(-10 * time.Minute),
		data:      map[string]any{"id": "stale-q"},
	})

	resp := mustGet(t, adapter.URL()+"/question")
	var qs []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&qs); err != nil {
		resp.Body.Close()
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()

	if len(qs) != 0 {
		t.Fatalf("expected 0 questions after TTL eviction, got %d", len(qs))
	}
}

// TestSessionDeleted_RemovesPendingEntries verifies that when a session is deleted,
// all pending permission and question entries for that session are removed.
func TestSessionDeleted_RemovesPendingEntries(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	sessionA := "session-A"
	sessionB := "session-B"

	// Add permissions for both sessions
	for _, s := range []string{sessionA, sessionB} {
		adapter.pendingPerms.Store("perm-"+s, &pendingEntry{
			sessionID: s,
			createdAt: time.Now(),
			data:      map[string]any{"id": "perm-" + s},
		})
		adapter.pendingQs.Store("q-"+s, &pendingEntry{
			sessionID: s,
			createdAt: time.Now(),
			data:      map[string]any{"id": "q-" + s},
		})
	}

	// Simulate session.deleted for sessionA
	adapter.adaptEventMap("session.deleted", map[string]any{
		"id":         sessionA,
		"session_id": sessionA,
	}, &streamingState{
		blocks:       make(map[int]*blockState),
		toolUseParts: make(map[string]*toolPartMeta),
	})

	// Session A's entries should be gone
	_, permA := adapter.pendingPerms.Load("perm-" + sessionA)
	_, qA := adapter.pendingQs.Load("q-" + sessionA)
	if permA || qA {
		t.Fatal("expected session A entries to be removed after session.deleted")
	}

	// Session B's entries should still exist
	_, permB := adapter.pendingPerms.Load("perm-" + sessionB)
	_, qB := adapter.pendingQs.Load("q-" + sessionB)
	if !permB || !qB {
		t.Fatal("expected session B entries to remain after session.deleted")
	}
}

// TestCleanupPendingForSession_NoopOnUnknownSession verifies cleanupPendingForSession
// doesn't remove entries for other sessions.
func TestCleanupPendingForSession_NoopOnUnknownSession(t *testing.T) {
	upstream := startTestUpstream(t, nil)
	adapter, err := NewAdapterServer(upstream.URL)
	if err != nil {
		t.Fatalf("NewAdapterServer: %v", err)
	}

	adapter.pendingPerms.Store("perm-1", &pendingEntry{
		sessionID: "session-1",
		createdAt: time.Now(),
		data:      map[string]any{"id": "perm-1"},
	})

	// Clean up a non-existent session
	adapter.cleanupPendingForSession("session-unknown")

	// The original entry should still be there
	_, loaded := adapter.pendingPerms.Load("perm-1")
	if !loaded {
		t.Fatal("expected entry for session-1 to remain after cleaning unknown session")
	}
}
