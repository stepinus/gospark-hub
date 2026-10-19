package recipes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apiauth "gospark-hub/internal/api/auth"
	"gospark-hub/internal/auth"
	"gospark-hub/internal/config"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
	"gospark-hub/internal/web"
)

// localRuntime answers the two questions the delete route asks, and the rest
// of the Runtime interface with idle values.
type localRuntime struct {
	installed map[string]struct{}
	running   map[string]struct{}
}

func (r localRuntime) InstalledRecipes(context.Context) (map[string]struct{}, error) {
	return r.installed, nil
}

func (r localRuntime) IsRecipeRunning(_ context.Context, slug string) bool {
	_, ok := r.running[slug]
	return ok
}

func (r localRuntime) HasLeftovers(context.Context, string) (bool, error) { return false, nil }
func (r localRuntime) IsReady(string) bool                                { return false }
func (r localRuntime) GetPending(string) string                           { return "" }
func (r localRuntime) GetStartupError(string) string                      { return "" }
func (r localRuntime) StartHealthCheck(context.Context, string)           {}

type localEnv struct {
	home    string
	manager *registry.Manager
	cache   *registry.Cache
	cfg     config.Config
	db      *store.DB
	srv     *httptest.Server
	runtime localRuntime
}

// newLocalEnv builds a registry home with one source holding searxng, so the
// copy route has something to copy without touching the network.
func newLocalEnv(t *testing.T) *localEnv {
	t.Helper()
	home := t.TempDir()
	src, err := registry.ParseSource("https://github.com/example/recipes")
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	doc := fmt.Sprintf("sources:\n  - id: %s\n    url: %s\n    ref: main\n", src.ID, src.URL)
	if err := os.WriteFile(filepath.Join(home, "sources.yaml"), []byte(doc), 0o644); err != nil {
		t.Fatalf("write sources.yaml: %v", err)
	}
	writeLocalRecipe(t, filepath.Join(home, "vendored", src.ID), "searxng")

	mgr, err := registry.NewManager(home)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	cache := registry.NewCache(mgr.Roots()...)
	if err := cache.Load(); err != nil {
		t.Fatalf("load cache: %v", err)
	}

	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := config.Config{
		AuthEnabled:       true,
		SessionCookieName: "spark_ai_hub_session",
		SessionTTLDays:    30,
		DataDir:           dir,
	}
	r := web.NewRouter()
	apiauth.Register(r, apiauth.Deps{
		Store:         db,
		Config:        cfg,
		Logger:        log.New(io.Discard, "", 0),
		TokenResolver: auth.NoopResolver{},
	})
	env := &localEnv{
		home:    home,
		manager: mgr,
		cache:   cache,
		cfg:     cfg,
		db:      db,
		runtime: localRuntime{installed: map[string]struct{}{}, running: map[string]struct{}{}},
	}

	// Deps.Runtime is an interface field; keep the same value the tests mutate
	// so a delete can be made to conflict without rebuilding the router.
	Register(r, Deps{Runtime: &env.runtime, Cache: cache, Manager: mgr})
	env.srv = httptest.NewServer(r)
	t.Cleanup(env.srv.Close)
	return env
}

func writeLocalRecipe(t *testing.T, root, slug string) {
	t.Helper()
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir recipe: %v", err)
	}
	recipe := fmt.Sprintf("name: %s\nslug: %s\ncategory: search\nversion: 1.0.0\n", slug, slug)
	if err := os.WriteFile(filepath.Join(dir, "recipe.yaml"), []byte(recipe), 0o644); err != nil {
		t.Fatalf("write recipe.yaml: %v", err)
	}
	compose := fmt.Sprintf("services:\n  %s:\n    image: example/%s:latest\n    ports:\n      - 8080:8080\n", slug, slug)
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatalf("write docker-compose.yml: %v", err)
	}
}

