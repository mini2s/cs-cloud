package csc

import (
	"bufio"
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
	upstream *url.URL
	http     *http.Server
	ln       net.Listener
	url      string
	mu       sync.Mutex
	closed   bool
}

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

	proxy := httputil.NewSingleHostReverseProxy(a.upstream)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = path
		req.URL.RawPath = ""
		req.Host = a.upstream.Host
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		return adaptResponse(r.URL.Path, resp)
	}
	proxy.ServeHTTP(w, r)
}

func adaptResponse(path string, resp *http.Response) error {
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/event-stream") && path == "/event" {
		resp.Body = wrapEventStream(resp.Body)
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
	adapted, changed, err := adaptJSON(path, body)
	if err != nil || !changed {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}
	resp.Body = io.NopCloser(bytes.NewReader(adapted))
	resp.ContentLength = int64(len(adapted))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(adapted)))
	return nil
}

func adaptJSON(path string, body []byte) ([]byte, bool, error) {
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
			}
			// 前端期望直接返回数组，不是 {"sessions":[...]}
			out, err := json.Marshal(payload.Sessions)
			return out, err == nil, err
		}
		var single map[string]any
		if err := json.Unmarshal(trimmed, &single); err == nil {
			normalizeSession(single)
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
						// 前端期望 type 字段: "idle" | "busy"
						if _, exists := session["type"]; !exists {
							if st, _ := session["status"].(string); st == "running" {
								session["type"] = "busy"
							} else {
								session["type"] = "idle"
							}
						}
					}
				}
				// 前端期望扁平的 Record<string, SessionStatus>，不是 {"sessions":{...}}
				out, err := json.Marshal(sessions)
				return out, err == nil, err
			}
		}
	case strings.HasPrefix(path, "/session/") && !strings.HasSuffix(path, "/message") && !strings.HasSuffix(path, "/todo") && !strings.HasSuffix(path, "/diff"):
		var payload map[string]any
		if err := json.Unmarshal(trimmed, &payload); err == nil {
			normalizeSession(payload)
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
			// 前端期望 [{info: Message, parts: Part[]}] 格式的数组
			result := make([]map[string]any, 0, len(payload.Messages))
			for _, msg := range payload.Messages {
				normalizeMessage(msg)
				parts := buildMessageParts(msg)
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
		// 前端需要 camelCase 的 sessionID 字段
		if _, exists := session["sessionID"]; !exists {
			session["sessionID"] = id
		}
	}
	if status, ok := adapterString(session["status"]); ok {
		if _, exists := session["state"]; !exists {
			session["state"] = status
		}
	}
	if _, exists := session["backend"]; !exists {
		session["backend"] = "csc"
	}
	if _, exists := session["driver"]; !exists {
		session["driver"] = "http"
	}
	if _, exists := session["updated_at"]; !exists {
		if lastActive, ok := session["last_active_at"]; ok {
			session["updated_at"] = lastActive
		} else if createdAt, ok := session["created_at"]; ok {
			session["updated_at"] = createdAt
		}
	}

	// 补齐前端 SDK Session 类型要求的字段
	// slug: 用 id 代替
	if _, exists := session["slug"]; !exists {
		if id, ok := adapterString(session["id"]); ok {
			session["slug"] = id
		}
	}
	// projectID: 用 cwd 的 hash 或直接用 id
	if _, exists := session["projectID"]; !exists {
		if cwd, ok := adapterString(session["cwd"]); ok {
			session["projectID"] = cwd
		} else if id, ok := adapterString(session["id"]); ok {
			session["projectID"] = id
		}
	}
	// directory: 用 cwd
	if _, exists := session["directory"]; !exists {
		if cwd, ok := adapterString(session["cwd"]); ok {
			session["directory"] = cwd
		}
	}
	// version: 固定为 "1"
	if _, exists := session["version"]; !exists {
		session["version"] = "1"
	}
	// time: 前端期望 {created, updated} 结构
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
		perm["conversation_id"] = sessionID
	}
}

