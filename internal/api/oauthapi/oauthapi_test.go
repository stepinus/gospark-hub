package oauthapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	apiauth "gospark-hub/internal/api/auth"
	"gospark-hub/internal/auth"
	"gospark-hub/internal/config"
	"gospark-hub/internal/oauth"
	"gospark-hub/internal/store"
	"gospark-hub/internal/web"
)

const (
	callback      = "https://claude.ai/api/mcp/auth_callback"
	ownerEmail    = "owner@example.com"
	ownerPassword = "correct horse battery"
)

var requestIDRE = regexp.MustCompile(`name="request_id" value="([^"]+)"`)

type env struct {
	t      *testing.T
	db     *store.DB
	cfg    config.Config
	svc    *oauth.Service
	srv    *httptest.Server
	origin string
	owner  *store.User
	client *http.Client
}

func newEnv(t *testing.T) *env {
	t.Helper()
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
	svc := oauth.NewService(db)
	// The resolver makes OAuth access tokens valid on /mcp, exactly as main.go
	// will wire it once the phases are merged.
	apiauth.Register(r, auth.Deps{
		Store:         db,
		Config:        cfg,
		Logger:        log.New(io.Discard, "", 0),
		TokenResolver: svc,
	})
	Register(r, Deps{
		Config:  cfg,
		Store:   db,
		Logger:  log.New(io.Discard, "", 0),
		Service: svc,
	})
	// A stand-in for the phase 7 /mcp endpoint: the middleware is what the
	// OAuth tests exercise here.
	r.Handle("POST", "/mcp", func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"tools": []any{}},
		})
	})

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	e := &env{t: t, db: db, cfg: cfg, svc: svc, srv: srv, origin: srv.URL}
	e.client = e.newClient()
	e.owner = e.createOwner()
	return e
}

func (e *env) newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (e *env) createOwner() *store.User {
	e.t.Helper()
	hash, err := auth.HashPassword(ownerPassword)
	if err != nil {
		e.t.Fatalf("hash: %v", err)
	}
	key, err := auth.NewAPIKey()
	if err != nil {
		e.t.Fatalf("api key: %v", err)
	}
	u, err := e.db.CreateUser(ownerEmail, "", hash, "", "", key)
	if err != nil {
		e.t.Fatalf("create owner: %v", err)
	}
	return u
}

func (e *env) do(c *http.Client, method, path string, body io.Reader, contentType string, headers map[string]string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, body)
	if err != nil {
		e.t.Fatalf("request: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func decodeJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	body := readBody(t, resp)
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return out
}

// ── helpers that walk the flow ──────────────────────────────────────────────

func pkce(t *testing.T) (verifier, challenge string) {
	t.Helper()
	raw, err := store.NewRandomToken(48)
	if err != nil {
		t.Fatalf("random: %v", err)
	}
	sum := sha256.Sum256([]byte(raw))
	return raw, base64.RawURLEncoding.EncodeToString(sum[:])
}

func (e *env) register() string {
	e.t.Helper()
	body := fmt.Sprintf(`{"redirect_uris":["%s"],"client_name":"Claude","token_endpoint_auth_method":"none"}`, callback)
	resp := e.do(e.client, "POST", "/oauth/register", strings.NewReader(body), "application/json", nil)
	if resp.StatusCode != http.StatusCreated {
		e.t.Fatalf("register: %d %s", resp.StatusCode, readBody(e.t, resp))
	}
	out := decodeJSON(e.t, resp)
	id, _ := out["client_id"].(string)
	if id == "" {
		e.t.Fatalf("no client_id in %v", out)
	}
	return id
}

func (e *env) authorizeParams(clientID, challenge string) url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {callback},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {"xyz"},
		"resource":              {e.origin + "/mcp"},
		"scope":                 {"mcp"},
	}
}

// authorizePage fetches the consent (or sign-in) page and returns the request id.
func (e *env) authorizePage(c *http.Client, clientID, challenge string) (string, *http.Response) {
	e.t.Helper()
	resp := e.do(c, "GET", "/oauth/authorize?"+e.authorizeParams(clientID, challenge).Encode(), nil, "", nil)
	body := readBody(e.t, resp)
	m := requestIDRE.FindStringSubmatch(body)
	if m == nil {
		e.t.Fatalf("no request_id in %d %s", resp.StatusCode, body)
	}
	return m[1], resp
}

