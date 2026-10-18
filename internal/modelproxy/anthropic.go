package modelproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

var finishToStop = map[string]string{
	"stop":           "end_turn",
	"length":         "max_tokens",
	"tool_calls":     "tool_use",
	"content_filter": "end_turn",
}

// AnthropicMessages handles POST /v1/messages.
func (p *Proxy) AnthropicMessages(w http.ResponseWriter, r *http.Request, userID int64, authSource string) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "invalid_request_error",
				"message": "invalid JSON body",
			},
		})
		return
	}

	requestedModel := "claude"
	if m, ok := body["model"].(string); ok && m != "" {
		requestedModel = m
	}

	up, modelID, ok := p.Route(r.Context(), requestedModel)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, NoModelRunning)
		return
	}

	upstreamPayload := anthropicToOpenAI(body, modelID)
	url := up.base + "/chat/completions"

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url,
		bytes.NewReader(mustJSON(upstreamPayload)))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header = ProbeHeaders(p.deps.Config)
	req.Header.Set("Content-Type", "application/json")

	if upstreamPayload["stream"] == true {
		p.streamAnthropic(w, req, requestedModel, userID, authSource)
		return
	}
	p.postAnthropic(w, req, requestedModel, userID, authSource)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (p *Proxy) streamAnthropic(w http.ResponseWriter, req *http.Request, requestedModel string, userID int64, authSource string) {
	resp, err := p.streamCl.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// The upstream rejected the request before any SSE began. Mirror the
		// upstream body as a single Anthropic error event on a 200 stream,
		// exactly as the upstream Hub does.
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSE(w, "error", map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "api_error",
				"message": strings.TrimSpace(string(errBody)),
			},
		})
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	usageSink := map[string]any{}
	for ev := range translateStream(resp.Body, requestedModel, usageSink) {
		writeSSEBytes(w, ev)
		flusher.Flush()
	}
	p.RecordUsage(userID, "anthropic:messages", authSource, usageSink)
}

func (p *Proxy) postAnthropic(w http.ResponseWriter, req *http.Request, requestedModel string, userID int64, authSource string) {
	resp, err := p.postCl.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		data = map[string]any{"error": map[string]any{"message": strings.TrimSpace(string(body))}}
	}

	if resp.StatusCode != http.StatusOK {
		msg := ""
		if errObj, ok := data["error"].(map[string]any); ok {
			if m, ok := errObj["message"].(string); ok {
				msg = m
			}
		}
		if msg == "" {
			if data != nil {
				msg = strings.TrimSpace(string(body))
				if len(msg) > 800 {
					msg = msg[:800]
				}
			}
			if msg == "" {
				msg = "upstream error"
			}
		}
		writeJSON(w, resp.StatusCode, map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "api_error",
				"message": msg,
			},
		})
		return
	}

	p.RecordUsage(userID, "anthropic:messages", authSource, usageFrom(data))
	writeJSON(w, http.StatusOK, openAIToAnthropic(data, requestedModel))
}

// CountTokens handles POST /v1/messages/count_tokens.
func (p *Proxy) CountTokens(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		body = map[string]any{}
	}
	blob, _ := json.Marshal(map[string]any{
		"system":   body["system"],
		"messages": body["messages"],
		"tools":    body["tools"],
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"input_tokens": max(1, len(blob)/4),
	})
}

