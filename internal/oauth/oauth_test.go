package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"gospark-hub/internal/auth"
	"gospark-hub/internal/store"
)

const testCallback = "https://claude.ai/api/mcp/auth_callback"

func newTestService(t *testing.T) (*Service, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewService(db), db
}

func newTestUser(t *testing.T, db *store.DB, email, status string) *store.User {
	t.Helper()
	hash, err := auth.HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	key, err := auth.NewAPIKey()
	if err != nil {
		t.Fatalf("api key: %v", err)
	}
	u, err := db.CreateUser(email, "", hash, "", status, key)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

func pkcePair(t *testing.T) (verifier, challenge string) {
	t.Helper()
	verifier, err := store.NewRandomToken(48)
	if err != nil {
		t.Fatalf("random: %v", err)
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func registerTestClient(t *testing.T, svc *Service) string {
	t.Helper()
	client, err := svc.RegisterClient(map[string]any{
		"redirect_uris": []any{testCallback},
		"client_name":   "Claude",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	id, _ := client["client_id"].(string)
	if id == "" {
		t.Fatalf("empty client_id in %v", client)
	}
	return id
}

func TestValidRedirectURI(t *testing.T) {
	cases := []struct {
		uri  string
		want bool
	}{
		{testCallback, true},
		{"http://127.0.0.1/callback", true},
		{"http://127.0.0.1:53211/callback", true},
		{"http://localhost:8080/callback", true},
		{"http://[::1]:1234/callback", true},
		{"http://evil.example/callback", false},
		{"https://evil.example/callback#fragment", false},
		{"https://", false},
		{"not a url", false},
		{"ftp://127.0.0.1/callback", false},
	}
	for _, tc := range cases {
		if got := ValidRedirectURI(tc.uri); got != tc.want {
			t.Errorf("ValidRedirectURI(%q) = %v, want %v", tc.uri, got, tc.want)
		}
	}
}

func TestRedirectMatches(t *testing.T) {
	if !RedirectMatches([]string{"http://127.0.0.1/callback"}, "http://127.0.0.1:53211/callback") {
		t.Error("loopback redirect should ignore the port")
	}
	if RedirectMatches([]string{"http://127.0.0.1/callback"}, "http://127.0.0.1:53211/other") {
		t.Error("loopback redirect must keep the path")
	}
	if RedirectMatches([]string{testCallback}, "https://claude.ai:444/api/mcp/auth_callback") {
		t.Error("https redirects must match exactly, port included")
	}
	if !RedirectMatches([]string{testCallback}, testCallback) {
		t.Error("exact match failed")
	}
}

func TestRegisterClientValidation(t *testing.T) {
	svc, _ := newTestService(t)

	_, err := svc.RegisterClient(map[string]any{})
	var oe *Error
	if !errors.As(err, &oe) || oe.Code != "invalid_redirect_uri" {
		t.Fatalf("missing redirect_uris: got %v", err)
	}

	_, err = svc.RegisterClient(map[string]any{"redirect_uris": []any{}})
	if !errors.As(err, &oe) || oe.Code != "invalid_redirect_uri" {
		t.Fatalf("empty redirect_uris: got %v", err)
	}

	_, err = svc.RegisterClient(map[string]any{"redirect_uris": []any{1}})
	if !errors.As(err, &oe) || oe.Code != "invalid_redirect_uri" {
		t.Fatalf("non-string redirect_uris: got %v", err)
	}

	_, err = svc.RegisterClient(map[string]any{
		"redirect_uris": []any{"http://evil.example/cb"},
	})
	if !errors.As(err, &oe) || oe.Code != "invalid_redirect_uri" ||
		oe.Description != "http://evil.example/cb must be https, or http on a loopback address." {
		t.Fatalf("http redirect: got %v", err)
	}

	_, err = svc.RegisterClient(map[string]any{
		"redirect_uris":              []any{testCallback},
		"token_endpoint_auth_method": "client_secret_basic",
	})
	if !errors.As(err, &oe) || oe.Code != "invalid_client_metadata" {
		t.Fatalf("confidential client: got %v", err)
	}
}

func TestRegisterClientLimit(t *testing.T) {
	svc, _ := newTestService(t)
	svc.MaxClients = 1

	registerTestClient(t, svc)
	_, err := svc.RegisterClient(map[string]any{"redirect_uris": []any{testCallback}})

	var oe *Error
	if !errors.As(err, &oe) || oe.Code != "invalid_client_metadata" ||
		oe.Status != http.StatusServiceUnavailable {
		t.Fatalf("client limit: got %v", err)
	}
}

func TestRegisterClientResponseShape(t *testing.T) {
	svc, _ := newTestService(t)
	client, err := svc.RegisterClient(map[string]any{
		"redirect_uris": []any{testCallback},
		"client_name":   "Claude",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	id, _ := client["client_id"].(string)
	if len(id) <= len("sah-client-") || id[:len("sah-client-")] != "sah-client-" {
		t.Errorf("client_id = %q, want sah-client- prefix", id)
	}
	if name, _ := client["client_name"].(string); name != "Claude" {
		t.Errorf("client_name = %v", client["client_name"])
	}
	if method := client["token_endpoint_auth_method"]; method != "none" {
		t.Errorf("token_endpoint_auth_method = %v", method)
	}

	got, err := svc.GetClient(id)
	if err != nil || got == nil {
		t.Fatalf("GetClient: %v %v", got, err)
	}
	if len(got.RedirectURIs) != 1 || got.RedirectURIs[0] != testCallback {
		t.Errorf("redirect_uris = %v", got.RedirectURIs)
	}

	if unknown, err := svc.GetClient("sah-client-nope"); err != nil || unknown != nil {
		t.Errorf("unknown client = %v, %v", unknown, err)
	}
	if empty, err := svc.GetClient(""); err != nil || empty != nil {
		t.Errorf("empty client = %v, %v", empty, err)
	}
}

func TestExchangeCodeIssuesTokens(t *testing.T) {
	svc, db := newTestService(t)
	user := newTestUser(t, db, "owner@example.com", "active")
	clientID := registerTestClient(t, svc)
	verifier, challenge := pkcePair(t)

	code, err := svc.CreateCode(clientID, user.ID, testCallback, challenge, "https://hub.example/mcp", Scope)
	if err != nil {
		t.Fatalf("create code: %v", err)
	}
	tokens, err := svc.ExchangeCode(code, clientID, testCallback, verifier, "https://hub.example/mcp")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if tokens["token_type"] != "Bearer" || tokens["expires_in"] != AccessTTL || tokens["scope"] != Scope {
		t.Errorf("token response = %v", tokens)
	}
	access, _ := tokens["access_token"].(string)
	if len(access) < len("sah-oat-") || access[:len("sah-oat-")] != "sah-oat-" {
		t.Errorf("access_token = %q", access)
	}

	resolved, err := svc.Resolve(access)
	if err != nil || resolved == nil || resolved.ID != user.ID {
		t.Fatalf("Resolve: %v %v", resolved, err)
	}
}

func TestCodeIsSingleUseEvenWhenPKCEFails(t *testing.T) {
	svc, db := newTestService(t)
	user := newTestUser(t, db, "owner@example.com", "active")
	clientID := registerTestClient(t, svc)
	verifier, challenge := pkcePair(t)

	code, _ := svc.CreateCode(clientID, user.ID, testCallback, challenge, "https://hub.example/mcp", Scope)
	_, wrongVerifier := pkcePair(t)

	_, err := svc.ExchangeCode(code, clientID, testCallback, wrongVerifier, "")
	var oe *Error
	if !errors.As(err, &oe) || oe.Code != "invalid_grant" ||
		oe.Description != "PKCE verification failed." {
		t.Fatalf("wrong verifier: got %v", err)
	}
	_, err = svc.ExchangeCode(code, clientID, testCallback, verifier, "")
	if !errors.As(err, &oe) || oe.Code != "invalid_grant" ||
		oe.Description != "The authorization code is invalid or was already used." {
		t.Fatalf("reused code: got %v", err)
	}
}

func TestExpiredCode(t *testing.T) {
	svc, db := newTestService(t)
	user := newTestUser(t, db, "owner@example.com", "active")
	clientID := registerTestClient(t, svc)
	verifier, challenge := pkcePair(t)

	code, _ := svc.CreateCode(clientID, user.ID, testCallback, challenge, "https://hub.example/mcp", Scope)
	if _, err := db.Exec("UPDATE oauth_codes SET expires_at = '2000-01-01 00:00:00'"); err != nil {
		t.Fatalf("expire code: %v", err)
	}
	_, err := svc.ExchangeCode(code, clientID, testCallback, verifier, "")
	var oe *Error
	if !errors.As(err, &oe) || oe.Code != "invalid_grant" ||
		oe.Description != "The authorization code has expired." {
		t.Fatalf("expired code: got %v", err)
	}
}

func TestExchangeRejectsWrongRedirectAndClient(t *testing.T) {
	svc, db := newTestService(t)
	user := newTestUser(t, db, "owner@example.com", "active")
	clientID := registerTestClient(t, svc)
	verifier, challenge := pkcePair(t)

	code, _ := svc.CreateCode(clientID, user.ID, testCallback, challenge, "https://hub.example/mcp", Scope)
	_, err := svc.ExchangeCode(code, clientID, "https://claude.ai/other", verifier, "")
	var oe *Error
	if !errors.As(err, &oe) || oe.Code != "invalid_grant" ||
		oe.Description != "The code was issued to a different client or redirect_uri." {
		t.Fatalf("wrong redirect: got %v", err)
	}
}

func TestResourceMismatchIsInvalidTarget(t *testing.T) {
	svc, db := newTestService(t)
	user := newTestUser(t, db, "owner@example.com", "active")
	clientID := registerTestClient(t, svc)
	verifier, challenge := pkcePair(t)

	code, _ := svc.CreateCode(clientID, user.ID, testCallback, challenge, "https://hub.example/mcp", Scope)
	_, err := svc.ExchangeCode(code, clientID, testCallback, verifier, "https://elsewhere.example/mcp")
	var oe *Error
	if !errors.As(err, &oe) || oe.Code != "invalid_target" {
		t.Fatalf("resource mismatch: got %v", err)
	}
}

func TestInactiveUserCannotExchange(t *testing.T) {
	svc, db := newTestService(t)
	user := newTestUser(t, db, "owner@example.com", "active")
	clientID := registerTestClient(t, svc)
	verifier, challenge := pkcePair(t)

	code, _ := svc.CreateCode(clientID, user.ID, testCallback, challenge, "https://hub.example/mcp", Scope)
	if _, err := db.UpdateUser(user.ID, map[string]any{"status": "rejected"}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	_, err := svc.ExchangeCode(code, clientID, testCallback, verifier, "")
	var oe *Error
	if !errors.As(err, &oe) || oe.Code != "invalid_grant" ||
		oe.Description != "The account is no longer active." {
		t.Fatalf("inactive user: got %v", err)
	}
}

func TestRefreshRotatesAndRevokes(t *testing.T) {
	svc, db := newTestService(t)
	user := newTestUser(t, db, "owner@example.com", "active")
	clientID := registerTestClient(t, svc)
	verifier, challenge := pkcePair(t)

	code, _ := svc.CreateCode(clientID, user.ID, testCallback, challenge, "https://hub.example/mcp", Scope)
	tokens, err := svc.ExchangeCode(code, clientID, testCallback, verifier, "")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	first, _ := tokens["refresh_token"].(string)

	rotated, err := svc.Refresh(first, clientID, "")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	second, _ := rotated["refresh_token"].(string)
	if second == first {
		t.Fatal("refresh token was not rotated")
	}

	_, err = svc.Refresh(first, clientID, "")
	var oe *Error
	if !errors.As(err, &oe) || oe.Code != "invalid_grant" ||
		oe.Description != "The refresh token is invalid, expired or already used." {
		t.Fatalf("reused refresh: got %v", err)
	}
}

func TestResolveIgnoresDeactivatedUser(t *testing.T) {
	svc, db := newTestService(t)
	user := newTestUser(t, db, "owner@example.com", "active")
	clientID := registerTestClient(t, svc)
	verifier, challenge := pkcePair(t)

	code, _ := svc.CreateCode(clientID, user.ID, testCallback, challenge, "https://hub.example/mcp", Scope)
	tokens, _ := svc.ExchangeCode(code, clientID, testCallback, verifier, "")
	access, _ := tokens["access_token"].(string)

	if _, err := db.UpdateUser(user.ID, map[string]any{"status": "rejected"}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	svc.InvalidateCache()

	resolved, err := svc.Resolve(access)
	if err != nil || resolved != nil {
		t.Fatalf("deactivated user resolved: %v %v", resolved, err)
	}

	if _, err := svc.RevokeUserTokens(user.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	var count int64
	if err := db.QueryRow("SELECT COUNT(*) FROM oauth_tokens WHERE user_id = ?", user.ID).Scan(&count); err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if count != 0 {
		t.Errorf("%d tokens left after revoke", count)
	}
}

func TestPurgeExpired(t *testing.T) {
	svc, db := newTestService(t)
	user := newTestUser(t, db, "owner@example.com", "active")
	clientID := registerTestClient(t, svc)
	verifier, challenge := pkcePair(t)

	code, _ := svc.CreateCode(clientID, user.ID, testCallback, challenge, "https://hub.example/mcp", Scope)
	tokens, _ := svc.ExchangeCode(code, clientID, testCallback, verifier, "")
	_ = tokens

	if _, err := db.Exec("UPDATE oauth_codes SET expires_at = '2000-01-01 00:00:00'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE oauth_tokens SET expires_at = '2000-01-01 00:00:00'"); err != nil {
		t.Fatal(err)
	}
	if err := svc.PurgeExpired(); err != nil {
		t.Fatalf("purge: %v", err)
	}
	var codes, tokensLeft int64
	_ = db.QueryRow("SELECT COUNT(*) FROM oauth_codes").Scan(&codes)
	_ = db.QueryRow("SELECT COUNT(*) FROM oauth_tokens").Scan(&tokensLeft)
	if codes != 0 || tokensLeft != 0 {
		t.Errorf("after purge: %d codes, %d tokens", codes, tokensLeft)
	}
}

func TestClientIDMetadataDocument(t *testing.T) {
	svc, _ := newTestService(t)

	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc := map[string]any{
			"client_id":     server.URL + "/meta",
			"client_name":   "Claude",
			"redirect_uris": []string{testCallback},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	defer server.Close()

	svc.HTTP = server.Client()
	svc.AllowCIMDHost = func(string, int) bool { return true }

	client, err := svc.GetClient(server.URL + "/meta")
	if err != nil || client == nil {
		t.Fatalf("GetClient metadata: %v %v", client, err)
	}
	if client.ClientName != "Claude" || len(client.RedirectURIs) != 1 {
		t.Errorf("metadata client = %+v", client)
	}

	// A document that does not name itself is not a client.
	if other, err := svc.GetClient(server.URL + "/other"); err != nil || other != nil {
		t.Errorf("mismatched document = %v, %v", other, err)
	}
}
