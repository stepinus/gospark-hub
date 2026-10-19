package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gospark-hub/internal/config"
	"gospark-hub/internal/registry"
)

// testConfig is the upstream shape: daemon 9110, public 9100, auth on. Ports
// are normalised in the golden comparison, so the exact numbers do not have to
// match upstream's 9010/9000.
func testConfig(t *testing.T) config.Config {
	t.Helper()
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, probeTokenName), []byte("test-token"), 0o600); err != nil {
		t.Fatalf("write probe token: %v", err)
	}
	return config.Config{
		Host:             "0.0.0.0",
		Port:             9110,
		PublicPort:       9100,
		ProxyEnabled:     true,
		AuthEnabled:      true,
		Project:          "gospark-hub",
		AppNetwork:       "spark-ai-hub-net",
		AppProjectPrefix: "spark-ai-hub",
		RegistryPath:     filepath.Join("..", "..", "registry", "recipes"),
		DataDir:          dataDir,
	}
}

func loadRecipes(t *testing.T, cfg config.Config) []registry.Recipe {
	t.Helper()
	cache := registry.NewCache(cfg.RegistryPath)
	if err := cache.Load(); err != nil {
		t.Fatalf("load registry: %v", err)
	}
	recipes, err := cache.All()
	if err != nil {
		t.Fatalf("list recipes: %v", err)
	}
	return recipes
}

var (
	siteLineRe   = regexp.MustCompile(`(?m)^:\d+ \{`)
	daemonHostRe = regexp.MustCompile(`host\.docker\.internal:\d+`)
	probeTokenRe = regexp.MustCompile(`(header X-Sah-Probe ")[^"]*(")`)
)

// normalizeCaddy removes the two things that legitimately differ between the
// upstream reference deployment and this one: the probe token and the port
// pair (public site + daemon behind forward_auth).
func normalizeCaddy(text string, cfg config.Config) string {
	text = siteLineRe.ReplaceAllString(text, fmt.Sprintf(":%d {", cfg.PublicPort))
	text = daemonHostRe.ReplaceAllString(text, fmt.Sprintf("host.docker.internal:%d", cfg.Port))
	text = probeTokenRe.ReplaceAllString(text, "${1}<token>${2}")
	return text
}

func firstDiff(got, want string) string {
	gotLines := strings.Split(got, "\n")
	wantLines := strings.Split(want, "\n")
	for i := 0; i < len(gotLines) && i < len(wantLines); i++ {
		if gotLines[i] != wantLines[i] {
			return fmt.Sprintf("line %d:\n  got:  %q\n  want: %q", i+1, gotLines[i], wantLines[i])
		}
	}
	return fmt.Sprintf("line count differs: got %d, want %d", len(gotLines), len(wantLines))
}

// TestCaddyfileGolden is the phase gate: the generated file must match the
// upstream reference built from the same 148 recipes, line for line.
func TestCaddyfileGolden(t *testing.T) {
	cfg := testConfig(t)
	recipes := loadRecipes(t, cfg)
	got := normalizeCaddy(Render(cfg, recipes), cfg)

	raw, err := os.ReadFile(filepath.Join("testdata", "upstream.Caddyfile"))
	if err != nil {
		t.Fatalf("read reference: %v", err)
	}
	want := normalizeCaddy(string(raw), cfg)

	if got != want {
		t.Fatalf("generated Caddyfile differs from testdata/upstream.Caddyfile: %s", firstDiff(got, want))
	}
}

// parseRoutes reads the generated config back into slug -> container:port,
// which is what Caddy will dial. It is intentionally a parser over the text,
// not over the recipes: it proves the rendered file, not the input.
func parseRoutes(text string) map[string]string {
	routes := map[string]string{}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		slug, ok := strings.CutPrefix(line, "\t# ")
		if !ok || strings.Contains(slug, "Everything else") {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], "\t\treverse_proxy ") {
				routes[slug] = strings.TrimPrefix(lines[j], "\t\treverse_proxy ")
				break
			}
		}
	}
	return routes
}