func (e *localEnv) user(t *testing.T, email, role string) *store.User {
	t.Helper()
	key, err := auth.NewAPIKey()
	if err != nil {
		t.Fatalf("api key: %v", err)
	}
	hash, err := auth.HashPassword("secret123")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	u, err := e.db.CreateUser(email, "", hash, role, "active", key)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

func (e *localEnv) jar(t *testing.T, u *store.User) *cookiejar.Jar {
	t.Helper()
	tok, err := e.db.CreateSession(u.ID, "test", "127.0.0.1", e.cfg.SessionTTLDays)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	jar, _ := cookiejar.New(nil)
	base, _ := url.Parse(e.srv.URL)
	jar.SetCookies(base, []*http.Cookie{{Name: e.cfg.SessionCookieName, Value: tok}})
	return jar
}

func (e *localEnv) adminJar(t *testing.T) *cookiejar.Jar {
	t.Helper()
	return e.jar(t, e.user(t, "admin@example.com", "admin"))
}

func (e *localEnv) do(t *testing.T, jar *cookiejar.Jar, method, path, body string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{}
	if jar != nil {
		client.Jar = jar
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func fileHashes(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		sum := sha256.Sum256(data)
		out[e.Name()] = hex.EncodeToString(sum[:])
	}
	return out
}

func TestForkAndDeleteAreAdminOnly(t *testing.T) {
	e := newLocalEnv(t)
	userJar := e.jar(t, e.user(t, "user@example.com", "user"))

	for _, rt := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/recipes/searxng/fork", `{"slug":"my-searxng"}`},
		{http.MethodDelete, "/api/recipes/searxng/local", ""},
	} {
		code, body := e.do(t, userJar, rt.method, rt.path, rt.body)
		if code != http.StatusForbidden {
			t.Fatalf("%s %s as user: got %d, want 403 (%s)", rt.method, rt.path, code, body)
		}
		var got map[string]string
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("unmarshal 403: %v", err)
		}
		if got["detail"] != "Admin only" {
			t.Fatalf("detail = %q, want %q", got["detail"], "Admin only")
		}
	}
}

func TestForkValidatesSlug(t *testing.T) {
	e := newLocalEnv(t)
	jar := e.adminJar(t)

	for _, body := range []string{
		`{"slug":""}`,
		`{"slug":"A"}`,
		`{"slug":"-leading"}`,
		`{"slug":"has space"}`,
		`{"slug":"way-too-long-` + strings.Repeat("x", 70) + `"}`,
		`not json`,
	} {
		code, resp := e.do(t, jar, http.MethodPost, "/api/recipes/searxng/fork", body)
		if code != http.StatusBadRequest {
			t.Fatalf("fork %s: got %d, want 400 (%s)", body, code, resp)
		}
	}
}

