package auth_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	apiauth "gospark-hub/internal/api/auth"
	"gospark-hub/internal/auth"
	"gospark-hub/internal/config"
	"gospark-hub/internal/store"
	"gospark-hub/internal/web"
)

func testConfig() config.Config {
	return config.Config{
		AuthEnabled:       true,
		SessionCookieName: "spark_ai_hub_session",
		SessionTTLDays:    30,
		ForceSecureCookie: false,
	}
}

func setup(t *testing.T) (*store.DB, *httptest.Server, *cookiejar.Jar, func()) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	cfg := testConfig()
	r := web.NewRouter()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("jar: %v", err)
	}
	apiauth.Register(r, apiauth.Deps{
		Store:         db,
		Config:        cfg,
		Logger:        log.New(io.Discard, "", 0),
		TokenResolver: auth.NoopResolver{},
	})
	srv := httptest.NewServer(r)
	// Dummy guarded routes so the auth middleware is exercised on paths whose
	// real handlers arrive in later phases.
	ok := func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(http.StatusOK) }
	r.Handle("GET", "/api/recipes", ok)
	r.Handle("GET", "/v1/", ok)
	r.Handle("GET", "/mcp", ok)
	r.Handle("GET", "/ws/", ok)
	r.Handle("OPTIONS", "/api/auth/me", ok)
	return db, srv, jar, func() {
		srv.Close()
		_ = db.Close()
	}
}

func client(jar *cookiejar.Jar) *http.Client {
	if jar == nil {
		return &http.Client{}
	}
	return &http.Client{Jar: jar}
}

func mustJSON(t *testing.T, body io.Reader) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(body).Decode(&m); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	return m
}

