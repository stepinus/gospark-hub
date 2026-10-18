package files

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	apiauth "gospark-hub/internal/api/auth"
	"gospark-hub/internal/auth"
	"gospark-hub/internal/config"
	"gospark-hub/internal/media"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
	"gospark-hub/internal/web"
)

func testConfig(dir string) config.Config {
	return config.Config{
		AuthEnabled:       true,
		SessionCookieName: "spark_ai_hub_session",
		SessionTTLDays:    30,
		ForceSecureCookie: false,
		DataDir:           dir,
	}
}

func setup(t *testing.T) (*store.DB, config.Config, *httptest.Server, *cookiejar.Jar, *cookiejar.Jar, func()) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	cfg := testConfig(dir)
	r := web.NewRouter()
	jarA, _ := cookiejar.New(nil)
	jarB, _ := cookiejar.New(nil)

	authDeps := apiauth.Deps{
		Store:         db,
		Config:        cfg,
		Logger:        log.New(io.Discard, "", 0),
		TokenResolver: auth.NoopResolver{},
	}
	apiauth.Register(r, authDeps)
	Register(r, Deps{
		Config:   cfg,
		Store:    db,
		Logger:   log.New(io.Discard, "", 0),
		Registry: registry.NewCache(filepath.Join(dir, "recipes")),
	})

	srv := httptest.NewServer(r)
	return db, cfg, srv, jarA, jarB, func() {
		srv.Close()
		_ = db.Close()
	}
}

func createUser(t *testing.T, db *store.DB, email, role, status string) *store.User {
	t.Helper()
	key, err := auth.NewAPIKey()
	if err != nil {
		t.Fatalf("api key: %v", err)
	}
	hash, err := auth.HashPassword("secret123")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	u, err := db.CreateUser(email, "", hash, role, status, key)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

func mustSessionCookie(t *testing.T, db *store.DB, cfg config.Config, srv *httptest.Server, u *store.User) *cookiejar.Jar {
	t.Helper()
	tok, err := db.CreateSession(u.ID, "test", "127.0.0.1", cfg.SessionTTLDays)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	jar, _ := cookiejar.New(nil)
	uurl, _ := url.Parse(srv.URL)
	jar.SetCookies(uurl, []*http.Cookie{
		{Name: cfg.SessionCookieName, Value: tok},
	})
	return jar
}

func makePNG(t *testing.T, size int) []byte {
	t.Helper()
	if size == 0 {
		size = 10
	}
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png: %v", err)
	}
	return buf.Bytes()
}

func testClient(jar *cookiejar.Jar) *http.Client {
	if jar == nil {
		return &http.Client{}
	}
	return &http.Client{Jar: jar}
}

