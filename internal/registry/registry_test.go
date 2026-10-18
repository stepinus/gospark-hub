package registry

import (
	"os"
	"path/filepath"
	"testing"

	"gospark-hub/internal/config"
)

func writeRecipe(t *testing.T, root, slug, body string) {
	t.Helper()
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "recipe.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRecipes(t *testing.T) {
	root := t.TempDir()
	writeRecipe(t, root, "plain", "name: Plain\nslug: plain\n")
	writeRecipe(t, root, "gated", `name: Gated
slug: gated
requires_hf_token: true
gated_repos:
  - org/model-a
  - org/model-b
`)
	// A recipe without a slug falls back to its directory name.
	writeRecipe(t, root, "no-slug", "name: No Slug\n")
	// A broken recipe is skipped, not fatal.
	if err := os.MkdirAll(filepath.Join(root, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "broken", "recipe.yaml"), []byte("\tnot: [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory without a recipe.yaml is skipped.
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	reg := New(config.Config{RegistryPath: root})
	if err := reg.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := reg.Len(), 3; got != want {
		t.Fatalf("Len = %d, want %d", got, want)
	}

	gated := reg.GetRecipe("gated")
	if gated == nil {
		t.Fatal("gated recipe missing")
	}
	if !gated.RequiresHFToken {
		t.Fatal("requires_hf_token not parsed")
	}
	if len(gated.GatedRepos) != 2 || gated.GatedRepos[0] != "org/model-a" || gated.GatedRepos[1] != "org/model-b" {
		t.Fatalf("gated_repos = %v", gated.GatedRepos)
	}

	if rec := reg.GetRecipe("no-slug"); rec == nil {
		t.Fatal("slug fallback to directory name failed")
	}
	if reg.GetRecipe("missing") != nil {
		t.Fatal("unknown slug should return nil")
	}
}

func TestLoadMissingRegistry(t *testing.T) {
	reg := New(config.Config{RegistryPath: filepath.Join(t.TempDir(), "nope")})
	if err := reg.Load(); err != nil {
		t.Fatalf("missing registry should not be an error, got %v", err)
	}
	if reg.Len() != 0 {
		t.Fatalf("Len = %d, want 0", reg.Len())
	}
}