func TestForkCopiesRecipeAndLeavesOriginalAlone(t *testing.T) {
	e := newLocalEnv(t)
	jar := e.adminJar(t)

	srcDir := filepath.Join(e.home, "vendored", e.manager.List()[0].ID, "searxng")
	before := fileHashes(t, srcDir)

	code, body := e.do(t, jar, http.MethodPost, "/api/recipes/searxng/fork", `{"slug":"my-searxng"}`)
	if code != http.StatusOK {
		t.Fatalf("fork: %d %s", code, body)
	}
	var created struct {
		Slug       string `json:"slug"`
		CopiedFrom string `json:"copied_from"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("unmarshal fork: %v", err)
	}
	if created.Slug != "my-searxng" || created.CopiedFrom != "searxng" {
		t.Fatalf("fork response = %s", body)
	}

	dstDir := filepath.Join(e.home, "local", "my-searxng")
	yaml, err := os.ReadFile(filepath.Join(dstDir, "recipe.yaml"))
	if err != nil {
		t.Fatalf("read copy: %v", err)
	}
	if !strings.Contains(string(yaml), "slug: my-searxng") || strings.Contains(string(yaml), "slug: searxng") {
		t.Fatalf("copy slug not rewritten:\n%s", yaml)
	}
	if _, err := os.Stat(filepath.Join(dstDir, ForkOriginFile)); err != nil {
		t.Fatalf("provenance file missing: %v", err)
	}

	after := fileHashes(t, srcDir)
	if len(before) != len(after) {
		t.Fatalf("original file set changed: %v -> %v", before, after)
	}
	for name, sum := range before {
		if after[name] != sum {
			t.Fatalf("original %s changed by fork", name)
		}
	}

	// The copy is now a local recipe in the catalogue, the original is not.
	code, body = e.do(t, jar, http.MethodGet, "/api/recipes/my-searxng", "")
	if code != http.StatusOK {
		t.Fatalf("detail after fork: %d %s", code, body)
	}
	var detail struct {
		Slug  string `json:"slug"`
		Local bool   `json:"local"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	if !detail.Local || detail.Slug != "my-searxng" {
		t.Fatalf("copy is not marked local: %s", body)
	}

	code, body = e.do(t, jar, http.MethodGet, "/api/recipes/searxng", "")
	if code != http.StatusOK {
		t.Fatalf("original detail: %d %s", code, body)
	}
	// A fresh struct: decoding into the one above would keep the previous
	// Local=true when the key is absent from the JSON (omitempty).
	var original struct {
		Local bool `json:"local"`
	}
	if err := json.Unmarshal(body, &original); err != nil {
		t.Fatalf("unmarshal original: %v", err)
	}
	if original.Local {
		t.Fatalf("vendored recipe marked local: %s", body)
	}
}

func TestForkSlugTaken(t *testing.T) {
	e := newLocalEnv(t)
	jar := e.adminJar(t)

	// A slug that already exists in the source.
	code, body := e.do(t, jar, http.MethodPost, "/api/recipes/searxng/fork", `{"slug":"searxng"}`)
	if code != http.StatusConflict {
		t.Fatalf("fork onto vendored slug: %d %s", code, body)
	}

	// A slug that already exists locally.
	if _, body := e.do(t, jar, http.MethodPost, "/api/recipes/searxng/fork", `{"slug":"my-searxng"}`); !strings.Contains(string(body), "my-searxng") {
		t.Fatalf("first fork failed: %s", body)
	}
	code, body = e.do(t, jar, http.MethodPost, "/api/recipes/searxng/fork", `{"slug":"my-searxng"}`)
	if code != http.StatusConflict {
		t.Fatalf("fork onto local slug: %d %s", code, body)
	}
}

func TestForkUnknownRecipe(t *testing.T) {
	e := newLocalEnv(t)
	jar := e.adminJar(t)

	code, body := e.do(t, jar, http.MethodPost, "/api/recipes/nope/fork", `{"slug":"my-nope"}`)
	if code != http.StatusNotFound {
		t.Fatalf("fork unknown: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(e.home, "local", "my-nope")); !os.IsNotExist(err) {
		t.Fatalf("failed fork left a directory behind: %v", err)
	}
}

func TestDeleteLocalRecipe(t *testing.T) {
	e := newLocalEnv(t)
	jar := e.adminJar(t)

	if code, body := e.do(t, jar, http.MethodPost, "/api/recipes/searxng/fork", `{"slug":"my-searxng"}`); code != http.StatusOK {
		t.Fatalf("fork: %d %s", code, body)
	}
	dstDir := filepath.Join(e.home, "local", "my-searxng")

	code, body := e.do(t, jar, http.MethodDelete, "/api/recipes/my-searxng/local", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"deleted":true`) {
		t.Fatalf("delete local: %d %s", code, body)
	}
	if _, err := os.Stat(dstDir); !os.IsNotExist(err) {
		t.Fatalf("catalogue directory still there: %v", err)
	}
	if _, ok := e.cache.Get("my-searxng"); ok {
		t.Fatal("deleted recipe still in the cache")
	}
	if code, _ := e.do(t, jar, http.MethodGet, "/api/recipes/my-searxng", ""); code != http.StatusNotFound {
		t.Fatalf("detail after delete: got %d, want 404", code)
	}
}

func TestDeleteLocalInstalledOrRunning(t *testing.T) {
	e := newLocalEnv(t)
	jar := e.adminJar(t)

	if code, body := e.do(t, jar, http.MethodPost, "/api/recipes/searxng/fork", `{"slug":"my-searxng"}`); code != http.StatusOK {
		t.Fatalf("fork: %d %s", code, body)
	}
	dstDir := filepath.Join(e.home, "local", "my-searxng")

	e.runtime.installed["my-searxng"] = struct{}{}
	code, body := e.do(t, jar, http.MethodDelete, "/api/recipes/my-searxng/local", "")
	if code != http.StatusConflict {
		t.Fatalf("delete installed: %d %s", code, body)
	}
	if _, err := os.Stat(dstDir); err != nil {
		t.Fatalf("conflict removed the recipe anyway: %v", err)
	}

	delete(e.runtime.installed, "my-searxng")
	e.runtime.running["my-searxng"] = struct{}{}
	code, body = e.do(t, jar, http.MethodDelete, "/api/recipes/my-searxng/local", "")
	if code != http.StatusConflict {
		t.Fatalf("delete running: %d %s", code, body)
	}
	if _, err := os.Stat(dstDir); err != nil {
		t.Fatalf("conflict removed the recipe anyway: %v", err)
	}

	// Once it is neither installed nor running the delete goes through.
	delete(e.runtime.running, "my-searxng")
	if code, body := e.do(t, jar, http.MethodDelete, "/api/recipes/my-searxng/local", ""); code != http.StatusOK {
		t.Fatalf("delete idle: %d %s", code, body)
	}
}

func TestDeleteNonLocalRecipe(t *testing.T) {
	e := newLocalEnv(t)
	jar := e.adminJar(t)

	code, body := e.do(t, jar, http.MethodDelete, "/api/recipes/searxng/local", "")
	if code != http.StatusNotFound {
		t.Fatalf("delete vendored: %d %s", code, body)
	}
	srcDir := filepath.Join(e.home, "vendored", e.manager.List()[0].ID, "searxng")
	if _, err := os.Stat(filepath.Join(srcDir, "recipe.yaml")); err != nil {
		t.Fatalf("vendored recipe was touched: %v", err)
	}

	if code, _ := e.do(t, jar, http.MethodDelete, "/api/recipes/nope/local", ""); code != http.StatusNotFound {
		t.Fatalf("delete unknown: got %d, want 404", code)
	}
}
