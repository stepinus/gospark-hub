package registries

import (
	"bytes"
	"context"
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
	"gospark-hub/internal/api/recipes"
	"gospark-hub/internal/auth"
	"gospark-hub/internal/config"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
	"gospark-hub/internal/web"
)

// fakeRuntime keeps the delete route's "is it in use?" question answerable
// without Docker or the database.
type fakeRuntime struct {
	installed map[string]struct{}
	running   map[string]struct{}
}

func (f fakeRuntime) InstalledRecipes(context.Context) (map[string]struct{}, error) {
	return f.installed, nil
}

func (f fakeRuntime) IsRecipeRunning(_ context.Context, slug string) bool {
	_, ok := f.running[slug]
	return ok
}

type env struct {
	home     string
	sourceID string
	manager  *registry.Manager
	cache    *registry.Cache
	cfg      config.Config
	db       *store.DB
	srv      *httptest.Server
}

// newEnv builds a registry home with one configured source (no network: the
// checkout is written by hand) and a daemon-like router with the real auth
// middleware in front of the routes.
func newEnv(t *testing.T, remoteSlugs ...string) *env {
	t.Helper()
	home := t.TempDir()
	src, err := registry.ParseSource("https://github.com/example/recipes")
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	doc := fmt.Sprintf("sources:\n  - id: %s\n    url: %s\n    ref: main\n    commit: abc1234\n", src.ID, src.URL)
	if err := os.WriteFile(filepath.Join(home, "sources.yaml"), []byte(doc), 0o644); err != nil {
		t.Fatalf("write sources.yaml: %v", err)
	}
	for _, slug := range remoteSlugs {
		writeRecipe(t, filepath.Join(home, "vendored", src.ID), slug)
	}

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
	Register(r, Deps{
		Manager: mgr,
		Cache:   cache,
		Runtime: fakeRuntime{},
	})

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &env{home: home, sourceID: src.ID, manager: mgr, cache: cache, cfg: cfg, db: db, srv: srv}
}

func writeRecipe(t *testing.T, root, slug string) {
	t.Helper()
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir recipe: %v", err)
	}
	body := fmt.Sprintf("name: %s\nslug: %s\ncategory: image-gen\n", slug, slug)
	if err := os.WriteFile(filepath.Join(dir, "recipe.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write recipe.yaml: %v", err)
	}
}