func (e *env) login(c *http.Client, requestID, password string) *http.Response {
	e.t.Helper()
	form := url.Values{
		"request_id": {requestID},
		"action":     {"login"},
		"email":      {ownerEmail},
		"password":   {password},
	}
	return e.do(c, "POST", "/oauth/authorize", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", nil)
}

func (e *env) decide(c *http.Client, requestID, action string) *http.Response {
	e.t.Helper()
	form := url.Values{"request_id": {requestID}}
	if action != "" {
		form.Set("action", action)
	}
	return e.do(c, "POST", "/oauth/authorize", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", nil)
}

// code walks register → authorize → sign in → allow and returns the code.
func (e *env) code(clientID, challenge string) string {
	e.t.Helper()
	requestID, _ := e.authorizePage(e.client, clientID, challenge)
	resp := e.login(e.client, requestID, ownerPassword)
	body := readBody(e.t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Allow Claude?") {
		e.t.Fatalf("login: %d %s", resp.StatusCode, body)
	}

	resp = e.decide(e.client, requestID, "allow")
	body = readBody(e.t, resp)
	if resp.StatusCode != http.StatusSeeOther {
		e.t.Fatalf("allow: %d %s", resp.StatusCode, body)
	}
	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		e.t.Fatalf("location: %v", err)
	}
	if got := location.Scheme + "://" + location.Host + location.Path; got != callback {
		e.t.Fatalf("redirected to %q, want %q", got, callback)
	}
	q := location.Query()
	if q.Get("state") != "xyz" || q.Get("iss") != e.origin {
		e.t.Fatalf("redirect query = %s", location.RawQuery)
	}
	code := q.Get("code")
	if code == "" {
		e.t.Fatalf("no code in %s", location)
	}
	return code
}

func (e *env) exchangeWith(c *http.Client, clientID, code, verifier string) (*http.Response, map[string]any) {
	e.t.Helper()
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {clientID},
		"redirect_uri":  {callback},
		"code_verifier": {verifier},
		"resource":      {e.origin + "/mcp"},
	}
	resp := e.do(c, "POST", "/oauth/token", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", nil)
	return resp, decodeJSON(e.t, resp)
}

func (e *env) tokens(clientID, code, verifier string) map[string]any {
	e.t.Helper()
	resp, body := e.exchangeWith(e.client, clientID, code, verifier)
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("token: %d %v", resp.StatusCode, body)
	}
	return body
}

func (e *env) mcp(c *http.Client, token string) *http.Response {
	e.t.Helper()
	return e.do(c, "POST", "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`),
		"application/json", map[string]string{"Authorization": "Bearer " + token})
}

// ── discovery ───────────────────────────────────────────────────────────────

