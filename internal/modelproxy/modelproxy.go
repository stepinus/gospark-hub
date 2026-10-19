// Package modelproxy implements the OpenAI- and Anthropic-compatible proxy
// logic that sits in front of every running LLM container.
package modelproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"gospark-hub/internal/config"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
)

// Runner reports which recipe slugs are currently running.
type Runner interface {
	RunningSlugs(ctx context.Context) ([]string, error)
}

// Deps is everything the proxy needs from the application.
type Deps struct {
	Config   config.Config
	Registry *registry.Cache
	Store    *store.DB
	Logger   *log.Logger
	Runner   Runner
}

// Proxy is the runtime state of the model proxy.
type Proxy struct {
	deps Deps

	cacheMu  sync.RWMutex
	cache    []upstream
	cachedAt time.Time
	cacheTTL time.Duration
	streamCl *http.Client
	postCl   *http.Client
	probeCl  *http.Client
}

// New builds a Proxy from dependencies. A nil Runner defaults to DockerRunner.
func New(deps Deps) *Proxy {
	if deps.Logger == nil {
		deps.Logger = log.Default()
	}
	if deps.Runner == nil {
		deps.Runner = &DockerRunner{Project: deps.Config.Project}
	}
	return &Proxy{
		deps:     deps,
		cacheTTL: 5 * time.Second,
		streamCl: &http.Client{Timeout: 0},
		postCl:   &http.Client{Timeout: 600 * time.Second},
		probeCl:  &http.Client{Timeout: 2 * time.Second},
	}
}

// upstream describes one running model server.
type upstream struct {
	slug    string
	base    string // http://127.0.0.1:{public_port}/run/{slug}/v1
	model   string // the id the server itself answers to
	entries []map[string]any
}