func (e *env) user(t *testing.T, email, role string) *store.User {
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

func (e *env) jar(t *testing.T, u *store.User) *cookiejar.Jar {
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

func (e *env) do(t *testing.T, jar *cookiejar.Jar, method, path, body string) (int, []byte) {
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

func adminJar(t *testing.T, e *env) *cookiejar.Jar {
	t.Helper()
	return e.jar(t, e.user(t, "admin@example.com", "admin"))
}

func TestAdminOnly(t *testing.T) {
	e := newEnv(t, "demo")
	userJar := e.jar(t, e.user(t, "user@example.com", "user"))

	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/api/registries", ""},
		{http.MethodPost, "/api/registries", `{"urls":"https://github.com/example/other"}`},
		{http.MethodPost, "/api/registries/sync", ""},
		{http.MethodPost, "/api/registries/abc/sync", ""},
		{http.MethodDelete, "/api/registries/abc", ""},
		{http.MethodGet, "/api/local-recipes", ""},
	}
	for _, rt := range routes {
		code, body := e.do(t, userJar, rt.method, rt.path, rt.body)
		if code != http.StatusForbidden {
			t.Fatalf("%s %s as user: got %d, want 403 (%s)", rt.method, rt.path, code, body)
		}
		var got map[string]string
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("unmarshal 403: %v", err)
		}
		if got["detail"] != "Admin only" {
			t.Fatalf("%s %s detail = %q, want %q", rt.method, rt.path, got["detail"], "Admin only")
		}
	}

	// Anonymous callers are stopped by the middleware before the guard.
	if code, body := e.do(t, nil, http.MethodGet, "/api/registries", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: got %d, want 401 (%s)", code, body)
	}
}

func TestListRegistries(t *testing.T) {
	e := newEnv(t, "one", "two")
	jar := adminJar(t, e)

	code, body := e.do(t, jar, http.MethodGet, "/api/registries", "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, body)
	}
	var got struct {
		Sources []struct {
			ID      string `json:"id"`
			URL     string `json:"url"`
			Commit  string `json:"commit"`
			Recipes int    `json:"recipes"`
		} `json:"sources"`
		LocalCount int  `json:"local_count"`
		Syncing    bool `json:"syncing"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(got.Sources) != 1 {
		t.Fatalf("sources = %d, want 1: %s", len(got.Sources), body)
	}
	if got.Sources[0].ID != e.sourceID || got.Sources[0].Commit != "abc1234" {
		t.Fatalf("source = %+v, want id %s and commit abc1234", got.Sources[0], e.sourceID)
	}
	if got.Sources[0].Recipes != 2 {
		t.Fatalf("recipes = %d, want 2", got.Sources[0].Recipes)
	}
	if got.LocalCount != 0 || got.Syncing {
		t.Fatalf("local_count = %d, syncing = %v; want 0, false", got.LocalCount, got.Syncing)
	}

	// A local recipe shows up in local_count and in the local list.
	writeRecipe(t, filepath.Join(e.home, "local"), "mine")
	code, body = e.do(t, jar, http.MethodGet, "/api/registries", "")
	if code != http.StatusOK {
		t.Fatalf("list after local: %d %s", code, body)
	}
	got.LocalCount = -1
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if got.LocalCount != 1 {
		t.Fatalf("local_count = %d, want 1: %s", got.LocalCount, body)
	}
}

func TestAddSourceRejectsBadLines(t *testing.T) {
	e := newEnv(t, "demo")
	jar := adminJar(t, e)

	// Empty body is a whole-request error.
	code, body := e.do(t, jar, http.MethodPost, "/api/registries", `{"urls":"   "}`)
	if code != http.StatusBadRequest || !strings.Contains(string(body), "urls is required") {
		t.Fatalf("empty urls: %d %s", code, body)
	}

	// Localhost and plain garbage must never reach git.
	code, body = e.do(t, jar, http.MethodPost, "/api/registries", `{"urls":"http://localhost:9000/o/r\nnot a repo"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("bad urls: %d %s", code, body)
	}
	var got struct {
		Sources []json.RawMessage `json:"sources"`
		Added   []json.RawMessage `json:"added"`
		Errors  []string          `json:"errors"`
		Detail  string            `json:"detail"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal add: %v", err)
	}
	if len(got.Errors) != 2 {
		t.Fatalf("errors = %v, want one per bad line", got.Errors)
	}
	if len(got.Added) != 0 {
		t.Fatalf("added = %v, want none", got.Added)
	}
	if len(got.Sources) != 1 {
		t.Fatalf("sources = %v, want the existing source", got.Sources)
	}
	if got.Detail == "" {
		t.Fatalf("400 without detail: %s", body)
	}
}

func TestAddSourceDuplicateIsReported(t *testing.T) {
	e := newEnv(t, "demo")
	jar := adminJar(t, e)

	// Adding the already configured repository is a per-line error and, since
	// nothing was added, a 400.
	code, body := e.do(t, jar, http.MethodPost, "/api/registries",
		`{"urls":"https://github.com/example/recipes"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("duplicate source: %d %s", code, body)
	}
	if !strings.Contains(string(body), "already configured") {
		t.Fatalf("duplicate error missing: %s", body)
	}
}

func TestSyncAndDeleteUnknownSource(t *testing.T) {
	e := newEnv(t, "demo")
	jar := adminJar(t, e)

	for _, rt := range []struct{ method, path string }{
		{http.MethodPost, "/api/registries/nope/sync"},
		{http.MethodDelete, "/api/registries/nope"},
	} {
		code, body := e.do(t, jar, rt.method, rt.path, "")
		if code != http.StatusNotFound {
			t.Fatalf("%s %s: got %d, want 404 (%s)", rt.method, rt.path, code, body)
		}
	}
}

func TestDeleteSourceKeepsLocal(t *testing.T) {
	e := newEnv(t, "demo")
	jar := adminJar(t, e)
	localDir := filepath.Join(e.home, "local", "mine")
	writeRecipe(t, filepath.Join(e.home, "local"), "mine")

	code, body := e.do(t, jar, http.MethodDelete, "/api/registries/"+e.sourceID, "")
	if code != http.StatusOK || !strings.Contains(string(body), `"deleted":true`) {
		t.Fatalf("delete source: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(e.home, "vendored", e.sourceID)); !os.IsNotExist(err) {
		t.Fatalf("vendored checkout still there: %v", err)
	}
	if _, err := os.Stat(localDir); err != nil {
		t.Fatalf("local recipe was touched: %v", err)
	}
}

func TestListLocalRecipes(t *testing.T) {
	e := newEnv(t, "searxng")
	jar := adminJar(t, e)

	localRoot := filepath.Join(e.home, "local")
	writeRecipe(t, localRoot, "my-searxng")
	// The fork route writes this file; the list route must read it.
	if err := os.WriteFile(filepath.Join(localRoot, "my-searxng", recipes.ForkOriginFile), []byte("searxng\n"), 0o644); err != nil {
		t.Fatalf("write origin: %v", err)
	}
	if err := e.cache.Load(); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	if err := e.cache.Load(); err != nil {
		t.Fatalf("load cache: %v", err)
	}

	code, body := e.do(t, jar, http.MethodGet, "/api/local-recipes", "")
	if code != http.StatusOK {
		t.Fatalf("local recipes: %d %s", code, body)
	}
	var got []struct {
		Slug       string `json:"slug"`
		Name       string `json:"name"`
		CopiedFrom string `json:"copied_from"`
		MTime      string `json:"mtime"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal local recipes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("local recipes = %d, want 1: %s", len(got), body)
	}
	if got[0].Slug != "my-searxng" || got[0].CopiedFrom != "searxng" || got[0].MTime == "" {
		t.Fatalf("local recipe = %+v, want my-searxng from searxng with an mtime", got[0])
	}
}

// TestEmptyHome starts from a registry home that does not exist yet: the
// daemon must answer with zero sources and zero recipes, without network.
func TestEmptyHome(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "sources.yaml"), []byte("sources: []\n"), 0o644); err != nil {
		t.Fatalf("write sources.yaml: %v", err)
	}
	mgr, err := registry.NewManager(home)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	cache := registry.NewCache(mgr.Roots()...)

	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := config.Config{AuthEnabled: true, SessionCookieName: "spark_ai_hub_session", SessionTTLDays: 30}
	r := web.NewRouter()
	apiauth.Register(r, apiauth.Deps{Store: db, Config: cfg, Logger: log.New(io.Discard, "", 0), TokenResolver: auth.NoopResolver{}})
	Register(r, Deps{Manager: mgr, Cache: cache, Runtime: fakeRuntime{}})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	hash, _ := auth.HashPassword("secret123")
	key, _ := auth.NewAPIKey()
	u, err := db.CreateUser("admin@example.com", "", hash, "admin", "active", key)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	tok, err := db.CreateSession(u.ID, "test", "127.0.0.1", cfg.SessionTTLDays)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	jar, _ := cookiejar.New(nil)
	base, _ := url.Parse(srv.URL)
	jar.SetCookies(base, []*http.Cookie{{Name: cfg.SessionCookieName, Value: tok}})

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/registries", nil)
	resp, err := (&http.Client{Jar: jar}).Do(req)
	if err != nil {
		t.Fatalf("get registries: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("empty home: %d %s", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"sources":[]`)) || !bytes.Contains(body, []byte(`"local_count":0`)) {
		t.Fatalf("empty home body = %s", body)
	}
}