func TestMetadataDocuments(t *testing.T) {
	e := newEnv(t)

	for _, path := range []string{
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/mcp",
	} {
		resp := e.do(e.client, "GET", path, nil, "", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
		prm := decodeJSON(t, resp)
		if prm["resource"] != e.origin+"/mcp" {
			t.Errorf("%s resource = %v", path, prm["resource"])
		}
		if !reflect.DeepEqual(prm["authorization_servers"], []any{e.origin}) {
			t.Errorf("%s authorization_servers = %v", path, prm["authorization_servers"])
		}
		if !reflect.DeepEqual(prm["scopes_supported"], []any{"mcp"}) {
			t.Errorf("%s scopes_supported = %v", path, prm["scopes_supported"])
		}
		if !reflect.DeepEqual(prm["bearer_methods_supported"], []any{"header"}) {
			t.Errorf("%s bearer_methods_supported = %v", path, prm["bearer_methods_supported"])
		}
		if prm["resource_name"] != "Spark AI Hub" {
			t.Errorf("%s resource_name = %v", path, prm["resource_name"])
		}
	}

	resp := e.do(e.client, "GET", "/.well-known/oauth-authorization-server", nil, "", nil)
	asm := decodeJSON(t, resp)
	if asm["issuer"] != e.origin {
		t.Errorf("issuer = %v", asm["issuer"])
	}
	if asm["authorization_endpoint"] != e.origin+"/oauth/authorize" ||
		asm["token_endpoint"] != e.origin+"/oauth/token" ||
		asm["registration_endpoint"] != e.origin+"/oauth/register" {
		t.Errorf("endpoints = %v", asm)
	}
	if !reflect.DeepEqual(asm["code_challenge_methods_supported"], []any{"S256"}) {
		t.Errorf("code_challenge_methods_supported = %v", asm["code_challenge_methods_supported"])
	}
	if asm["client_id_metadata_document_supported"] != true {
		t.Errorf("client_id_metadata_document_supported = %v", asm["client_id_metadata_document_supported"])
	}
	if !reflect.DeepEqual(asm["token_endpoint_auth_methods_supported"], []any{"none"}) {
		t.Errorf("token_endpoint_auth_methods_supported = %v", asm["token_endpoint_auth_methods_supported"])
	}
	if !reflect.DeepEqual(asm["grant_types_supported"], []any{"authorization_code", "refresh_token"}) {
		t.Errorf("grant_types_supported = %v", asm["grant_types_supported"])
	}
}

func TestUnknownWellKnownIs404JSON(t *testing.T) {
	e := newEnv(t)
	resp := e.do(e.client, "GET", "/.well-known/openid-configuration", nil, "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	body := decodeJSON(t, resp)
	if body["error"] != "not_found" {
		t.Errorf("body = %v", body)
	}
}

func TestUnauthenticatedMCPPointsAtResourceMetadata(t *testing.T) {
	e := newEnv(t)
	resp := e.mcp(e.newClient(), "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	want := fmt.Sprintf(`resource_metadata="%s/.well-known/oauth-protected-resource/mcp"`, e.origin)
	if !strings.Contains(challenge, want) {
		t.Errorf("challenge = %q, want %q", challenge, want)
	}
}

func TestBadBearerOnMCPSaysInvalidToken(t *testing.T) {
	e := newEnv(t)
	resp := e.mcp(e.newClient(), "sah-oat-not-a-real-token")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Errorf("challenge = %q", resp.Header.Get("WWW-Authenticate"))
	}
}

// ── the whole flow ──────────────────────────────────────────────────────────

func TestFullFlowAndRefreshRotation(t *testing.T) {
	e := newEnv(t)
	clientID := e.register()
	verifier, challenge := pkce(t)

	tokens := e.tokens(clientID, e.code(clientID, challenge), verifier)
	if tokens["token_type"] != "Bearer" || tokens["scope"] != "mcp" {
		t.Errorf("token response = %v", tokens)
	}
	access, _ := tokens["access_token"].(string)
	if resp := e.mcp(e.newClient(), access); resp.StatusCode != http.StatusOK {
		t.Fatalf("mcp with access token: %d %s", resp.StatusCode, readBody(t, resp))
	}

	refresh, _ := tokens["refresh_token"].(string)
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}}
	resp := e.do(e.client, "POST", "/oauth/token", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", nil)
	rotated := decodeJSON(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh: %d %v", resp.StatusCode, rotated)
	}
	if rotated["refresh_token"] == tokens["refresh_token"] {
		t.Error("refresh token was not rotated")
	}

	// The old refresh token is dead.
	resp = e.do(e.client, "POST", "/oauth/token", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", nil)
	reused := decodeJSON(t, resp)
	if reused["error"] != "invalid_grant" {
		t.Errorf("reused refresh = %v", reused)
	}
}

func TestCodeIsSingleUseAndPKCEIsEnforced(t *testing.T) {
	e := newEnv(t)
	clientID := e.register()
	verifier, challenge := pkce(t)
	code := e.code(clientID, challenge)

	_, wrongVerifier := pkce(t)
	if _, body := e.exchangeWith(e.client, clientID, code, wrongVerifier); body["error"] != "invalid_grant" {
		t.Fatalf("wrong verifier = %v", body)
	}
	// The failed attempt burned the code.
	if _, body := e.exchangeWith(e.client, clientID, code, verifier); body["error"] != "invalid_grant" {
		t.Fatalf("reused code = %v", body)
	}
}

func TestOAuthTokenDoesNotOpenOtherAPIs(t *testing.T) {
	e := newEnv(t)
	clientID := e.register()
	verifier, challenge := pkce(t)
	tokens := e.tokens(clientID, e.code(clientID, challenge), verifier)
	access, _ := tokens["access_token"].(string)

	resp := e.do(e.newClient(), "GET", "/api/auth/me", nil, "", map[string]string{
		"Authorization": "Bearer " + access,
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/api/auth/me with OAuth token: %d", resp.StatusCode)
	}
}

func TestAPIKeyStillOpensMCP(t *testing.T) {
	e := newEnv(t)
	resp := e.mcp(e.newClient(), e.owner.APIKey)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mcp with API key: %d %s", resp.StatusCode, readBody(t, resp))
	}
}

// ── refusals ────────────────────────────────────────────────────────────────

func TestUnregisteredRedirectURIIsNotFollowed(t *testing.T) {
	e := newEnv(t)
	clientID := e.register()
	_, challenge := pkce(t)

	q := e.authorizeParams(clientID, challenge)
	q.Set("redirect_uri", "https://evil.example/cb")
	resp := e.do(e.client, "GET", "/oauth/authorize?"+q.Encode(), nil, "", nil)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d %s", resp.StatusCode, body)
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		t.Errorf("unregistered redirect got a Location: %s", loc)
	}
	if !strings.Contains(body, "did not register") {
		t.Errorf("body = %s", body)
	}
}

func TestMissingPKCERedirectsWithError(t *testing.T) {
	e := newEnv(t)
	clientID := e.register()
	_, challenge := pkce(t)

	q := e.authorizeParams(clientID, challenge)
	q.Del("code_challenge")
	resp := e.do(e.client, "GET", "/oauth/authorize?"+q.Encode(), nil, "", nil)
	readBody(t, resp)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	got := loc.Query()
	if got.Get("error") != "invalid_request" || got.Get("state") != "xyz" || got.Get("iss") != e.origin {
		t.Errorf("redirect = %s", loc)
	}
}

func TestWrongResponseTypeRedirects(t *testing.T) {
	e := newEnv(t)
	clientID := e.register()
	_, challenge := pkce(t)

	q := e.authorizeParams(clientID, challenge)
	q.Set("response_type", "token")
	resp := e.do(e.client, "GET", "/oauth/authorize?"+q.Encode(), nil, "", nil)
	readBody(t, resp)
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || loc.Query().Get("error") != "unsupported_response_type" {
		t.Errorf("status=%d location=%v", resp.StatusCode, loc)
	}
}

func TestConsentWithoutTheViewingSessionIsRefused(t *testing.T) {
	e := newEnv(t)
	clientID := e.register()
	_, challenge := pkce(t)

	viewer := e.newClient()
	requestID, _ := e.authorizePage(viewer, clientID, challenge)

	// A forged cross-site POST arrives with no session cookie.
	attacker := e.newClient()
	resp := e.decide(attacker, requestID, "allow")
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Sign in on this page") {
		t.Errorf("body = %s", body)
	}
}

