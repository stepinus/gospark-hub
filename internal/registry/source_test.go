package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestParseSourceForms(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		url     string
		ref     string
		subpath string
	}{
		{
			name: "host/path",
			raw:  "github.com/o/r",
			url:  "https://github.com/o/r",
			ref:  "main",
		},
		{
			name:    "tree ref and subpath",
			raw:     "https://github.com/o/r/tree/dev/sub/dir",
			url:     "https://github.com/o/r",
			ref:     "dev",
			subpath: "sub/dir",
		},
		{
			name:    "repo subpath",
			raw:     "https://github.com/o/r/registry/recipes",
			url:     "https://github.com/o/r",
			ref:     "main",
			subpath: "registry/recipes",
		},
		{
			name: "dot git suffix",
			raw:  "https://github.com/o/r.git",
			url:  "https://github.com/o/r",
			ref:  "main",
		},
		{
			name: "scp like",
			raw:  "git@github.com:o/r.git",
			url:  "https://github.com/o/r",
			ref:  "main",
		},
		{
			name:    "gitlab tree",
			raw:     "https://gitlab.com/o/r/-/tree/dev/registry/recipes",
			url:     "https://gitlab.com/o/r",
			ref:     "dev",
			subpath: "registry/recipes",
		},
		{
			name:    "codeberg",
			raw:     "codeberg.org/o/r/recipes",
			url:     "https://codeberg.org/o/r",
			ref:     "main",
			subpath: "recipes",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src, err := ParseSource(tc.raw)
			if err != nil {
				t.Fatalf("ParseSource(%q): %v", tc.raw, err)
			}
			if src.URL != tc.url {
				t.Errorf("URL = %q, want %q", src.URL, tc.url)
			}
			if src.Ref != tc.ref {
				t.Errorf("Ref = %q, want %q", src.Ref, tc.ref)
			}
			if src.Subpath != tc.subpath {
				t.Errorf("Subpath = %q, want %q", src.Subpath, tc.subpath)
			}
			if want := sourceID(src.URL, src.Subpath); src.ID != want {
				t.Errorf("ID = %q, want %q", src.ID, want)
			}
		})
	}
}

func TestParseSourceRejects(t *testing.T) {
	for _, raw := range []string{
		"",
		"   ",
		"not a url",
		"https://localhost/o/r",
		"http://127.0.0.1/o/r",
		"https://192.168.1.5/o/r",
		"https://10.0.0.2/o/r",
		"file:///tmp/repo",
		"https://example.com/o/r",
		"https://github.com/o",
		"https://github.com/o/r/tree/",
	} {
		if src, err := ParseSource(raw); err == nil {
			t.Errorf("ParseSource(%q) = %+v, want error", raw, src)
		}
	}
}

func TestNewManagerSeedsDefaultSource(t *testing.T) {
	home := filepath.Join(t.TempDir(), "registry")
	m, err := NewManager(home)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	list := m.List()
	if len(list) != 1 {
		t.Fatalf("sources = %d, want 1", len(list))
	}
	src := list[0]
	if src.URL != defaultSourceURL {
		t.Errorf("URL = %q, want %q", src.URL, defaultSourceURL)
	}
	if src.Subpath != defaultSourceSubpath {
		t.Errorf("Subpath = %q, want %q", src.Subpath, defaultSourceSubpath)
	}
	if src.Ref != "main" {
		t.Errorf("Ref = %q, want main", src.Ref)
	}
	if src.ID == "" {
		t.Error("ID is empty")
	}
	if _, err := os.Stat(filepath.Join(home, "sources.yaml")); err != nil {
		t.Errorf("sources.yaml not written: %v", err)
	}

	// Empty home: local/ exists but is empty, so there are no recipes and no
	// errors. No network access happened.
	cache := NewCache(m.Roots()...)
	all, err := cache.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("recipes = %d, want 0", len(all))
	}
}

func TestNewManagerSeedsEmptyFile(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "sources.yaml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(home)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if got := len(m.List()); got != 1 {
		t.Fatalf("sources = %d, want 1", got)
	}
}

func TestNewManagerUnwritableHome(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(filepath.Join(file, "registry")); err == nil {
		t.Fatal("NewManager on unwritable home returned nil error")
	}
}

