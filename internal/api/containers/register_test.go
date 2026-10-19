package containers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"gospark-hub/internal/config"
	"gospark-hub/internal/dockerx"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
	"gospark-hub/internal/web"
)

// fakeDockerScript is the same CLI stand-in the dockerx tests use: argv goes
// to $FAKE_DOCKER_LOG, outputs come from env vars.
const fakeDockerScript = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_DOCKER_LOG" 2>/dev/null
cmd="$1"
shift
case "$cmd" in
  ps)
    if [ "$1" = "-a" ]; then
      printf '%s\n' "$FAKE_PS_ANY"
    else
      case "$*" in
        *Status*) printf '%s\n' "$FAKE_PS_LIST" ;;
        *Label*) printf '%s\n' "$FAKE_PS_PROJECTS" ;;
        *) printf '%s\n' "$FAKE_PS_PROJECTS" ;;
      esac
    fi
    ;;
  inspect) printf '%s\n' "$FAKE_INSPECT" ;;
  logs) printf '%s\n' "$FAKE_LOGS" ;;
  images) printf '%s\n' "$FAKE_IMAGES" ;;
  volume)
    if [ "$1" = "ls" ]; then printf '%s\n' "$FAKE_VOLUMES"; fi
    ;;
  compose) printf '%s\n' "$FAKE_COMPOSE_OUT" ;;
esac
exit 0
`

func installFakeDocker(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(fakeDockerScript), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	logPath := filepath.Join(dir, "args.log")
	t.Setenv("FAKE_DOCKER_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func writeRecipe(t *testing.T, root, slug, recipeYAML, composeYAML string) {
	t.Helper()
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir recipe: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "recipe.yaml"), []byte(recipeYAML), 0o644); err != nil {
		t.Fatalf("write recipe.yaml: %v", err)
	}
	if composeYAML != "" {
		if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(composeYAML), 0o644); err != nil {
			t.Fatalf("write compose: %v", err)
		}
	}
}

func newTestServer(t *testing.T, regDir string) (*httptest.Server, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := config.Config{Project: "testproj", AppProjectPrefix: "testproj", AppNetwork: "testproj-net", DataDir: t.TempDir(), RegistryPath: regDir}
	r := web.NewRouter()
	Register(r, Deps{
		Config:   cfg,
		Registry: registry.NewCache(regDir),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:       db,
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, db
}

func getJSON(t *testing.T, srv *httptest.Server, method, path, body string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func TestInstallFlowAndBuildStatus(t *testing.T) {
	logPath := installFakeDocker(t)
	t.Setenv("FAKE_COMPOSE_OUT", "Building app\n#8 DONE 12.3s")
	regDir := t.TempDir()
	writeRecipe(t, regDir, "demo",
		"name: Demo\nslug: demo\ndocker:\n  build: true\n",
		"services:\n  app:\n    image: testproj-demo\n")
	srv, db := newTestServer(t, regDir)

	// No build yet: idle.
	code, body := getJSON(t, srv, http.MethodGet, "/api/recipes/demo/build-status", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"status":"idle"`) || !strings.Contains(string(body), `"lines":[]`) {
		t.Fatalf("idle build-status: %d %s", code, body)
	}

	// Install: 200 building, work runs in the background.
	code, body = getJSON(t, srv, http.MethodPost, "/api/recipes/demo/install", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"status":"building"`) {
		t.Fatalf("install: %d %s", code, body)
	}

	// Poll until done.
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, body = getJSON(t, srv, http.MethodGet, "/api/recipes/demo/build-status", "")
		if code == http.StatusOK && strings.Contains(string(body), `"status":"done"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("build never finished: %d %s", code, body)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(string(body), "#8 DONE 12.3s") || !strings.Contains(string(body), "installed successfully!") {
		t.Fatalf("build lines missing:\n%s", body)
	}
	if !strings.Contains(string(body), "docker compose -p testproj-demo -f ") || !strings.Contains(string(body), " build") {
		t.Fatalf("build command wrong:\n%s", body)
	}

	installed, _ := db.InstalledRecipes(context.Background())
	if _, ok := installed["demo"]; !ok {
		t.Fatalf("demo not recorded as installed: %#v", installed)
	}

	// A repeated install while the map says building/done but not pending is a
	// fresh build; the important guard is the pending one. Assert the compose
	// project prefix reached the CLI.
	args, _ := os.ReadFile(logPath)
	if !strings.Contains(string(args), "compose -p testproj-demo -f ") {
		t.Fatalf("compose project prefix not used:\n%s", args)
	}
}

func TestInstallUnknownSlug(t *testing.T) {
	installFakeDocker(t)
	srv, _ := newTestServer(t, t.TempDir())
	code, body := getJSON(t, srv, http.MethodPost, "/api/recipes/nope/install", "")
	if code != http.StatusNotFound || !strings.Contains(string(body), "Recipe not found") {
		t.Fatalf("unknown install: %d %s", code, body)
	}
}

