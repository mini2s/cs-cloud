package csc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func (a *AdapterServer) adaptJSON(path string, body []byte) ([]byte, bool, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return body, false, nil
	}

	switch {
	case path == "/session":
		var payload struct {
			Sessions []map[string]any `json:"sessions"`
		}
		if err := json.Unmarshal(trimmed, &payload); err == nil && payload.Sessions != nil {
			for _, session := range payload.Sessions {
				normalizeSession(session)
				a.cacheSessionModel(session)
			}
			out, err := json.Marshal(payload.Sessions)
			return out, err == nil, err
		}
		var single map[string]any
		if err := json.Unmarshal(trimmed, &single); err == nil {
			normalizeSession(single)
			a.cacheSessionModel(single)
			out, err := json.Marshal(single)
			return out, err == nil, err
		}
	case path == "/session/status":
		var payload map[string]any
		if err := json.Unmarshal(trimmed, &payload); err == nil {
			if sessions, ok := payload["sessions"].(map[string]any); ok {
				for _, raw := range sessions {
					if session, ok := raw.(map[string]any); ok {
						if status, ok := session["status"]; ok {
							session["state"] = status
						}
						if _, exists := session["type"]; !exists {
							if st, _ := session["status"].(string); st == "running" {
								session["type"] = "busy"
							} else {
								session["type"] = "idle"
							}
						}
						a.cacheSessionModel(session)
					}
				}
				out, err := json.Marshal(sessions)
				return out, err == nil, err
			}
			out, err := json.Marshal(payload)
			return out, err == nil, err
		}
	case strings.HasPrefix(path, "/session/") && !strings.HasSuffix(path, "/message") && !strings.HasSuffix(path, "/todo") && !strings.HasSuffix(path, "/tasks") && !strings.HasSuffix(path, "/diff"):
		var payload map[string]any
		if err := json.Unmarshal(trimmed, &payload); err == nil {
			normalizeSession(payload)
			a.cacheSessionModel(payload)
			out, err := json.Marshal(payload)
			return out, err == nil, err
		}
	case path == "/permission":
		var payload struct {
			Permissions []map[string]any `json:"permissions"`
		}
		if err := json.Unmarshal(trimmed, &payload); err == nil {
			for _, perm := range payload.Permissions {
				normalizePermission(perm)
			}
			out, err := json.Marshal(payload)
			return out, err == nil, err
		}
	case path == "/question":
		var payload struct {
			Questions []map[string]any `json:"questions"`
		}
		if err := json.Unmarshal(trimmed, &payload); err == nil {
			for _, q := range payload.Questions {
				normalizeQuestion(q)
			}
			out, err := json.Marshal(payload)
			return out, err == nil, err
		}
	case path == "/provider/capabilities":
		var payload map[string]any
		if err := json.Unmarshal(trimmed, &payload); err == nil {
			normalizeModelCapabilities(payload)
			out, err := json.Marshal(payload)
			return out, err == nil, err
		}
	case path == "/agent":
		var payload []map[string]any
		if err := json.Unmarshal(trimmed, &payload); err == nil {
			for _, item := range payload {
				if _, ok := item["driver"]; !ok {
					item["driver"] = "http"
				}
				if _, ok := item["available"]; !ok {
					item["available"] = true
				}
			}
			out, err := json.Marshal(payload)
			return out, err == nil, err
		}
case strings.HasSuffix(path, "/message"):
		var payload struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.Unmarshal(trimmed, &payload); err == nil {
			sessionID := extractPathSegment(path, -2)
			sessionModel, _ := a.sessionModels.Load(sessionID)

			msgRoles := make(map[string]string, len(payload.Messages))
			msgParents := make(map[string]string, len(payload.Messages))
			for _, msg := range payload.Messages {
				id, _ := adapterString(msg["uuid"])
				if id == "" {
					continue
				}
				if isLocalCommandMessage(msg) {
					continue
				}
				if isInterruptMessage(msg) {
					continue
				}
				role := msg["role"]
				if r, ok := role.(string); ok && r != "" {
					msgRoles[id] = r
				}
				if p, _ := adapterString(msg["parent_uuid"]); p != "" {
					msgParents[id] = p
				}
			}

			resolveUserParent := func(parentUUID string) string {
				visited := map[string]bool{}
				cur := parentUUID
				for cur != "" && !visited[cur] {
					visited[cur] = true
					r, ok := msgRoles[cur]
					if !ok {
						return cur
					}
					if r == "user" {
						return cur
					}
					cur = msgParents[cur]
				}
				return cur
			}

			result := make([]map[string]any, 0, len(payload.Messages))
			toolUseParts := map[string]map[string]any{}
			for _, msg := range payload.Messages {
				if isLocalCommandMessage(msg) {
					continue
				}
				if isInterruptMessage(msg) {
					markLastAssistantAborted(result)
					continue
				}
				normalizeMessage(msg, a.getSessionAgent(sessionID))
				role, _ := adapterString(msg["role"])
				parts := buildMessageParts(msg, toolUseParts)
				if role == "user" && len(parts) == 0 {
					continue
				}
				switch role {
				case "user":
					if _, exists := msg["model"]; !exists {
						if modelMap, ok := sessionModel.(map[string]string); ok {
							msg["model"] = map[string]any{
								"providerID": modelMap["providerID"],
								"modelID":    modelMap["modelID"],
							}
						}
					}
				case "assistant":
					parentUUID, _ := adapterString(msg["parent_uuid"])
					userParent := resolveUserParent(parentUUID)
					if userParent != "" {
						msg["parentID"] = userParent
						msg["parent_id"] = userParent
					}
					if _, exists := msg["modelID"]; !exists {
						if modelMap, ok := sessionModel.(map[string]string); ok {
							msg["modelID"] = modelMap["modelID"]
							msg["providerID"] = modelMap["providerID"]
						}
					}
				}
				if len(parts) == 0 {
					if _, hasErr := msg["error"]; !hasErr {
						continue
					}
				}
				result = append(result, map[string]any{
					"info":  msg,
					"parts": parts,
				})
			}
			out, err := json.Marshal(result)
			return out, err == nil, err
		}
}