// internalURL returns how the daemon reaches an app. Containers are only
// reachable through the front door's /run/<slug> route; a native recipe
// listens on its own host port, and with the front door disabled that route
// does not exist at all — which is why native models never showed up.
func internalURL(cfg config.Config, rec registry.Recipe) string {
	if rec.IsNative() && rec.Native != nil {
		port := rec.Native.Port
		if port == 0 {
			port = rec.UI.Port
		}
		return fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	return fmt.Sprintf("http://127.0.0.1:%d/run/%s", cfg.PublicPort, rec.Slug)
}

// RunningSlugs uses the configured Runner to discover running compose projects.
func (p *Proxy) RunningSlugs(ctx context.Context) ([]string, error) {
	return p.deps.Runner.RunningSlugs(ctx)
}

// Upstreams returns every live LLM upstream, largest first, with a short TTL cache.
func (p *Proxy) Upstreams(ctx context.Context) ([]upstream, error) {
	now := time.Now()
	p.cacheMu.RLock()
	cached := p.cache
	at := p.cachedAt
	p.cacheMu.RUnlock()
	if len(cached) > 0 && now.Sub(at) < p.cacheTTL {
		return cached, nil
	}

	slugs, err := p.RunningSlugs(ctx)
	if err != nil {
		return nil, err
	}

	type sized struct {
		up       upstream
		paramsB  float64
		memoryGB float64
	}

	var found []sized
	for _, slug := range slugs {
		rec, ok := p.deps.Registry.Get(slug)
		if !ok || !rec.IsLLM() {
			continue
		}
		up, err := p.probe(ctx, rec)
		if err != nil {
			p.deps.Logger.Printf("[modelproxy] probe %s: %v", slug, err)
			continue
		}
		if up == nil {
			continue
		}
		pb := 0.0
		if rec.ParamsB != nil {
			pb = *rec.ParamsB
		}
		found = append(found, sized{
			up:       *up,
			paramsB:  pb,
			memoryGB: rec.MemoryGB,
		})
	}

	sort.Slice(found, func(i, j int) bool {
		if found[i].paramsB != found[j].paramsB {
			return found[i].paramsB > found[j].paramsB
		}
		return found[i].memoryGB > found[j].memoryGB
	})

	ups := make([]upstream, len(found))
	for i, f := range found {
		ups[i] = f.up
	}

	// Two builds of one model answer to the same id. List each copy under its
	// recipe slug so every build is reachable by name.
	counts := make(map[string]int)
	for _, up := range ups {
		for _, e := range up.entries {
			if id, ok := e["id"].(string); ok && id != "" {
				counts[id]++
			}
		}
	}
	for i := range ups {
		for _, e := range ups[i].entries {
			if id, ok := e["id"].(string); ok && counts[id] > 1 {
				e["id"] = ups[i].slug
			}
		}
	}

	p.cacheMu.Lock()
	if len(ups) > 0 {
		p.cache = ups
		p.cachedAt = now
	} else {
		p.cache = nil
		p.cachedAt = time.Time{}
	}
	p.cacheMu.Unlock()

	return ups, nil
}

// Route chooses the upstream for a requested model name.
// The second return value is the model id that should be sent to that upstream.
func (p *Proxy) Route(ctx context.Context, requested string) (upstream, string, bool) {
	ups, err := p.Upstreams(ctx)
	if err != nil || len(ups) == 0 {
		return upstream{}, "", false
	}
	for _, up := range ups {
		if up.slug == requested || up.model == requested {
			return up, up.model, true
		}
		for _, e := range up.entries {
			if id, ok := e["id"].(string); ok && id == requested {
				return up, up.model, true
			}
		}
	}
	return ups[0], ups[0].model, true
}

// ModelsList returns the merged /v1/models list from every running upstream.
func (p *Proxy) ModelsList(ctx context.Context) ([]map[string]any, error) {
	ups, err := p.Upstreams(ctx)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{} // an empty model list is [], never null
	for _, up := range ups {
		for _, e := range up.entries {
			out = append(out, e)
		}
	}
	return out, nil
}

func (p *Proxy) probe(ctx context.Context, rec registry.Recipe) (*upstream, error) {
	base := internalURL(p.deps.Config, rec) + "/v1"
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/models", nil)
	if err != nil {
		return nil, err
	}
	for k, vv := range ProbeHeaders(p.deps.Config) {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}

	resp, err := p.probeCl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}

	var data struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	entries := make([]map[string]any, 0, len(data.Data))
	for _, e := range data.Data {
		if e == nil {
			continue
		}
		if id, ok := e["id"].(string); !ok || id == "" {
			continue
		}
		if _, exists := e["capabilities"]; !exists {
			e["capabilities"] = rec.Capabilities
		}
		if rec.ParamsB != nil {
			e["params_b"] = *rec.ParamsB
		} else {
			e["params_b"] = nil
		}
		e["recipe"] = rec.Slug
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return nil, nil
	}
	firstID, _ := entries[0]["id"].(string)
	return &upstream{
		slug:    rec.Slug,
		base:    base,
		model:   firstID,
		entries: entries,
	}, nil
}

// NoModelRunning is the error body returned when no model is running.
var NoModelRunning = map[string]any{
	"error": map[string]any{
		"type":    "no_model_running",
		"message": "No model is currently running. Launch one from the Spark AI Hub first.",
	},
}

// HopByHop headers that must not be forwarded in either direction.
var HopByHop = map[string]bool{
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailers":            true,
	"transfer-encoding":   true,
	"upgrade":             true,
	"host":                true,
	"content-length":      true,
}

// CredentialHeaders that authenticate to the Hub and must not be forwarded.
var CredentialHeaders = map[string]bool{
	"authorization": true,
	"x-api-key":     true,
	"api-key":       true,
	"cookie":        true,
}

// FilterHeaders returns headers suitable to send back to the client.
func FilterHeaders(src http.Header) http.Header {
	out := make(http.Header)
	for k, vv := range src {
		if HopByHop[strings.ToLower(k)] {
			continue
		}
		for _, v := range vv {
			out.Add(k, v)
		}
	}
	return out
}

