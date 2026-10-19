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

// nativeBuildRecipe is the recipe whose engine is a client built from the
// model repository's image, not the release installed on the host.
const nativeBuildRecipe = `name: Qwen Native Patched
slug: qwen-native-patched
kind: native
category: llm
ui:
  type: api-only
  port: 8888
native:
  engine: tensorfold
  model: "hf:nvidia/Qwen3.8-Flash-Next@b9ce18a6"
  args: ["--context", "262144"]
  env:
    TF_FLASHNEXT_DRAFTS: "6"
  engine_build:
    image: "tensorfold-qwen38:zig-db28187"
    artifacts: ["native/bin", "share", "python", "fatbin"]
    env:
      TENSORFOLD_CUDA_KERNELS: "share/tensorfold/cuda/sm121"
      PYTHONPATH: "python"
`

// engineDocker answers the docker commands a client build issues, so the
// install route runs without a daemon.
type engineDocker struct {
	images map[string]bool
	calls  [][]string
}

func (d *engineDocker) manager(home string) *engines.Manager {
	m := engines.NewManager(home)
	m.LookPath = func(string) (string, error) { return "/usr/bin/docker", nil }
	m.Run = func(_ context.Context, _ string, args ...string) (string, int, error) {
		d.calls = append(d.calls, args)
		switch {
		case len(args) >= 4 && args[0] == "docker" && args[1] == "image" && args[2] == "inspect":
			if d.images[args[3]] {
				return "sha256:abc", 0, nil
			}
			return "no such image", 1, nil
		case len(args) >= 3 && args[0] == "docker" && args[1] == "create":
			return "container123\n", 0, nil
		case len(args) >= 4 && args[0] == "docker" && args[1] == "cp":
			src := strings.TrimPrefix(strings.TrimPrefix(args[2], "container123:"), "/")
			if err := os.MkdirAll(args[3], 0o755); err != nil {
				return "", -1, err
			}
			name := "marker"
			if filepath.Base(src) == "bin" {
				name = "tensorfold-native"
			}
			if err := os.WriteFile(filepath.Join(args[3], name), []byte("#!/bin/sh\n"), 0o755); err != nil {
				return "", -1, err
			}
			return "", 0, nil
		}
		return "", 0, nil
	}
	return m
}

