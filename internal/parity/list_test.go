package parity

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"gospark-hub/internal/registry"
)

// TestRegistryList loads every recipe.yaml under registry/recipes and checks
// that the loader sees exactly the directories that exist on disk.
func TestRegistryList(t *testing.T) {
	// Recipes are not shipped in the checkout any more: the daemon reads them
	// from the registry home, filled from configured sources. The loader still
	// deserves a test against real files, so it runs when a checkout happens to
	// carry them (old branches, a source synced to disk) and is skipped
	// otherwise.
	path := filepath.Join("..", "..", "registry", "recipes")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no checkout recipes at %s: registry now lives under REGISTRY_HOME", path)
	}

	cache := registry.NewCache(path)
	if err := cache.Load(); err != nil {
		t.Fatalf("load recipes: %v", err)
	}

	recipes, err := cache.All()
	if err != nil {
		t.Fatalf("all recipes: %v", err)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("read registry dir: %v", err)
	}

	var want []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(path, e.Name(), "recipe.yaml")); err != nil {
			continue
		}
		want = append(want, e.Name())
	}
	sort.Strings(want)

	got := make([]string, len(recipes))
	for i, r := range recipes {
		got[i] = r.Slug
	}
	sort.Strings(got)

	if len(got) != len(want) {
		t.Fatalf("recipe count mismatch: got %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("slug mismatch at %d: got %q, want %q", i, got[i], want[i])
		}
	}
}
