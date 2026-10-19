package registry

import (
	"path/filepath"
	"testing"
)

func TestCacheMultipleRootsPriority(t *testing.T) {
	home := t.TempDir()
	first := filepath.Join(home, "vendored", "one", "recipes")
	second := filepath.Join(home, "vendored", "two", "recipes")
	local := filepath.Join(home, "local")

	mustMkdir(t, filepath.Join(first, "only-first"))
	writeRecipe(t, filepath.Join(first, "only-first"), "only-first", "First only")
	mustMkdir(t, filepath.Join(first, "shadowed"))
	writeRecipe(t, filepath.Join(first, "shadowed"), "shadowed", "From first")
	mustMkdir(t, filepath.Join(second, "shadowed"))
	writeRecipe(t, filepath.Join(second, "shadowed"), "shadowed", "From second")
	mustMkdir(t, filepath.Join(local, "shadowed"))
	writeRecipe(t, filepath.Join(local, "shadowed"), "shadowed", "From local")
	mustMkdir(t, filepath.Join(local, "mine"))
	writeRecipe(t, filepath.Join(local, "mine"), "mine", "Mine")

	cache := NewCache(first, second, local)
	all, err := cache.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("recipes = %d, want 3: %v", len(all), slugs(all))
	}

	if rec, ok := cache.Get("shadowed"); !ok || rec.Name != "From local" {
		t.Fatalf("shadowed = %+v, %v; want From local", rec, ok)
	}
	root, dir, ok := cache.Origin("shadowed")
	if !ok || root != local || dir != filepath.Join(local, "shadowed") {
		t.Fatalf("Origin(shadowed) = %q, %q, %v; want local", root, dir, ok)
	}
	if !cache.IsLocal("shadowed") || !cache.IsLocal("mine") {
		t.Fatal("local recipes are not marked local")
	}
	if cache.IsLocal("only-first") {
		t.Fatal("vendored recipe marked local")
	}
	if root, _, _ := cache.Origin("only-first"); root != first {
		t.Fatalf("Origin(only-first) root = %q, want %q", root, first)
	}
	if d, ok := cache.Dir("only-first"); !ok || d != filepath.Join(first, "only-first") {
		t.Fatalf("Dir(only-first) = %q, %v", d, ok)
	}
}

func TestCacheLocalRootWithoutLocalDir(t *testing.T) {
	home := t.TempDir()
	vendored := filepath.Join(home, "vendored", "one", "recipes")
	mustMkdir(t, filepath.Join(vendored, "remote"))
	writeRecipe(t, filepath.Join(vendored, "remote"), "remote", "Remote")

	// local/ does not exist here: the only root must not be mistaken for it.
	cache := NewCache(vendored)
	if _, ok := cache.Get("remote"); !ok {
		t.Fatal("remote recipe missing")
	}
	if cache.IsLocal("remote") {
		t.Fatal("vendored recipe marked local when local/ is absent")
	}
}

func TestCacheEmptyAndMissingRoots(t *testing.T) {
	for _, cache := range []*Cache{
		NewCache(),
		NewCache(filepath.Join(t.TempDir(), "does-not-exist")),
	} {
		all, err := cache.All()
		if err != nil {
			t.Fatalf("All: %v", err)
		}
		if len(all) != 0 {
			t.Fatalf("recipes = %d, want 0", len(all))
		}
		if _, ok := cache.Get("anything"); ok {
			t.Fatal("Get on empty cache returned a recipe")
		}
		if _, ok := cache.Dir("anything"); ok {
			t.Fatal("Dir on empty cache returned a recipe")
		}
		if _, _, ok := cache.Origin("anything"); ok {
			t.Fatal("Origin on empty cache returned a recipe")
		}
		if cache.IsLocal("anything") {
			t.Fatal("IsLocal on empty cache returned true")
		}
	}
}

func TestCacheReloadsAfterRecipeAppears(t *testing.T) {
	root := t.TempDir()
	cache := NewCache(root)
	if all, err := cache.All(); err != nil || len(all) != 0 {
		t.Fatalf("initial All = %d, %v; want 0, nil", len(all), err)
	}

	mustMkdir(t, filepath.Join(root, "late"))
	writeRecipe(t, filepath.Join(root, "late"), "late", "Late")

	rec, ok := cache.Get("late")
	if !ok {
		t.Fatal("late recipe not picked up")
	}
	if rec.Slug != "late" {
		t.Fatalf("slug = %q, want late", rec.Slug)
	}
}

func slugs(recipes []Recipe) []string {
	out := make([]string, len(recipes))
	for i, rec := range recipes {
		out[i] = rec.Slug
	}
	return out
}
