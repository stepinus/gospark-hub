package mcpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gospark-hub/internal/auth"
	"gospark-hub/internal/config"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
	"gospark-hub/internal/web"
)

// --- fakes ------------------------------------------------------------------

type fakeRuntime struct {
	mu        sync.Mutex
	installed map[string]bool
	running   map[string]bool
	ready     map[string]bool
	launched  []string
	stopped   []string
}

func newFakeRuntime(installed ...string) *fakeRuntime {
	r := &fakeRuntime{installed: map[string]bool{}, running: map[string]bool{}, ready: map[string]bool{}}
	for _, slug := range installed {
		r.installed[slug] = true
		r.running[slug] = true
		r.ready[slug] = true
	}
	return r
}

func (r *fakeRuntime) Installed(context.Context) (map[string]bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]bool{}
	for k, v := range r.installed {
		out[k] = v
	}
	return out, nil
}

func (r *fakeRuntime) Running(_ context.Context, slug string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running[slug], nil
}

func (r *fakeRuntime) Ready(slug string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ready[slug]
}

func (r *fakeRuntime) StartHealthCheck(string) {}

func (r *fakeRuntime) Launch(_ context.Context, slug string, _ *store.User, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.launched = append(r.launched, slug)
	r.running[slug], r.ready[slug] = true, true
	return nil
}

func (r *fakeRuntime) Stop(_ context.Context, slug string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = append(r.stopped, slug)
	delete(r.running, slug)
	delete(r.ready, slug)
	return nil
}

type fakeApp struct{ base string }

func (f fakeApp) InternalURL(slug string) string { return f.base + "/run/" + slug }
func (f fakeApp) ProbeHeaders() map[string]string {
	return map[string]string{"X-Sah-Probe": "test"}
}

// --- harness ----------------------------------------------------------------

type harness struct {
	t      *testing.T
	server *httptest.Server
	db     *store.DB
	cfg    config.Config
	rt     *fakeRuntime
	app    *httptest.Server
	userID int64
	apiKey string
}

func newPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 30, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func newObjectPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			c := color.RGBA{R: 255, G: 255, B: 255, A: 255}
			if x >= 30 && x < 70 && y >= 30 && y < 70 {
				c = color.RGBA{R: 200, G: 30, B: 30, A: 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func wavBytes(seconds int) []byte {
	const rate = 8000
	data := make([]byte, rate*seconds*2)
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(36+len(data)))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint32(rate))
	binary.Write(&buf, binary.LittleEndian, uint32(rate*2))
	binary.Write(&buf, binary.LittleEndian, uint16(2))
	binary.Write(&buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, uint32(len(data)))
	buf.Write(data)
	return buf.Bytes()
}

func setup(t *testing.T, authEnabled bool, rt *fakeRuntime) *harness {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		BaseDir:           dir,
		DataDir:           filepath.Join(dir, "data"),
		AuthEnabled:       authEnabled,
		SessionCookieName: "spark_ai_hub_session",
		SessionTTLDays:    30,
		Project:           "gospark-hub",
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		t.Fatalf("data dir: %v", err)
	}
	db, err := store.Open(filepath.Join(cfg.DataDir, "hub.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	user, err := db.CreateUser("owner@x", "Owner", "x", "user", "active", "ownerkey")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	app := httptest.NewServer(appHandler())
	t.Cleanup(app.Close)

	if rt == nil {
		rt = newFakeRuntime()
	}

	r := web.NewRouter()
	r.Use(auth.NewMiddleware(auth.Deps{Store: db, Config: cfg, Logger: log.Default()}).Handler)
	recipeDir := filepath.Join(dir, "recipes")
	writeTestRecipes(t, recipeDir)
	reg := registry.NewCache(recipeDir)
	Register(r, Deps{
		Config:   cfg,
		Store:    db,
		Registry: reg,
		Logger:   log.New(io.Discard, "", 0),
		Runtime:  rt,
		App:      fakeApp{base: app.URL},
		HTTP:     &http.Client{Timeout: 30 * time.Second},
	})
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	return &harness{t: t, server: server, db: db, cfg: cfg, rt: rt, app: app, userID: user.ID, apiKey: "ownerkey"}
}

// writeTestRecipes seeds a registry whose tags make the fake runtime's
// installed slugs real backends.
func writeTestRecipes(t *testing.T, dir string) {
	t.Helper()
	recipes := map[string]string{
		"qwen-image-2512": "tags: [openai-images, text-to-image, image-edit]",
		"wan":             "tags: [openai-videos, text-to-video]",
		"music":           "tags: [openai-music, text-to-music]",
		"hunyuan3d":       "tags: [openai-meshes, image-to-3d]",
		"clef":            "tags: [systemone-api, vision]",
	}
	for slug, tags := range recipes {
		dir := filepath.Join(dir, slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("recipe dir: %v", err)
		}
		body := "name: " + slug + "\nslug: " + slug + "\n" + tags + "\n"
		if err := os.WriteFile(filepath.Join(dir, "recipe.yaml"), []byte(body), 0o644); err != nil {
			t.Fatalf("recipe yaml: %v", err)
		}
	}
}

func (h *harness) post(body any, opts ...func(*http.Request)) *http.Response {
	h.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		h.t.Fatalf("marshal: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/mcp", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if h.cfg.AuthEnabled {
		req.Header.Set("Authorization", "Bearer "+h.apiKey)
	}
	for _, opt := range opts {
		opt(req)
	}
	resp, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatalf("post: %v", err)
	}
	return resp
}

func rpc(method string, params any) map[string]any {
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	return body
}

func decode(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func sseData(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data:") {
			var event map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(line[5:])), &event); err != nil {
				t.Fatalf("sse decode: %v", err)
			}
			out = append(out, event)
		}
	}
	return out
}