func anthropicToOpenAI(body map[string]any, model string) map[string]any {
	out := map[string]any{
		"model":  model,
		"stream": false,
	}
	if v, ok := body["stream"].(bool); ok {
		out["stream"] = v
	}
	if v, ok := body["max_tokens"].(float64); ok {
		out["max_tokens"] = int(v)
	}
	for _, k := range []string{"temperature", "top_p"} {
		if v, ok := body[k]; ok {
			out[k] = v
		}
	}
	if stops, ok := body["stop_sequences"].([]any); ok {
		out["stop"] = stops
	}
	if out["stream"] == true {
		out["stream_options"] = map[string]any{"include_usage": true}
	}

	var messages []map[string]any
	if sysText := flattenText(body["system"]); sysText != "" {
		messages = append(messages, map[string]any{"role": "system", "content": sysText})
	}

	if rawMsgs, ok := body["messages"].([]any); ok {
		for _, m := range rawMsgs {
			msg, ok := m.(map[string]any)
			if !ok {
				continue
			}
			role, _ := msg["role"].(string)
			content := msg["content"]
			if s, ok := content.(string); ok {
				messages = append(messages, map[string]any{"role": role, "content": s})
				continue
			}
			parts, ok := content.([]any)
			if !ok {
				continue
			}

			switch role {
			case "user":
				messages = append(messages, convertUserMessage(parts)...)
			case "assistant":
				messages = append(messages, convertAssistantMessage(parts))
			}
		}
	}
	out["messages"] = hoistSystemMessages(messages)

	if tools, ok := body["tools"].([]any); ok {
		out["tools"] = convertTools(tools)
	}
	if tc, ok := body["tool_choice"].(map[string]any); ok {
		out["tool_choice"] = convertToolChoice(tc)
	}

	return out
}

func flattenText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	parts, ok := v.([]any)
	if !ok {
		return ""
	}
	var out []string
	for _, p := range parts {
		blk, ok := p.(map[string]any)
		if !ok || blk["type"] != "text" {
			continue
		}
		if t, ok := blk["text"].(string); ok {
			out = append(out, t)
		}
	}
	return strings.Join(out, "")
}

func hoistSystemMessages(messages []map[string]any) []map[string]any {
	var system []string
	var rest []map[string]any
	for _, m := range messages {
		if m["role"] == "system" {
			if s, ok := m["content"].(string); ok {
				system = append(system, s)
			}
			continue
		}
		rest = append(rest, m)
	}
	if len(system) == 0 {
		return rest
	}
	return append([]map[string]any{{"role": "system", "content": strings.Join(system, "\n\n")}}, rest...)
}

func convertUserMessage(parts []any) []map[string]any {
	var result []map[string]any
	var userParts []map[string]any
	hasImage := false

	flush := func() {
		if len(userParts) == 0 {
			return
		}
		result = append(result, map[string]any{
			"role":    "user",
			"content": userContent(userParts, hasImage),
		})
		userParts = nil
		hasImage = false
	}

	for _, p := range parts {
		blk, ok := p.(map[string]any)
		if !ok {
			continue
		}
		t, _ := blk["type"].(string)
		switch t {
		case "tool_result":
			flush()
			text, images := splitToolResult(blk["content"])
			result = append(result, map[string]any{
				"role":         "tool",
				"tool_call_id": blk["tool_use_id"],
				"content":      text,
			})
			if len(images) > 0 {
				result = append(result, map[string]any{
					"role": "user",
					"content": append([]map[string]any{
						{"type": "text", "text": "[tool_result images]"},
					}, images...),
				})
			}
		case "text":
			userParts = append(userParts, map[string]any{
				"type": "text",
				"text": blk["text"],
			})
		case "image":
			src, _ := blk["source"].(map[string]any)
			url := imageURL(src)
			if url != "" {
				userParts = append(userParts, map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": url,
					},
				})
				hasImage = true
			}
		}
	}
	flush()
	return result
}

func imageURL(src map[string]any) string {
	if src["type"] == "base64" {
		mediaType, _ := src["media_type"].(string)
		if mediaType == "" {
			mediaType = "image/png"
		}
		data, _ := src["data"].(string)
		return fmt.Sprintf("data:%s;base64,%s", mediaType, data)
	}
	if url, ok := src["url"].(string); ok {
		return url
	}
	return ""
}

func userContent(parts []map[string]any, hasImage bool) any {
	if !hasImage {
		var texts []string
		for _, p := range parts {
			if p["type"] == "text" {
				if t, ok := p["text"].(string); ok {
					texts = append(texts, t)
				}
			}
		}
		return strings.Join(texts, "")
	}
	return parts
}

func splitToolResult(content any) (string, []map[string]any) {
	if content == nil {
		return "", nil
	}
	if s, ok := content.(string); ok {
		return s, nil
	}
	parts, ok := content.([]any)
	if !ok {
		b, _ := json.Marshal(content)
		return string(b), nil
	}
	var texts []string
	var images []map[string]any
	for _, p := range parts {
		blk, ok := p.(map[string]any)
		if !ok {
			continue
		}
		t, _ := blk["type"].(string)
		if t == "text" {
			if s, ok := blk["text"].(string); ok {
				texts = append(texts, s)
			}
		} else if t == "image" {
			src, _ := blk["source"].(map[string]any)
			if url := imageURL(src); url != "" {
				images = append(images, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": url},
				})
			}
		}
	}
	return strings.Join(texts, ""), images
}

