// Package oauthapi mounts the Hub's OAuth endpoints: the two discovery
// documents, dynamic client registration, the token endpoint, and the sign-in
// and consent pages that hand an authorization code back to an MCP connector.
//
// The protocol logic lives in internal/oauth. This package mirrors the route
// table and response shapes of daemon/routers/oauth.py, including its exact
// error strings, status codes and cache headers.
package oauthapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gospark-hub/internal/auth"
	"gospark-hub/internal/config"
	"gospark-hub/internal/oauth"
	"gospark-hub/internal/store"
	"gospark-hub/internal/web"
)

const (
	mcpPath    = "/mcp"
	requestTTL = 600 * time.Second
)

// The service is the real TokenResolver the auth middleware uses to accept an
// OAuth access token as `Bearer` on /mcp; main.go passes one Service to both.
var _ auth.TokenResolver = (*oauth.Service)(nil)

// Deps is what the OAuth routes need from the application.
type Deps struct {
	Config config.Config
	Store  *store.DB
	Logger *log.Logger
	// Service lets the caller share one oauth.Service with the auth middleware,
	// so access tokens resolve on /mcp. A nil value is created from Store.
	Service *oauth.Service
}

// Register mounts the OAuth routes on r. All of them are public: the auth
// middleware still resolves a session, but never blocks these paths.
func Register(r *web.Router, deps Deps) {
	if deps.Logger == nil {
		deps.Logger = log.Default()
	}
	svc := deps.Service
	if svc == nil {
		svc = oauth.NewService(deps.Store)
	}
	h := &handler{
		deps:    deps,
		cfg:     deps.Config,
		log:     deps.Logger,
		svc:     svc,
		pending: map[string]*pending{},
	}

	// Discovery. The catch-all is registered last on purpose: without it the
	// SPA fallback answers /.well-known/* with HTML and a 200, which a
	// discovery client reads as a broken document.
	r.Handle("GET", "/.well-known/oauth-protected-resource", h.protectedResource)
	r.Handle("GET", "/.well-known/oauth-protected-resource"+mcpPath, h.protectedResource)
	r.Handle("GET", "/.well-known/oauth-authorization-server", h.authorizationServer)
	r.Handle("GET", "/.well-known/{rest...}", h.wellKnownMissing)

	// Registration and tokens.
	r.Handle("POST", "/oauth/register", h.registerClient)
	r.Handle("POST", "/oauth/token", h.token)

	// The consent page, served as plain HTML: it must work for someone who
	// arrives from a connector with no Hub tab open, and must not be framable.
	r.Handle("GET", "/oauth/authorize", h.authorize)
	r.Handle("POST", "/oauth/authorize", h.authorizeDecision)
}

type handler struct {
	deps Deps
	cfg  config.Config
	log  *log.Logger
	svc  *oauth.Service

	mu      sync.Mutex
	pending map[string]*pending
}

// pending is an authorization request between the consent page and the Allow
// click. It is bound to the session that saw the page, so a forged POST from
// another site (which carries no cookie under SameSite=lax) cannot approve it.
type pending struct {
	clientID      string
	clientName    string
	redirectURI   string
	codeChallenge string
	resource      string
	scope         string
	state         *string
	origin        string
	created       time.Time
	session       *string
	userID        *int64
}

// ── discovery ───────────────────────────────────────────────────────────────

func (h *handler) protectedResource(w http.ResponseWriter, r *http.Request) {
	origin := h.origin(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 origin + mcpPath,
		"authorization_servers":    []string{origin},
		"scopes_supported":         []string{oauth.Scope},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Spark AI Hub",
	})
}

func (h *handler) authorizationServer(w http.ResponseWriter, r *http.Request) {
	origin := h.origin(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         origin,
		"authorization_endpoint":                         origin + "/oauth/authorize",
		"token_endpoint":                                 origin + "/oauth/token",
		"registration_endpoint":                          origin + "/oauth/register",
		"scopes_supported":                               []string{oauth.Scope},
		"response_types_supported":                       []string{"code"},
		"response_modes_supported":                       []string{"query"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"client_id_metadata_document_supported":          true,
		"authorization_response_iss_parameter_supported": true,
	})
}

func (h *handler) wellKnownMissing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
}

// ── registration ────────────────────────────────────────────────────────────

func (h *handler) registerClient(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	var meta any
	if err != nil || json.Unmarshal(raw, &meta) != nil {
		writeNoStore(w, http.StatusBadRequest, map[string]any{
			"error":             "invalid_client_metadata",
			"error_description": "Body must be a JSON object.",
		})
		return
	}
	body, ok := meta.(map[string]any)
	if !ok {
		writeNoStore(w, http.StatusBadRequest, map[string]any{
			"error":             "invalid_client_metadata",
			"error_description": "Body must be a JSON object.",
		})
		return
	}

	client, err := h.svc.RegisterClient(body)
	if err != nil {
		h.writeOAuthError(w, err)
		return
	}
	writeNoStore(w, http.StatusCreated, client)
}

