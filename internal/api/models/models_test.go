package models

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gospark-hub/internal/config"
	"gospark-hub/internal/modelproxy"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
	"gospark-hub/internal/web"
)

// mockRunner reports fixed slugs without touching docker.
type mockRunner struct {
	slugs []string
}

func (m *mockRunner) RunningSlugs(context.Context) ([]string, error) {
	return m.slugs, nil
}

// testRecipe builds a minimal LLM recipe for the registry.
func testRecipe(slug string, paramsB float64) registry.Recipe {
	return registry.Recipe{
		Name:     slug,
		Slug:     slug,
		Category: "llm",
		Tags:     []string{"tool-use"},
		UI: registry.RecipeUI{
			Type:        "api-only",
			Port:        8000,
			Proxy:       true,
			StripPrefix: true,
		},
		Integration:  &registry.RecipeIntegration{},
		ParamsB:      &paramsB,
		MemoryGB:     max(paramsB*2, 8),
		Capabilities: []string{"completion", "tools"},
	}
}

func writeRecipe(t *testing.T, dir string, r registry.Recipe) {
	d := filepath.Join(dir, r.Slug)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	paramsB := 0.0
	if r.ParamsB != nil {
		paramsB = *r.ParamsB
	}
	yaml := `name: ` + r.Name + `
slug: ` + r.Slug + `
category: llm
tags:
  - tool-use
ui:
  type: api-only
  port: 8000
  proxy: true
  strip_prefix: true
integration:
  api_url: ""
  model_id: ""
params_b: ` + jsonNumber(paramsB) + `
requirements:
  min_memory_gb: 8
`
	if err := os.WriteFile(filepath.Join(d, "recipe.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}

func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func newTestRegistry(t *testing.T, recipes ...registry.Recipe) *registry.Cache {
	dir := t.TempDir()
	for _, r := range recipes {
		writeRecipe(t, dir, r)
	}
	c := registry.NewCache(dir)
	if err := c.Load(); err != nil {
		t.Fatal(err)
	}
	return c
}

// upstreamServer is a fake model container reached through the front door.
type upstreamServer struct {
	mu            sync.Mutex
	lastHeaders   http.Header
	lastBody      map[string]any
	lastPath      string
	modelID       string
	status        int
	streamChunks  []string
	responseBody  map[string]any
	responseBytes []byte
}

func newUpstreamServer(modelID string) *upstreamServer {
	return &upstreamServer{
		modelID: modelID,
		status:  http.StatusOK,
		responseBody: map[string]any{
			"id":     "chat-1",
			"object": "chat.completion",
			"model":  modelID,
			"choices": []any{map[string]any{"message": map[string]any{
				"role":    "assistant",
				"content": "hello",
			}}},
			"usage": map[string]any{
				"prompt_tokens":     3,
				"completion_tokens": 1,
				"total_tokens":      4,
			},
		},
	}
}

func (s *upstreamServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastHeaders = r.Header.Clone()
	s.lastPath = r.URL.Path
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &s.lastBody)
	}

	path := r.URL.Path
	// The daemon reaches apps at /run/{slug}/v1/... through the front door.
	if idx := strings.Index(path, "/v1/"); idx >= 0 {
		path = path[idx:]
	}

	if strings.Trim(path, "/") == "v1/models" {
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data":   []any{map[string]any{"id": s.modelID}},
		})
		return
	}

	if path == "/v1/chat/completions" && s.streamChunks != nil {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(s.status)
		for _, chunk := range s.streamChunks {
			w.Write([]byte(chunk))
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		}
		return
	}

	if s.responseBytes != nil {
		w.WriteHeader(s.status)
		w.Write(s.responseBytes)
		return
	}

	writeJSON(w, s.status, s.responseBody)
}

func (s *upstreamServer) headers() http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastHeaders
}

func (s *upstreamServer) body() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastBody
}

func (s *upstreamServer) path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastPath
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func setup(t *testing.T, recipes []registry.Recipe, runner modelproxy.Runner) (*httptest.Server, *modelproxy.Proxy, *store.DB) {
	t.Helper()
	reg := newTestRegistry(t, recipes...)

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	upstream := newUpstreamServer("test-model")
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)

	cfg := config.Config{
		PublicPort: parsePort(srv.URL),
		Project:    "test",
		DataDir:    t.TempDir(),
	}

	proxy := modelproxy.New(modelproxy.Deps{
		Config:   cfg,
		Registry: reg,
		Store:    db,
		Runner:   runner,
	})

	return srv, proxy, db
}

