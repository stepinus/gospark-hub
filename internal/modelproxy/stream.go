package modelproxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
)

// translateStream converts an OpenAI SSE stream into Anthropic SSE events.
func translateStream(r io.Reader, requestedModel string, usageSink map[string]any) <-chan map[string]any {
	out := make(chan map[string]any)
	go func() {
		defer close(out)
		msgID := newID("msg")
		out <- sse("message_start", map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":            msgID,
				"type":          "message",
				"role":          "assistant",
				"content":       []any{},
				"model":         requestedModel,
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
			},
		})

		var textIdx, thinkingIdx *int
		toolBlocks := make(map[int]map[string]any)
		nextIdx := 0
		finishReason := ""
		outputTokens := 0

		scanner := bufio.NewScanner(r)
		scanner.Split(scanSSELines)
		for scanner.Scan() {
			line := scanner.Bytes()
			payload := extractData(line)
			if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
				continue
			}
			var ev map[string]any
			if err := json.Unmarshal(payload, &ev); err != nil {
				continue
			}

			// Capture the upstream usage block — including the usage-only
			// chunk the proxy asked for via stream_options.include_usage — and
			// hand it to the caller so the call can be billed. It is not
			// forwarded as an Anthropic event; message_delta carries the
			// output count.
			if evUsage := usageFrom(ev); evUsage != nil {
				outputTokens = intValue(evUsage["completion_tokens"])
				if usageSink != nil {
					for k, v := range evUsage {
						usageSink[k] = v
					}
				}
			}

			choices, _ := ev["choices"].([]any)
			if len(choices) == 0 {
				continue
			}
			ch, _ := choices[0].(map[string]any)
			delta, _ := ch["delta"].(map[string]any)
			if fr, ok := ch["finish_reason"].(string); ok && fr != "" {
				finishReason = fr
			}

			if thinking := reasoning(delta); thinking != "" {
				if thinkingIdx == nil {
					idx := nextIdx
					nextIdx++
					thinkingIdx = &idx
					out <- sse("content_block_start", map[string]any{
						"type":  "content_block_start",
						"index": idx,
						"content_block": map[string]any{
							"type":     "thinking",
							"thinking": "",
						},
					})
				}
				out <- sse("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": *thinkingIdx,
					"delta": map[string]any{"type": "thinking_delta", "thinking": thinking},
				})
			} else if thinkingIdx != nil && (delta["content"] != nil || delta["tool_calls"] != nil) {
				out <- sse("content_block_stop", map[string]any{
					"type":  "content_block_stop",
					"index": *thinkingIdx,
				})
				thinkingIdx = nil
			}

			if text, ok := delta["content"].(string); ok && text != "" {
				if textIdx == nil {
					idx := nextIdx
					nextIdx++
					textIdx = &idx
					out <- sse("content_block_start", map[string]any{
						"type":  "content_block_start",
						"index": idx,
						"content_block": map[string]any{
							"type": "text",
							"text": "",
						},
					})
				}
				out <- sse("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": *textIdx,
					"delta": map[string]any{"type": "text_delta", "text": text},
				})
			}

			if tcs, ok := delta["tool_calls"].([]any); ok {
				for _, tc := range tcs {
					tool, _ := tc.(map[string]any)
					tci := intValue(tool["index"])
					fn, _ := tool["function"].(map[string]any)
					if _, ok := toolBlocks[tci]; !ok {
						idx := nextIdx
						nextIdx++
						toolBlocks[tci] = map[string]any{
							"anthropic_idx": idx,
							"id":            tool["id"],
							"name":          fn["name"],
						}
						out <- sse("content_block_start", map[string]any{
							"type":  "content_block_start",
							"index": idx,
							"content_block": map[string]any{
								"type":  "tool_use",
								"id":    tool["id"],
								"name":  fn["name"],
								"input": map[string]any{},
							},
						})
					}
					if args, ok := fn["arguments"].(string); ok && args != "" {
						out <- sse("content_block_delta", map[string]any{
							"type":  "content_block_delta",
							"index": toolBlocks[tci]["anthropic_idx"],
							"delta": map[string]any{"type": "input_json_delta", "partial_json": args},
						})
					}
				}
			}
		}

		if thinkingIdx != nil {
			out <- sse("content_block_stop", map[string]any{
				"type":  "content_block_stop",
				"index": *thinkingIdx,
			})
		}
		if textIdx != nil {
			out <- sse("content_block_stop", map[string]any{
				"type":  "content_block_stop",
				"index": *textIdx,
			})
		}
		for _, b := range toolBlocks {
			out <- sse("content_block_stop", map[string]any{
				"type":  "content_block_stop",
				"index": b["anthropic_idx"],
			})
		}

		stopReason := finishToStop[finishReason]
		if stopReason == "" {
			stopReason = "end_turn"
		}
		out <- sse("message_delta", map[string]any{
			"type": "message_delta",
			"delta": map[string]any{
				"stop_reason":   stopReason,
				"stop_sequence": nil,
			},
			"usage": map[string]any{"output_tokens": outputTokens},
		})
		out <- sse("message_stop", map[string]any{"type": "message_stop"})
	}()
	return out
}

func sse(event string, data map[string]any) map[string]any {
	return map[string]any{"event": event, "data": data}
}

func extractData(line []byte) []byte {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil
	}
	return bytes.TrimSpace(line[5:])
}

// scanSSELines splits on \n while preserving the line content without the delimiter.
func scanSSELines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}