func TestExpiredSignInRequest(t *testing.T) {
	e := newEnv(t)
	resp := e.decide(e.client, "not-a-real-request", "allow")
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "expired") {
		t.Fatalf("status = %d %s", resp.StatusCode, body)
	}
}

func TestWrongPasswordStaysOnSignIn(t *testing.T) {
	e := newEnv(t)
	clientID := e.register()
	_, challenge := pkce(t)

	requestID, _ := e.authorizePage(e.newClient(), clientID, challenge)
	resp := e.login(e.newClient(), requestID, "nope-nope")
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(body, "Incorrect email or password") {
		t.Errorf("body = %s", body)
	}
}

func TestPendingAccountCannotSignIn(t *testing.T) {
	e := newEnv(t)
	hash, _ := auth.HashPassword(ownerPassword)
	key, _ := auth.NewAPIKey()
	if _, err := e.db.CreateUser("pending@example.com", "", hash, "", "pending", key); err != nil {
		t.Fatalf("create pending user: %v", err)
	}

	clientID := e.register()
	_, challenge := pkce(t)
	requestID, _ := e.authorizePage(e.newClient(), clientID, challenge)

	form := url.Values{
		"request_id": {requestID},
		"action":     {"login"},
		"email":      {"pending@example.com"},
		"password":   {ownerPassword},
	}
	resp := e.do(e.newClient(), "POST", "/oauth/authorize", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", nil)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "not active yet") {
		t.Fatalf("status = %d %s", resp.StatusCode, body)
	}
}

func TestDenyRedirectsWithAccessDenied(t *testing.T) {
	e := newEnv(t)
	clientID := e.register()
	_, challenge := pkce(t)

	requestID, _ := e.authorizePage(e.client, clientID, challenge)
	readBody(t, e.login(e.client, requestID, ownerPassword))

	resp := e.decide(e.client, requestID, "deny")
	readBody(t, resp)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	q := loc.Query()
	if q.Get("error") != "access_denied" || q.Get("state") != "xyz" || q.Get("iss") != e.origin {
		t.Errorf("redirect = %s", loc)
	}
}

// ── registration and token errors ───────────────────────────────────────────