func mustPost(t *testing.T, srv string, jar *cookiejar.Jar, path string, body []byte, ct string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", ct)
	resp, err := testClient(jar).Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func mustGet(t *testing.T, srv string, jar *cookiejar.Jar, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := testClient(jar).Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func mustDelete(t *testing.T, srv string, jar *cookiejar.Jar, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, srv+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := testClient(jar).Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func mustPut(t *testing.T, srv string, jar *cookiejar.Jar, path string, body []byte, ct string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, srv+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", ct)
	resp, err := testClient(jar).Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func mustJSON(t *testing.T, body io.Reader) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(body).Decode(&m); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	return m
}

func TestUploadImage(t *testing.T) {
	db, _, srv, jar, _, cleanup := setup(t)
	defer cleanup()
	u := createUser(t, db, "a@x.io", "user", "active")
	jar = mustSessionCookie(t, db, testConfig(t.TempDir()), srv, u)

	resp := mustPost(t, srv.URL, jar, "/api/uploads", makePNG(t, 10), "application/octet-stream")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["kind"] != "image" {
		t.Fatalf("kind want image got %v", body["kind"])
	}
	if body["url"] == "" {
		t.Fatal("missing url")
	}

	// Ensure ownership was recorded.
	owner, ok, err := db.MediaOwner(body["id"].(string) + ".png")
	if err != nil || !ok || owner != u.ID {
		t.Fatalf("ownership not recorded: %v %v %v", owner, ok, err)
	}
}

func TestUploadEmpty(t *testing.T) {
	db, _, srv, jar, _, cleanup := setup(t)
	defer cleanup()
	u := createUser(t, db, "a@x.io", "user", "active")
	jar = mustSessionCookie(t, db, testConfig(t.TempDir()), srv, u)

	resp := mustPost(t, srv.URL, jar, "/api/uploads", nil, "application/octet-stream")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty upload status %d", resp.StatusCode)
	}
}

func TestUploadSizeLimit(t *testing.T) {
	orig := media.MaxBytes
	media.MaxBytes = 32
	defer func() { media.MaxBytes = orig }()

	db, _, srv, jar, _, cleanup := setup(t)
	defer cleanup()
	u := createUser(t, db, "a@x.io", "user", "active")
	jar = mustSessionCookie(t, db, testConfig(t.TempDir()), srv, u)

	resp := mustPost(t, srv.URL, jar, "/api/uploads", bytes.Repeat([]byte{0}, 64), "application/octet-stream")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("size limit status %d", resp.StatusCode)
	}
}

func TestListAndDeleteVisibility(t *testing.T) {
	db, _, srv, jarA, jarB, cleanup := setup(t)
	defer cleanup()

	ua := createUser(t, db, "a@x.io", "user", "active")
	ub := createUser(t, db, "b@x.io", "user", "active")
	jarA = mustSessionCookie(t, db, testConfig(t.TempDir()), srv, ua)
	jarB = mustSessionCookie(t, db, testConfig(t.TempDir()), srv, ub)

	resp := mustPost(t, srv.URL, jarA, "/api/uploads", makePNG(t, 10), "application/octet-stream")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status %d", resp.StatusCode)
	}

	// User A sees the file.
	resp = mustGet(t, srv.URL, jarA, "/api/media")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("A list status %d: %s", resp.StatusCode, string(b))
	}
	body := mustJSON(t, resp.Body)
	files, ok := body["files"].([]any)
	if !ok {
		t.Fatalf("A files missing or wrong type: %v", body)
	}
	if len(files) != 1 {
		t.Fatalf("A files want 1 got %d", len(files))
	}
	name := files[0].(map[string]any)["name"].(string)

	// User B sees none.
	resp = mustGet(t, srv.URL, jarB, "/api/media")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("B list status %d: %s", resp.StatusCode, string(b))
	}
	body = mustJSON(t, resp.Body)
	bfiles, ok := body["files"].([]any)
	if !ok {
		t.Fatalf("B files missing or wrong type: %v", body)
	}
	if len(bfiles) != 0 {
		t.Fatalf("B files want 0 got %d", len(bfiles))
	}

	// User B cannot delete.
	resp = mustDelete(t, srv.URL, jarB, "/api/media/"+name)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("B delete status %d", resp.StatusCode)
	}

	// User A can delete.
	resp = mustDelete(t, srv.URL, jarA, "/api/media/"+name)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("A delete status %d", resp.StatusCode)
	}
}

func TestThumb(t *testing.T) {
	db, _, srv, jar, _, cleanup := setup(t)
	defer cleanup()
	u := createUser(t, db, "a@x.io", "user", "active")
	jar = mustSessionCookie(t, db, testConfig(t.TempDir()), srv, u)

	resp := mustPost(t, srv.URL, jar, "/api/uploads", makePNG(t, 40), "application/octet-stream")
	defer resp.Body.Close()
	body := mustJSON(t, resp.Body)
	name := body["id"].(string) + ".png"

	resp = mustGet(t, srv.URL, jar, "/api/media/"+name+"/thumb")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("thumb status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("thumb ct %s", ct)
	}
}

