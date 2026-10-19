package engines

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
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
	engine "gospark-hub/internal/engines"
	"gospark-hub/internal/native"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
	"gospark-hub/internal/web"
)

// env is a daemon-like router: real auth middleware, the engine routes, one
// native recipe in the cache and a scripted systemd runner.
type env struct {
	srv   *httptest.Server
	cfg   config.Config
	db    *store.DB
	mgr   *engine.Manager
	run   *fakeSystemd
	home  string
	cache *registry.Cache
}

// fakeSystemd answers is-active from active and records every command.
type fakeSystemd struct {
	active bool
	calls  [][]string
}

func (f *fakeSystemd) runner() *native.Runner {
	return &native.Runner{
		LookPath: func(string) (string, error) { return "/bin/true", nil },
		Run: func(_ context.Context, args ...string) (string, int, error) {
			f.calls = append(f.calls, args)
			if len(args) >= 3 && args[0] == "systemctl" && args[2] == "is-active" {
				if f.active {
					return "active", 0, nil
				}
				return "inactive", 3, nil
			}
			return "", 0, nil
		},
	}
}

func newEnv(t *testing.T, engineHome string) *env {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	recipes := filepath.Join(dir, "recipes")
	if err := os.MkdirAll(filepath.Join(recipes, "qwen-native"), 0o755); err != nil {
		t.Fatalf("mkdir recipe: %v", err)
	}
	body := "name: Qwen Native\nslug: qwen-native\nkind: native\nnative:\n  engine: tensorfold\n  model: \"hf:x/y@rev\"\n"
	if err := os.WriteFile(filepath.Join(recipes, "qwen-native", "recipe.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write recipe: %v", err)
	}
	cache := registry.NewCache(recipes)

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
	run := &fakeSystemd{}
	mgr := engine.NewManager(engineHome)
	Register(r, Deps{Engines: mgr, Native: run.runner(), Reg: cache, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &env{srv: srv, cfg: cfg, db: db, mgr: mgr, run: run, home: engineHome, cache: cache}
}

// tarGz builds a one-file gzipped tarball with an executable member.
func tarGz(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (e *env) user(t *testing.T, email, role string) *store.User {
	t.Helper()
	key, err := auth.NewAPIKey()
	if err != nil {
		t.Fatalf("api key: %v", err)
	}
	hash, err := auth.HashPassword("secret123")
	if err != nil {
		t.Fatalf("hash: %v", err)
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

func (e *env) do(t *testing.T, jar *cookiejar.Jar, method, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
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
	e := newEnv(t, filepath.Join(t.TempDir(), "engines"))
	userJar := e.jar(t, e.user(t, "user@example.com", "user"))

	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/engines"},
		{http.MethodPost, "/api/engines/tensorfold/install"},
		{http.MethodPost, "/api/engines/tensorfold/update"},
		{http.MethodDelete, "/api/engines/tensorfold"},
	}
	for _, rt := range routes {
		code, body := e.do(t, userJar, rt.method, rt.path)
		if code != http.StatusForbidden {
			t.Fatalf("%s %s as user: %d %s", rt.method, rt.path, code, body)
		}
	}
	if code, body := e.do(t, nil, http.MethodGet, "/api/engines"); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d %s", code, body)
	}
}

func TestTableRoutesRefuseBusyEngine(t *testing.T) {
	// A running native recipe is detected through the recipe cache plus the
	// systemd runner, so update and delete become 409.
	e := newEnv(t, t.TempDir())
	e.run.active = true
	jar := adminJar(t, e)

	for _, rt := range []struct{ method, path string }{
		{http.MethodPost, "/api/engines/tensorfold/update"},
		{http.MethodDelete, "/api/engines/tensorfold"},
	} {
		code, body := e.do(t, jar, rt.method, rt.path)
		if code != http.StatusConflict {
			t.Fatalf("%s %s: %d %s, want 409", rt.method, rt.path, code, body)
		}
		var got map[string]string
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got["detail"] != "A native recipe is running on this engine" {
			t.Fatalf("detail = %q", got["detail"])
		}
	}
}

func TestUnknownEngineIsNotFound(t *testing.T) {
	e := newEnv(t, t.TempDir())
	jar := adminJar(t, e)

	if code, body := e.do(t, jar, http.MethodPost, "/api/engines/nope/install"); code != http.StatusNotFound {
		t.Fatalf("install unknown: %d %s", code, body)
	}
	if code, body := e.do(t, jar, http.MethodDelete, "/api/engines/nope"); code != http.StatusNotFound {
		t.Fatalf("delete unknown: %d %s", code, body)
	}
}

// TestInstallConflictAndUnchangedUpdate drives the real Manager against a fake
// GitHub, so the 409 wording and the changed=false answer are pinned.
func TestInstallConflictAndUnchangedUpdate(t *testing.T) {
	version := "1.1.1"
	arch, err := (&engine.Manager{}).DetectArch()
	if err != nil {
		t.Fatalf("arch: %v", err)
	}
	asset := fmt.Sprintf("tensorfold-%s-%s.tar.gz", version, arch)
	dirName := fmt.Sprintf("tensorfold-%s-%s", version, arch)
	tarball := tarGz(t, dirName+"/bin/tensorfold-native", "#!/bin/sh\necho "+version+"\n")
	sum := sha256Hex(tarball)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/ashhart/TensorFold/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v%s"}`, version)
	})
	assetPath := "/ashhart/TensorFold/releases/download/v" + version + "/" + asset
	mux.HandleFunc(assetPath, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(tarball) })
	mux.HandleFunc(assetPath+".sha256", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", sum, asset)
	})
	gh := httptest.NewServer(mux)
	t.Cleanup(gh.Close)

	home := filepath.Join(t.TempDir(), "engines")
	e := newEnv(t, home)
	e.mgr.HTTP = gh.Client()
	e.mgr.GitHubAPI = gh.URL
	e.mgr.DownloadBase = gh.URL
	jar := adminJar(t, e)

	code, body := e.do(t, jar, http.MethodGet, "/api/engines")
	if code != http.StatusOK || !strings.Contains(string(body), `"latest":"1.1.1"`) || !strings.Contains(string(body), `"installed":false`) {
		t.Fatalf("list: %d %s", code, body)
	}

	code, body = e.do(t, jar, http.MethodPost, "/api/engines/tensorfold/install")
	if code != http.StatusOK || !strings.Contains(string(body), `"installed":true`) {
		t.Fatalf("install: %d %s", code, body)
	}

	code, body = e.do(t, jar, http.MethodPost, "/api/engines/tensorfold/install")
	if code != http.StatusConflict || !strings.Contains(string(body), "Engine tensorfold 1.1.1 is already installed") {
		t.Fatalf("repeat install: %d %s", code, body)
	}

	code, body = e.do(t, jar, http.MethodPost, "/api/engines/tensorfold/update")
	if code != http.StatusOK || !strings.Contains(string(body), `"changed":false`) || !strings.Contains(string(body), `"version":"1.1.1"`) {
		t.Fatalf("unchanged update: %d %s", code, body)
	}

	code, body = e.do(t, jar, http.MethodDelete, "/api/engines/tensorfold")
	if code != http.StatusOK || !strings.Contains(string(body), `"deleted":true`) {
		t.Fatalf("delete: %d %s", code, body)
	}
}