return body, false, nil
}

func normalizeSession(session map[string]any) {
	if session == nil {
		return
	}
	if id, ok := adapterString(session["session_id"]); ok {
		if _, exists := session["id"]; !exists {
			session["id"] = id
		}
		if _, exists := session["sessionID"]; !exists {
			session["sessionID"] = id
		}
		if _, exists := session["slug"]; !exists {
			if len(id) >= 8 {
				session["slug"] = id[:8]
			} else {
				session["slug"] = id
			}
		}
	}
	if cwd, ok := adapterString(session["cwd"]); ok {
		if _, exists := session["directory"]; !exists {
			session["directory"] = cwd
		}
	}
	if status, ok := adapterString(session["status"]); ok {
		if _, exists := session["state"]; !exists {
			session["state"] = status
		}
	}
	if _, exists := session["time"]; !exists {
		timeObj := map[string]any{}
		if v, ok := session["created_at"]; ok {
			timeObj["created"] = v
			timeObj["updated"] = v
		}
		if v, ok := session["last_active_at"]; ok {
			timeObj["updated"] = v
		}
		if timeObj["created"] == nil {
			now := time.Now().UnixMilli()
			timeObj["created"] = now
			if timeObj["updated"] == nil {
				timeObj["updated"] = now
			}
		}
		session["time"] = timeObj
	}
	if _, exists := session["backend"]; !exists {
		session["backend"] = "csc"
	}
	if _, exists := session["driver"]; !exists {
		session["driver"] = "http"
	}
	if _, exists := session["projectID"]; !exists {
		session["projectID"] = "prj_default"
	}
	if _, exists := session["version"]; !exists {
		session["version"] = "1.0.0"
	}
	if _, exists := session["title"]; !exists {
		if createdAt, ok := session["created_at"]; ok {
			ts, ok := createdAt.(float64)
			if ok {
				session["title"] = fmt.Sprintf("New session - %s", time.UnixMilli(int64(ts)).Format(time.RFC3339))
			}
		}
	}

	if _, exists := session["slug"]; !exists {
		if id, ok := adapterString(session["id"]); ok {
			session["slug"] = id
		}
	}
	if _, exists := session["projectID"]; !exists {
		if cwd, ok := adapterString(session["cwd"]); ok {
			session["projectID"] = cwd
		} else if id, ok := adapterString(session["id"]); ok {
			session["projectID"] = id
		}
	}
	if _, exists := session["directory"]; !exists {
		if cwd, ok := adapterString(session["cwd"]); ok {
			session["directory"] = cwd
		}
	}
	if _, exists := session["version"]; !exists {
		session["version"] = "1"
	}
	if _, exists := session["time"]; !exists {
		created, _ := session["created_at"]
		updated, _ := session["updated_at"]
		if updated == nil {
			updated = created
		}
		session["time"] = map[string]any{
			"created": created,
			"updated": updated,
		}
	}
}