func TestRegistrationRejectsNonHTTPSRedirects(t *testing.T) {
	e := newEnv(t)
	resp := e.do(e.client, "POST", "/oauth/register",
		strings.NewReader(`{"redirect_uris":["http://evil.example/cb"]}`), "application/json", nil)
	if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Pragma") != "no-cache" {
		t.Errorf("cache headers = %v", resp.Header)
	}
	body := decodeJSON(t, resp)
	if body["error"] != "invalid_redirect_uri" {
		t.Errorf("body = %v", body)
	}
}

func TestRegistrationRejectsNonJSONObject(t *testing.T) {
	e := newEnv(t)
	for _, payload := range []string{`[]`, `not json`, `null`} {
		resp := e.do(e.client, "POST", "/oauth/register", strings.NewReader(payload), "application/json", nil)
		body := decodeJSON(t, resp)
		if resp.StatusCode != http.StatusBadRequest || body["error"] != "invalid_client_metadata" {
			t.Errorf("%q: status=%d body=%v", payload, resp.StatusCode, body)
		}
	}
}

func TestTokenRequiresFormEncoding(t *testing.T) {
	e := newEnv(t)
	resp := e.do(e.client, "POST", "/oauth/token", strings.NewReader(`{}`), "application/json", nil)
	body := decodeJSON(t, resp)
	if resp.StatusCode != http.StatusBadRequest || body["error"] != "invalid_request" {
		t.Fatalf("status=%d body=%v", resp.StatusCode, body)
	}
}

func TestTokenUnknownClient(t *testing.T) {
	e := newEnv(t)
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {"sah-client-nope"}}
	resp := e.do(e.client, "POST", "/oauth/token", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", nil)
	body := decodeJSON(t, resp)
	if resp.StatusCode != http.StatusUnauthorized || body["error"] != "invalid_client" {
		t.Fatalf("status=%d body=%v", resp.StatusCode, body)
	}
}

func TestUnsupportedGrantType(t *testing.T) {
	e := newEnv(t)
	clientID := e.register()
	form := url.Values{"grant_type": {"password"}, "client_id": {clientID}}
	resp := e.do(e.client, "POST", "/oauth/token", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", nil)
	body := decodeJSON(t, resp)
	if resp.StatusCode != http.StatusBadRequest || body["error"] != "unsupported_grant_type" {
		t.Fatalf("status=%d body=%v", resp.StatusCode, body)
	}
	if body["error_description"] != "grant_type 'password' is not supported." {
		t.Errorf("description = %v", body["error_description"])
	}
}

// ── expired code and revoked user ───────────────────────────────────────────

func TestExpiredAuthorizationCode(t *testing.T) {
	e := newEnv(t)
	clientID := e.register()
	verifier, challenge := pkce(t)
	code := e.code(clientID, challenge)

	if _, err := e.db.Exec("UPDATE oauth_codes SET expires_at = '2000-01-01 00:00:00'"); err != nil {
		t.Fatalf("expire code: %v", err)
	}
	_, body := e.exchangeWith(e.client, clientID, code, verifier)
	if body["error"] != "invalid_grant" || body["error_description"] != "The authorization code has expired." {
		t.Fatalf("expired code = %v", body)
	}
}

func TestDeactivatedUserCannotExchangeCode(t *testing.T) {
	e := newEnv(t)
	clientID := e.register()
	verifier, challenge := pkce(t)
	code := e.code(clientID, challenge)

	if _, err := e.db.UpdateUser(e.owner.ID, map[string]any{"status": "rejected"}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	_, body := e.exchangeWith(e.client, clientID, code, verifier)
	if body["error"] != "invalid_grant" || body["error_description"] != "The account is no longer active." {
		t.Fatalf("deactivated user exchange = %v", body)
	}
}

func TestDeactivatedUserLosesAccessToken(t *testing.T) {
	e := newEnv(t)
	clientID := e.register()
	verifier, challenge := pkce(t)
	tokens := e.tokens(clientID, e.code(clientID, challenge), verifier)
	access, _ := tokens["access_token"].(string)

	if resp := e.mcp(e.newClient(), access); resp.StatusCode != http.StatusOK {
		t.Fatalf("live token: %d", resp.StatusCode)
	}

	if _, err := e.db.UpdateUser(e.owner.ID, map[string]any{"status": "rejected"}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	e.svc.InvalidateCache()

	if resp := e.mcp(e.newClient(), access); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("deactivated token still opened /mcp: %d", resp.StatusCode)
	}
}