// --- app stand-in -----------------------------------------------------------

func appHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/run/qwen-image-2512/v1/images/generations", func(w http.ResponseWriter, r *http.Request) {
		writeImageJSON(w, newPNG(64, 32))
	})
	mux.HandleFunc("/run/qwen-image-2512/v1/images/edits", func(w http.ResponseWriter, r *http.Request) {
		writeImageJSON(w, newPNG(64, 32))
	})
	mux.HandleFunc("/run/wan/v1/videos", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": "remote-1", "status": "queued"})
	})
	mux.HandleFunc("/run/wan/v1/videos/remote-1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"status": "completed", "completed_at": float64(time.Now().Unix()), "inference_time_s": 301.2,
		})
	})
	mux.HandleFunc("/run/wan/v1/videos/remote-1/content", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fake-mp4"))
	})
	mux.HandleFunc("/run/music/v1/audio/speech", func(w http.ResponseWriter, r *http.Request) {
		w.Write(wavBytes(2))
	})
	mux.HandleFunc("/run/hunyuan3d/v1/meshes", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": "mesh-1", "status": "completed"})
	})
	mux.HandleFunc("/run/hunyuan3d/v1/meshes/mesh-1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": "mesh-1", "status": "completed"})
	})
	mux.HandleFunc("/run/hunyuan3d/v1/meshes/mesh-1/content", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fake-glb"))
	})
	mux.HandleFunc("/run/clef/v1/systemone", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{"q1": []any{"yes", 0.9}}})
	})
	mux.HandleFunc("/run/searxng/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"results": []any{
			map[string]any{"title": "A", "url": "https://a.example", "content": "first"},
		}})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	return mux
}

func writeImageJSON(w http.ResponseWriter, pngRaw []byte) {
	json.NewEncoder(w).Encode(map[string]any{
		"data": []any{map[string]any{"b64_json": base64.StdEncoding.EncodeToString(pngRaw)}},
	})
}

// --- protocol tests ---------------------------------------------------------

func TestInitializeEchoesSupportedVersion(t *testing.T) {
	h := setup(t, false, nil)
	resp := h.post(rpc("initialize", map[string]any{"protocolVersion": "2025-06-18"}))
	out := decode(t, resp)
	result := out["result"].(map[string]any)
	if result["protocolVersion"] != "2025-06-18" {
		t.Fatalf("version = %v", result["protocolVersion"])
	}
	if _, ok := result["capabilities"].(map[string]any)["tools"]; !ok {
		t.Fatalf("capabilities missing tools")
	}
	if result["serverInfo"].(map[string]any)["name"] != "spark-ai-hub" {
		t.Fatalf("serverInfo name = %v", result["serverInfo"])
	}
}