func TestCaddyfileRoutes(t *testing.T) {
	cfg := testConfig(t)
	text := Render(cfg, loadRecipes(t, cfg))
	routes := parseRoutes(text)

	if len(routes) != 141 {
		t.Fatalf("routes: got %d, want 141", len(routes))
	}
	cases := map[string]string{
		"acestep":          "spark-ai-hub-acestep:7860",
		"facefusion-spark": "spark-ai-hub-facefusion:7860",
		"trellis2-spark":   "spark-ai-hub-trellis2:12156",
		"vllm-qwen35-4b":   "spark-ai-hub-vllm-qwen35-4b:8000",
	}
	for slug, want := range cases {
		if got := routes[slug]; got != want {
			t.Errorf("route %s: got %q, want %q", slug, got, want)
		}
	}

	// strip_prefix: false keeps the prefix on (minimax-music3), everything
	// else uses handle_path and strips it in the probe too.
	if !strings.Contains(text, "\thandle /run/minimax-music3/* {") {
		t.Error("minimax-music3 should use handle (strip_prefix: false)")
	}
	if strings.Contains(text, "\turi strip_prefix /run/minimax-music3") {
		t.Error("minimax-music3 probe must not strip the prefix")
	}
	if !strings.Contains(text, "\thandle_path /run/acestep/* {") {
		t.Error("acestep should use handle_path")
	}
	if !strings.Contains(text, "\t\turi strip_prefix /run/acestep") {
		t.Error("acestep probe should strip the prefix")
	}

	// The order of the three rules is load-bearing: probe, redirect, guard.
	probe := strings.Index(text, "\t@probe-acestep {")
	redir := strings.Index(text, "\tredir /run/acestep /run/acestep/ 308")
	guard := strings.Index(text, "\thandle_path /run/acestep/* {")
	if probe < 0 || redir < 0 || guard < 0 || !(probe < redir && redir < guard) {
		t.Errorf("rule order is wrong: probe=%d redir=%d guard=%d", probe, redir, guard)
	}
}

func TestCaddyfileEmptyRegistry(t *testing.T) {
	cfg := testConfig(t)
	_ = os.Remove(filepath.Join(cfg.DataDir, probeTokenName))

	text := Render(cfg, nil)
	if !strings.HasPrefix(text, "# Generated by Spark AI Hub") {
		t.Error("missing generated header")
	}
	if strings.Contains(text, "/run/") {
		t.Error("empty registry must not render app routes")
	}
	if !strings.Contains(text, fmt.Sprintf("\t\treverse_proxy host.docker.internal:%d", cfg.Port)) {
		t.Error("fallback to the daemon is missing")
	}
	// Rendering is what creates the probe token, so health checks can use it
	// before Caddy is up.
	if data, err := os.ReadFile(filepath.Join(cfg.DataDir, probeTokenName)); err != nil || strings.TrimSpace(string(data)) == "" {
		t.Fatalf("probe token was not created: %v", err)
	}
}

func TestCaddyfileRecipeWithoutProxy(t *testing.T) {
	cfg := testConfig(t)
	cfg.Project = "test"
	cfg.RegistryPath = t.TempDir() // no compose files: fallback names

	recipes := []registry.Recipe{
		{Slug: "no-proxy", UI: registry.RecipeUI{Type: "web", Port: 1111}},
		{Slug: "not-servable", UI: registry.RecipeUI{Type: "cli", Port: 2222, Proxy: true}},
		{Slug: "api", UI: registry.RecipeUI{Type: "api-only", Port: 3333, Proxy: true, StripPrefix: true}},
		{Slug: "web", UI: registry.RecipeUI{Type: "web", Port: 4444, Proxy: true, StripPrefix: false}},
	}
	text := Render(cfg, recipes)
	routes := parseRoutes(text)

	if _, ok := routes["no-proxy"]; ok {
		t.Error("recipe with ui.proxy=false must not get a route")
	}
	if _, ok := routes["not-servable"]; ok {
		t.Error("recipe with an unsupported ui.type must not get a route")
	}
	if got, want := routes["api"], "test-api:3333"; got != want {
		t.Errorf("api route: got %q, want %q", got, want)
	}
	if got, want := routes["web"], "test-web:4444"; got != want {
		t.Errorf("web route: got %q, want %q", got, want)
	}
	if !strings.Contains(text, "\thandle_path /run/api/* {") {
		t.Error("strip_prefix=true should render handle_path")
	}
	if !strings.Contains(text, "\thandle /run/web/* {") {
		t.Error("strip_prefix=false should render handle")
	}
}

func TestListenPortProxyDisabled(t *testing.T) {
	cfg := config.Config{Port: 9110, PublicPort: 9100}
	// The daemon itself must answer on the public port when Caddy is off,
	// otherwise nothing listens there (upstream R3).
	if got := ListenPort(cfg); got != 9100 {
		t.Fatalf("proxy disabled: got port %d, want 9100", got)
	}
	cfg.ProxyEnabled = true
	if got := ListenPort(cfg); got != 9110 {
		t.Fatalf("proxy enabled: got port %d, want 9110", got)
	}
}

