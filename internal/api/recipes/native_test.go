package recipes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gospark-hub/internal/engines"
	"gospark-hub/internal/native"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/web"
)

// scriptedRunner answers Running from active and never touches systemd.
type scriptedRunner struct{ active bool }

func (s scriptedRunner) runner() *native.Runner {
	return &native.Runner{
		Run: func(_ context.Context, args ...string) (string, int, error) {
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

func writeNativeRecipe(t *testing.T, dir, slug, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, slug), 0o755); err != nil {
		t.Fatalf("mkdir recipe: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, slug, "recipe.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write recipe: %v", err)
	}
}

const nativeAPITestRecipe = `name: Qwen Native
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

const containerAPITestRecipe = `name: Demo
slug: demo
category: image-gen
`

func installFakeEngineHome(t *testing.T, home, version string) {
	t.Helper()
	bin := filepath.Join(home, "tensorfold", version, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir engine: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bin, "tensorfold-native"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write engine: %v", err)
	}
	if err := os.Symlink(version, filepath.Join(home, "tensorfold", "current")); err != nil {
		t.Fatalf("link engine: %v", err)
	}
}

func newNativeAPIServer(t *testing.T, engineHome string, active bool) *web.Router {
	t.Helper()
	dir := t.TempDir()
	writeNativeRecipe(t, dir, "qwen-native", nativeAPITestRecipe)
	writeNativeRecipe(t, dir, "demo", containerAPITestRecipe)

	r := web.NewRouter()
	Register(r, Deps{
		Cache:   registry.NewCache(dir),
		Engines: engines.NewManager(engineHome),
		Native:  scriptedRunner{active: active}.runner(),
	})
	return r
}

func TestNativeRuntimeFlags(t *testing.T) {
	home := filepath.Join(t.TempDir(), "engines")
	installFakeEngineHome(t, home, "1.1.1")
	r := newNativeAPIServer(t, home, true)

	req := httptest.NewRequest(http.MethodGet, "/api/recipes/qwen-native", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status: %d", rec.Code)
	}
	var body registry.Recipe
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Kind != "native" {
		t.Fatalf("kind = %q", body.Kind)
	}
	// The engine is installed and the unit is active: installed/running/ready.
	if !body.Installed || !body.Running || !body.Ready {
		t.Fatalf("flags installed=%v running=%v ready=%v", body.Installed, body.Running, body.Ready)
	}
	if body.Starting || body.Installing || body.HasLeftovers || body.Error != nil {
		t.Fatalf("native recipe carries container state: starting=%v installing=%v leftovers=%v err=%v",
			body.Starting, body.Installing, body.HasLeftovers, body.Error)
	}
	if !strings.Contains(rec.Body.String(), `"native":{`) {
		t.Fatalf("native block missing from %s", rec.Body.String())
	}
}

func TestNativeWithoutEngineIsNotInstalled(t *testing.T) {
	r := newNativeAPIServer(t, filepath.Join(t.TempDir(), "engines"), true)

	req := httptest.NewRequest(http.MethodGet, "/api/recipes/qwen-native", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var body registry.Recipe
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Installed || body.Running || body.Ready {
		t.Fatalf("flags installed=%v running=%v ready=%v, want all false", body.Installed, body.Running, body.Ready)
	}
}

// TestContainerRecipeJSONHasNoKind is the parity guard at the API level: the
// list response must not grow a kind or native field.
func TestContainerRecipeJSONHasNoKind(t *testing.T) {
	r := newNativeAPIServer(t, filepath.Join(t.TempDir(), "engines"), false)

	req := httptest.NewRequest(http.MethodGet, "/api/recipes?search=Demo", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status: %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `"kind"`) {
		t.Fatalf("container list carries kind: %s", body)
	}
	if strings.Contains(body, `"native"`) {
		t.Fatalf("container list carries native: %s", body)
	}
}