func TestInitializeOffersLatestForUnknownVersion(t *testing.T) {
	h := setup(t, false, nil)
	out := decode(t, h.post(rpc("initialize", map[string]any{"protocolVersion": "1999-01-01"})))
	got := out["result"].(map[string]any)["protocolVersion"]
	if got != "2025-11-25" {
		t.Fatalf("version = %v", got)
	}
}

func TestNotificationIsAcknowledgedWithoutBody(t *testing.T) {
	h := setup(t, false, nil)
	resp := h.post(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 0 {
		t.Fatalf("body = %q", body)
	}
}

func TestToolsListNames(t *testing.T) {
	h := setup(t, false, nil)
	out := decode(t, h.post(rpc("tools/list", nil)))
	tools := out["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 21 {
		t.Fatalf("tool count = %d, want 21", len(tools))
	}
	want := map[string]bool{
		"generate_image": true, "edit_image": true, "get_image": true,
		"list_image_models": true, "generate_video": true, "get_video": true,
		"list_video_models": true, "generate_3d": true, "get_3d": true, "list_3d_models": true,
		"generate_music": true, "get_music": true,
		"create_upload": true, "create_download": true, "delete_file": true,
		"start_model": true, "stop_model": true, "web_search": true, "web_fetch": true,
		"decide": true, "list_decision_models": true,
	}
	got := map[string]bool{}
	for _, toolAny := range tools {
		tool := toolAny.(map[string]any)
		got[tool["name"].(string)] = true
		if tool["inputSchema"] == nil {
			t.Errorf("tool %v missing inputSchema", tool["name"])
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("missing tool %s", name)
		}
	}
}

func TestGetHasNoStream(t *testing.T) {
	h := setup(t, false, nil)
	req, _ := http.NewRequest(http.MethodGet, h.server.URL+"/mcp", nil)
	resp, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get("Allow") != "POST" {
		t.Fatalf("Allow = %q", resp.Header.Get("Allow"))
	}
}

func TestUnknownMethod(t *testing.T) {
	h := setup(t, false, nil)
	out := decode(t, h.post(rpc("resources/list", nil)))
	errObj := out["error"].(map[string]any)
	if errObj["code"].(float64) != -32601 {
		t.Fatalf("code = %v", errObj["code"])
	}
	if errObj["message"] != "Method not found: resources/list" {
		t.Fatalf("message = %v", errObj["message"])
	}
}

func TestUnknownTool(t *testing.T) {
	h := setup(t, false, nil)
	out := decode(t, h.post(rpc("tools/call", map[string]any{"name": "rm_rf", "arguments": map[string]any{}})))
	if out["error"].(map[string]any)["code"].(float64) != -32602 {
		t.Fatalf("code = %v", out["error"])
	}
}

func TestArgumentsMustBeObject(t *testing.T) {
	h := setup(t, false, nil)
	out := decode(t, h.post(rpc("tools/call", map[string]any{"name": "web_search", "arguments": "nope"})))
	if out["error"].(map[string]any)["code"].(float64) != -32602 {
		t.Fatalf("expected -32602, got %v", out)
	}
}

func TestParseError(t *testing.T) {
	h := setup(t, false, nil)
	req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/mcp", strings.NewReader("{nope"))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := h.server.Client().Do(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	out := decode(t, resp)
	if out["error"].(map[string]any)["code"].(float64) != -32700 {
		t.Fatalf("code = %v", out["error"])
	}
}

func TestBadEnvelope(t *testing.T) {
	h := setup(t, false, nil)
	req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/mcp", strings.NewReader(`{"id":1,"method":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := h.server.Client().Do(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	out := decode(t, resp)
	if out["error"].(map[string]any)["code"].(float64) != -32600 {
		t.Fatalf("code = %v", out["error"])
	}
}

// --- tool tests -------------------------------------------------------------

func TestGenerateImageThenGetImage(t *testing.T) {
	rt := newFakeRuntime("qwen-image-2512")
	h := setup(t, false, rt)
	resp := h.post(rpc("tools/call", map[string]any{
		"name": "generate_image", "arguments": map[string]any{"prompt": "a cat", "seed": "7"},
	}))
	out := decode(t, resp)
	res := out["result"].(map[string]any)
	if res["isError"].(bool) {
		t.Fatalf("unexpected error: %v", res)
	}
	started := res["structuredContent"].(map[string]any)
	if !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "Not done yet") {
		t.Fatalf("started text = %v", res["content"])
	}
	args := started["next_call"].(map[string]any)["arguments"]

	got := decode(t, h.post(rpc("tools/call", map[string]any{"name": "get_image", "arguments": args})))
	gres := got["result"].(map[string]any)
	if gres["isError"].(bool) {
		t.Fatalf("get error: %v", gres)
	}
	blocks := gres["content"].([]any)
	text := blocks[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "/images/") || !strings.Contains(text, ".png") {
		t.Fatalf("text = %s", text)
	}
	img := blocks[1].(map[string]any)
	if img["type"] != "image" || img["mimeType"] != "image/jpeg" || img["data"] == "" {
		t.Fatalf("image block = %v", img)
	}
	if gres["structuredContent"].(map[string]any)["seed"].(float64) != 7 {
		t.Fatalf("seed = %v", gres["structuredContent"])
	}
}

func TestGenerateImageAsSSE(t *testing.T) {
	rt := newFakeRuntime("qwen-image-2512")
	h := setup(t, false, rt)
	resp := h.post(rpc("tools/call", map[string]any{
		"name": "generate_image", "arguments": map[string]any{"prompt": "a cat"},
	}), func(r *http.Request) { r.Header.Set("Accept", "application/json, text/event-stream") })
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content-type = %s", resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("cache-control = %s", resp.Header.Get("Cache-Control"))
	}
	body, _ := io.ReadAll(resp.Body)
	events := sseData(t, string(body))
	if len(events) == 0 {
		t.Fatal("no SSE events")
	}
	final := events[len(events)-1]
	if final["result"] == nil {
		t.Fatalf("final event = %v", final)
	}
}