func convertAssistantMessage(parts []any) map[string]any {
	var textParts []string
	var toolCalls []map[string]any
	for _, p := range parts {
		blk, ok := p.(map[string]any)
		if !ok {
			continue
		}
		t, _ := blk["type"].(string)
		switch t {
		case "text":
			if s, ok := blk["text"].(string); ok {
				textParts = append(textParts, s)
			}
		case "tool_use":
			input, _ := blk["input"].(map[string]any)
			toolCalls = append(toolCalls, map[string]any{
				"id":   blk["id"],
				"type": "function",
				"function": map[string]any{
					"name":      blk["name"],
					"arguments": mustJSON(input),
				},
			})
		}
	}
	msg := map[string]any{"role": "assistant"}
	text := strings.Join(textParts, "")
	if text == "" {
		msg["content"] = nil
	} else {
		msg["content"] = text
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	return msg
}

func convertTools(tools []any) []map[string]any {
	var out []map[string]any
	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			continue
		}
		schema, ok := tool["input_schema"].(map[string]any)
		if !ok {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool["name"],
				"description": tool["description"],
				"parameters":  schema,
			},
		})
	}
	return out
}

func convertToolChoice(tc map[string]any) any {
	typeVal, _ := tc["type"].(string)
	switch typeVal {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "tool":
		if name, ok := tc["name"].(string); ok && name != "" {
			return map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": name,
				},
			}
		}
	case "none":
		return "none"
	}
	return nil
}

func openAIToAnthropic(resp map[string]any, requestedModel string) map[string]any {
	choices, _ := resp["choices"].([]any)
	choice := map[string]any{}
	if len(choices) > 0 {
		choice, _ = choices[0].(map[string]any)
	}
	msg, _ := choice["message"].(map[string]any)
	if msg == nil {
		msg = map[string]any{}
	}

	var blocks []map[string]any
	if thinking := reasoning(msg); thinking != "" {
		blocks = append(blocks, map[string]any{"type": "thinking", "thinking": thinking})
	}
	if text, ok := msg["content"].(string); ok && text != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	if tcs, ok := msg["tool_calls"].([]any); ok {
		for _, tc := range tcs {
			tool, _ := tc.(map[string]any)
			fn, _ := tool["function"].(map[string]any)
			args := map[string]any{}
			if raw, ok := fn["arguments"].(string); ok {
				_ = json.Unmarshal([]byte(raw), &args)
			}
			blocks = append(blocks, map[string]any{
				"type":  "tool_use",
				"id":    tool["id"],
				"name":  fn["name"],
				"input": args,
			})
		}
	}

	usage, _ := resp["usage"].(map[string]any)
	if usage == nil {
		usage = map[string]any{}
	}
	stopReason, _ := choice["finish_reason"].(string)
	if sr, ok := finishToStop[stopReason]; ok {
		stopReason = sr
	} else {
		stopReason = "end_turn"
	}

	id, _ := resp["id"].(string)
	if id == "" {
		id = newID("msg")
	}
	return map[string]any{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"model":         requestedModel,
		"content":       blocks,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  intValue(usage["prompt_tokens"]),
			"output_tokens": intValue(usage["completion_tokens"]),
		},
	}
}

func reasoning(msg map[string]any) string {
	if v, ok := msg["reasoning_content"].(string); ok && v != "" {
		return v
	}
	if v, ok := msg["reasoning"].(string); ok && v != "" {
		return v
	}
	return ""
}

func newID(prefix string) string {
	return prefix + "_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:24]
}

func writeSSE(w http.ResponseWriter, event string, data any) {
	writeSSEBytes(w, map[string]any{"event": event, "data": data})
}

func writeSSEBytes(w http.ResponseWriter, ev map[string]any) {
	event, _ := ev["event"].(string)
	data := ev["data"]
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, string(mustJSON(data)))
}
