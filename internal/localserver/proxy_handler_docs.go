package localserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"time"
)

// --- Conversations ---

// @Summary      Create conversation
// @Description  Proxies to the agent backend to create a new conversation.
// @Tags         Conversation
// @Accept       json
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations [post]
func (s *Server) handleConversationCreate(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))

	trimmed := bytes.TrimSpace(body)
	canUsePrewarm := len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}"))

	dir := getWorkspaceDir(r)
	if canUsePrewarm && dir != "" {
		if abs, err := filepath.Abs(filepath.Clean(dir)); err == nil {
			dir = abs
		}
		if st := s.ConsumePrewarmedSession(dir, 500*time.Millisecond); st != nil {
			payload := map[string]any{
				"id":             st.SessionID,
				"session_id":     st.SessionID,
				"sessionID":      st.SessionID,
				"status":         st.SessionStatus,
				"state":          st.SessionStatus,
				"directory":      dir,
				"cwd":            dir,
				"backend":        "csc",
				"driver":         "http",
				"prewarmed":      true,
				"prewarm_status": st.Status,
			}
			if payload["status"] == "" {
				payload["status"] = "starting"
				payload["state"] = "starting"
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(payload)
			go s.RefillPrewarm(dir)
			return
		}
	}

	r.Body = io.NopCloser(bytes.NewReader(body))
	s.handleProxy(w, r)
}

// @Summary      List conversations
// @Description  Proxies to the agent backend to list conversations.
// @Tags         Conversation
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations [get]
func (s *Server) handleConversationList(w http.ResponseWriter, r *http.Request) {
	endpoint := s.manager.Endpoint()
	if endpoint == "" {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "no agent backend available")
		return
	}
	target, err := url.Parse(endpoint + "/session")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "invalid backend endpoint")
		return
	}
	target.RawQuery = r.URL.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "failed to build backend request")
		return
	}
	backend := s.manager.DefaultBackend()
	if d, ok := s.manager.GetDriver(backend); ok {
		for from, to := range d.HeaderMap() {
			if v := r.Header.Get(from); v != "" {
				req.Header.Set(to, v)
			}
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "BAD_GATEWAY", err.Error())
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= http.StatusBadRequest {
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}
	var sessions []map[string]any
	if err := json.Unmarshal(body, &sessions); err != nil {
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}
	filtered := sessions[:0]
	for _, session := range sessions {
		id, _ := session["session_id"].(string)
		if id == "" {
			id, _ = session["id"].(string)
		}
		if id != "" && s.IsUnconsumedPrewarmSession(id) {
			continue
		}
		filtered = append(filtered, session)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_ = json.NewEncoder(w).Encode(filtered)
}

// @Summary      Get conversation status
// @Description  Proxies to the agent backend to get overall conversation status.
// @Tags         Conversation
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/status [get]
func (s *Server) handleConversationStatus(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Get conversation
// @Description  Proxies to the agent backend to retrieve a specific conversation.
// @Tags         Conversation
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id} [get]
func (s *Server) handleConversationGet(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Update conversation
// @Description  Proxies to the agent backend to update a specific conversation.
// @Tags         Conversation
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id} [patch]
func (s *Server) handleConversationUpdate(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Delete conversation
// @Description  Proxies to the agent backend to delete a specific conversation.
// @Tags         Conversation
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id} [delete]
func (s *Server) handleConversationDelete(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Send prompt
// @Description  Proxies to the agent backend to send a prompt to a conversation.
// @Tags         Conversation
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id}/prompt [post]
func (s *Server) handleConversationPrompt(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Send prompt asynchronously
// @Description  Proxies to the agent backend to send a prompt asynchronously to a conversation.
// @Tags         Conversation
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id}/prompt/async [post]
func (s *Server) handleConversationPromptAsync(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Abort conversation
// @Description  Proxies to the agent backend to abort an ongoing conversation.
// @Tags         Conversation
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id}/abort [post]
func (s *Server) handleConversationAbort(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Get conversation messages
// @Description  Proxies to the agent backend to retrieve messages for a conversation.
// @Tags         Conversation
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id}/messages [get]
func (s *Server) handleConversationMessages(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Get conversation todo
// @Description  Proxies to the agent backend to retrieve the todo list for a conversation.
// @Tags         Conversation
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id}/todo [get]
func (s *Server) handleConversationTodo(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Get conversation tasks
// @Description  Proxies to the agent backend to retrieve task history for a conversation.
// @Tags         Conversation
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id}/tasks [get]
func (s *Server) handleConversationTasks(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Execute shell command
// @Description  Proxies to the agent backend to execute a shell command in a conversation.
// @Tags         Conversation
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id}/shell [post]
func (s *Server) handleConversationShell(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Execute conversation command
// @Description  Proxies to the agent backend to execute a command in a conversation.
// @Tags         Conversation
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id}/command [post]
func (s *Server) handleConversationCommand(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Execute conversation command asynchronously
// @Description  Proxies to the agent backend to execute a command asynchronously in a conversation.
// @Tags         Conversation
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Conversation ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /conversations/{id}/command/async [post]
func (s *Server) handleConversationCommandAsync(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// --- Events ---
// handleEvents is implemented in handle_events.go (merged SSE: backend proxy + host events)

// --- Permissions ---

// @Summary      List pending permissions
// @Description  Proxies to the agent backend to list pending permission requests.
// @Tags         Permission
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /permissions [get]
func (s *Server) handlePermissionList(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Reply to permission
// @Description  Proxies to the agent backend to reply to a pending permission request.
// @Tags         Permission
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Permission ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /permissions/{id}/reply [post]
func (s *Server) handlePermissionReply(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// --- Questions ---

// @Summary      List pending questions
// @Description  Proxies to the agent backend to list pending question requests.
// @Tags         Question
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /questions [get]
func (s *Server) handleQuestionList(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Reply to question
// @Description  Proxies to the agent backend to reply to a pending question.
// @Tags         Question
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Question ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /questions/{id}/reply [post]
func (s *Server) handleQuestionReply(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      Reject question
// @Description  Proxies to the agent backend to reject a pending question.
// @Tags         Question
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Question ID"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /questions/{id}/reject [post]
func (s *Server) handleQuestionReject(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}