func TestImageErrorIsToolError(t *testing.T) {
	h := setup(t, false, newFakeRuntime())
	out := decode(t, h.post(rpc("tools/call", map[string]any{
		"name": "generate_image", "arguments": map[string]any{"prompt": "x"},
	})))
	res := out["result"].(map[string]any)
	if !res["isError"].(bool) {
		t.Fatalf("expected isError, got %v", res)
	}
	if !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "is installed") {
		t.Fatalf("text = %v", res["content"])
	}
}

func TestEditRequiresImages(t *testing.T) {
	h := setup(t, false, newFakeRuntime("qwen-image-2512"))
	out := decode(t, h.post(rpc("tools/call", map[string]any{
		"name": "edit_image", "arguments": map[string]any{"prompt": "make it blue"},
	})))
	if !out["result"].(map[string]any)["isError"].(bool) {
		t.Fatalf("expected isError, got %v", out)
	}
}

func TestGenerateVideoThenGetVideo(t *testing.T) {
	rt := newFakeRuntime("wan")
	h := setup(t, false, rt)
	out := decode(t, h.post(rpc("tools/call", map[string]any{
		"name": "generate_video", "arguments": map[string]any{"prompt": "waves"},
	})))
	res := out["result"].(map[string]any)
	if res["isError"].(bool) {
		t.Fatalf("error: %v", res)
	}
	args := res["structuredContent"].(map[string]any)["next_call"].(map[string]any)["arguments"]
	got := decode(t, h.post(rpc("tools/call", map[string]any{"name": "get_video", "arguments": args})))
	gres := got["result"].(map[string]any)
	if gres["isError"].(bool) {
		t.Fatalf("get error: %v", gres)
	}
	text := gres["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "/videos/") || !strings.Contains(text, ".mp4") {
		t.Fatalf("text = %s", text)
	}
}

func TestGenerateMusicThenGetImageCollectsIt(t *testing.T) {
	rt := newFakeRuntime("music")
	h := setup(t, false, rt)
	out := decode(t, h.post(rpc("tools/call", map[string]any{
		"name": "generate_music", "arguments": map[string]any{"lyrics": "[Verse]\nla", "style": "pop"},
	})))
	args := out["result"].(map[string]any)["structuredContent"].(map[string]any)["next_call"].(map[string]any)["arguments"]
	got := decode(t, h.post(rpc("tools/call", map[string]any{"name": "get_image", "arguments": args})))
	gres := got["result"].(map[string]any)
	if gres["isError"].(bool) {
		t.Fatalf("error: %v", gres)
	}
	if !strings.Contains(gres["content"].([]any)[0].(map[string]any)["text"].(string), ".wav") {
		t.Fatalf("text = %v", gres["content"])
	}
}

