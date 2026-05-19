package csc

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

func buildMessageParts(msg map[string]any, toolUseParts map[string]map[string]any) []map[string]any {
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
		var userText string
		switch v := content.(type) {
		case string:
			if v != "" {
				parts = append(parts, makePart("text", map[string]any{
					"type": "text",
					"text": v,
				}))
				userText = v
			}
		case []any:
			for i, item := range v {
				if block, ok := item.(map[string]any); ok {
					blockType, _ := adapterString(block["type"])
					switch blockType {
					case "text":
						text, _ := block["text"].(string)
						if strings.TrimSpace(text) == "" {
							continue
						}
						parts = append(parts, makePart(fmt.Sprintf("text-%d", i), map[string]any{
							"type": "text",
							"text": text,
						}))
						userText = text
					case "tool_result":
						toolUseID, _ := block["tool_use_id"].(string)
						if toolUseID == "" {
							continue
						}
						toolPart, tracked := toolUseParts[toolUseID]
						if !tracked {
							continue
						}
						state, _ := toolPart["state"].(map[string]any)
						if state == nil {
							state = map[string]any{}
							toolPart["state"] = state
						}
						output := extractToolResultContent(block)
						isError, _ := block["is_error"].(bool)
						if isError {
							state["status"] = "error"
							state["error"] = output
						} else {
							state["status"] = "completed"
							state["output"] = output
							state["metadata"] = map[string]any{"output": output}
						}
					}
				}
			}
		}
		if userText != "" {
			for i, ap := range buildAgentPartsFromText(userText, id, sessionID, 0) {
				ap["id"] = fmt.Sprintf("%s-agent-%d", id, i)
				parts = append(parts, ap)
			}
		}
	case "assistant":
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
				text, _ := block["text"].(string)
				if strings.TrimSpace(text) == "" {
					continue
				}
				parts = append(parts, makePart(fmt.Sprintf("text-%d", i), map[string]any{
					"type": "text",
					"text": text,
				}))
			case "tool_use":
				toolID, _ := block["id"].(string)
				toolName := normalizeToolName(block["name"].(string))
				inputMap, _ := block["input"].(map[string]any)
				inputMap = normalizeToolInput(inputMap)
				title := toolName
				if desc, ok := inputMap["description"].(string); ok && desc != "" {
					title = desc
				}
				metadata := map[string]any{}
				toolLower := strings.ToLower(toolName)
				if toolLower == "edit" || toolLower == "fileedittool" {
					if filediff := buildFileDiff(inputMap); filediff != nil {
						metadata["filediff"] = filediff
					}
				}
			toolTime := extractMessageTime(msg)
			state := map[string]any{
				"status":   "completed",
				"input":    inputMap,
				"title":    title,
				"metadata": metadata,
				"time":     map[string]any{"start": toolTime, "end": toolTime},
			}
			part := makePart(fmt.Sprintf("tool-%d", i), map[string]any{
					"type":   "tool",
					"callID": toolID,
					"tool":   toolName,
					"state":  state,
				})
				parts = append(parts, part)
				if toolID != "" {
					toolUseParts[toolID] = part
				}
				if toolLower == "task" || toolLower == "agent" {
					subagentType, _ := inputMap["subagent_type"].(string)
					if subagentType == "" {
						subagentType = "general-purpose"
					}
					desc, _ := inputMap["description"].(string)
					prompt, _ := inputMap["prompt"].(string)
					if prompt == "" {
						prompt = desc
					}
					parts = append(parts, makePart(fmt.Sprintf("subtask-%d", i), map[string]any{
						"type":        "subtask",
						"prompt":      prompt,
						"description": desc,
						"agent":       subagentType,
					}))
				}
			case "thinking":
				thinking, _ := block["thinking"].(string)
				if strings.TrimSpace(thinking) == "" {
					continue
				}
				msgTime := extractMessageTime(msg)
				parts = append(parts, makePart(fmt.Sprintf("think-%d", i), map[string]any{
					"type":     "reasoning",
					"thinking": thinking,
					"text":     thinking,
					"time":     map[string]any{"start": msgTime, "end": msgTime},
				}))
			}
		}
	}

	return parts
}

func normalizeToolInput(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	renames := map[string]string{
		"file_path":   "filePath",
		"old_string":  "oldString",
		"new_string":  "newString",
		"replace_all": "replaceAll",
	}
	for old, new := range renames {
		if v, ok := input[old]; ok {
			delete(input, old)
			input[new] = v
		}
	}
	return input
}

func normalizeToolName(name string) string {
	m := map[string]string{
		"Agent":     "task",
		"Task":      "task",
		"Bash":      "bash",
		"Read":      "read",
		"Edit":      "edit",
		"Write":     "write",
		"Glob":      "glob",
		"Grep":      "grep",
		"LS":        "list",
		"WebFetch":  "webfetch",
		"WebSearch": "websearch",
	}
	if v, ok := m[name]; ok {
		return v
	}
	return name
}