func normalizePermission(perm map[string]any) {
	if perm == nil {
		return
	}
	if id, ok := adapterString(perm["requestId"]); ok {
		perm["id"] = id
	}
	if callID, ok := adapterString(perm["toolUseId"]); ok {
		perm["call_id"] = callID
	} else if id, ok := adapterString(perm["requestId"]); ok {
		perm["call_id"] = id
	}
	if _, ok := perm["kind"]; !ok {
		perm["kind"] = "tool"
	}
	if _, ok := perm["options"]; !ok {
		perm["options"] = []map[string]any{
			{"option_id": "allow", "name": "Allow", "kind": "allow"},
			{"option_id": "deny", "name": "Deny", "kind": "deny"},
		}
	}
	if sessionID, ok := adapterString(perm["sessionId"]); ok {
		perm["sessionID"] = sessionID
		perm["conversation_id"] = sessionID
	}
	if toolName, ok := adapterString(perm["toolName"]); ok {
		perm["permission"] = toolName
	}
	adapterSetDefault(perm, "patterns", []string{})
	adapterSetDefault(perm, "metadata", map[string]any{})
	adapterSetDefault(perm, "always", []string{})
}

func normalizeQuestion(q map[string]any) {
	if q == nil {
		return
	}
	if id, ok := adapterString(q["requestId"]); ok {
		q["id"] = id
	}
	if sessionID, ok := adapterString(q["sessionId"]); ok {
		q["sessionID"] = sessionID
		q["conversation_id"] = sessionID
	}
	message, _ := adapterString(q["message"])
	serverName, _ := adapterString(q["mcpServerName"])
	mode, _ := adapterString(q["mode"])
	if _, exists := q["questions"]; !exists && message != "" {
		q["questions"] = []map[string]any{
			{
				"question": message,
				"header":   serverName,
				"options":  []map[string]any{},
				"multiple": mode == "form",
				"custom":   true,
			},
		}
	}
	adapterSetDefault(q, "title", message)
	adapterSetDefault(q, "tool", nil)
}

func normalizeModelCapabilities(payload map[string]any) {
	connected, ok := payload["connected"].([]any)
	if !ok {
		return
	}
	for _, providerRaw := range connected {
		provider, ok := providerRaw.(map[string]any)
		if !ok {
			continue
		}
		models, ok := provider["models"].([]any)
		if !ok {
			continue
		}
		for _, modelRaw := range models {
			model, ok := modelRaw.(map[string]any)
			if !ok {
				continue
			}
			adapterSetDefault(model, "context_window", 0)
			adapterSetDefault(model, "max_output_tokens", 0)
			adapterSetDefault(model, "supports_images", false)
			adapterSetDefault(model, "input_cost_per_million", 0)
			adapterSetDefault(model, "output_cost_per_million", 0)
		}
	}
}