func TestGenerate3DThenGet3D(t *testing.T) {
	rt := newFakeRuntime("hunyuan3d")
	h := setup(t, false, rt)
	pngPath := filepath.Join(h.cfg.DataDir, "object.png")
	if err := os.WriteFile(pngPath, newObjectPNG(), 0o644); err != nil {
		t.Fatal(err)
	}
	// A public URL cannot be fetched in tests; use a Hub file the owner made.
	name := strings.Repeat("a", 32) + ".png"
	imageDir := filepath.Join(h.cfg.DataDir, "images")
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imageDir, name), newObjectPNG(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.db.RecordMedia(name, h.userID, "", nil); err != nil {
		t.Fatal(err)
	}
	out := decode(t, h.post(rpc("tools/call", map[string]any{
		"name": "generate_3d", "arguments": map[string]any{"image": "/images/" + name},
	})))
	res := out["result"].(map[string]any)
	if res["isError"].(bool) {
		t.Fatalf("error: %v", res)
	}
	args := res["structuredContent"].(map[string]any)["next_call"].(map[string]any)["arguments"]
	got := decode(t, h.post(rpc("tools/call", map[string]any{"name": "get_3d", "arguments": args})))
	gres := got["result"].(map[string]any)
	if gres["isError"].(bool) {
		t.Fatalf("get error: %v", gres)
	}
	if !strings.Contains(gres["content"].([]any)[0].(map[string]any)["text"].(string), ".glb") {
		t.Fatalf("text = %v", gres["content"])
	}
}

func TestWebSearch(t *testing.T) {
	rt := newFakeRuntime()
	h := setup(t, false, rt)
	out := decode(t, h.post(rpc("tools/call", map[string]any{
		"name": "web_search", "arguments": map[string]any{"query": "rust"},
	})))
	res := out["result"].(map[string]any)
	if res["isError"].(bool) {
		t.Fatalf("error: %v", res)
	}
	if !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "https://a.example") {
		t.Fatalf("text = %v", res["content"])
	}
}

func TestDecide(t *testing.T) {
	rt := newFakeRuntime("clef")
	h := setup(t, false, rt)
	out := decode(t, h.post(rpc("tools/call", map[string]any{
		"name": "decide",
		"arguments": map[string]any{
			"state": "a cat photo",
			"questions": map[string]any{
				"q1": map[string]any{"type": "choice", "instructions": "cat or dog?",
					"criteria": map[string]any{"cat": "a cat", "dog": "a dog"}},
			},
		},
	})))
	res := out["result"].(map[string]any)
	if res["isError"].(bool) {
		t.Fatalf("error: %v", res)
	}
	structured := res["structuredContent"].(map[string]any)
	if structured["model"] != "clef" {
		t.Fatalf("model = %v", structured)
	}
}

func TestStartAndStopModel(t *testing.T) {
	rt := newFakeRuntime()
	rt.installed["wan"] = true
	h := setup(t, false, rt)
	out := decode(t, h.post(rpc("tools/call", map[string]any{
		"name": "start_model", "arguments": map[string]any{"model": "wan"},
	})))
	res := out["result"].(map[string]any)
	if res["isError"].(bool) {
		t.Fatalf("start error: %v", res)
	}
	if res["structuredContent"].(map[string]any)["status"] != "ready" {
		t.Fatalf("status = %v", res["structuredContent"])
	}
	out = decode(t, h.post(rpc("tools/call", map[string]any{
		"name": "stop_model", "arguments": map[string]any{"model": "wan"},
	})))
	res = out["result"].(map[string]any)
	if res["isError"].(bool) {
		t.Fatalf("stop error: %v", res)
	}
	if len(rt.stopped) != 1 || rt.stopped[0] != "wan" {
		t.Fatalf("stopped = %v", rt.stopped)
	}
}