func mustPost(t *testing.T, srv string, jar *cookiejar.Jar, path string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, srv+path, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client(jar).Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func mustDo(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
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
	resp, err := client(jar).Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func mustPatch(t *testing.T, srv string, jar *cookiejar.Jar, path string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPatch, srv+path, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client(jar).Do(req)
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
	resp, err := client(jar).Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func register(t *testing.T, srv string, jar *cookiejar.Jar, email, password, name string) map[string]any {
	t.Helper()
	resp := mustPost(t, srv, jar, "/api/auth/register", map[string]string{
		"email": email, "password": password, "name": name,
	})
	defer resp.Body.Close()
	return mustJSON(t, resp.Body)
}

func login(t *testing.T, srv string, jar *cookiejar.Jar, email, password string) *http.Response {
	t.Helper()
	return mustPost(t, srv, jar, "/api/auth/login", map[string]string{
		"email": email, "password": password,
	})
}

func TestStateNeedsSetup(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()

	resp := mustGet(t, srv.URL, jar, "/api/auth/state")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("state status %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["auth_enabled"] != true {
		t.Fatalf("auth_enabled want true got %v", body["auth_enabled"])
	}
	if body["needs_setup"] != true {
		t.Fatalf("needs_setup want true got %v", body["needs_setup"])
	}
	if body["user"] != nil {
		t.Fatalf("user want nil got %v", body["user"])
	}
}

func TestFirstRegistrationIsAdmin(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()

	resp := mustPost(t, srv.URL, jar, "/api/auth/register", map[string]string{
		"email": "a@x.io", "password": "secret123", "name": "A",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register status %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["status"] != "active" {
		t.Fatalf("status want active got %v", body["status"])
	}
	user, ok := body["user"].(map[string]any)
	if !ok {
		t.Fatalf("user not object")
	}
	if user["role"] != "admin" || user["status"] != "active" {
		t.Fatalf("want admin/active got %v", user)
	}
	if user["reviewed_at"] == nil {
		t.Fatalf("reviewed_at expected")
	}
	if _, has := user["api_key"]; has {
		t.Fatalf("public user must not contain api_key")
	}

	setCookie := findSetCookie(resp.Header.Values("Set-Cookie"), "spark_ai_hub_session")
	if setCookie == "" {
		t.Fatalf("expected Set-Cookie")
	}
	for _, want := range []string{"HttpOnly", "SameSite=Lax", "Path=/", "Max-Age=2592000"} {
		if !strings.Contains(setCookie, want) {
			t.Fatalf("cookie missing %q: %s", want, setCookie)
		}
	}
	if strings.Contains(setCookie, "Secure") {
		t.Fatalf("plain HTTP cookie should not be Secure")
	}
}

func TestSecureCookieForwardedProto(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "a@x.io", "secret123", "A")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/auth/login", strings.NewReader(`{"email":"a@x.io","password":"secret123"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := client(jar).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	c := findSetCookie(resp.Header.Values("Set-Cookie"), "spark_ai_hub_session")
	if !strings.Contains(c, "Secure") {
		t.Fatalf("expected Secure cookie, got %s", c)
	}
}

func TestForceSecureCookie(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	cfg.ForceSecureCookie = true
	r := web.NewRouter()
	jar, _ := cookiejar.New(nil)
	apiauth.Register(r, apiauth.Deps{Store: db, Config: cfg, Logger: log.New(io.Discard, "", 0), TokenResolver: auth.NoopResolver{}})
	srv := httptest.NewServer(r)
	defer srv.Close()
	defer db.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/auth/register", strings.NewReader(`{"email":"a@x.io","password":"secret123","name":"A"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client(jar).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	c := findSetCookie(resp.Header.Values("Set-Cookie"), "spark_ai_hub_session")
	if !strings.Contains(c, "Secure") {
		t.Fatalf("expected Secure cookie with FORCE_SECURE_COOKIE, got %s", c)
	}
}

func TestStateWithSession(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "a@x.io", "secret123", "A")

	resp := mustGet(t, srv.URL, jar, "/api/auth/state")
	defer resp.Body.Close()
	body := mustJSON(t, resp.Body)
	if body["needs_setup"] != false {
		t.Fatalf("needs_setup want false")
	}
	user := body["user"].(map[string]any)
	if user["email"] != "a@x.io" || user["role"] != "admin" {
		t.Fatalf("unexpected user %v", user)
	}
}

func TestPendingRegistration(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "admin@x.io", "secret123", "Admin")

	resp := mustPost(t, srv.URL, nil, "/api/auth/register", map[string]string{
		"email": "b@x.io", "password": "secret123", "name": "B",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["status"] != "pending" {
		t.Fatalf("want pending got %v", body["status"])
	}
	wantMsg := "Your account was created and is waiting for an administrator to approve it."
	if body["message"] != wantMsg {
		t.Fatalf("message want %q got %v", wantMsg, body["message"])
	}
	user := body["user"].(map[string]any)
	if user["status"] != "pending" || user["reviewed_at"] != nil {
		t.Fatalf("unexpected user %v", user)
	}
	if findSetCookie(resp.Header.Values("Set-Cookie"), "spark_ai_hub_session") != "" {
		t.Fatalf("pending registration must not set cookie")
	}
}

func TestPendingLogin(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "admin@x.io", "secret123", "Admin")
	register(t, srv.URL, nil, "b@x.io", "secret123", "B")

	resp := login(t, srv.URL, nil, "b@x.io", "secret123")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["detail"] != "Your account is waiting for an administrator to approve it." {
		t.Fatalf("unexpected detail %v", body)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "a@x.io", "secret123", "A")

	resp := login(t, srv.URL, nil, "a@x.io", "wrong")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["detail"] != "Incorrect email or password." {
		t.Fatalf("unexpected detail %v", body)
	}

	resp2 := login(t, srv.URL, nil, "nope@x.io", "secret123")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 for unknown email got %d", resp2.StatusCode)
	}
}

func TestLoginMissingPassword(t *testing.T) {
	_, srv, _, cleanup := setup(t)
	defer cleanup()
	resp := mustPost(t, srv.URL, nil, "/api/auth/login", map[string]string{"email": "a@x.io"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	detail := body["detail"].([]any)
	item := detail[0].(map[string]any)
	if item["type"] != "missing" || item["loc"].([]any)[1] != "password" {
		t.Fatalf("unexpected detail %v", item)
	}
}

func TestRegisterValidation(t *testing.T) {
	cases := []struct {
		name   string
		body   map[string]string
		status int
		want   string
	}{
		{"short", map[string]string{"email": "a@x.io", "password": "1234567"}, http.StatusBadRequest, "Password must be at least 8 characters."},
		{"bad_email", map[string]string{"email": "nope", "password": "secret123"}, http.StatusBadRequest, "Enter a valid email address."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, srv, _, cleanup := setup(t)
			defer cleanup()
			resp := mustPost(t, srv.URL, nil, "/api/auth/register", tc.body)
			defer resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("want %d got %d", tc.status, resp.StatusCode)
			}
			body := mustJSON(t, resp.Body)
			if body["detail"] != tc.want {
				t.Fatalf("want %q got %v", tc.want, body)
			}
		})
	}
}

func TestRegisterDuplicateEmail(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "a@x.io", "secret123", "A")
	resp := mustPost(t, srv.URL, nil, "/api/auth/register", map[string]string{"email": "A@X.IO", "password": "secret123", "name": "B"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["detail"] != "An account with that email already exists." {
		t.Fatalf("unexpected detail %v", body)
	}
}

func TestMeUnauthorized(t *testing.T) {
	_, srv, _, cleanup := setup(t)
	defer cleanup()
	resp := mustGet(t, srv.URL, nil, "/api/auth/me")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["detail"] != "Authentication required." {
		t.Fatalf("unexpected detail %v", body)
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	if !strings.HasPrefix(challenge, `Bearer realm="Spark AI Hub"`) {
		t.Fatalf("unexpected www-authenticate %q", challenge)
	}
}

func TestMe(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "a@x.io", "secret123", "A")

	resp := mustGet(t, srv.URL, jar, "/api/auth/me")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	key, ok := body["api_key"].(string)
	if !ok || !strings.HasPrefix(key, "sah-") || len(key) != 47 {
		t.Fatalf("bad api_key %v", body["api_key"])
	}
	usage, ok := body["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage not object")
	}
	for _, k := range []string{"requests", "prompt_tokens", "completion_tokens", "total_tokens", "last_used_at", "requests_30d", "tokens_30d", "requests_24h", "tokens_24h"} {
		if _, has := usage[k]; !has {
			t.Fatalf("usage missing %s", k)
		}
	}
}

func TestGuardedRoutesRequireAuth(t *testing.T) {
	_, srv, _, cleanup := setup(t)
	defer cleanup()
	resp := mustGet(t, srv.URL, nil, "/api/recipes")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["detail"] != "Authentication required." {
		t.Fatalf("unexpected detail %v", body)
	}

	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/api/auth/me", nil)
	resp2 := mustDo(t, req)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("options want 401 got %d", resp2.StatusCode)
	}
}

func TestUpdateMeName(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "a@x.io", "secret123", "A")

	resp := mustPatch(t, srv.URL, jar, "/api/auth/me", map[string]string{"name": "B"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["name"] != "B" {
		t.Fatalf("name want B got %v", body["name"])
	}
	if _, has := body["api_key"]; has {
		t.Fatalf("update me response must not contain api_key")
	}
	if _, has := body["usage"]; has {
		t.Fatalf("update me response must not contain usage")
	}
}

func TestUpdateMeEmailRequiresPassword(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "a@x.io", "secret123", "A")

	resp := mustPatch(t, srv.URL, jar, "/api/auth/me", map[string]string{"email": "c@x.io"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["detail"] != "Your current password is incorrect." {
		t.Fatalf("unexpected detail %v", body)
	}
}

func TestUpdateMeEmail(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "a@x.io", "secret123", "A")

	resp := mustPatch(t, srv.URL, jar, "/api/auth/me", map[string]string{"email": "c@x.io", "current_password": "secret123"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["email"] != "c@x.io" {
		t.Fatalf("email want c@x.io got %v", body["email"])
	}
}

func TestUpdateMeShortPassword(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "a@x.io", "secret123", "A")

	resp := mustPatch(t, srv.URL, jar, "/api/auth/me", map[string]string{"current_password": "secret123", "new_password": "1234567"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["detail"] != "Password must be at least 8 characters." {
		t.Fatalf("unexpected detail %v", body)
	}
}

func TestPasswordChangeKeepsSessions(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "a@x.io", "secret123", "A")
	jar2, _ := cookiejar.New(nil)
	login(t, srv.URL, jar2, "a@x.io", "secret123").Body.Close()

	resp := mustPatch(t, srv.URL, jar, "/api/auth/me", map[string]string{"current_password": "secret123", "new_password": "newsecret123"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("password change failed %d", resp.StatusCode)
	}

	resp2 := mustGet(t, srv.URL, jar2, "/api/auth/me")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("other session should still work got %d", resp2.StatusCode)
	}
}

func TestRotateAPIKey(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "a@x.io", "secret123", "A")
	me := mustGet(t, srv.URL, jar, "/api/auth/me")
	oldKey := mustJSON(t, me.Body)["api_key"].(string)
	me.Body.Close()

	resp := mustPost(t, srv.URL, jar, "/api/auth/me/api-key/rotate", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 got %d", resp.StatusCode)
	}
	newKey := mustJSON(t, resp.Body)["api_key"].(string)
	if newKey == oldKey || !strings.HasPrefix(newKey, "sah-") {
		t.Fatalf("key not rotated: %s -> %s", oldKey, newKey)
	}

	me2 := mustGet(t, srv.URL, jar, "/api/auth/me")
	defer me2.Body.Close()
	if got := mustJSON(t, me2.Body)["api_key"].(string); got != newKey {
		t.Fatalf("me key mismatch got %s want %s", got, newKey)
	}
}

func TestAPIKeyOnV1(t *testing.T) {
	db, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "a@x.io", "secret123", "A")
	me := mustGet(t, srv.URL, jar, "/api/auth/me")
	key := mustJSON(t, me.Body)["api_key"].(string)
	me.Body.Close()

	// Old key rejected after rotation.
	mustPost(t, srv.URL, jar, "/api/auth/me/api-key/rotate", nil).Body.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp := mustDo(t, req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	err := body["error"].(map[string]any)
	if err["code"] != "invalid_api_key" {
		t.Fatalf("unexpected error %v", body)
	}

	// API key must not work on /api.
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/recipes", nil)
	req2.Header.Set("Authorization", "Bearer "+key)
	resp2 := mustDo(t, req2)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("api key on /api want 401 got %d", resp2.StatusCode)
	}

	// Usage record with new key should be attributed.
	me2 := mustGet(t, srv.URL, jar, "/api/auth/me")
	newKey := mustJSON(t, me2.Body)["api_key"].(string)
	me2.Body.Close()
	uid, _ := db.GetUserByEmail("a@x.io")
	_ = db.RecordUsage(store.UsageEvent{UserID: uid.ID, Endpoint: "openai:chat/completions", TotalTokens: 42, Source: "api_key"})
	usage := mustGet(t, srv.URL, jar, "/api/auth/me/usage")
	uBody := mustJSON(t, usage.Body)
	usage.Body.Close()
	if uBody["usage"].(map[string]any)["total_tokens"] != float64(42) {
		t.Fatalf("usage not updated %v", uBody)
	}
	_ = newKey
}

func TestMCPAuthChallenge(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "a@x.io", "secret123", "A")

	// With API key allowed.
	me := mustGet(t, srv.URL, jar, "/api/auth/me")
	key := mustJSON(t, me.Body)["api_key"].(string)
	me.Body.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp := mustDo(t, req)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("mcp with api key should be authorized")
	}

	// Without key.
	resp2 := mustGet(t, srv.URL, nil, "/mcp")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", resp2.StatusCode)
	}
	challenge := resp2.Header.Get("WWW-Authenticate")
	if !strings.Contains(challenge, "resource_metadata=") || !strings.Contains(challenge, `scope="mcp"`) {
		t.Fatalf("challenge missing mcp metadata: %s", challenge)
	}

	// Invalid bearer.
	req3, _ := http.NewRequest(http.MethodGet, srv.URL+"/mcp", nil)
	req3.Header.Set("Authorization", "Bearer bad")
	resp3 := mustDo(t, req3)
	defer resp3.Body.Close()
	challenge3 := resp3.Header.Get("WWW-Authenticate")
	if !strings.Contains(challenge3, `error="invalid_token"`) {
		t.Fatalf("challenge missing invalid_token: %s", challenge3)
	}
}

