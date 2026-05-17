package csc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (a *AdapterServer) wrapEventStream(body io.ReadCloser) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer body.Close()
		defer pw.Close()

		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		var eventName string
		var dataLines []string
		ss := &streamingState{
			blocks:       make(map[int]*blockState),
			toolUseParts: make(map[string]*toolPartMeta),
		}

		flush := func() error {
			if len(dataLines) == 0 && eventName == "" {
				return nil
			}
			joined := strings.Join(dataLines, "\n")
			var frames []sseFrame
			if strings.TrimSpace(joined) != "" {
				frames = a.adaptEventPayload(eventName, joined, ss)
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

func (a *AdapterServer) adaptEventPayload(eventName, joined string, ss *streamingState) []sseFrame {
	var payload map[string]any
	if err := json.Unmarshal([]byte(joined), &payload); err != nil {
		return []sseFrame{{event: eventName, data: joined}}
	}
	return a.adaptEventMap(eventName, payload, ss)
}

func (a *AdapterServer) adaptEventMap(eventName string, payload map[string]any, ss *streamingState) []sseFrame {
	sessionID := extractSessionID(payload)

	// Update agent from stored session agent
	if sessionID != "" {
		if agent := a.getSessionAgent(sessionID); agent != "" {
			ss.agent = agent
		}
	}

	switch eventName {
	case "session.created":
		normalizeSession(payload)
		return []sseFrame{frame("session.created", map[string]any{
			"sessionID": sessionID,
			"info":      payload,
		})}

	case "session.ready":
		normalizeSession(payload)
		if p, ok := payload["provider_id"].(string); ok && p != "" {
			ss.providerID = p
		}
		return []sseFrame{frame("session.updated", map[string]any{
			"sessionID": sessionID,
			"info":      payload,
		})}

	case "session.deleted":
		normalizeSession(payload)
		a.cleanupPendingForSession(sessionID)
		return []sseFrame{frame("session.deleted", map[string]any{
			"sessionID": sessionID,
			"info":      payload,
		})}

	case "session.stream_event":
		return adaptStreamEvent(ss, sessionID, payload)

	case "session.message":
		return a.adaptMessageEvent(sessionID, payload, ss)

	case "session.result":
		return adaptResultEvent(sessionID, payload, ss)

	case "session.control_request":
		return a.adaptControlRequestEvent(sessionID, payload)

	case "session.permission_replied":
		requestID, _ := adapterString(payload["request_id"])
		if requestID == "" {
			return nil
		}
		a.pendingPerms.Delete(requestID)
		return []sseFrame{frame("permission.replied", map[string]any{
			"sessionID": sessionID,
			"requestID": requestID,
		})}

	case "session.question_replied":
		requestID, _ := adapterString(payload["request_id"])
		if requestID == "" {
			return nil
		}
		a.pendingQs.Delete(requestID)
		return []sseFrame{frame("question.replied", map[string]any{
			"sessionID": sessionID,
			"requestID": requestID,
		})}

	default:
		typeName, _ := payload["type"].(string)
		if typeName == "" {
			typeName = eventName
		}
		if typeName == "server.heartbeat" || typeName == "server.connected" {
			return []sseFrame{frame(typeName, map[string]any{})}
		}

		if inner, _ := payload["properties"].(map[string]any); inner != nil {
			return a.adaptEventMap(typeName, inner, ss)
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

func adaptResultEvent(sessionID string, payload map[string]any, ss *streamingState) []sseFrame {
	isInterrupted, _ := payload["is_interrupted"].(bool)
	var frames []sseFrame

	if isInterrupted {
		now := time.Now().UnixMilli()
		needStepFinish := ss.active

		for toolID, meta := range ss.toolUseParts {
			frames = append(frames, frame("message.part.updated", map[string]any{
				"sessionID": sessionID,
				"part": map[string]any{
					"id":        meta.partID,
					"sessionID": sessionID,
					"messageID": meta.msgID,
					"type":      "tool",
					"callID":    toolID,
					"tool":      meta.toolName,
					"state": map[string]any{
						"status": "error",
						"error":  "Tool execution aborted",
						"input":  meta.input,
						"title":  meta.toolName,
						"time":   map[string]any{"start": now, "end": now},
					},
				},
				"time": now,
			}))
		}

		if ss.msgID != "" {
			if needStepFinish {
				frames = append(frames, frame("message.part.updated", map[string]any{
					"sessionID": sessionID,
					"part": map[string]any{
						"id":        fmt.Sprintf("prt_%d_finish", ss.partSeq),
						"sessionID": sessionID,
						"messageID": ss.msgID,
						"type":      "step-finish",
						"reason":    "stop",
					},
					"time": now,
				}))
			}
			frames = append(frames, frame("message.updated", map[string]any{
				"sessionID": sessionID,
				"info": map[string]any{
					"id":         ss.msgID,
					"sessionID":  sessionID,
					"role":       "assistant",
					"parentID":   ss.parentMsgID,
					"time":       map[string]any{"created": now, "completed": now},
					"modelID":    ss.modelID,
					"providerID": ss.providerID,
					"mode":       ss.agent,
					"agent":      ss.agent,
					"path":       map[string]any{"cwd": sessionID, "root": sessionID},
					"cost":       0,
					"tokens": map[string]any{
						"input": 0, "output": 0, "reasoning": 0,
						"cache": map[string]any{"read": 0, "write": 0},
					},
					"error": map[string]any{
						"name": "MessageAbortedError",
						"data": map[string]any{"message": "Aborted"},
					},
				},
			}))
			frames = append(frames, frame("session.error", map[string]any{
				"sessionID": sessionID,
				"error": map[string]any{
					"name": "MessageAbortedError",
					"data": map[string]any{"message": "Aborted"},
				},
			}))
		}
	}

	ss.active = false
	ss.blocks = make(map[int]*blockState)
	ss.toolUseParts = make(map[string]*toolPartMeta)
	ss.turnParentID = ""

	frames = append(frames,
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
	)
	return frames
}

func (a *AdapterServer) adaptControlRequestEvent(sessionID string, payload map[string]any) []sseFrame {
	request, _ := payload["request"].(map[string]any)
	requestID, _ := adapterString(payload["request_id"])
	if request == nil {
		request = map[string]any{}
	}
	if requestID == "" {
		return []sseFrame{frame("session.control_request", map[string]any{
			"sessionID": sessionID,
			"requestID": requestID,
			"request":   request,
		})}
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

		// Tools that do not require user approval — auto-approve by
		// sending a reply directly to the raw CSC backend, without
		// surfacing a permission.asked event to the frontend.
		noApprovalTools := map[string]bool{
			"AskUserQuestion":    true,
			"ask_question":      true,
			"request_user_input": true,
			"requestUserInput":  true,
		}
		if noApprovalTools[toolName] {
			go a.autoApprovePermission(toolUseID)

			// For question-type tools, extract questions from input,
			// store in pendingQs and emit question.asked so the
			// frontend renders the question card and can recover on refresh.
			if inputMap, ok := input.(map[string]any); ok {
				if questionsRaw, qOk := inputMap["questions"]; qOk {
					q := map[string]any{
						"id":        requestID,
						"sessionID": sessionID,
						"questions": questionsRaw,
						"tool": map[string]any{
							"callID":    toolUseID,
							"messageID": "",
						},
					}
					a.pendingQs.Store(requestID, &pendingEntry{
						sessionID: sessionID,
						createdAt: time.Now(),
						data:      q,
					})
					return []sseFrame{frame("question.asked", q)}
				}
			}

			return []sseFrame{}
		}
		patterns := extractPatterns(toolName, input)
		permissionKey := toPermissionKey(toolName)
		perm := map[string]any{
			"id":         requestID,
			"sessionID":  sessionID,
			"permission": permissionKey,
			"patterns":   patterns,
			"metadata":   map[string]any{"input": input},
			"always":     []string{},
			"tool": map[string]any{
				"messageID": "",
				"callID":    toolUseID,
			},
		}
		a.pendingPerms.Store(requestID, &pendingEntry{
			sessionID: sessionID,
			createdAt: time.Now(),
			data:      perm,
		})
		return []sseFrame{frame("permission.asked", perm)}

	case "elicitation":
		message, _ := request["message"].(string)
		serverName, _ := request["mcp_server_name"].(string)
		schema := request["requested_schema"]
		if schema == nil {
			schema = map[string]any{}
		}
		q := map[string]any{
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
		}
		a.pendingQs.Store(requestID, &pendingEntry{
			sessionID: sessionID,
			createdAt: time.Now(),
			data:      q,
		})
		return []sseFrame{frame("question.asked", q)}

	default:
		return []sseFrame{frame("session.control_request", map[string]any{
			"sessionID": sessionID,
			"requestID": requestID,
			"request":   request,
		})}
	}
}

// autoApprovePermission sends an automatic allow reply to the raw CSC
// backend for tools that don't require user approval.
func (a *AdapterServer) autoApprovePermission(toolUseID string) {
	if a.upstream == nil || toolUseID == "" {
		return
	}
	url := a.upstream.String() + "/permission/" + toolUseID + "/reply"
	body, _ := json.Marshal(map[string]any{"behavior": "allow"})
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

func extractPatterns(toolName string, input any) []string {
	m, ok := input.(map[string]any)
	if !ok {
		return []string{}
	}
	var patterns []string
	for _, key := range []string{"file_path", "path", "pattern", "glob"} {
		if v, ok := adapterString(m[key]); ok && v != "" {
			patterns = append(patterns, v)
		}
	}
	if v, ok := adapterString(m["command"]); ok && v != "" {
		patterns = append(patterns, v)
	}
	if len(patterns) == 0 {
		return []string{}
	}
	return patterns
}

func toPermissionKey(toolName string) string {
	var knownPascal = map[string]string{
		"Read": "read", "Edit": "edit", "Write": "edit",
		"Glob": "glob", "Grep": "grep", "LS": "list",
		"Bash": "bash", "PowerShell": "bash",
		"Agent": "task",
		"WebFetch": "webfetch", "WebSearch": "websearch",
		"TodoRead": "todoread", "TodoWrite": "todowrite",
	}
	if v, ok := knownPascal[toolName]; ok {
		return v
	}
	return strings.ToLower(toolName)
}