func parsePort(url string) int {
	parts := strings.Split(url, ":")
	if len(parts) < 3 {
		return 0
	}
	port := 0
	_ = json.Unmarshal([]byte(parts[2]), &port)
	return port
}

func TestOpenAIProxy_RewritesModelAndForwardsBody(t *testing.T) {
	recipes := []registry.Recipe{testRecipe("small", 4)}
	srv, proxy, _ := setup(t, recipes, &mockRunner{slugs: []string{"small"}})

	body := map[string]any{
		"model":    "claude",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", jsonBody(body))
	w := httptest.NewRecorder()
	proxy.OpenAIProxy(w, r, "chat/completions", 0, "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content-type = %q, want json", got)
	}

	us := srv.Config.Handler.(*upstreamServer)
	if got := us.body()["model"]; got != "test-model" {
		t.Fatalf("upstream model = %v, want test-model", got)
	}
}

func TestOpenAIProxy_NoModelRunning(t *testing.T) {
	recipes := []registry.Recipe{testRecipe("small", 4)}
	_, proxy, _ := setup(t, recipes, &mockRunner{slugs: []string{}})

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	proxy.OpenAIProxy(w, r, "chat/completions", 0, "")

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	errObj, _ := resp["error"].(map[string]any)
	if errObj["type"] != "no_model_running" {
		t.Fatalf("error type = %v, want no_model_running", errObj["type"])
	}
}

func TestOpenAIProxy_UpstreamError(t *testing.T) {
	recipes := []registry.Recipe{testRecipe("small", 4)}
	reg := newTestRegistry(t, recipes...)
	upstream := newUpstreamServer("test-model")
	upstream.status = http.StatusBadRequest
	upstream.responseBytes = []byte(`{"error":{"message":"bad request"}}`)
	srv := httptest.NewServer(upstream)
	defer srv.Close()
	cfg := config.Config{PublicPort: parsePort(srv.URL), Project: "test", DataDir: t.TempDir()}
	p := modelproxy.New(modelproxy.Deps{Config: cfg, Registry: reg, Runner: &mockRunner{slugs: []string{"small"}}})

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", jsonBody(map[string]any{"messages": []any{}}))
	w := httptest.NewRecorder()
	p.OpenAIProxy(w, r, "chat/completions", 0, "")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestOpenAIProxy_HeadersDropCredentials(t *testing.T) {
	recipes := []registry.Recipe{testRecipe("small", 4)}
	srv, proxy, _ := setup(t, recipes, &mockRunner{slugs: []string{"small"}})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", jsonBody(map[string]any{"messages": []any{}}))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Api-Key", "secret2")
	req.Header.Set("Cookie", "session=abc")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	proxy.OpenAIProxy(w, req, "chat/completions", 0, "")

	us := srv.Config.Handler.(*upstreamServer)
	hdrs := us.headers()
	if hdrs.Get("Authorization") != "" || hdrs.Get("X-Api-Key") != "" || hdrs.Get("Cookie") != "" {
		t.Fatalf("credential headers leaked to upstream: %v", hdrs)
	}
	if hdrs.Get("X-Sah-Probe") == "" {
		t.Fatal("probe header missing")
	}
}

func TestOpenAIProxy_StreamingWithoutClientUsage(t *testing.T) {
	recipes := []registry.Recipe{testRecipe("small", 4)}
	reg := newTestRegistry(t, recipes...)
	upstream := newUpstreamServer("test-model")
	upstream.streamChunks = []string{
		"data: " + jsonString(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "Hi"}}}, "object": "chat.completion.chunk"}) + "\n\n",
		"data: " + jsonString(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}, "object": "chat.completion.chunk"}) + "\n\n",
		"data: [DONE]\n\n",
	}
	srv := httptest.NewServer(upstream)
	defer srv.Close()
	cfg := config.Config{PublicPort: parsePort(srv.URL), Project: "test", DataDir: t.TempDir()}
	proxy := modelproxy.New(modelproxy.Deps{Config: cfg, Registry: reg, Runner: &mockRunner{slugs: []string{"small"}}})

	body := map[string]any{"model": "claude", "messages": []any{map[string]any{"role": "user", "content": "hi"}}, "stream": true}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", jsonBody(body))
	w := httptest.NewRecorder()
	proxy.OpenAIProxy(w, r, "chat/completions", 0, "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	bodyStr := w.Body.String()
	if !strings.Contains(bodyStr, "Hi") {
		t.Fatalf("missing streamed content: %s", bodyStr)
	}
	if strings.Contains(bodyStr, `"usage"`) {
		t.Fatalf("usage-only chunk should be filtered: %s", bodyStr)
	}
}

