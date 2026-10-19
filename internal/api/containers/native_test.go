package containers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gospark-hub/internal/config"
	"gospark-hub/internal/engines"
	"gospark-hub/internal/native"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
	"gospark-hub/internal/web"
)

const nativeRecipe = `name: Qwen Native
slug: qwen-native
kind: native
category: llm
ui:
  type: api-only
  port: 8888
native:
  engine: tensorfold
  model: "hf:nvidia/Qwen3.8-Flash-Next@b9ce18a6"
  args: ["--context", "262144"]
`

// systemdStub records every systemd command and answers is-active from active.
type systemdStub struct {
	active bool
	calls  [][]string
}

func (s *systemdStub) runner() *native.Runner {
	return &native.Runner{
		LookPath: func(string) (string, error) { return "/bin/true", nil },
		Run: func(_ context.Context, args ...string) (string, int, error) {
			s.calls = append(s.calls, args)
			if len(args) >= 3 && args[0] == "systemctl" && args[2] == "is-active" {
				if s.active {
					return "active", 0, nil
				}
				return "inactive", 3, nil
			}
			return "", 0, nil
		},
	}
}

func (s *systemdStub) last() []string {
	if len(s.calls) == 0 {
		return nil
	}
	return s.calls[len(s.calls)-1]
}

// installFakeEngine writes the release layout the manager expects:
// tensorfold/<version>/bin/tensorfold-native plus the current symlink.
func installFakeEngine(t *testing.T, home, version string) {
	t.Helper()
	binDir := filepath.Join(home, "tensorfold", version, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir engine: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "tensorfold-native"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write engine: %v", err)
	}
	if err := os.Symlink(version, filepath.Join(home, "tensorfold", "current")); err != nil {
		t.Fatalf("link engine: %v", err)
	}
}

func newNativeServer(t *testing.T, engineHome string) (*httptest.Server, *systemdStub) {
	t.Helper()
	regDir := t.TempDir()
	writeRecipe(t, regDir, "qwen-native", nativeRecipe, "")

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := config.Config{
		Project:          "testproj",
		AppProjectPrefix: "testproj",
		AppNetwork:       "testproj-net",
		DataDir:          t.TempDir(),
		RegistryPath:     regDir,
	}
	stub := &systemdStub{}
	r := web.NewRouter()
	Register(r, Deps{
		Config:   cfg,
		Registry: registry.NewCache(regDir),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:       db,
		Engines:  engines.NewManager(engineHome),
		Native:   stub.runner(),
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, stub
}

func TestNativeLaunchWithoutEngineIsConflict(t *testing.T) {
	srv, _ := newNativeServer(t, filepath.Join(t.TempDir(), "engines"))

	code, body := getJSON(t, srv, http.MethodPost, "/api/recipes/qwen-native/launch", "")
	if code != http.StatusConflict {
		t.Fatalf("launch without engine: %d %s", code, body)
	}
	if !strings.Contains(string(body), "Engine tensorfold is not installed") {
		t.Fatalf("detail = %s", body)
	}
}

func TestNativeLifecycle(t *testing.T) {
	cacheHome := t.TempDir()
	t.Setenv("HF_HUB_CACHE", cacheHome)
	engineHome := filepath.Join(t.TempDir(), "engines")
	installFakeEngine(t, engineHome, "1.1.1")
	srv, stub := newNativeServer(t, engineHome)

	// Install is a no-op for a native recipe: the UI keeps the same button.
	code, body := getJSON(t, srv, http.MethodPost, "/api/recipes/qwen-native/install", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"status":"installed"`) {
		t.Fatalf("install: %d %s", code, body)
	}

	// Launch starts the unit with the recipe's own args plus port and name.
	code, body = getJSON(t, srv, http.MethodPost, "/api/recipes/qwen-native/launch", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"status":"launched"`) {
		t.Fatalf("launch: %d %s", code, body)
	}
	args := stub.last()
	if args[0] != "systemd-run" {
		t.Fatalf("launch ran %q, want systemd-run", args)
	}
	wantModel := filepath.Join(cacheHome, "models--nvidia--Qwen3.8-Flash-Next", "snapshots", "b9ce18a6")
	if !slices.Contains(args, "--unit=gospark-native-qwen-native") {
		t.Fatalf("launch args %q missing unit", args)
	}
	if !slices.Contains(args, wantModel) {
		t.Fatalf("launch args %q missing resolved model %q", args, wantModel)
	}
	if slices.Contains(args, "--setenv") {
		t.Fatalf("launch args %q carry an env flag for a recipe without env", args)
	}
	if !slices.Contains(args, "--port") || !slices.Contains(args, "8888") {
		t.Fatalf("launch args %q missing --port 8888", args)
	}
	if !slices.Contains(args, "Qwen Native") {
		t.Fatalf("launch args %q missing served name", args)
	}
	stub.active = true

	// A second launch while the unit is active is a 409.
	code, body = getJSON(t, srv, http.MethodPost, "/api/recipes/qwen-native/launch", "")
	if code != http.StatusConflict || !strings.Contains(string(body), "already running") {
		t.Fatalf("second launch: %d %s", code, body)
	}

	// Stop goes through systemctl and is idempotent.
	code, body = getJSON(t, srv, http.MethodPost, "/api/recipes/qwen-native/stop", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"status":"stopped"`) {
		t.Fatalf("stop: %d %s", code, body)
	}
	if got := stub.last(); !slices.Equal(got, []string{"systemctl", "--user", "stop", "gospark-native-qwen-native"}) {
		t.Fatalf("stop args = %q", got)
	}

	// Remove and purge are the same for a native recipe: stop, no images.
	code, body = getJSON(t, srv, http.MethodDelete, "/api/recipes/qwen-native", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"status":"removed"`) {
		t.Fatalf("remove: %d %s", code, body)
	}
	code, body = getJSON(t, srv, http.MethodPost, "/api/recipes/qwen-native/purge", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"status":"removed"`) {
		t.Fatalf("purge: %d %s", code, body)
	}
}

// TestNativeRecipeJSONKind pins the recipe JSON contract for a recipe that
// comes through the shared cache: kind and the native block are present.
func TestNativeRecipeJSONKind(t *testing.T) {
	dir := t.TempDir()
	writeRecipe(t, dir, "qwen-native", nativeRecipe, "")
	cache := registry.NewCache(dir)
	got, ok := cache.Get("qwen-native")
	if !ok {
		t.Fatal("recipe not found")
	}
	if !got.IsNative() || got.Kind != "native" {
		t.Fatalf("kind = %q native = %v", got.Kind, got.Native)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"kind":"native"`) {
		t.Fatalf("native JSON = %s", raw)
	}
}