// UpstreamHeaders returns headers suitable to send to the upstream container.
func UpstreamHeaders(src http.Header, probe http.Header) http.Header {
	drop := make(map[string]bool)
	for k := range HopByHop {
		drop[k] = true
	}
	for k := range CredentialHeaders {
		drop[k] = true
	}
	out := make(http.Header)
	for k, vv := range src {
		if drop[strings.ToLower(k)] {
			continue
		}
		for _, v := range vv {
			out.Add(k, v)
		}
	}
	for k, vv := range probe {
		out[k] = vv
	}
	return out
}

// copyBody reads and returns the request body, restoring it for re-reads.
func copyBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	return b, nil
}

// usageFrom extracts an OpenAI-shaped usage object from a response payload.
func usageFrom(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	usage, ok := payload["usage"].(map[string]any)
	if !ok {
		if inner, ok := payload["response"].(map[string]any); ok {
			return usageFrom(inner)
		}
		return nil
	}
	prompt, ok := intField(usage, "prompt_tokens")
	if !ok {
		prompt, _ = intField(usage, "input_tokens")
	}
	completion, ok := intField(usage, "completion_tokens")
	if !ok {
		completion, _ = intField(usage, "output_tokens")
	}
	total, _ := intField(usage, "total_tokens")
	if total == 0 {
		total = prompt + completion
	}
	model := ""
	if v, ok := payload["model"].(string); ok {
		model = v
	}
	return map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"total_tokens":      total,
		"model":             model,
	}
}

// inspectSSELine parses one SSE line. It returns (usage, drop) where usage is
// non-nil if the line carried usage, and drop is true when the line is the
// extra usage-only chunk that should not be forwarded to the client.
func inspectSSELine(line []byte, clientWantsUsage bool) (map[string]any, bool) {
	stripped := bytes.TrimSpace(line)
	if !bytes.HasPrefix(stripped, []byte("data:")) {
		return nil, false
	}
	payload := bytes.TrimSpace(stripped[5:])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return nil, false
	}
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		return nil, false
	}
	usage := usageFrom(obj)
	if usage == nil {
		return nil, false
	}
	objType, _ := obj["object"].(string)
	_, hasUsage := obj["usage"].(map[string]any)
	choices, hasChoices := obj["choices"].([]any)
	usageOnlyChunk := objType == "chat.completion.chunk" && hasUsage && (!hasChoices || len(choices) == 0)
	return usage, usageOnlyChunk && !clientWantsUsage
}

// buildUpstreamRequest constructs the outbound request to the model server.
func (p *Proxy) buildUpstreamRequest(r *http.Request, path string, rawBody []byte, up upstream) (*http.Request, error) {
	u, err := url.Parse(up.base + "/" + path)
	if err != nil {
		return nil, err
	}
	u.RawQuery = r.URL.RawQuery

	body := io.Reader(nil)
	if len(rawBody) > 0 {
		body = bytes.NewReader(rawBody)
	}
	method := r.Method
	if method == "" {
		method = "GET"
	}
	req, err := http.NewRequestWithContext(r.Context(), method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header = UpstreamHeaders(r.Header, ProbeHeaders(p.deps.Config))
	if len(rawBody) > 0 && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// RecordUsage writes a usage event if userID is non-zero.
func (p *Proxy) RecordUsage(userID int64, endpoint, authSource string, usage map[string]any) {
	if userID == 0 || p.deps.Store == nil || usage == nil {
		return
	}
	ev := store.UsageEvent{
		UserID:           userID,
		Model:            stringValue(usage["model"]),
		Endpoint:         endpoint,
		PromptTokens:     intValue(usage["prompt_tokens"]),
		CompletionTokens: intValue(usage["completion_tokens"]),
		TotalTokens:      intValue(usage["total_tokens"]),
		Source:           authSource,
	}
	if err := p.deps.Store.RecordUsage(ev); err != nil {
		p.deps.Logger.Printf("[modelproxy] record usage: %v", err)
	}
}

func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func intValue(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// intField returns m[key] as an int, reporting whether the key was present.
func intField(m map[string]any, key string) (int, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	return intValue(v), true
}