func TestLaunchPlanNonLLM(t *testing.T) {
	installFakeDocker(t)
	regDir := t.TempDir()
	writeRecipe(t, regDir, "webapp", "name: Web\nslug: webapp\ncategory: image-gen\nui:\n  type: web\n",
		"services:\n  app:\n    image: testproj-webapp\n")
	srv, _ := newTestServer(t, regDir)
	code, body := getJSON(t, srv, http.MethodGet, "/api/recipes/webapp/launch-plan", "")
	if code != http.StatusOK {
		t.Fatalf("launch-plan: %d %s", code, body)
	}
	var plan struct {
		Stop []string `json:"stop"`
	}
	if err := json.Unmarshal(body, &plan); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if plan.Stop == nil || len(plan.Stop) != 0 {
		t.Fatalf("non-LLM plan should be an empty array, got %s", body)
	}
}

func TestLaunchPlanConflict(t *testing.T) {
	installFakeDocker(t)
	t.Setenv("FAKE_PS_PROJECTS", "testproj-b\ntestproj-c")
	meminfo := filepath.Join(t.TempDir(), "meminfo")
	if err := os.WriteFile(meminfo, []byte("MemTotal: 104857600 kB\nMemAvailable: 10485760 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := dockerx.MeminfoPath
	dockerx.MeminfoPath = meminfo
	t.Cleanup(func() { dockerx.MeminfoPath = old })

	regDir := t.TempDir()
	llm := func(slug string, mem int) string {
		return "name: " + slug + "\nslug: " + slug + "\ncategory: llm\nmin_memory_gb: " + itoa(mem) + "\nui:\n  type: api-only\n"
	}
	writeRecipe(t, regDir, "a", llm("a", 8), "services:\n  app:\n    image: testproj-a\n")
	writeRecipe(t, regDir, "b", llm("b", 20), "services:\n  app:\n    image: testproj-b\n")
	writeRecipe(t, regDir, "c", llm("c", 40), "services:\n  app:\n    image: testproj-c\n")
	srv, db := newTestServer(t, regDir)
	ctx := context.Background()
	_ = db.UpsertInstalledRecipe(ctx, "b", "testproj-b")
	_ = db.UpsertInstalledRecipe(ctx, "c", "testproj-c")

	code, body := getJSON(t, srv, http.MethodGet, "/api/recipes/a/launch-plan", "")
	if code != http.StatusOK {
		t.Fatalf("launch-plan: %d %s", code, body)
	}
	var plan struct {
		Stop []string `json:"stop"`
	}
	if err := json.Unmarshal(body, &plan); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if strings.Join(plan.Stop, ",") != "c,b" {
		t.Fatalf("conflict plan = %s, want [c,b]", body)
	}
}

func TestListContainers(t *testing.T) {
	installFakeDocker(t)
	t.Setenv("FAKE_PS_LIST", "spark-comfyui\tUp 2 hours\tspark-comfyui:latest\t0.0.0.0:8188->8188/tcp")
	srv, _ := newTestServer(t, t.TempDir())
	code, body := getJSON(t, srv, http.MethodGet, "/api/containers", "")
	if code != http.StatusOK {
		t.Fatalf("containers: %d %s", code, body)
	}
	var list []dockerx.ContainerInfo
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(list) != 1 || list[0].Name != "spark-comfyui" || list[0].Ports["8188"] != 8188 {
		t.Fatalf("containers: %s", body)
	}
	if list[0].CPUPercent != 0 || list[0].MemoryMB != 0 {
		t.Fatalf("cpu/memory should be 0: %s", body)
	}
}

func TestComposeGetPutReset(t *testing.T) {
	installFakeDocker(t)
	regDir := t.TempDir()
	compose := "services:\n  app:\n    image: testproj-demo\n"
	writeRecipe(t, regDir, "demo", "name: Demo\nslug: demo\n", compose)
	srv, _ := newTestServer(t, regDir)

	code, body := getJSON(t, srv, http.MethodGet, "/api/recipes/demo/compose", "")
	if code != http.StatusOK {
		t.Fatalf("get compose: %d %s", code, body)
	}
	var got struct {
		Content        string `json:"content"`
		DefaultContent string `json:"default_content"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Content != compose {
		t.Fatalf("content = %q, want %q", got.Content, compose)
	}
	// Outside a git repo the git lookup fails, so default_content == content.
	if got.DefaultContent != got.Content {
		t.Fatalf("default_content fallback failed: %q", got.DefaultContent)
	}

	updated := "services:\n  app:\n    image: testproj-demo:v2\n"
	code, body = getJSON(t, srv, http.MethodPut, "/api/recipes/demo/compose", `{"content":`+jsonString(updated)+`}`)
	if code != http.StatusOK || !strings.Contains(string(body), "saved") {
		t.Fatalf("put compose: %d %s", code, body)
	}
	onDisk, _ := os.ReadFile(filepath.Join(regDir, "demo", "docker-compose.yml"))
	if string(onDisk) != updated {
		t.Fatalf("compose not written byte for byte: %q", onDisk)
	}

	// No git repo here: reset must 404, exactly like upstream when git fails.
	code, body = getJSON(t, srv, http.MethodPost, "/api/recipes/demo/compose/reset", "")
	if code != http.StatusNotFound || !strings.Contains(string(body), "Default docker-compose.yml not available") {
		t.Fatalf("reset without git: %d %s", code, body)
	}
}

func TestBuildWebSocketFraming(t *testing.T) {
	installFakeDocker(t)
	t.Setenv("FAKE_COMPOSE_OUT", "step one\nstep two")
	regDir := t.TempDir()
	writeRecipe(t, regDir, "demo",
		"name: Demo\nslug: demo\ndocker:\n  build: true\n",
		"services:\n  app:\n    image: testproj-demo\n")
	srv, _ := newTestServer(t, regDir)

	code, body := getJSON(t, srv, http.MethodPost, "/api/recipes/demo/install", "")
	if code != http.StatusOK {
		t.Fatalf("install: %d %s", code, body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/build/demo"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial build ws: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	var messages []string
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (messages so far: %#v)", err, messages)
		}
		text := string(data)
		messages = append(messages, text)
		if text == "[done]" {
			break
		}
	}

	// Every message except the final [done] is a build line, exactly as it
	// arrived; [done] appears once and last.
	if !strings.Contains(strings.Join(messages, "\n"), "step two") {
		t.Fatalf("build lines missing: %#v", messages)
	}
	doneCount := 0
	for i, m := range messages {
		if m == "[done]" {
			doneCount++
			if i != len(messages)-1 {
				t.Fatalf("[done] must be last: %#v", messages)
			}
		}
	}
	if doneCount != 1 {
		t.Fatalf("want exactly one [done], got %d: %#v", doneCount, messages)
	}
}

func TestBuildWebSocketWithoutBuild(t *testing.T) {
	installFakeDocker(t)
	srv, _ := newTestServer(t, t.TempDir())

	oldTicks, oldTick := buildWaitTicks, buildWaitTick
	buildWaitTicks, buildWaitTick = 2, 5*time.Millisecond
	t.Cleanup(func() { buildWaitTicks, buildWaitTick = oldTicks, oldTick })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/build/unknown"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial build ws: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "[done]" {
		t.Fatalf("want [done], got %q", data)
	}
}

func TestLogsWebSocketContainerNotRunning(t *testing.T) {
	installFakeDocker(t)
	regDir := t.TempDir()
	writeRecipe(t, regDir, "demo", "name: Demo\nslug: demo\n", "services:\n  app:\n    image: testproj-demo\n")
	srv, _ := newTestServer(t, regDir)

	oldTicks, oldTick := logsWaitTicks, logsWaitTick
	logsWaitTicks, logsWaitTick = 2, 5*time.Millisecond
	t.Cleanup(func() { logsWaitTicks, logsWaitTick = oldTicks, oldTick })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/logs/demo"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial logs ws: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	var messages []string
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		messages = append(messages, string(data))
	}
	joined := strings.Join(messages, "\n")
	if strings.Count(joined, "Waiting for container to start...") != 1 {
		t.Fatalf("wait line should appear exactly once:\n%s", joined)
	}
	if !strings.Contains(joined, "[spark-ai-hub] Container not running") {
		t.Fatalf("missing terminal line:\n%s", joined)
	}
}

func TestLogsWebSocketNoLogDriverStillDeliversReady(t *testing.T) {
	installFakeDocker(t)
	t.Setenv("FAKE_PS_ANY", "testproj-demo-1")
	t.Setenv("FAKE_INSPECT", "none")

	// A fake app on the recipe's UI port turns the health check ready, so the
	// no-log path has a verdict to deliver.
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer app.Close()
	parsed, err := url.Parse(app.URL)
	if err != nil {
		t.Fatalf("parse app url: %v", err)
	}

	oldInterval, oldTicks := dockerx.DefaultHealthInterval, dockerx.DefaultHealthTicks
	dockerx.DefaultHealthInterval, dockerx.DefaultHealthTicks = 20*time.Millisecond, 100
	t.Cleanup(func() { dockerx.DefaultHealthInterval, dockerx.DefaultHealthTicks = oldInterval, oldTicks })
	oldReadyTicks, oldReadyTick := readyWaitTicks, readyWaitTick
	readyWaitTicks, readyWaitTick = 200, 10*time.Millisecond
	t.Cleanup(func() { readyWaitTicks, readyWaitTick = oldReadyTicks, oldReadyTick })

	regDir := t.TempDir()
	recipeYAML := "name: Demo\nslug: demo\nui:\n  type: web\n  port: " + parsed.Port() + "\n"
	writeRecipe(t, regDir, "demo", recipeYAML, "services:\n  app:\n    image: testproj-demo\n    logging:\n      driver: none\n")
	srv, _ := newTestServer(t, regDir)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/logs/demo"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial logs ws: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	var messages []string
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		messages = append(messages, string(data))
	}
	joined := strings.Join(messages, "\n")
	if !strings.Contains(joined, "keeps no logs") {
		t.Fatalf("missing no-log message:\n%s", joined)
	}
	if !strings.Contains(joined, "[spark-ai-hub:ready]") {
		t.Fatalf("no-log path must still deliver the ready verdict:\n%s", joined)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