// newBuildServer mounts the container routes over a registry holding the
// patched recipe and a container recipe, with docker faked out.
func newBuildServer(t *testing.T, engineHome string, dock *engineDocker) (*httptest.Server, *systemdStub) {
	t.Helper()
	regDir := t.TempDir()
	writeRecipe(t, regDir, "qwen-native-patched", nativeBuildRecipe, "")
	writeRecipe(t, regDir, "demo", "name: Demo\nslug: demo\ncategory: image-gen\n", "")

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	stub := &systemdStub{}
	r := web.NewRouter()
	Register(r, Deps{
		Config: config.Config{
			Project:          "testproj",
			AppProjectPrefix: "testproj",
			AppNetwork:       "testproj-net",
			DataDir:          t.TempDir(),
			RegistryPath:     regDir,
		},
		Registry: registry.NewCache(regDir),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:       db,
		Engines:  dock.manager(engineHome),
		Native:   stub.runner(),
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, stub
}

func TestEngineBuildInstallWithoutImageIsConflict(t *testing.T) {
	engineHome := filepath.Join(t.TempDir(), "engines")
	dock := &engineDocker{images: map[string]bool{}}
	srv, _ := newBuildServer(t, engineHome, dock)

	code, body := getJSON(t, srv, http.MethodPost, "/api/recipes/qwen-native-patched/install", "")
	if code != http.StatusConflict {
		t.Fatalf("install without image: %d %s", code, body)
	}
	if !strings.Contains(string(body), "Engine image tensorfold-qwen38:zig-db28187 is not built") {
		t.Fatalf("detail = %s", body)
	}
	if _, err := os.Stat(filepath.Join(engineHome, "tensorfold", "builds", "qwen-native-patched")); !os.IsNotExist(err) {
		t.Fatalf("client dir created without an image: %v", err)
	}

	// Launch has nothing to run either, and says so specifically.
	code, body = getJSON(t, srv, http.MethodPost, "/api/recipes/qwen-native-patched/launch", "")
	if code != http.StatusConflict || !strings.Contains(string(body), "Engine client is not built") {
		t.Fatalf("launch without client: %d %s", code, body)
	}
}

func TestEngineBuildLifecycle(t *testing.T) {
	t.Setenv("HF_HUB_CACHE", t.TempDir())
	engineHome := filepath.Join(t.TempDir(), "engines")
	dock := &engineDocker{images: map[string]bool{"tensorfold-qwen38:zig-db28187": true}}
	srv, stub := newBuildServer(t, engineHome, dock)

	buildDir := filepath.Join(engineHome, "tensorfold", "builds", "qwen-native-patched")
	code, body := getJSON(t, srv, http.MethodPost, "/api/recipes/qwen-native-patched/install", "")
	if code != http.StatusOK {
		t.Fatalf("install: %d %s", code, body)
	}
	var installed struct {
		Slug        string `json:"slug"`
		Status      string `json:"status"`
		EngineBuild string `json:"engine_build"`
	}
	if err := json.Unmarshal(body, &installed); err != nil {
		t.Fatalf("install body: %v (%s)", err, body)
	}
	if installed.Slug != "qwen-native-patched" || installed.Status != "installed" || installed.EngineBuild != buildDir {
		t.Fatalf("install = %+v", installed)
	}
	if _, err := os.Stat(filepath.Join(buildDir, "build.json")); err != nil {
		t.Fatalf("build.json: %v", err)
	}

	// Launch runs the client's binary, not the release under current/, with
	// the block's env expanded against the client directory.
	code, body = getJSON(t, srv, http.MethodPost, "/api/recipes/qwen-native-patched/launch", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"status":"launched"`) {
		t.Fatalf("launch: %d %s", code, body)
	}
	args := stub.last()
	if args[0] != "systemd-run" {
		t.Fatalf("launch ran %q", args)
	}
	wantBin := filepath.Join(buildDir, "bin", "tensorfold-native")
	if !slices.Contains(args, wantBin) {
		t.Fatalf("launch args %q missing client binary %q", args, wantBin)
	}
	for _, env := range []string{
		"--setenv=PYTHONPATH=" + filepath.Join(buildDir, "python"),
		"--setenv=TENSORFOLD_CUDA_KERNELS=" + filepath.Join(buildDir, "share/tensorfold/cuda/sm121"),
		"--setenv=TF_FLASHNEXT_DRAFTS=6",
	} {
		if !slices.Contains(args, env) {
			t.Fatalf("launch args %q missing %q", args, env)
		}
	}
	wantModel := filepath.Join(os.Getenv("HF_HUB_CACHE"), "models--nvidia--Qwen3.8-Flash-Next", "snapshots", "b9ce18a6")
	if !slices.Contains(args, wantModel) {
		t.Fatalf("launch args %q missing resolved model %q", args, wantModel)
	}

	// Delete stops the unit and drops the client directory.
	code, body = getJSON(t, srv, http.MethodDelete, "/api/recipes/qwen-native-patched/install", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"status":"removed"`) {
		t.Fatalf("remove install: %d %s", code, body)
	}
	if got := stub.last(); !slices.Equal(got, []string{"systemctl", "--user", "stop", "gospark-native-qwen-native-patched"}) {
		t.Fatalf("remove install ran %q", got)
	}
	if _, err := os.Stat(buildDir); !os.IsNotExist(err) {
		t.Fatalf("client dir survived removal: %v", err)
	}

	// And the process can no longer be launched from a vanished client.
	code, body = getJSON(t, srv, http.MethodPost, "/api/recipes/qwen-native-patched/launch", "")
	if code != http.StatusConflict || !strings.Contains(string(body), "Engine client is not built") {
		t.Fatalf("launch after removal: %d %s", code, body)
	}
}

func TestEngineBuildDeleteRejectsContainerRecipe(t *testing.T) {
	srv, stub := newBuildServer(t, filepath.Join(t.TempDir(), "engines"), &engineDocker{})
	code, body := getJSON(t, srv, http.MethodDelete, "/api/recipes/demo/install", "")
	if code != http.StatusConflict || !strings.Contains(string(body), "Recipe has no engine build") {
		t.Fatalf("delete install on container recipe: %d %s", code, body)
	}
	if stub.last() != nil {
		t.Fatalf("remove install touched systemd: %q", stub.last())
	}
}