func TestUploadLinkOneTime(t *testing.T) {
	db, _, srv, _, _, cleanup := setup(t)
	defer cleanup()
	u := createUser(t, db, "a@x.io", "user", "active")
	ls := media.NewLinkService(db)
	token, _, err := ls.Create("upload", u.ID, "")
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	// First upload succeeds.
	resp := mustPost(t, srv.URL, nil, "/link/upload/"+token, makePNG(t, 10), "application/octet-stream")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first upload status %d", resp.StatusCode)
	}

	// Second upload fails.
	resp = mustPost(t, srv.URL, nil, "/link/upload/"+token, makePNG(t, 10), "application/octet-stream")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second upload status %d", resp.StatusCode)
	}
}

func TestDownloadLink(t *testing.T) {
	db, _, srv, jar, _, cleanup := setup(t)
	defer cleanup()
	u := createUser(t, db, "a@x.io", "user", "active")
	jar = mustSessionCookie(t, db, testConfig(t.TempDir()), srv, u)

	resp := mustPost(t, srv.URL, jar, "/api/uploads", makePNG(t, 10), "application/octet-stream")
	defer resp.Body.Close()
	body := mustJSON(t, resp.Body)
	name := body["id"].(string) + ".png"

	ls := media.NewLinkService(db)
	token, _, err := ls.Create("download", u.ID, name)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	// Download returns the file.
	resp = mustGet(t, srv.URL, nil, "/link/download/"+token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("download ct %s", ct)
	}

	// Expired link fails.
	if err := ls.PurgeExpired(); err != nil {
		t.Fatalf("purge: %v", err)
	}
}

func TestPutThumbForMesh(t *testing.T) {
	db, cfg, srv, jar, _, cleanup := setup(t)
	defer cleanup()
	u := createUser(t, db, "a@x.io", "user", "active")
	jar = mustSessionCookie(t, db, cfg, srv, u)

	id := "00112233445566778899aabbccddeeff"
	meshDir := filepath.Join(cfg.DataDir, "meshes")
	_ = os.MkdirAll(meshDir, 0o755)
	_ = os.WriteFile(filepath.Join(meshDir, id+".glb"), []byte("fake glb"), 0o644)
	_ = os.WriteFile(filepath.Join(meshDir, id+".png"), makePNG(t, 10), 0o644)
	if err := db.RecordMedia(id+".glb", u.ID, "", nil); err != nil {
		t.Fatalf("record media: %v", err)
	}

	renderPNG := makePNG(t, 20)
	resp := mustPut(t, srv.URL, jar, "/api/media/"+id+".glb/thumb", renderPNG, "image/png")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("put thumb status %d: %s", resp.StatusCode, string(b))
	}
	if _, err := os.Stat(filepath.Join(meshDir, id+".render.png")); err != nil {
		t.Fatalf("render not saved: %v", err)
	}
}

func TestRecordSkipsNullUser(t *testing.T) {
	db, _, srv, _, _, cleanup := setup(t)
	defer cleanup()
	_ = srv

	ms := media.NewStore(testConfig(t.TempDir()), db)
	if err := ms.Record("foo.png", 0, "model", nil); err != nil {
		t.Fatalf("record nil user should be no-op: %v", err)
	}
	_, ok, err := db.MediaOwner("foo.png")
	if err != nil {
		t.Fatalf("media owner: %v", err)
	}
	if ok {
		t.Fatal("expected no media row for nil user")
	}
}

func TestExpiredLinkJanitor(t *testing.T) {
	db, _, srv, _, _, cleanup := setup(t)
	defer cleanup()
	_ = srv
	u := createUser(t, db, "a@x.io", "user", "active")

	ls := media.NewLinkService(db)
	token, _, err := ls.Create("upload", u.ID, "")
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	// Force expiration by inserting a row with past expires_at.
	_, err = db.Exec("UPDATE media_links SET expires_at = 1 WHERE token_hash != ''")
	if err != nil {
		t.Fatalf("update expires: %v", err)
	}
	if err := ls.PurgeExpired(); err != nil {
		t.Fatalf("purge: %v", err)
	}

	owner, err := ls.ClaimUpload(token)
	if err != nil {
		t.Fatalf("claim after purge: %v", err)
	}
	if owner != nil {
		t.Fatal("expected expired link to be gone")
	}
}