func toolCompletedState(meta *toolPartMeta, output string, isError bool) map[string]any {
	state := map[string]any{
		"status": "completed",
		"input":  meta.input,
		"title":  meta.toolName,
		"output": output,
		"metadata": map[string]any{
			"output": output,
		},
	}
	if desc, ok := meta.input["description"].(string); ok && desc != "" {
		state["title"] = desc
	}
	if isError {
		state["status"] = "error"
		state["error"] = output
		delete(state, "output")
	}
	toolLower := strings.ToLower(meta.toolName)
	if toolLower == "edit" || toolLower == "fileedittool" {
		if filediff := buildFileDiff(meta.input); filediff != nil {
			state["metadata"] = map[string]any{
				"output":   output,
				"filediff": filediff,
			}
		}
	}
	if toolLower == "task" || toolLower == "agent" {
		metadata := map[string]any{
			"output": output,
		}
		if sessionID := extractAgentSessionID(output); sessionID != "" {
			metadata["sessionId"] = sessionID
		}
		state["metadata"] = metadata
	}
	return state
}

func extractAgentSessionID(output string) string {
	for _, prefix := range []string{"agentId: ", "task_id: "} {
		if idx := strings.Index(output, prefix); idx >= 0 {
			rest := output[idx+len(prefix):]
			if end := strings.IndexAny(rest, " \n\r"); end >= 0 {
				return rest[:end]
			}
			return rest
		}
	}
	return ""
}

func buildFileDiff(input map[string]any) map[string]any {
	filePath, _ := input["filePath"].(string)
	if filePath == "" {
		return nil
	}
	before, _ := input["oldString"].(string)
	after, _ := input["newString"].(string)
	additions, deletions := countLineDiff(before, after)
	return map[string]any{
		"file":      filePath,
		"before":    before,
		"after":     after,
		"additions": additions,
		"deletions": deletions,
	}
}

func countLineDiff(before, after string) (additions, deletions int) {
	bLines := strings.Count(before, "\n")
	aLines := strings.Count(after, "\n")
	if before != "" && !strings.HasSuffix(before, "\n") {
		bLines++
	}
	if after != "" && !strings.HasSuffix(after, "\n") {
		aLines++
	}
	if aLines >= bLines {
		additions = aLines - bLines
		if bLines > 0 && aLines > 0 && aLines == bLines {
			additions = aLines
			deletions = bLines
		}
	} else {
		deletions = bLines - aLines
	}
	return
}

type agentMention struct {
	value string
	name  string
	start int
	end   int
}

var (
	agentMentionQuotedRe   = regexp.MustCompile(`@"([^"]+)"`)
	agentMentionUnquotedRe = regexp.MustCompile(`@([\w][\w:.\-]*)`)
)

func extractAgentMentions(text string) []agentMention {
	var mentions []agentMention
	seen := map[string]bool{}

	quoted := agentMentionQuotedRe.FindAllStringSubmatchIndex(text, -1)
	for _, idx := range quoted {
		fullStart, fullEnd := idx[0], idx[1]
		nameStart, nameEnd := idx[2], idx[3]
		name := text[nameStart:nameEnd]
		if seen[name] {
			continue
		}
		seen[name] = true
		mentions = append(mentions, agentMention{
			value: text[fullStart:fullEnd],
			name:  name,
			start: fullStart,
			end:   fullEnd,
		})
	}

	positions := agentMentionUnquotedRe.FindAllStringSubmatchIndex(text, -1)
	for _, idx := range positions {
		fullStart, fullEnd := idx[0], idx[1]
		nameStart, nameEnd := idx[2], idx[3]
		name := text[nameStart:nameEnd]

		overlaps := false
		for _, m := range mentions {
			if fullStart >= m.start && fullStart < m.end {
				overlaps = true
				break
			}
		}
		if overlaps {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		mentions = append(mentions, agentMention{
			value: text[fullStart:fullEnd],
			name:  name,
			start: fullStart,
			end:   fullEnd,
		})
	}

	return mentions
}

func buildAgentPartsFromText(text, msgID, sessionID string, seq uint64) []map[string]any {
	mentions := extractAgentMentions(text)
	if len(mentions) == 0 {
		return nil
	}
	var parts []map[string]any
	for i, m := range mentions {
		parts = append(parts, map[string]any{
			"id":        fmt.Sprintf("prt_%d_agent_%d", seq, i),
			"messageID": msgID,
			"sessionID": sessionID,
			"type":      "agent",
			"name":      m.name,
			"source": map[string]any{
				"value": m.value,
				"start": m.start,
				"end":   m.end,
			},
		})
	}
	return parts
}

func extractMessageTime(msg map[string]any) any {
	if t, ok := msg["time"].(map[string]any); ok {
		if created, ok := t["created"]; ok {
			return created
		}
	}
	if ts, ok := msg["timestamp"]; ok {
		return ts
	}
	return time.Now().UnixMilli()
}