func TestAnthropicMessages_NonStreamConversion(t *testing.T) {
	recipes := []registry.Recipe{testRecipe("small", 4)}
	reg := newTestRegistry(t, recipes...)
	upstream := newUpstreamServer("test-model")
	srv := httptest.NewServer(upstream)
	defer srv.Close()
	cfg := config.Config{PublicPort: parsePort(srv.URL), Project: "test", DataDir: t.TempDir()}
	proxy := modelproxy.New(modelproxy.Deps{Config: cfg, Registry: reg, Runner: &mockRunner{slugs: []string{"small"}}})

	body := map[string]any{
		"model":    "claude-3",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"stream":   false,
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", jsonBody(body))
	w := httptest.NewRecorder()
	proxy.AnthropicMessages(w, r, 0, "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["model"] != "claude-3" {
		t.Fatalf("model = %v, want claude-3", resp["model"])
	}
	if resp["type"] != "message" {
		t.Fatalf("type = %v, want message", resp["type"])
	}
	if resp["role"] != "assistant" {
		t.Fatalf("role = %v, want assistant", resp["role"])
	}
}

func TestAnthropicMessages_StreamTranslation(t *testing.T) {
	recipes := []registry.Recipe{testRecipe("small", 4)}
	reg := newTestRegistry(t, recipes...)
	upstream := newUpstreamServer("test-model")
	upstream.streamChunks = []string{
		"data: " + jsonString(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "Hello"}}}, "object": "chat.completion.chunk"}) + "\n\n",
		"data: [DONE]\n\n",
	}
	srv := httptest.NewServer(upstream)
	defer srv.Close()
	cfg := config.Config{PublicPort: parsePort(srv.URL), Project: "test", DataDir: t.TempDir()}
	proxy := modelproxy.New(modelproxy.Deps{Config: cfg, Registry: reg, Runner: &mockRunner{slugs: []string{"small"}}})

	body := map[string]any{
		"model":    "claude-3",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"stream":   true,
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", jsonBody(body))
	w := httptest.NewRecorder()
	proxy.AnthropicMessages(w, r, 0, "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	bodyStr := w.Body.String()
	if !strings.Contains(bodyStr, "message_start") {
		t.Fatalf("missing message_start: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, `"text_delta"`) {
		t.Fatalf("missing text_delta: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "message_stop") {
		t.Fatalf("missing message_stop: %s", bodyStr)
	}
}

func TestAnthropicMessages_StreamRecordsUsage(t *testing.T) {
	recipes := []registry.Recipe{testRecipe("small", 4)}
	reg := newTestRegistry(t, recipes...)
	upstream := newUpstreamServer("test-model")
	upstream.streamChunks = []string{
		"data: " + jsonString(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "Hello"}}}, "object": "chat.completion.chunk"}) + "\n\n",
		"data: " + jsonString(map[string]any{
			"choices": []any{},
			"model":   "test-model",
			"object":  "chat.completion.chunk",
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 7, "total_tokens": 107},
		}) + "\n\n",
		"data: [DONE]\n\n",
	}
	srv := httptest.NewServer(upstream)
	defer srv.Close()
	cfg := config.Config{PublicPort: parsePort(srv.URL), Project: "test", DataDir: t.TempDir()}

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser("u@example.com", "u", "hash", "user", "active", "key-123")
	if err != nil {
		t.Fatal(err)
	}

	proxy := modelproxy.New(modelproxy.Deps{Config: cfg, Registry: reg, Store: db, Runner: &mockRunner{slugs: []string{"small"}}})
	body := map[string]any{
		"model":    "claude-3",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"stream":   true,
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", jsonBody(body))
	w := httptest.NewRecorder()
	proxy.AnthropicMessages(w, r, user.ID, "api_key")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	summary, err := db.UsageSummary(&user.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := summary[user.ID]
	if got.TotalTokens != 107 || got.PromptTokens != 100 || got.CompletionTokens != 7 {
		t.Fatalf("usage = %+v, want prompt=100 completion=7 total=107", got)
	}
}

func TestCountTokens(t *testing.T) {
	_, proxy, _ := setup(t, nil, &mockRunner{})
	body := map[string]any{
		"system":   "sys",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", jsonBody(body))
	w := httptest.NewRecorder()
	proxy.CountTokens(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["input_tokens"].(float64) < 1 {
		t.Fatalf("input_tokens = %v, want >= 1", resp["input_tokens"])
	}
}

func TestModelsList(t *testing.T) {
	recipes := []registry.Recipe{testRecipe("small", 4)}
	_, proxy, _ := setup(t, recipes, &mockRunner{slugs: []string{"small"}})

	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	proxy.OpenAIProxy(w, r, "models", 0, "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	data, _ := resp["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("data len = %d, want 1", len(data))
	}
}

func TestRegister(t *testing.T) {
	recipes := []registry.Recipe{testRecipe("small", 4)}
	reg := newTestRegistry(t, recipes...)
	upstream := newUpstreamServer("test-model")
	srv := httptest.NewServer(upstream)
	defer srv.Close()
	cfg := config.Config{PublicPort: parsePort(srv.URL), Project: "test", DataDir: t.TempDir()}
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	r := web.NewRouter()
	Register(r, Deps{Config: cfg, Registry: reg, Store: db, Runner: &mockRunner{slugs: []string{"small"}}})

	body := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", jsonBody(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestOpenAIProxy_RecordsUsage(t *testing.T) {
	recipes := []registry.Recipe{testRecipe("small", 4)}
	reg := newTestRegistry(t, recipes...)
	upstream := newUpstreamServer("test-model")
	upstream.responseBody["usage"] = map[string]any{"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
	srv := httptest.NewServer(upstream)
	defer srv.Close()
	cfg := config.Config{PublicPort: parsePort(srv.URL), Project: "test", DataDir: t.TempDir()}
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser("u@example.com", "u", "hash", "user", "active", "key-123")
	if err != nil {
		t.Fatal(err)
	}

	proxy := modelproxy.New(modelproxy.Deps{Config: cfg, Registry: reg, Store: db, Runner: &mockRunner{slugs: []string{"small"}}})
	proxy.OpenAIProxy(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", jsonBody(map[string]any{"messages": []any{}})), "chat/completions", user.ID, "api_key")

	time.Sleep(50 * time.Millisecond)
	summary, err := db.UsageSummary(&user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary[user.ID].TotalTokens != 8 {
		t.Fatalf("total tokens = %d, want 8", summary[user.ID].TotalTokens)
	}
}

func jsonBody(v any) io.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestOpenAIProxy_BodyPreservedWhenModelMatches guards against a regression
// where a request whose model already equals the upstream id was rewritten to
// an empty body, silently dropping the conversation.
func TestOpenAIProxy_BodyPreservedWhenModelMatches(t *testing.T) {
	recipes := []registry.Recipe{testRecipe("small", 4)}
	srv, proxy, _ := setup(t, recipes, &mockRunner{slugs: []string{"small"}})

	body := map[string]any{
		"model":    "test-model",
		"messages": []any{map[string]any{"role": "user", "content": "keep me"}},
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", jsonBody(body))
	w := httptest.NewRecorder()
	proxy.OpenAIProxy(w, r, "chat/completions", 0, "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	us := srv.Config.Handler.(*upstreamServer)
	got, ok := us.body()["messages"].([]any)
	if !ok || len(got) != 1 {
		t.Fatalf("upstream body = %v, want the original messages", us.body())
	}
}

func TestOpenAIProxy_UnknownModelRoutesToLargest(t *testing.T) {
	recipes := []registry.Recipe{testRecipe("small", 4), testRecipe("big", 30)}
	srv, proxy, _ := setup(t, recipes, &mockRunner{slugs: []string{"small", "big"}})

	body := map[string]any{
		"model":    "totally-unknown",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", jsonBody(body))
	w := httptest.NewRecorder()
	proxy.OpenAIProxy(w, r, "chat/completions", 0, "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	us := srv.Config.Handler.(*upstreamServer)
	if !strings.Contains(us.path(), "/run/big/") {
		t.Fatalf("upstream path = %q, want the largest model (big)", us.path())
	}
}

// TestOpenAIProxy_ResponseWithoutUsage verifies that a 200 body with no usage
// block still reaches the client and does not fabricate a usage event.
func TestOpenAIProxy_ResponseWithoutUsage(t *testing.T) {
	recipes := []registry.Recipe{testRecipe("small", 4)}
	reg := newTestRegistry(t, recipes...)
	upstream := newUpstreamServer("test-model")
	upstream.responseBody = map[string]any{
		"id":      "chat-1",
		"object":  "chat.completion",
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "hello"}}},
	}
	srv := httptest.NewServer(upstream)
	defer srv.Close()
	cfg := config.Config{PublicPort: parsePort(srv.URL), Project: "test", DataDir: t.TempDir()}

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser("u@example.com", "u", "hash", "user", "active", "key-123")
	if err != nil {
		t.Fatal(err)
	}

	proxy := modelproxy.New(modelproxy.Deps{Config: cfg, Registry: reg, Store: db, Runner: &mockRunner{slugs: []string{"small"}}})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", jsonBody(map[string]any{"messages": []any{}}))
	w := httptest.NewRecorder()
	proxy.OpenAIProxy(w, r, "chat/completions", user.ID, "api_key")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "hello") {
		t.Fatalf("body = %s, want the upstream response", w.Body.String())
	}
	summary, err := db.UsageSummary(&user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary) != 0 {
		t.Fatalf("usage recorded for a response without usage: %v", summary)
	}
}

// blockingUpstream emits the first SSE chunk, then waits for release before
// emitting the rest, so a buffering proxy cannot serve the first chunk.
type blockingUpstream struct {
	modelID string
	first   chan struct{}
	release chan struct{}
}

func (b *blockingUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if idx := strings.Index(path, "/v1/"); idx >= 0 {
		path = path[idx:]
	}
	if strings.Trim(path, "/") == "v1/models" {
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data":   []any{map[string]any{"id": b.modelID}},
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	chunk := func(text string) {
		w.Write([]byte("data: " + jsonString(map[string]any{
			"object":  "chat.completion.chunk",
			"choices": []any{map[string]any{"delta": map[string]any{"content": text}}},
		}) + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	chunk("first")
	close(b.first)
	<-b.release
	chunk("second")
	w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
}

func TestOpenAIProxy_StreamIsProgressive(t *testing.T) {
	recipes := []registry.Recipe{testRecipe("small", 4)}
	reg := newTestRegistry(t, recipes...)
	upstream := &blockingUpstream{modelID: "test-model", first: make(chan struct{}), release: make(chan struct{})}
	srv := httptest.NewServer(upstream)
	defer srv.Close()
	cfg := config.Config{PublicPort: parsePort(srv.URL), Project: "test", DataDir: t.TempDir()}
	proxy := modelproxy.New(modelproxy.Deps{Config: cfg, Registry: reg, Runner: &mockRunner{slugs: []string{"small"}}})

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.OpenAIProxy(w, r, strings.TrimPrefix(r.URL.Path, "/v1/"), 0, "")
	}))
	defer gateway.Close()

	body := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}, "stream": true}
	req, err := http.NewRequest(http.MethodPost, gateway.URL+"/v1/chat/completions", jsonBody(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read first line: %v", err)
	}
	if !strings.Contains(line, "first") {
		t.Fatalf("first line = %q, want the first chunk", line)
	}

	// The upstream is still blocked on release. If the proxy buffered, the
	// stream would produce nothing more until the whole body was read.
	done := make(chan struct{})
	go func() {
		defer close(done)
		rest, _ := io.ReadAll(reader)
		if !strings.Contains(string(rest), "second") {
			t.Errorf("missing second chunk: %q", rest)
		}
	}()
	select {
	case <-done:
		t.Fatal("proxy buffered the entire stream instead of forwarding chunks")
	case <-time.After(150 * time.Millisecond):
	}

	close(upstream.release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not finish after upstream release")
	}
}
