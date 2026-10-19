package containers

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// git runs git in dir and fails the test on a non-zero exit.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

// TestComposeResetFromSourceCheckout pins the root of `git show HEAD:<rel>` to
// the cache origin: the recipe sits three levels below the checkout, so a
// fixed number of parent directories would resolve the wrong root.
func TestComposeResetFromSourceCheckout(t *testing.T) {
	installFakeDocker(t)

	repo := t.TempDir()
	git(t, repo, "init", "-q")
	recipesRoot := filepath.Join(repo, "nested", "recipes")
	committed := "services:\n  app:\n    image: testproj-demo:1.0\n"
	writeRecipe(t, recipesRoot, "demo", "name: Demo\nslug: demo\n", committed)
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "init")

	edited := "services:\n  app:\n    image: testproj-demo:2.0\n"
	if err := os.WriteFile(filepath.Join(recipesRoot, "demo", "docker-compose.yml"), []byte(edited), 0o644); err != nil {
		t.Fatalf("edit compose: %v", err)
	}
	srv, _ := newTestServer(t, recipesRoot)

	code, body := getJSON(t, srv, http.MethodPost, "/api/recipes/demo/compose/reset", "")
	if code != http.StatusOK {
		t.Fatalf("reset from source: %d %s", code, body)
	}
	if !strings.Contains(string(body), `"status":"reset"`) {
		t.Fatalf("reset body: %s", body)
	}
	onDisk, err := os.ReadFile(filepath.Join(recipesRoot, "demo", "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	if string(onDisk) != committed {
		t.Fatalf("reset content = %q, want %q", onDisk, committed)
	}
}

// TestComposeResetLocalRecipeConflict: a local recipe has no source, so reset
// is refused instead of pretending to restore something.
func TestComposeResetLocalRecipeConflict(t *testing.T) {
	installFakeDocker(t)
	home := t.TempDir()
	localRoot := filepath.Join(home, "local")
	writeRecipe(t, localRoot, "mine", "name: Mine\nslug: mine\n",
		"services:\n  app:\n    image: testproj-mine\n")
	srv, _ := newTestServer(t, localRoot)

	code, body := getJSON(t, srv, http.MethodPost, "/api/recipes/mine/compose/reset", "")
	if code != http.StatusConflict {
		t.Fatalf("reset local: %d %s, want 409", code, body)
	}
	if !strings.Contains(string(body), "no source") {
		t.Fatalf("reset local body: %s", body)
	}
	// The file must be untouched by the refused reset.
	onDisk, err := os.ReadFile(filepath.Join(localRoot, "mine", "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	if !strings.Contains(string(onDisk), "testproj-mine") {
		t.Fatalf("compose was rewritten: %q", onDisk)
	}
}