func TestAdminList(t *testing.T) {
	db, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "admin@x.io", "secret123", "Admin")
	// create normal user
	register(t, srv.URL, nil, "user@x.io", "secret123", "User")
	_ = db.RecordUsage(store.UsageEvent{UserID: 2, Endpoint: "openai:x", TotalTokens: 10, Source: "api_key"})

	resp := mustGet(t, srv.URL, jar, "/api/admin/users")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	for _, k := range []string{"users", "totals", "models"} {
		if _, has := body[k]; !has {
			t.Fatalf("missing %s", k)
		}
	}
	users := body["users"].([]any)
	if len(users) != 2 {
		t.Fatalf("want 2 users got %d", len(users))
	}
	totals := body["totals"].(map[string]any)
	for _, k := range []string{"users", "pending", "active", "requests", "total_tokens", "storage_bytes"} {
		if _, has := totals[k]; !has {
			t.Fatalf("totals missing %s", k)
		}
	}
	if totals["total_tokens"] != float64(10) {
		t.Fatalf("total_tokens want 10 got %v", totals["total_tokens"])
	}
}

func TestAdminListForbiddenForUser(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "admin@x.io", "secret123", "Admin")
	mustPost(t, srv.URL, jar, "/api/admin/users", map[string]string{
		"email": "user@x.io", "password": "secret123",
	}).Body.Close()
	ujar, _ := cookiejar.New(nil)
	login(t, srv.URL, ujar, "user@x.io", "secret123").Body.Close()

	resp := mustGet(t, srv.URL, ujar, "/api/admin/users")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["detail"] != "Only an administrator can do that." {
		t.Fatalf("unexpected detail %v", body)
	}
}