func normalizeQuestion(q map[string]any) {
	if q == nil {
		return
	}
	if id, ok := adapterString(q["requestId"]); ok {
		q["id"] = id
	}
	if sessionID, ok := adapterString(q["sessionId"]); ok {
		q["conversation_id"] = sessionID
	}
	if message, ok := adapterString(q["message"]); ok {
		q["title"] = message
	}
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

func normalizeMessage(msg map[string]any) {
	if msg == nil {
		return
	}
	// id
	if id, ok := adapterString(msg["uuid"]); ok {
		msg["id"] = id
		msg["msg_id"] = id
	}
	// parentID (camelCase)
	if parent, ok := adapterString(msg["parent_uuid"]); ok {
		msg["parent_id"] = parent
		msg["parentID"] = parent
	}
	// timestamp -> created_at 和 time.created
	if timestamp, ok := msg["timestamp"]; ok {
		msg["created_at"] = timestamp
		if _, exists := msg["time"]; !exists {
			msg["time"] = map[string]any{"created": timestamp}
		}
	}
	// sessionID
	if _, exists := msg["sessionID"]; !exists {
		if sid, ok := adapterString(msg["session_id"]); ok {
			msg["sessionID"] = sid
		}
	}
	// role 归一化
	role, _ := adapterString(msg["role"])
	if role == "" {
		role, _ = adapterString(msg["type"])
		msg["role"] = role
	}
	// assistant 消息补充必要字段
	if role == "assistant" {
		adapterSetDefault(msg, "parentID", "")
		adapterSetDefault(msg, "modelID", "")
		adapterSetDefault(msg, "providerID", "")
		adapterSetDefault(msg, "mode", "")
		adapterSetDefault(msg, "agent", "")
		adapterSetDefault(msg, "path", map[string]any{"cwd": "", "root": ""})
		adapterSetDefault(msg, "cost", 0)
		adapterSetDefault(msg, "tokens", map[string]any{
			"input": 0, "output": 0, "reasoning": 0,
			"total": 0, "cache": map[string]any{"read": 0, "write": 0},
		})
	}
	// user 消息补充必要字段
	if role == "user" {
		adapterSetDefault(msg, "agent", "")
		adapterSetDefault(msg, "model", map[string]any{"providerID": "", "modelID": ""})
	}
}

// buildMessageParts 将 csc message 的 content 字段转换为前端 Part 数组格式
func buildMessageParts(msg map[string]any) []map[string]any {
	id, _ := adapterString(msg["id"])
	sessionID, _ := adapterString(msg["sessionID"])
	role, _ := adapterString(msg["role"])
	parts := make([]map[string]any, 0)

	content := msg["content"]
	if content == nil {
		return parts
	}

	makePart := func(partType string, extra map[string]any) map[string]any {
		part := map[string]any{
			"id":        id + "-" + partType,
			"messageID": id,
			"sessionID": sessionID,
		}
		for k, v := range extra {
			part[k] = v
		}
		return part
	}

	switch role {
	case "user":
		// user content 通常是字符串
		switch v := content.(type) {
		case string:
			if v != "" {
				parts = append(parts, makePart("text", map[string]any{
					"type": "text",
					"text": v,
				}))
			}
		case []any:
			for i, item := range v {
				if block, ok := item.(map[string]any); ok {
					blockType, _ := adapterString(block["type"])
					switch blockType {
					case "text":
						parts = append(parts, makePart(fmt.Sprintf("text-%d", i), map[string]any{
							"type": "text",
							"text": block["text"],
						}))
					}
				}
			}
		}
	case "assistant":
		// assistant content 通常是 content blocks 数组
		blocks, ok := content.([]any)
		if !ok {
			break
		}
		for i, item := range blocks {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			blockType, _ := adapterString(block["type"])
			switch blockType {
			case "text":
				parts = append(parts, makePart(fmt.Sprintf("text-%d", i), map[string]any{
					"type": "text",
					"text": block["text"],
				}))
			case "tool_use":
				parts = append(parts, makePart(fmt.Sprintf("tool-%d", i), map[string]any{
					"type":      "tool-invocation",
					"toolName":  block["name"],
					"toolCallID": block["id"],
					"state":     "result",
					"input":     block["input"],
				}))
			case "thinking":
				parts = append(parts, makePart(fmt.Sprintf("think-%d", i), map[string]any{
					"type":     "reasoning",
					"thinking": block["thinking"],
				}))
			}
		}
	}

	return parts
}

func adapterSetDefault(m map[string]any, key string, value any) {
	if _, ok := m[key]; !ok {
		m[key] = value
	}
}

func adapterString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok && s != ""
}

func wrapEventStream(body io.ReadCloser) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer body.Close()
		defer pw.Close()

		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		var eventName string
		var dataLines []string

		flush := func() error {
			if len(dataLines) == 0 && eventName == "" {
				return nil
			}
			joined := strings.Join(dataLines, "\n")
			adapted := joined
			if strings.TrimSpace(joined) != "" {
				adapted = adaptEventPayload(eventName, joined)
			}
			if eventName != "" {
				if _, err := io.WriteString(pw, "event: "+eventName+"\n"); err != nil {
					return err
				}
			}
			if adapted != "" {
				for _, line := range strings.Split(adapted, "\n") {
					if _, err := io.WriteString(pw, "data: "+line+"\n"); err != nil {
						return err
					}
				}
			}
			_, err := io.WriteString(pw, "\n")
			eventName = ""
			dataLines = nil
			return err
		}

		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if err := flush(); err != nil {
					_ = pw.CloseWithError(err)
					return
				}
				continue
			}
			if strings.HasPrefix(line, ":") {
				if _, err := io.WriteString(pw, line+"\n"); err != nil {
					_ = pw.CloseWithError(err)
					return
				}
				continue
			}
			if strings.HasPrefix(line, "event: ") {
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
				continue
			}
			if strings.HasPrefix(line, "data: ") {
				dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
				continue
			}
		}
		if err := scanner.Err(); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if err := flush(); err != nil {
			_ = pw.CloseWithError(err)
		}
	}()
	return pr
}

func adaptEventPayload(eventName, joined string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(joined), &payload); err != nil {
		return joined
	}
	typeName, _ := payload["type"].(string)
	if typeName == "" {
		typeName = eventName
	}
	conversationID := ""
	for _, key := range []string{"conversation_id", "session_id", "sessionId"} {
		if v, ok := adapterString(payload[key]); ok {
			conversationID = v
			break
		}
	}
	msgID := ""
	for _, key := range []string{"msg_id", "message_id", "uuid", "id"} {
		if v, ok := adapterString(payload[key]); ok {
			msgID = v
			break
		}
	}
	envelope := map[string]any{
		"type":    typeName,
		"backend": "csc",
		"data":    payload,
	}
	if conversationID != "" {
		envelope["conversation_id"] = conversationID
	}
	if msgID != "" {
		envelope["msg_id"] = msgID
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return joined
	}
	return string(encoded)
}