// ── token ───────────────────────────────────────────────────────────────────

func (h *handler) token(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		writeNoStore(w, http.StatusBadRequest, map[string]any{
			"error":             "invalid_request",
			"error_description": "Use application/x-www-form-urlencoded.",
		})
		return
	}
	form := parseForm(r)
	clientID := form["client_id"]

	client, err := h.svc.GetClient(clientID)
	if err != nil {
		h.log.Printf("oauth: resolve client: %v", err)
	}
	if client == nil {
		writeNoStore(w, http.StatusUnauthorized, map[string]any{
			"error":             "invalid_client",
			"error_description": "Unknown client_id.",
		})
		return
	}

	grant, grantPresent := form["grant_type"]
	var result map[string]any
	switch {
	case grantPresent && grant == "authorization_code":
		result, err = h.svc.ExchangeCode(form["code"], clientID, form["redirect_uri"],
			form["code_verifier"], form["resource"])
	case grantPresent && grant == "refresh_token":
		result, err = h.svc.Refresh(form["refresh_token"], clientID, form["resource"])
	default:
		writeNoStore(w, http.StatusBadRequest, map[string]any{
			"error":             "unsupported_grant_type",
			"error_description": fmt.Sprintf("grant_type %s is not supported.", grantRepr(grant, grantPresent)),
		})
		return
	}
	if err != nil {
		h.writeOAuthError(w, err)
		return
	}
	writeNoStore(w, http.StatusOK, result)
}

// ── authorize: sign in, consent, redirect ───────────────────────────────────

func (h *handler) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	client, err := h.svc.GetClient(q.Get("client_id"))
	if err != nil {
		h.log.Printf("oauth: resolve client: %v", err)
	}
	if client == nil {
		writeErrorPage(w, http.StatusBadRequest,
			"This app is not registered with the Hub. Remove the connector and add it again.")
		return
	}

	redirectURI := q.Get("redirect_uri")
	if redirectURI == "" && len(client.RedirectURIs) == 1 {
		redirectURI = client.RedirectURIs[0]
	}
	if redirectURI == "" || !oauth.RedirectMatches(client.RedirectURIs, redirectURI) {
		// Never redirect to a URI the client did not register: that is how a
		// code would be delivered to an attacker.
		writeErrorPage(w, http.StatusBadRequest,
			"The app asked to return to an address it did not register.")
		return
	}

	origin := h.origin(r)
	state := queryOptional(q, "state")
	fail := func(code, description string) {
		redirectTo(w, http.StatusFound, redirectWith(redirectURI, []param{
			{"error", ptr(code)},
			{"error_description", ptr(description)},
			{"state", state},
			{"iss", ptr(origin)},
		}))
	}

	if q.Get("response_type") != "code" {
		fail("unsupported_response_type", "Only response_type=code is supported.")
		return
	}
	challenge := q.Get("code_challenge")
	if challenge == "" || q.Get("code_challenge_method") != "S256" {
		fail("invalid_request", "PKCE with code_challenge_method=S256 is required.")
		return
	}
	resource := q.Get("resource")
	if resource == "" {
		resource = origin + mcpPath
	}
	resource = strings.TrimRight(resource, "/")
	if resource != origin+mcpPath {
		fail("invalid_target", fmt.Sprintf("The only resource here is %s%s.", origin, mcpPath))
		return
	}

	requestID, err := store.NewRandomToken(24)
	if err != nil {
		h.log.Printf("oauth: request id: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	p := &pending{
		clientID:      client.ClientID,
		clientName:    client.ClientName,
		redirectURI:   redirectURI,
		codeChallenge: challenge,
		resource:      resource,
		scope:         oauth.Scope,
		state:         state,
		origin:        origin,
		created:       time.Now(),
	}
	if p.clientName == "" {
		p.clientName = "An app"
	}

	user := auth.CurrentUser(r)
	if auth.AuthSource(r) != "session" {
		user = nil
	}
	if user != nil {
		p.session = sessionDigest(r, h.cfg.SessionCookieName)
		p.userID = &user.ID
	}

	h.mu.Lock()
	h.pruneLocked()
	h.pending[requestID] = p
	h.mu.Unlock()

	if user != nil {
		writeConsentPage(w, requestID, p, user)
		return
	}
	writeLoginPage(w, requestID, p, "")
}