func TestAdminCreateUser(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "admin@x.io", "secret123", "Admin")

	resp := mustPost(t, srv.URL, jar, "/api/admin/users", map[string]string{
		"email": "d@x.io", "password": "secret123",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["role"] != "user" || body["status"] != "active" || body["reviewed_at"] == nil {
		t.Fatalf("unexpected user %v", body)
	}
	usage := body["usage"].(map[string]any)
	if usage["requests"] != float64(0) {
		t.Fatalf("usage not empty %v", usage)
	}
}

func TestAdminCreateUserOnEmptyDB(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	// Establish an admin, then let the admin API create the next account. The
	// explicit role/status mean the positional first-user rule never fires, so
	// an admin-created account is always a plain user.
	register(t, srv.URL, jar, "admin@x.io", "secret123", "Admin")
	resp := mustPost(t, srv.URL, jar, "/api/admin/users", map[string]string{
		"email": "d@x.io", "password": "secret123",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["role"] != "user" {
		t.Fatalf("admin create must not become admin: %v", body)
	}
}

func TestAdminCreateValidation(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "admin@x.io", "secret123", "Admin")

	resp := mustPost(t, srv.URL, jar, "/api/admin/users", map[string]string{
		"email": "d@x.io", "password": "secret123", "role": "root",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["detail"] != "Role must be admin or user." {
		t.Fatalf("unexpected detail %v", body)
	}
}

func TestAdminApproveAndReject(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "admin@x.io", "secret123", "Admin")
	register(t, srv.URL, nil, "user@x.io", "secret123", "User")

	// approve
	resp := mustPatch(t, srv.URL, jar, "/api/admin/users/2", map[string]string{"status": "active"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve want 200 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["status"] != "active" || body["reviewed_at"] == nil {
		t.Fatalf("not approved %v", body)
	}
	ujar, _ := cookiejar.New(nil)
	lresp := login(t, srv.URL, ujar, "user@x.io", "secret123")
	lresp.Body.Close()
	if lresp.StatusCode != http.StatusOK {
		t.Fatalf("login after approve want 200 got %d", lresp.StatusCode)
	}

	// reject
	resp2 := mustPatch(t, srv.URL, jar, "/api/admin/users/2", map[string]string{"status": "rejected"})
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("reject want 200 got %d", resp2.StatusCode)
	}
	me := mustGet(t, srv.URL, ujar, "/api/auth/me")
	defer me.Body.Close()
	if me.StatusCode != http.StatusUnauthorized {
		t.Fatalf("rejected session want 401 got %d", me.StatusCode)
	}
}

func TestAdminUpdateNotFoundAndValidationOrder(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "admin@x.io", "secret123", "Admin")

	resp := mustPatch(t, srv.URL, jar, "/api/admin/users/999999", map[string]string{"status": "active"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 got %d", resp.StatusCode)
	}

	resp2 := mustPatch(t, srv.URL, jar, "/api/admin/users/999999", map[string]string{"role": "root"})
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("validation order: want 400 got %d", resp2.StatusCode)
	}
}

func TestOnlyAdminCannotBeDemoted(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "admin@x.io", "secret123", "Admin")

	resp := mustPatch(t, srv.URL, jar, "/api/admin/users/1", map[string]string{"role": "user"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	want := "This is the only administrator — promote someone else first."
	if body["detail"] != want {
		t.Fatalf("unexpected detail %v", body)
	}
}

func TestDeleteSelf(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "admin@x.io", "secret123", "Admin")

	resp := mustDelete(t, srv.URL, jar, "/api/admin/users/1")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["detail"] != "You cannot delete your own account." {
		t.Fatalf("unexpected detail %v", body)
	}
}

func TestDeleteOther(t *testing.T) {
	db, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "admin@x.io", "secret123", "Admin")
	register(t, srv.URL, nil, "user@x.io", "secret123", "User")
	u, _ := db.GetUserByEmail("user@x.io")
	_ = db.RecordUsage(store.UsageEvent{UserID: u.ID, Endpoint: "x", TotalTokens: 1})

	resp := mustDelete(t, srv.URL, jar, "/api/admin/users/2")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["status"] != "deleted" {
		t.Fatalf("unexpected body %v", body)
	}
	if u, err := db.GetUserByID(2); err != nil || u != nil {
		t.Fatalf("user still exists: %v %v", u, err)
	}
	summary, _ := db.UsageSummary(&u.ID)
	if len(summary) != 0 {
		t.Fatalf("usage not cascaded %v", summary)
	}
}

func TestUserUsage(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "admin@x.io", "secret123", "Admin")

	resp := mustGet(t, srv.URL, jar, "/api/admin/users/999999/usage")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 got %d", resp.StatusCode)
	}

	resp2 := mustGet(t, srv.URL, jar, "/api/admin/users/1/usage")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("want 200 got %d", resp2.StatusCode)
	}
	body := mustJSON(t, resp2.Body)
	if _, has := body["usage"]; !has {
		t.Fatalf("missing usage")
	}
	if _, has := body["models"]; !has {
		t.Fatalf("missing models")
	}
}

