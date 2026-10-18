package modelproxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// OpenAIProxy handles ANY /v1/{path} requests.
func (p *Proxy) OpenAIProxy(w http.ResponseWriter, r *http.Request, path string, userID int64, authSource string) {
	if r.Method == http.MethodGet && strings.Trim(path, "/") == "models" {
		entries, err := p.ModelsList(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data":   entries,
		})
		return
	}

	rawBody, err := copyBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var body map[string]any
	if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
		if len(rawBody) > 0 {
			_ = json.Unmarshal(rawBody, &body)
		}
	}

	requestedModel := ""
	if body != nil {
		if m, ok := body["model"].(string); ok {
			requestedModel = m
		}
	}

	up, modelID, ok := p.Route(r.Context(), requestedModel)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, NoModelRunning)
		return
	}

	if body != nil {
		// normalizeOpenAIBody returns nil when nothing changed; keep the
		// original bytes in that case so the body is never dropped.
		if normalized := normalizeOpenAIBody(body, modelID); normalized != nil {
			rawBody = normalized
		}
	}

	clientWantsUsage := false
	streaming := false
	if len(rawBody) > 0 {
		parsed, stream, wantsUsage := inspectStreamOptions(rawBody)
		streaming = stream
		clientWantsUsage = wantsUsage
		if parsed != nil && !clientWantsUsage {
			b, _ := json.Marshal(parsed)
			rawBody = b
		}
	}

	endpoint := "openai:" + strings.Trim(path, "/")

	req, err := p.buildUpstreamRequest(r, path, rawBody, up)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if streaming {
		p.streamOpenAI(w, req, clientWantsUsage, userID, endpoint, authSource)
		return
	}
	p.postOpenAI(w, req, userID, endpoint, authSource)
}

func normalizeOpenAIBody(body map[string]any, modelID string) []byte {
	mutated := false
	if m, ok := body["model"].(string); ok && m != modelID {
		body["model"] = modelID
		mutated = true
	}
	if normalizePDFFileParts(body) {
		mutated = true
	}
	if normalizeDeveloperRoles(body) {
		mutated = true
	}
	if mutated {
		b, _ := json.Marshal(body)
		return b
	}
	return nil
}

func normalizeDeveloperRoles(body map[string]any) bool {
	mutated := false
	if msgs, ok := body["messages"].([]any); ok {
		for _, m := range msgs {
			msg, ok := m.(map[string]any)
			if !ok {
				continue
			}
			if role, _ := msg["role"].(string); role == "developer" {
				msg["role"] = "system"
				mutated = true
			}
		}
	}
	if items, ok := body["input"].([]any); ok {
		var extra []string
		var kept []any
		for _, it := range items {
			item, ok := it.(map[string]any)
			if !ok {
				kept = append(kept, it)
				continue
			}
			if role, _ := item["role"].(string); role != "developer" {
				kept = append(kept, it)
				continue
			}
			mutated = true
			content := item["content"]
			if s, ok := content.(string); ok {
				extra = append(extra, s)
			} else if parts, ok := content.([]any); ok {
				for _, part := range parts {
					p, ok := part.(map[string]any)
					if !ok {
						continue
					}
					if t, ok := p["text"].(string); ok && t != "" {
						extra = append(extra, t)
					} else if c, ok := p["content"].(string); ok && c != "" {
						extra = append(extra, c)
					}
				}
			}
		}
		if len(extra) > 0 {
			existing := ""
			if v, ok := body["instructions"].(string); ok {
				existing = v
			}
			joined := strings.Join(extra, "\n\n")
			if existing == "" {
				body["instructions"] = joined
			} else {
				body["instructions"] = existing + "\n\n" + joined
			}
			body["input"] = kept
		}
	}
	return mutated
}

func inspectStreamOptions(rawBody []byte) (map[string]any, bool, bool) {
	var parsed map[string]any
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		return nil, false, false
	}
	stream, _ := parsed["stream"].(bool)
	if !stream {
		return nil, false, false
	}
	var opts map[string]any
	if v, ok := parsed["stream_options"].(map[string]any); ok {
		opts = v
	}
	wantsUsage, _ := opts["include_usage"].(bool)
	if !wantsUsage {
		if opts == nil {
			opts = map[string]any{}
		}
		opts["include_usage"] = true
		parsed["stream_options"] = opts
	}
	return parsed, true, wantsUsage
}

func (p *Proxy) streamOpenAI(w http.ResponseWriter, req *http.Request, clientWantsUsage bool, userID int64, endpoint, authSource string) {
	resp, err := p.streamCl.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(resp.StatusCode)
	flusher, ok := w.(http.Flusher)
	if !ok {
		io.Copy(w, resp.Body)
		return
	}

	var captured map[string]any
	scanner := bufio.NewScanner(resp.Body)
	scanner.Split(scanLines)
	for scanner.Scan() {
		line := append(scanner.Bytes(), '\n')
		usage, drop := inspectSSELine(line, clientWantsUsage)
		if usage != nil {
			captured = usage
		}
		if drop {
			continue
		}
		w.Write(line)
		flusher.Flush()
	}
	if err := scanner.Err(); err != nil {
		p.deps.Logger.Printf("[modelproxy] stream error: %v", err)
	}
	p.RecordUsage(userID, endpoint, authSource, captured)
}

func (p *Proxy) postOpenAI(w http.ResponseWriter, req *http.Request, userID int64, endpoint, authSource string) {
	resp, err := p.postCl.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusOK && req.Method == http.MethodPost {
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err == nil {
			p.RecordUsage(userID, endpoint, authSource, usageFrom(payload))
		}
	}

	hdrs := FilterHeaders(resp.Header)
	for k, vv := range hdrs {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// scanLines is a bufio.SplitFunc that splits on \n while preserving empty lines.
func scanLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