func TestManagerRootsAndLocalPriority(t *testing.T) {
	home := t.TempDir()
	vendored := filepath.Join(home, "vendored")
	mustMkdir(t, filepath.Join(vendored, "aaa", "recipes", "same"))
	mustMkdir(t, filepath.Join(vendored, "bbb", "recipes", "same"))
	mustMkdir(t, filepath.Join(home, "local", "same"))
	writeRecipe(t, filepath.Join(vendored, "aaa", "recipes", "same"), "same", "vendored-a")
	writeRecipe(t, filepath.Join(vendored, "bbb", "recipes", "same"), "same", "vendored-b")
	writeRecipe(t, filepath.Join(home, "local", "same"), "same", "local-copy")

	writeSourcesFile(t, home, []Source{
		{ID: "aaa", URL: "https://github.com/o/a", Ref: "main", Subpath: "recipes"},
		{ID: "bbb", URL: "https://github.com/o/b", Ref: "main", Subpath: "recipes"},
		{ID: "ccc", URL: "https://github.com/o/c", Ref: "main", Subpath: "missing"},
	})
	m, err := NewManager(home)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	roots := m.Roots()
	want := []string{
		filepath.Join(vendored, "aaa", "recipes"),
		filepath.Join(vendored, "bbb", "recipes"),
		filepath.Join(home, "local"),
	}
	if len(roots) != len(want) {
		t.Fatalf("Roots = %v, want %v", roots, want)
	}
	for i := range want {
		if roots[i] != want[i] {
			t.Fatalf("Roots[%d] = %q, want %q (all: %v)", i, roots[i], want[i], roots)
		}
	}

	cache := NewCache(roots...)
	rec, ok := cache.Get("same")
	if !ok {
		t.Fatal("cache does not contain same")
	}
	if rec.Name != "local-copy" {
		t.Fatalf("winning recipe = %q, want local-copy", rec.Name)
	}
	dir, ok := cache.Dir("same")
	if !ok || !strings.HasPrefix(dir, filepath.Join(home, "local")) {
		t.Fatalf("Dir = %q, %v; want local copy", dir, ok)
	}
	root, _, ok := cache.Origin("same")
	if !ok || root != filepath.Join(home, "local") {
		t.Fatalf("Origin = %q, %v; want local root", root, ok)
	}
	if !cache.IsLocal("same") {
		t.Fatal("IsLocal(same) = false, want true")
	}
}

func TestAddReportsEachBadLine(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	added, errs := m.Add("not a url, https://localhost/o/r\nhttps://example.com/o/r")
	if len(added) != 0 {
		t.Fatalf("added = %v, want none", added)
	}
	if len(errs) != 3 {
		t.Fatalf("errors = %d, want 3: %v", len(errs), errs)
	}
	if got := len(m.List()); got != 1 {
		t.Fatalf("sources = %d, want only the default", got)
	}
}

func TestForkCopiesAndRewritesSlug(t *testing.T) {
	home := t.TempDir()
	srcDir := filepath.Join(home, "vendored", "src1", "recipes", "orig")
	mustMkdir(t, filepath.Join(srcDir, "assets"))
	writeRecipe(t, srcDir, "orig", "Original Recipe")
	writeFile(t, filepath.Join(srcDir, "docker-compose.yml"), "services:\n  app:\n    image: example:1\n")
	writeFile(t, filepath.Join(srcDir, "assets", "note.txt"), "hello\n")
	writeSourcesFile(t, home, []Source{
		{ID: "src1", URL: "https://github.com/o/r", Ref: "main", Subpath: "recipes"},
	})

	m, err := NewManager(home)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	before := hashTree(t, srcDir)

	got, err := m.Fork("orig", "my-copy")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	want := filepath.Join(home, "local", "my-copy")
	if got != want {
		t.Fatalf("Fork dir = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(want, "assets", "note.txt")); err != nil {
		t.Fatalf("copied asset missing: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(want, "recipe.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "slug: my-copy") {
		t.Fatalf("copied recipe.yaml does not have the new slug:\n%s", data)
	}

	after := hashTree(t, srcDir)
	if len(before) != len(after) {
		t.Fatalf("original file count changed: %d -> %d", len(before), len(after))
	}
	for name, sum := range before {
		if after[name] != sum {
			t.Fatalf("original file %s changed during fork", name)
		}
	}

	if _, err := m.Fork("orig", "my-copy"); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("second Fork error = %v, want ErrSlugTaken", err)
	}

	mustMkdir(t, filepath.Join(home, "vendored", "src1", "recipes", "taken"))
	if _, err := m.Fork("orig", "taken"); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("vendored collision error = %v, want ErrSlugTaken", err)
	}
	if _, err := m.Fork("nope", "another"); !errors.Is(err, ErrRecipeNotFound) {
		t.Fatalf("unknown source error = %v, want ErrRecipeNotFound", err)
	}

	if err := m.DeleteLocal("my-copy"); err != nil {
		t.Fatalf("DeleteLocal: %v", err)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("local copy still exists: %v", err)
	}
	if err := m.DeleteLocal("orig"); !errors.Is(err, ErrNotLocal) {
		t.Fatalf("DeleteLocal(orig) = %v, want ErrNotLocal", err)
	}
}