func TestLogout(t *testing.T) {
	_, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "a@x.io", "secret123", "A")

	resp := mustPost(t, srv.URL, jar, "/api/auth/logout", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 got %d", resp.StatusCode)
	}
	body := mustJSON(t, resp.Body)
	if body["status"] != "signed_out" {
		t.Fatalf("unexpected body %v", body)
	}
	c := findSetCookie(resp.Header.Values("Set-Cookie"), "spark_ai_hub_session")
	if !strings.Contains(c, "Max-Age=0") {
		t.Fatalf("logout cookie missing Max-Age=0: %s", c)
	}

	me := mustGet(t, srv.URL, jar, "/api/auth/me")
	defer me.Body.Close()
	if me.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", me.StatusCode)
	}

	// logout without cookie still 200
	resp2 := mustPost(t, srv.URL, nil, "/api/auth/logout", nil)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("logout without cookie want 200 got %d", resp2.StatusCode)
	}
}

func TestAuthDisabled(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	cfg.AuthEnabled = false
	r := web.NewRouter()
	apiauth.Register(r, apiauth.Deps{Store: db, Config: cfg, Logger: log.New(io.Discard, "", 0), TokenResolver: auth.NoopResolver{}})
	srv := httptest.NewServer(r)
	defer srv.Close()
	defer db.Close()

	resp := mustGet(t, srv.URL, nil, "/api/auth/state")
	defer resp.Body.Close()
	body := mustJSON(t, resp.Body)
	if body["auth_enabled"] != false || body["user"] != nil {
		t.Fatalf("unexpected state %v", body)
	}

	me := mustGet(t, srv.URL, nil, "/api/auth/me")
	defer me.Body.Close()
	if me.StatusCode != http.StatusUnauthorized {
		t.Fatalf("auth disabled /me want 401 got %d", me.StatusCode)
	}
}

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := auth.HashPassword("secret123")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^pbkdf2_sha256\$260000\$[0-9a-f]{32}\$[0-9a-f]{64}$`).MatchString(hash) {
		t.Fatalf("hash format wrong: %s", hash)
	}
	if !auth.VerifyPassword("secret123", hash) {
		t.Fatal("verify correct password failed")
	}
	if auth.VerifyPassword("wrong", hash) {
		t.Fatal("verify wrong password succeeded")
	}
}

// TestUpstreamHashFormat verifies a hash produced by the Python upstream's
// exact algorithm (hashlib.pbkdf2_hmac with a fixed salt) so a live Hub's
// users table stays readable.
func TestUpstreamHashFormat(t *testing.T) {
	// Generated with:
	//   hashlib.pbkdf2_hmac('sha256', b'secret123', b'0011...', 260000)
	const upstreamHash = "pbkdf2_sha256$260000$00112233445566778899aabbccddeeff$ed8ec7d55cfb8f6e47e798b6cbc9b4552693881f74f2b5727f3d596264a98e25"
	if !auth.VerifyPassword("secret123", upstreamHash) {
		t.Fatal("upstream-format hash failed verification")
	}
	if auth.VerifyPassword("secret124", upstreamHash) {
		t.Fatal("wrong password accepted for upstream hash")
	}
}

func TestWebSocketUnauthorizedClose(t *testing.T) {
	_, srv, _, cleanup := setup(t)
	defer cleanup()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /ws/metrics HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", u.Host)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	frame := make([]byte, 4)
	if _, err := io.ReadFull(conn, frame); err != nil {
		t.Fatalf("read close frame: %v", err)
	}
	if frame[0] != 0x88 {
		t.Fatalf("expected close frame, got %#x", frame[0])
	}
	code := int(frame[2])<<8 | int(frame[3])
	if code != 1008 {
		t.Fatalf("close code want 1008 got %d", code)
	}
}

func TestJanitorPurge(t *testing.T) {
	db, srv, jar, cleanup := setup(t)
	defer cleanup()
	register(t, srv.URL, jar, "a@x.io", "secret123", "A")
	// create expired session manually
	u, _ := db.GetUserByEmail("a@x.io")
	old := "spark_ai_hub_session=oldsession; Path=/"
	_ = old
	_, _ = db.CreateSession(u.ID, "", "", -1) // negative TTL -> already expired
	if err := db.PurgeExpiredSessions(); err != nil {
		t.Fatalf("purge: %v", err)
	}
}

// helpers below

func respFor(t *testing.T, jar *cookiejar.Jar, srv, path string) *http.Response {
	t.Helper()
	resp, err := client(jar).Get(srv + path)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return resp
}

func respCookies(resp *http.Response) []string {
	defer resp.Body.Close()
	return resp.Header.Values("Set-Cookie")
}

func findSetCookie(cookies []string, name string) string {
	for _, c := range cookies {
		if strings.HasPrefix(c, name+"=") {
			return c
		}
	}
	return ""
}

// silence unused import warning if any test is skipped
var _ = os.ReadFile
