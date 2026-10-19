// Package testutil holds the few helpers shared by tests in different
// packages. Recipe fixtures no longer live in the checkout, so tests that need
// the real catalogue resolve it the same way the daemon does.
package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// RecipesDir returns a directory with real recipes for data-driven tests: the
// GOSPARK_TEST_RECIPES environment variable when set, otherwise the first
// vendored source under REGISTRY_HOME (default ~/.config/gospark/registry).
// It skips the test when nothing is available, because the checkout does not
// ship recipes any more.
func RecipesDir(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("GOSPARK_TEST_RECIPES"); dir != "" {
		return dir
	}
	home := os.Getenv("REGISTRY_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("no home directory: %v", err)
		}
		home = filepath.Join(userHome, ".config", "gospark", "registry")
	}
	matches, _ := filepath.Glob(filepath.Join(home, "vendored", "*", "registry", "recipes"))
	// Several sources can be vendored side by side; the catalogue the daemon
	// serves is the big one, so the fixture is the source with the most
	// recipes rather than whichever id sorts first.
	best, bestCount := "", 0
	for _, dir := range matches {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		if len(entries) > bestCount {
			best, bestCount = dir, len(entries)
		}
	}
	if best != "" {
		return best
	}
	t.Skipf("no recipes: sync a source into %s or set GOSPARK_TEST_RECIPES", home)
	return ""
}