func TestSyncLocalRepository(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "main")
	mustMkdir(t, filepath.Join(repo, "recipes", "demo"))
	writeRecipe(t, filepath.Join(repo, "recipes", "demo"), "demo", "Demo")
	mustMkdir(t, filepath.Join(repo, "covers"))
	writeFile(t, filepath.Join(repo, "covers", "demo.jpg"), "jpeg\n")
	mustMkdir(t, filepath.Join(repo, "other"))
	writeFile(t, filepath.Join(repo, "other", "secret.txt"), "not synced\n")
	gitRun(t, repo, "add", "-A")
	gitRun(t, repo, "commit", "-m", "first")

	home := t.TempDir()
	writeSourcesFile(t, home, []Source{
		{ID: "src1", URL: repo, Ref: "main", Subpath: "recipes"},
	})
	m, err := NewManager(home)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Sync("src1"); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	dest := filepath.Join(home, "vendored", "src1")
	if _, err := os.Stat(filepath.Join(dest, "recipes", "demo", "recipe.yaml")); err != nil {
		t.Fatalf("sparse checkout missing recipe: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "other", "secret.txt")); !os.IsNotExist(err) {
		t.Fatalf("file outside subpath was checked out: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "covers", "demo.jpg")); err != nil {
		t.Fatalf("covers directory not checked out: %v", err)
	}

	list := m.List()
	if list[0].Commit == "" {
		t.Fatal("commit was not recorded")
	}
	if list[0].Error != "" {
		t.Fatalf("source error = %q", list[0].Error)
	}
	if list[0].SyncedAt.IsZero() {
		t.Fatal("synced_at was not recorded")
	}
	if list[0].Covers != "covers" {
		t.Fatalf("covers = %q, want covers", list[0].Covers)
	}
	firstCommit := list[0].Commit

	mustMkdir(t, filepath.Join(repo, "recipes", "second"))
	writeRecipe(t, filepath.Join(repo, "recipes", "second"), "second", "Second")
	gitRun(t, repo, "add", "-A")
	gitRun(t, repo, "commit", "-m", "second")

	if err := m.SyncAll(); len(err) != 0 {
		t.Fatalf("SyncAll errors: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "recipes", "second", "recipe.yaml")); err != nil {
		t.Fatalf("new recipe not fetched: %v", err)
	}
	list = m.List()
	if list[0].Commit == firstCommit {
		t.Fatalf("commit did not change after new upstream commit: %s", firstCommit)
	}

	cache := NewCache(m.Roots()...)
	root, dir, ok := cache.Origin("second")
	if !ok || root != dest || dir != filepath.Join(dest, "recipes", "second") {
		t.Fatalf("Origin(second) = %q, %q, %v; want %q", root, dir, ok, dest)
	}

	if err := m.Remove("src1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("vendored clone still exists: %v", err)
	}
	if err := m.Sync("src1"); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("Sync(removed) = %v, want ErrSourceNotFound", err)
	}
}

func TestGitTimeoutIsBounded(t *testing.T) {
	if gitTimeout != 120*time.Second {
		t.Fatalf("gitTimeout = %s, want 120s", gitTimeout)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func writeRecipe(t *testing.T, dir, slug, name string) {
	t.Helper()
	body := "name: " + name + "\nslug: " + slug + "\ncategory: llm\nversion: \"1.0.0\"\n"
	writeFile(t, filepath.Join(dir, "recipe.yaml"), body)
}

func writeSourcesFile(t *testing.T, home string, sources []Source) {
	t.Helper()
	data, err := yaml.Marshal(sourcesFile{Sources: sources})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, "sources.yaml"), string(data))
}

func writeFile(t *testing.T, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func hashTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("hash tree: %v", err)
	}
	return out
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestSyncWholeRepositoryWithoutSubpath(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "main")
	mustMkdir(t, filepath.Join(repo, "demo"))
	writeRecipe(t, filepath.Join(repo, "demo"), "demo", "Demo")
	mustMkdir(t, filepath.Join(repo, "covers"))
	writeFile(t, filepath.Join(repo, "covers", "demo.jpg"), "jpeg\n")
	gitRun(t, repo, "add", "-A")
	gitRun(t, repo, "commit", "-m", "first")

	home := t.TempDir()
	writeSourcesFile(t, home, []Source{
		{ID: "whole", URL: repo, Ref: "main"},
	})
	m, err := NewManager(home)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Sync("whole"); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	dest := filepath.Join(home, "vendored", "whole")
	if _, err := os.Stat(filepath.Join(dest, "demo", "recipe.yaml")); err != nil {
		t.Fatalf("whole repository was not checked out: %v", err)
	}
	if got := m.List()[0].Covers; got != "covers" {
		t.Fatalf("covers = %q, want covers", got)
	}

	roots := m.Roots()
	if len(roots) == 0 || roots[0] != dest {
		t.Fatalf("Roots = %v, want %q first", roots, dest)
	}
	cache := NewCache(roots...)
	if _, ok := cache.Get("demo"); !ok {
		t.Fatal("cache does not see recipe from whole repository")
	}
}