func (h *handler) authorizeDecision(w http.ResponseWriter, r *http.Request) {
	form := parseForm(r)
	requestID := form["request_id"]

	p := h.lookupPending(requestID)
	if p == nil {
		writeErrorPage(w, http.StatusBadRequest,
			"This sign-in request expired. Start connecting again from the app.")
		return
	}

	action, actionPresent := form["action"]
	if actionPresent && action == "login" {
		user, err := h.deps.Store.GetUserByEmail(strings.TrimSpace(form["email"]))
		if err != nil {
			h.log.Printf("oauth: look up user: %v", err)
			user = nil
		}
		if user == nil || !auth.VerifyPassword(form["password"], user.PasswordHash) {
			writeLoginPage(w, requestID, p, "Incorrect email or password.")
			return
		}
		if user.Status != "active" {
			writeLoginPage(w, requestID, p, "This account is not active yet.")
			return
		}

		token, err := h.deps.Store.CreateSession(user.ID, r.UserAgent(), auth.ClientIP(r), h.cfg.SessionTTLDays)
		if err != nil {
			h.log.Printf("oauth: create session: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		digest := store.TokenDigest(token)
		h.bindSession(requestID, digest, user.ID)

		auth.SetSessionCookie(w, r, h.cfg, token)
		writeConsentPage(w, requestID, p, user)
		return
	}

	user := auth.CurrentUser(r)
	if auth.AuthSource(r) != "session" {
		user = nil
	}
	if user == nil || !h.sessionMatches(requestID, r, user.ID) {
		writeErrorPage(w, http.StatusForbidden,
			"Sign in on this page to approve the connection.")
		return
	}

	h.deletePending(requestID)
	if !actionPresent || action != "allow" {
		redirectTo(w, http.StatusSeeOther, redirectWith(p.redirectURI, []param{
			{"error", ptr("access_denied")},
			{"error_description", ptr("The user denied access.")},
			{"state", p.state},
			{"iss", ptr(p.origin)},
		}))
		return
	}

	code, err := h.svc.CreateCode(p.clientID, user.ID, p.redirectURI, p.codeChallenge, p.resource, p.scope)
	if err != nil {
		h.log.Printf("oauth: create code: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	redirectTo(w, http.StatusSeeOther, redirectWith(p.redirectURI, []param{
		{"code", ptr(code)},
		{"state", p.state},
		{"iss", ptr(p.origin)},
	}))
}

// ── pending store ───────────────────────────────────────────────────────────

func (h *handler) pruneLocked() {
	cutoff := time.Now().Add(-requestTTL)
	for id, p := range h.pending {
		if p.created.Before(cutoff) {
			delete(h.pending, id)
		}
	}
}

func (h *handler) lookupPending(id string) *pending {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()
	p, ok := h.pending[id]
	if !ok {
		return nil
	}
	return p
}

func (h *handler) deletePending(id string) {
	h.mu.Lock()
	delete(h.pending, id)
	h.mu.Unlock()
}

func (h *handler) bindSession(id, digest string, userID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p, ok := h.pending[id]; ok {
		p.session = &digest
		p.userID = &userID
	}
}

func (h *handler) sessionMatches(id string, r *http.Request, userID int64) bool {
	h.mu.Lock()
	p, ok := h.pending[id]
	h.mu.Unlock()
	if !ok || p.session == nil || p.userID == nil {
		return false
	}
	digest := sessionDigest(r, h.cfg.SessionCookieName)
	return digest != nil && *digest == *p.session && *p.userID == userID
}

func sessionDigest(r *http.Request, cookieName string) *string {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	digest := store.TokenDigest(c.Value)
	return &digest
}

// ── helpers ─────────────────────────────────────────────────────────────────

func (h *handler) origin(r *http.Request) string {
	if o := auth.RequestOrigin(r); o != "" {
		return o
	}
	return auth.RequestScheme(r) + "://" + r.Host
}

func (h *handler) writeOAuthError(w http.ResponseWriter, err error) {
	var oe *oauth.Error
	if errors.As(err, &oe) {
		writeNoStore(w, oe.Status, oe.Body())
		return
	}
	h.log.Printf("oauth: internal error: %v", err)
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}

// parseForm reads an urlencoded body keeping blank values, taking the first
// value of each key — the same shape as parse_qs(keep_blank_values=True).
func parseForm(r *http.Request) map[string]string {
	out := map[string]string{}
	raw, err := io.ReadAll(r.Body)
	if err != nil && len(raw) == 0 {
		return out
	}
	values, _ := url.ParseQuery(string(raw)) // partial values are fine
	for k, v := range values {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}

func queryOptional(q url.Values, key string) *string {
	v, ok := q[key]
	if !ok || len(v) == 0 {
		return nil
	}
	value := v[0]
	return &value
}

// grantRepr renders grant_type the way an f-string `{grant!r}` would.
func grantRepr(grant string, present bool) string {
	if !present {
		return "None"
	}
	return "'" + grant + "'"
}

func ptr(s string) *string { return &s }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeNoStore(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, status, v)
}