// fakeDocker stands in for the docker CLI. It models just enough state for
// Ensure: the shared network, the proxy container's status, and the argv it
// was asked to run.
type fakeDocker struct {
	calls        [][]string
	status       string // "" = absent, otherwise inspect's State.Status
	networkReady bool
}

func (f *fakeDocker) run(_ context.Context, args ...string) (string, int, error) {
	f.calls = append(f.calls, args)
	switch {
	case args[0] == "network" && args[1] == "inspect":
		if f.networkReady {
			return "", 0, nil
		}
		return "", 1, nil
	case args[0] == "network" && args[1] == "create":
		f.networkReady = true
		return "", 0, nil
	case args[0] == "inspect" && args[2] == "{{.State.Status}}":
		if f.status == "" {
			return "", 1, nil
		}
		return f.status, 0, nil
	case args[0] == "run":
		f.status = "running"
		return "cid", 0, nil
	case args[0] == "rm":
		f.status = ""
		return "", 0, nil
	}
	return "", 0, nil
}

func (f *fakeDocker) first(command string) []string {
	for _, call := range f.calls {
		if call[0] == command {
			return call
		}
	}
	return nil
}

func (f *fakeDocker) count(command string) int {
	n := 0
	for _, call := range f.calls {
		if call[0] == command {
			n++
		}
	}
	return n
}

func installFakeDocker(t *testing.T, fake *fakeDocker) {
	t.Helper()
	oldRun, oldLook := runDocker, lookPath
	t.Cleanup(func() { runDocker, lookPath = oldRun, oldLook })
	runDocker = fake.run
	lookPath = func(string) (string, error) { return "/usr/bin/docker", nil }
}

func TestEnsureProxyDisabledTouchesNothing(t *testing.T) {
	fake := &fakeDocker{}
	installFakeDocker(t, fake)

	cfg := testConfig(t)
	cfg.ProxyEnabled = false
	cache := registry.NewCache(cfg.RegistryPath)

	if err := Ensure(context.Background(), cfg, cache, slog.Default()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("proxy disabled must not call docker (got %d calls)", len(fake.calls))
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, caddyfileName)); !os.IsNotExist(err) {
		t.Fatalf("proxy disabled must not write a Caddyfile: %v", err)
	}
}

func TestEnsureCreatesProxyContainer(t *testing.T) {
	fake := &fakeDocker{status: "exited"}
	installFakeDocker(t, fake)

	cfg := testConfig(t)
	if err := Ensure(context.Background(), cfg, registry.NewCache(cfg.RegistryPath), slog.Default()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if !fake.networkReady {
		t.Error("shared network was not created")
	}
	if fake.count("rm") != 1 {
		t.Errorf("stopped container should be removed first, rm calls: %d", fake.count("rm"))
	}

	run := fake.first("run")
	want := []string{
		"run", "-d",
		"--name", "gospark-hub-proxy",
		"--restart", "unless-stopped",
		"--network", "spark-ai-hub-net",
		"--add-host", "host.docker.internal:host-gateway",
		"-p", "9100:9100",
		"-v", filepath.Join(cfg.DataDir, caddyfileName) + ":/etc/caddy/Caddyfile:ro",
		"caddy:2-alpine",
	}
	if !slices.Equal(run, want) {
		t.Errorf("docker run args:\n got: %q\nwant: %q", run, want)
	}
	if fake.count("exec") != 0 {
		t.Error("a fresh container must not be reloaded")
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, caddyfileName)); err != nil {
		t.Fatalf("Caddyfile was not written: %v", err)
	}
}

func TestEnsureReloadsOnlyWhenConfigChanges(t *testing.T) {
	fake := &fakeDocker{status: "running", networkReady: true}
	installFakeDocker(t, fake)

	cfg := testConfig(t)
	// A stale file makes the next Render count as a change.
	if err := os.WriteFile(filepath.Join(cfg.DataDir, caddyfileName), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := registry.NewCache(cfg.RegistryPath)

	if err := Ensure(context.Background(), cfg, cache, slog.Default()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	execCall := fake.first("exec")
	want := []string{"exec", "gospark-hub-proxy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"}
	if !slices.Equal(execCall, want) {
		t.Errorf("reload args:\n got: %q\nwant: %q", execCall, want)
	}

	// Same registry, same ports: the second pass has nothing to do.
	fake.calls = nil
	if err := Ensure(context.Background(), cfg, cache, slog.Default()); err != nil {
		t.Fatalf("Ensure again: %v", err)
	}
	if fake.count("exec") != 0 || fake.count("run") != 0 {
		t.Errorf("unchanged config must not reload or recreate: %q", fake.calls)
	}
}