func normalizeMessage(msg map[string]any, agent string) {
	if msg == nil {
		return
	}
	if agent == "" {
		agent = "build"
	}
	if id, ok := adapterString(msg["uuid"]); ok {
		msg["id"] = id
		msg["msg_id"] = id
	}
	if parent, ok := adapterString(msg["parent_uuid"]); ok {
		msg["parent_id"] = parent
		msg["parentID"] = parent
	}
	if sessionID, ok := adapterString(msg["session_id"]); ok {
		msg["sessionID"] = sessionID
	}
	if timestamp, ok := msg["timestamp"]; ok {
		msg["created_at"] = timestamp
		msg["time"] = map[string]any{"created": timestamp, "completed": timestamp}
	}
	if role, ok := adapterString(msg["role"]); ok {
		msg["role"] = role
	}
	if _, ok := msg["role"]; !ok {
		if _, isAssistant := msg["message"]; isAssistant {
			msg["role"] = "assistant"
		} else {
			msg["role"] = "user"
		}
	}

	var modelID string
	if m, ok := adapterString(msg["model"]); ok {
		modelID = m
		msg["modelID"] = m
	}
	if modelID == "" {
		if msgObj, ok := msg["message"].(map[string]any); ok {
			if m, ok := adapterString(msgObj["model"]); ok {
				modelID = m
				msg["modelID"] = m
			}
		}
	}
	providerID := extractProviderID(msg)
	if providerID == "" {
		if msgObj, ok := msg["message"].(map[string]any); ok {
			providerID = extractProviderID(msgObj)
			if providerID != "" {
				msg["providerID"] = providerID
			}
		}
	}
	if providerID != "" {
		msg["providerID"] = providerID
	}

	role, _ := adapterString(msg["role"])
	switch role {
	case "user":
		adapterSetDefault(msg, "agent", agent)
		if modelID != "" || providerID != "" {
			if _, exists := msg["model"]; exists {
				delete(msg, "model")
			}
			msg["model"] = map[string]any{
				"providerID": providerID,
				"modelID":    modelID,
			}
		}
	case "assistant":
		adapterSetDefault(msg, "agent", agent)
		adapterSetDefault(msg, "mode", agent)
		if _, exists := msg["path"]; !exists {
			sessionID, _ := adapterString(msg["sessionID"])
			msg["path"] = map[string]any{"cwd": sessionID, "root": sessionID}
		}
	}

	adapterSetDefault(msg, "cost", 0)
	adapterSetDefault(msg, "tokens", map[string]any{
		"input":     0,
		"output":    0,
		"reasoning": 0,
		"cache":     map[string]any{"read": 0, "write": 0},
	})
}

func (a *AdapterServer) cacheSessionModel(session map[string]any) {
	sessionID, _ := adapterString(session["sessionID"])
	if sessionID == "" {
		sessionID, _ = adapterString(session["id"])
	}
	if sessionID == "" {
		return
	}
	modelID, _ := adapterString(session["model"])
	providerID := extractProviderID(session)
	if modelID != "" || providerID != "" {
		a.sessionModels.Store(sessionID, map[string]string{
			"modelID":    modelID,
			"providerID": providerID,
		})
	}
}

func adapterSetDefault(m map[string]any, key string, value any) {
	if _, ok := m[key]; !ok {
		m[key] = value
	}
}

var localCommandTags = []string{
	"<local-command-stdout>",
	"<local-command-stderr>",
	"<local-command-caveat>",
	"<command-name>",
	"<command-message>",
	"<command-args>",
}

func containsLocalCommandTags(content string) bool {
	for _, tag := range localCommandTags {
		if strings.Contains(content, tag) {
			return true
		}
	}
	return false
}

func isLocalCommandMessage(msg map[string]any) bool {
	if isMeta, _ := msg["isMeta"].(bool); isMeta {
		return true
	}
	content, _ := msg["content"].(string)
	if content == "" {
		return false
	}
	return containsLocalCommandTags(content)
}

var interruptMessages = []string{
	"[Request interrupted by user]",
	"[Request interrupted by user for tool use]",
}

func isInterruptMessage(msg map[string]any) bool {
	switch v := msg["content"].(type) {
	case string:
		for _, m := range interruptMessages {
			if v == m {
				return true
			}
		}
	case []any:
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			text, _ := block["text"].(string)
			for _, m := range interruptMessages {
				if text == m {
					return true
				}
			}
		}
	}
	return false
}

func markLastAssistantAborted(result []map[string]any) {
	for i := len(result) - 1; i >= 0; i-- {
		info, ok := result[i]["info"].(map[string]any)
		if !ok {
			continue
		}
		role, _ := adapterString(info["role"])
		if role != "assistant" {
			continue
		}
		info["error"] = map[string]any{
			"name": "MessageAbortedError",
			"data": map[string]any{"message": "Interrupted"},
		}
		return
	}
}

func adapterString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok && s != ""
}

func extractSessionID(payload map[string]any) string {
	for _, key := range []string{"session_id", "sessionId", "sessionID"} {
		if v, ok := adapterString(payload[key]); ok {
			return v
		}
	}
	return ""
}

func extractPathSegment(path string, idx int) string {
	trimmed := strings.Trim(path, "/")
	segments := strings.Split(trimmed, "/")
	if idx < 0 {
		idx = len(segments) + idx
	}
	if idx < 0 || idx >= len(segments) {
		return ""
	}
	return segments[idx]
}

func extractProviderID(payload map[string]any) string {
	if s, ok := adapterString(payload["provider_id"]); ok {
		return s
	}
	return ""
}
