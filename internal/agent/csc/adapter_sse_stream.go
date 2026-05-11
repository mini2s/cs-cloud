package csc

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"
)

func adaptStreamEvent(ss *streamingState, sessionID string, payload map[string]any) []sseFrame {
	rawEvent, _ := payload["event"].(map[string]any)
	if rawEvent == nil {
		return nil
	}
	eventType, _ := rawEvent["type"].(string)
	now := time.Now().UnixMilli()

	switch eventType {
	case "message_start":
		ss.active = true
		ss.sessionID = sessionID
		ss.msgID = ""
		ss.stepStarted = false
		ss.blocks = make(map[int]*blockState)
		if msg, ok := rawEvent["message"].(map[string]any); ok {
			if m, ok := msg["model"].(string); ok {
				ss.modelID = m
			}
		}
		if p, ok := payload["provider_id"].(string); ok && p != "" {
			ss.providerID = p
		}
		return nil

	case "content_block_start":
		if !ss.active || ss.sessionID != sessionID {
			return nil
		}
		var frames []sseFrame
		if !ss.stepStarted {
			seq := atomic.AddUint64(&eventSeq, 1)
			ss.msgID = fmt.Sprintf("msg_%d", seq)
			ss.partSeq = atomic.AddUint64(&eventSeq, 1)
			if ss.turnParentID != "" {
				ss.parentMsgID = ss.turnParentID
			} else {
				parentSeq := atomic.AddUint64(&eventSeq, 1)
				ss.parentMsgID = fmt.Sprintf("msg_%d", parentSeq)
				ss.turnParentID = ss.parentMsgID
			}
			ss.stepStarted = true

			frames = append(frames,
				frame("session.status", map[string]any{
					"sessionID": sessionID,
					"status":    map[string]any{"type": "busy"},
				}),
				frame("message.updated", map[string]any{
					"sessionID": sessionID,
					"info": map[string]any{
						"id":         ss.msgID,
						"sessionID":  sessionID,
						"role":       "assistant",
						"parentID":   ss.parentMsgID,
						"time":       map[string]any{"created": now},
						"modelID":    ss.modelID,
						"providerID": ss.providerID,
						"mode":       "build",
						"agent":      "build",
						"path":       map[string]any{"cwd": sessionID, "root": sessionID},
						"cost":       0,
						"tokens": map[string]any{
							"input": 0, "output": 0, "reasoning": 0,
							"cache": map[string]any{"read": 0, "write": 0},
						},
					},
				}),
				frame("message.part.updated", map[string]any{
					"sessionID": sessionID,
					"part": map[string]any{
						"id":        fmt.Sprintf("prt_%d_start", ss.partSeq),
						"sessionID": sessionID,
						"messageID": ss.msgID,
						"type":      "step-start",
					},
					"time": now,
				}),
			)
		}
		index := 0
		if idx, ok := rawEvent["index"].(float64); ok {
			index = int(idx)
		}
		contentBlock, _ := rawEvent["content_block"].(map[string]any)
		blockType := ""
		if contentBlock != nil {
			blockType, _ = contentBlock["type"].(string)
		}
		ss.blocks[index] = &blockState{blockType: blockType}
		partID := fmt.Sprintf("prt_%d_%d", ss.partSeq, index)
		if blockType == "tool_use" && contentBlock != nil {
			toolID, _ := contentBlock["id"].(string)
			toolName := normalizeToolName(contentBlock["name"].(string))
			ss.blocks[index].toolID = toolID
			ss.blocks[index].toolName = toolName
		}
		switch blockType {
		case "text":
			frames = append(frames, frame("message.part.updated", map[string]any{
				"sessionID": sessionID,
				"part": map[string]any{
					"id":        partID,
					"sessionID": sessionID,
					"messageID": ss.msgID,
					"type":      "text",
					"text":      "",
					"time":      map[string]any{"start": now},
				},
				"time": now,
			}))
		case "thinking":
			frames = append(frames, frame("message.part.updated", map[string]any{
				"sessionID": sessionID,
				"part": map[string]any{
					"id":        partID,
					"sessionID": sessionID,
					"messageID": ss.msgID,
					"type":      "reasoning",
					"thinking":  "",
					"text":      "",
					"time":      map[string]any{"start": now},
				},
				"time": now,
			}))
		}
		return frames

	case "content_block_delta":
		if !ss.active || ss.sessionID != sessionID {
			return nil
		}
		index := 0
		if idx, ok := rawEvent["index"].(float64); ok {
			index = int(idx)
		}
		delta, _ := rawEvent["delta"].(map[string]any)
		if delta == nil {
			return nil
		}
		deltaType, _ := delta["type"].(string)
		block := ss.blocks[index]
		if block == nil {
			block = &blockState{blockType: "text"}
			ss.blocks[index] = block
		}

		switch deltaType {
		case "text_delta":
			text, _ := delta["text"].(string)
			partID := fmt.Sprintf("prt_%d_%d", ss.partSeq, index)
			return []sseFrame{frame("message.part.delta", map[string]any{
				"sessionID": sessionID,
				"messageID": ss.msgID,
				"partID":    partID,
				"field":     "text",
				"delta":     text,
			})}

		case "thinking_delta":
			thinking, _ := delta["thinking"].(string)
			partID := fmt.Sprintf("prt_%d_%d", ss.partSeq, index)
			return []sseFrame{frame("message.part.delta", map[string]any{
				"sessionID": sessionID,
				"messageID": ss.msgID,
				"partID":    partID,
				"field":     "text",
				"delta":     thinking,
			})}

		case "input_json_delta":
			partialJSON, _ := delta["partial_json"].(string)
			block.text += partialJSON
			return nil
		}
		return nil

	case "content_block_stop":
		if !ss.active || ss.sessionID != sessionID {
			return nil
		}
		index := 0
		if idx, ok := rawEvent["index"].(float64); ok {
			index = int(idx)
		}
		block := ss.blocks[index]
		if block == nil {
			return nil
		}
		var frames []sseFrame
		if block.blockType == "tool_use" {
			var input map[string]any
			if block.text != "" {
				_ = json.Unmarshal([]byte(block.text), &input)
			}
			input = normalizeToolInput(input)
			partID := fmt.Sprintf("prt_%d_%d", ss.partSeq, index)
			ss.toolUseParts[block.toolID] = &toolPartMeta{partID: partID, msgID: ss.msgID, toolName: block.toolName, input: input}
			frames = append(frames, frame("message.part.updated", map[string]any{
				"sessionID": sessionID,
				"part": map[string]any{
					"id":        partID,
					"sessionID": sessionID,
					"messageID": ss.msgID,
					"type":      "tool",
					"callID":    block.toolID,
					"tool":      block.toolName,
					"state": map[string]any{
						"status": "running",
						"input":  input,
						"title":  block.toolName,
					},
				},
				"time": now,
			}))
		}
		delete(ss.blocks, index)
		return frames

	case "message_delta", "message_stop":
		return nil
	}

	return nil
}