func TestCreateUploadAndDownload(t *testing.T) {
	h := setup(t, true, newFakeRuntime())
	out := decode(t, h.post(rpc("tools/call", map[string]any{"name": "create_upload", "arguments": map[string]any{}})))
	res := out["result"].(map[string]any)
	if res["isError"].(bool) {
		t.Fatalf("create_upload error: %v", res)
	}
	if !strings.Contains(res["structuredContent"].(map[string]any)["upload_url"].(string), "/link/upload/") {
		t.Fatalf("upload_url = %v", res["structuredContent"])
	}

	name := strings.Repeat("b", 32) + ".png"
	if err := os.MkdirAll(filepath.Join(h.cfg.DataDir, "images"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.cfg.DataDir, "images", name), newPNG(8, 8), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.db.RecordMedia(name, h.userID, "", nil); err != nil {
		t.Fatal(err)
	}
	out = decode(t, h.post(rpc("tools/call", map[string]any{
		"name": "create_download", "arguments": map[string]any{"url": "/images/" + name},
	})))
	res = out["result"].(map[string]any)
	if res["isError"].(bool) {
		t.Fatalf("create_download error: %v", res)
	}
	if res["structuredContent"].(map[string]any)["file"] != name {
		t.Fatalf("file = %v", res["structuredContent"])
	}
}

// --- usage ------------------------------------------------------------------

func TestEveryToolCallRecordsUsage(t *testing.T) {
	h := setup(t, true, newFakeRuntime())
	out := decode(t, h.post(rpc("tools/call", map[string]any{"name": "list_image_models", "arguments": map[string]any{}})))
	if out["result"].(map[string]any)["isError"].(bool) {
		t.Fatalf("unexpected error: %v", out)
	}
	rows, err := h.db.Query("SELECT endpoint, model, source FROM usage_events")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var endpoint, model, source string
		if err := rows.Scan(&endpoint, &model, &source); err != nil {
			t.Fatal(err)
		}
		if endpoint == "mcp:list_image_models" && source == "api_key" {
			found = true
		}
	}
	if !found {
		t.Fatal("no usage_events row for the MCP call")
	}
}

// --- artifacts --------------------------------------------------------------

func TestImageFileRoute(t *testing.T) {
	h := setup(t, false, newFakeRuntime())
	name := strings.Repeat("d", 32) + ".png"
	dir := filepath.Join(h.cfg.DataDir, "images")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), newPNG(16, 16), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.db.RecordMedia(name, h.userID, "", nil); err != nil {
		t.Fatal(err)
	}
	resp, err := h.server.Client().Get(h.server.URL + "/images/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Cache-Control"), "private") {
		t.Fatalf("cache-control = %q", resp.Header.Get("Cache-Control"))
	}
	if resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("content-type = %q", resp.Header.Get("Content-Type"))
	}
	missing, _ := h.server.Client().Get(h.server.URL + "/images/" + strings.Repeat("e", 32) + ".png")
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status = %d", missing.StatusCode)
	}
	traversal, _ := h.server.Client().Get(h.server.URL + "/images/..%2Fhub.db")
	if traversal.StatusCode != http.StatusNotFound {
		t.Fatalf("traversal status = %d", traversal.StatusCode)
	}
}

func TestMediaIsPrivateToItsOwner(t *testing.T) {
	h := setup(t, true, newFakeRuntime())
	name := strings.Repeat("f", 32) + ".png"
	dir := filepath.Join(h.cfg.DataDir, "images")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), newPNG(16, 16), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.db.RecordMedia(name, h.userID, "", nil); err != nil {
		t.Fatal(err)
	}

	anon, err := h.server.Client().Get(h.server.URL + "/images/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer anon.Body.Close()
	if anon.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anon status = %d", anon.StatusCode)
	}
	if anon.Header.Get("WWW-Authenticate") == "" {
		t.Fatal("missing WWW-Authenticate")
	}

	pageReq, _ := http.NewRequest(http.MethodGet, h.server.URL+"/images/"+name, nil)
	pageReq.Header.Set("Accept", "text/html")
	page, err := h.server.Client().Do(pageReq)
	if err != nil {
		t.Fatal(err)
	}
	defer page.Body.Close()
	if page.StatusCode != http.StatusUnauthorized {
		t.Fatalf("html status = %d", page.StatusCode)
	}
	body, _ := io.ReadAll(page.Body)
	if !strings.Contains(string(body), "Sign in") {
		t.Fatalf("html body = %s", body)
	}
}
