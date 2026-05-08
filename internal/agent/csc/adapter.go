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
	"sync/atomic"
	"time"
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
	if sessionID, ok := adapterString(msg["session_id"]); ok {
		msg["sessionID"] = sessionID
	}
	// timestamp -> created_at 和 time.created
	if timestamp, ok := msg["timestamp"]; ok {
		msg["created_at"] = timestamp
		msg["time"] = map[string]any{"created": timestamp}
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
	adapterSetDefault(msg, "cost", 0)
	adapterSetDefault(msg, "tokens", map[string]any{
		"input":     0,
		"output":    0,
		"reasoning": 0,
		"cache":     map[string]any{"read": 0, "write": 0},
	})
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
			var frames []sseFrame
			if strings.TrimSpace(joined) != "" {
				frames = adaptEventPayload(eventName, joined)
			}
			if len(frames) == 0 {
				eventName = ""
				dataLines = nil
				return nil
			}
			for _, f := range frames {
				if f.data != "" {
					if _, err := io.WriteString(pw, "data: "+f.data+"\n"); err != nil {
						return err
					}
				}
				if _, err := io.WriteString(pw, "\n"); err != nil {
					return err
				}
			}
			eventName = ""
			dataLines = nil
			return nil
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

var eventSeq uint64

type sseFrame struct {
	event string
	data  string
}

func adaptEventPayload(eventName, joined string) []sseFrame {
	var payload map[string]any
	if err := json.Unmarshal([]byte(joined), &payload); err != nil {
		return []sseFrame{{event: eventName, data: joined}}
	}

	sessionID := extractSessionID(payload)

	switch eventName {
	case "session.created":
		normalizeSession(payload)
		return []sseFrame{frame("session.created", map[string]any{
			"sessionID": sessionID,
			"info":      payload,
		})}

	case "session.ready":
		normalizeSession(payload)
		return []sseFrame{frame("session.updated", map[string]any{
			"sessionID": sessionID,
			"info":      payload,
		})}

	case "session.deleted":
		normalizeSession(payload)
		return []sseFrame{frame("session.deleted", map[string]any{
			"sessionID": sessionID,
			"info":      payload,
		})}

	case "session.message":
		return adaptMessageEvent(sessionID, payload)

	case "session.result":
		return []sseFrame{
			frame("session.status", map[string]any{
				"sessionID": sessionID,
				"status":    map[string]any{"type": "idle"},
			}),
			frame("session.idle", map[string]any{
				"sessionID": sessionID,
			}),
			frame("session.diff", map[string]any{
				"sessionID": sessionID,
				"diff":      []any{},
			}),
		}

	case "session.control_request":
		return adaptControlRequestEvent(sessionID, payload)

	default:
		typeName, _ := payload["type"].(string)
		if typeName == "" {
			typeName = eventName
		}
		if typeName == "server.heartbeat" {
			return []sseFrame{frame("server.heartbeat", map[string]any{})}
		}
		props := map[string]any{}
		for k, v := range payload {
			props[k] = v
		}
		if sessionID != "" {
			props["sessionID"] = sessionID
		}
		return []sseFrame{frame(typeName, props)}
	}
}

func extractSessionID(payload map[string]any) string {
	for _, key := range []string{"session_id", "sessionId", "sessionID"} {
		if v, ok := adapterString(payload[key]); ok {
			return v
		}
	}
	return ""
}

func adaptMessageEvent(sessionID string, payload map[string]any) []sseFrame {
	msgType, _ := payload["type"].(string)

	if msgType == "user" {
		return adaptUserMessageEvent(sessionID, payload)
	}

	if msgType != "assistant" {
		return nil
	}

	seq := atomic.AddUint64(&eventSeq, 1)
	now := time.Now().UnixMilli()
	msgID := fmt.Sprintf("msg_%d", seq)

	var userMsgID string
	if umid, ok := adapterString(payload["parent_tool_use_id"]); ok && umid != "" {
		userMsgID = umid
	}
	if userMsgID == "" {
		userMsgID = fmt.Sprintf("msg_%d", seq-1)
	}

	info := map[string]any{
		"id":         msgID,
		"sessionID":  sessionID,
		"role":       "assistant",
		"parentID":   userMsgID,
		"time":       map[string]any{"created": now},
		"modelID":    "",
		"providerID": "anthropic",
		"mode":       "build",
		"agent":      "build",
		"path":       map[string]any{"cwd": sessionID, "root": sessionID},
		"cost":       0,
		"tokens": map[string]any{
			"input": 0, "output": 0, "reasoning": 0,
			"cache": map[string]any{"read": 0, "write": 0},
		},
	}

	var parts []sseFrame

	if msg, ok := payload["message"].(map[string]any); ok {
		if cost, ok := payload["cost_usd"]; ok {
			info["cost"] = cost
		}
		if usage, ok := payload["usage"].(map[string]any); ok {
			inTokens := usage["input_tokens"]
			outTokens := usage["output_tokens"]
			totalTokens := 0
			if in, ok := inTokens.(float64); ok {
				totalTokens += int(in)
			}
			if out, ok := outTokens.(float64); ok {
				totalTokens += int(out)
			}
			info["tokens"] = map[string]any{
				"total":     totalTokens,
				"input":     inTokens,
				"output":    outTokens,
				"reasoning": 0,
				"cache":     map[string]any{"read": 0, "write": 0},
			}
		}
		if model, ok := payload["model"].(string); ok {
			info["modelID"] = model
		}

		if content, ok := msg["content"].([]any); ok {
			partSeq := atomic.AddUint64(&eventSeq, 1)

			parts = append(parts, frame("message.part.updated", map[string]any{
				"sessionID": sessionID,
				"part": map[string]any{
					"id":        fmt.Sprintf("prt_%d_start", partSeq),
					"sessionID": sessionID,
					"messageID": msgID,
					"type":      "step-start",
				},
				"time": now,
			}))

			for i, block := range content {
				blockMap, ok := block.(map[string]any)
				if !ok {
					continue
				}
				blockType, _ := blockMap["type"].(string)
				partID := fmt.Sprintf("prt_%d_%d", partSeq, i)

				switch blockType {
				case "text":
					text, _ := blockMap["text"].(string)
					parts = append(parts, frame("message.part.updated", map[string]any{
						"sessionID": sessionID,
						"part": map[string]any{
							"id":        partID,
							"sessionID": sessionID,
							"messageID": msgID,
							"type":      "text",
							"text":      text,
							"time":      map[string]any{"start": now, "end": now},
						},
						"time": now,
					}))
				case "tool_use":
					toolName, _ := blockMap["name"].(string)
					toolID, _ := blockMap["id"].(string)
					input := blockMap["input"]
					if input == nil {
						input = map[string]any{}
					}
					parts = append(parts, frame("message.part.updated", map[string]any{
						"sessionID": sessionID,
						"part": map[string]any{
							"id":        partID,
							"sessionID": sessionID,
							"messageID": msgID,
							"type":      "tool",
							"callID":    toolID,
							"tool":      toolName,
							"state": map[string]any{
								"status": "completed",
								"input":  input,
								"title":  toolName,
							},
						},
						"time": now,
					}))
				case "thinking":
					thinking, _ := blockMap["thinking"].(string)
					parts = append(parts, frame("message.part.updated", map[string]any{
						"sessionID": sessionID,
						"part": map[string]any{
							"id":        partID,
							"sessionID": sessionID,
							"messageID": msgID,
							"type":      "reasoning",
							"text":      thinking,
							"time":      map[string]any{"start": now},
						},
						"time": now,
					}))
				}
			}

			parts = append(parts, frame("message.part.updated", map[string]any{
				"sessionID": sessionID,
				"part": map[string]any{
					"id":        fmt.Sprintf("prt_%d_finish", partSeq),
					"sessionID": sessionID,
					"messageID": msgID,
					"type":      "step-finish",
					"reason":    "stop",
					"cost":      info["cost"],
					"tokens":    info["tokens"],
				},
				"time": now,
			}))
		}
	} else if msgType == "tool_progress" {
		partSeq := atomic.AddUint64(&eventSeq, 1)
		partID := fmt.Sprintf("prt_%d_tool", partSeq)
		parts = append(parts, frame("message.part.updated", map[string]any{
			"sessionID": sessionID,
			"part": map[string]any{
				"id":        partID,
				"sessionID": sessionID,
				"messageID": msgID,
				"type":      "tool",
			},
			"time": now,
		}))
	}

	result := []sseFrame{
		frame("session.status", map[string]any{
			"sessionID": sessionID,
			"status":    map[string]any{"type": "busy"},
		}),
		frame("message.updated", map[string]any{
			"sessionID": sessionID,
			"info":      info,
		}),
	}
	result = append(result, parts...)

	infoCompleted := map[string]any{}
	for k, v := range info {
		infoCompleted[k] = v
	}
	infoCompleted["time"] = map[string]any{"created": now, "completed": now + 1}
	if _, ok := info["tokens"]; ok {
		infoCompleted["finish"] = "stop"
	}

	result = append(result, frame("message.updated", map[string]any{
		"sessionID": sessionID,
		"info":      infoCompleted,
	}))

	return result
}

func adaptUserMessageEvent(sessionID string, payload map[string]any) []sseFrame {
	seq := atomic.AddUint64(&eventSeq, 1)
	now := time.Now().UnixMilli()
	msgID := fmt.Sprintf("msg_%d", seq)

	text := ""
	if content, ok := payload["content"].(string); ok {
		text = content
	} else if msg, ok := payload["message"].(map[string]any); ok {
		if c, ok := msg["content"].(string); ok {
			text = c
		}
	}

	return []sseFrame{
		frame("message.updated", map[string]any{
			"sessionID": sessionID,
			"info": map[string]any{
				"id":        msgID,
				"sessionID": sessionID,
				"role":      "user",
				"time":      map[string]any{"created": now},
				"agent":     "build",
				"model":     map[string]any{"providerID": "anthropic", "modelID": ""},
			},
		}),
		frame("message.part.updated", map[string]any{
			"sessionID": sessionID,
			"part": map[string]any{
				"id":        fmt.Sprintf("prt_%d_0", seq),
				"type":      "text",
				"text":      text,
				"messageID": msgID,
				"sessionID": sessionID,
			},
			"time": now,
		}),
	}
}

func adaptControlRequestEvent(sessionID string, payload map[string]any) []sseFrame {
	request, _ := payload["request"].(map[string]any)
	requestID, _ := adapterString(payload["request_id"])
	if request == nil {
		request = map[string]any{}
	}
	subtype, _ := request["subtype"].(string)

	switch subtype {
	case "can_use_tool":
		toolName, _ := request["tool_name"].(string)
		toolUseID, _ := request["tool_use_id"].(string)
		input := request["input"]
		if input == nil {
			input = map[string]any{}
		}
		return []sseFrame{frame("permission.asked", map[string]any{
			"id":         requestID,
			"sessionID":  sessionID,
			"permission": toolName,
			"patterns":   []string{},
			"metadata":   map[string]any{"input": input},
			"always":     []string{},
			"tool": map[string]any{
				"messageID": "",
				"callID":    toolUseID,
			},
		})}

	case "elicitation":
		message, _ := request["message"].(string)
		serverName, _ := request["mcp_server_name"].(string)
		schema := request["requested_schema"]
		if schema == nil {
			schema = map[string]any{}
		}
		return []sseFrame{frame("question.asked", map[string]any{
			"id":        requestID,
			"sessionID": sessionID,
			"questions": []map[string]any{
				{
					"question": message,
					"header":   serverName,
					"options":  []map[string]any{},
					"multiple": false,
					"custom":   true,
				},
			},
			"tool": nil,
		})}

	default:
		return []sseFrame{frame("session.control_request", map[string]any{
			"sessionID": sessionID,
			"requestID": requestID,
			"request":   request,
		})}
	}
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
