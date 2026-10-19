// Package oauth implements the Hub's OAuth 2.1 authorization server for the
// /mcp endpoint: protected-resource and authorization-server metadata, RFC 7591
// dynamic client registration, Client ID Metadata Documents, the authorization
// code + PKCE grant and rotating refresh tokens.
//
// It mirrors daemon/services/oauth_service.py of the upstream Hub: only SHA-256
// digests of codes and tokens are stored, an access token opens /mcp and nothing
// else, and every lifetime (code 600 s, access 3600 s, refresh 30 days) is the
// upstream one.
package oauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"sync"
	"time"

	"gospark-hub/internal/store"
)

// Wire constants shared with the handlers and the discovery documents.
const (
	Scope        = "mcp"
	AccessTTL    = 3600
	RefreshTTL   = 30 * 24 * 3600
	CodeTTL      = 600
	MaxClients   = 5000
	CIMDTTL      = 600
	CIMDMaxBytes = 64 * 1024
)

const accessCacheTTL = 15 * time.Second

// Error is an RFC 6749 error: Code is what the client acts on, Description is
// the human sentence upstream puts in `error_description`, and Status is the
// HTTP status (400 unless a caller says otherwise).
type Error struct {
	Code        string
	Description string
	Status      int
}

func (e *Error) Error() string {
	if e.Description != "" {
		return e.Description
	}
	return e.Code
}

// Body is the JSON document an OAuth endpoint returns for this error.
func (e *Error) Body() map[string]any {
	out := map[string]any{"error": e.Code}
	if e.Description != "" {
		out["error_description"] = e.Description
	}
	return out
}

func newError(code, description string) *Error {
	return &Error{Code: code, Description: description, Status: http.StatusBadRequest}
}

func newErrorStatus(code, description string, status int) *Error {
	return &Error{Code: code, Description: description, Status: status}
}

// Client is a registered OAuth client, from the database or from a Client ID
// Metadata Document.
type Client struct {
	ClientID     string
	ClientName   string
	RedirectURIs []string
}

// Service owns the OAuth persistence and token lifetime rules. It implements
// auth.TokenResolver, so the auth middleware can accept `Bearer` access tokens
// on /mcp.
type Service struct {
	DB *store.DB

	// MaxClients overrides the registration ceiling; zero means MaxClients.
	MaxClients int
	// HTTP fetches Client ID Metadata Documents. It must not follow redirects;
	// a zero value gets a 5-second client that does not.
	HTTP *http.Client
	// AllowCIMDHost overrides the "public internet only" address check. Tests
	// point it at a local server; production leaves it nil.
	AllowCIMDHost func(host string, port int) bool

	cimdMu sync.Mutex
	cimd   map[string]cimdEntry
	tokens *tokenCache
}

type cimdEntry struct {
	at     time.Time
	client *Client
}

// NewService builds a service backed by db.
func NewService(db *store.DB) *Service {
	return &Service{
		DB:     db,
		cimd:   map[string]cimdEntry{},
		tokens: newTokenCache(),
	}
}

// InvalidateCache drops every cached access-token resolution and metadata
// document, matching upstream oauth_service.invalidate_caches().
func (s *Service) InvalidateCache() {
	if s.tokens != nil {
		s.tokens.clear()
	}
	s.cimdMu.Lock()
	s.cimd = map[string]cimdEntry{}
	s.cimdMu.Unlock()
}

func nowISO() string { return time.Now().UTC().Format(store.TimeFmt) }

func expiresISO(seconds int) string {
	return time.Now().UTC().Add(time.Duration(seconds) * time.Second).Format(store.TimeFmt)
}

// pkceMatches checks BASE64URL(SHA256(verifier)) against the stored challenge.
func pkceMatches(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	digest := base64.RawURLEncoding.EncodeToString(sum[:])
	return hmac.Equal([]byte(digest), []byte(challenge))
}

// tokenCache is the 15-second access-token cache upstream keeps in memory. It
// caches negative answers too, exactly like the Python dict.
type tokenCache struct {
	mu      sync.Mutex
	entries map[string]tokenEntry
}

type tokenEntry struct {
	at   time.Time
	user *store.User
}

func newTokenCache() *tokenCache {
	return &tokenCache{entries: map[string]tokenEntry{}}
}

func (c *tokenCache) get(key string) (*store.User, bool) {
	c.mu.Lock()
	ent, ok := c.entries[key]
	c.mu.Unlock()
	if !ok || time.Since(ent.at) > accessCacheTTL {
		return nil, false
	}
	return ent.user, true
}

func (c *tokenCache) set(key string, u *store.User) {
	c.mu.Lock()
	c.entries[key] = tokenEntry{at: time.Now(), user: u}
	c.mu.Unlock()
}

func (c *tokenCache) clear() {
	c.mu.Lock()
	c.entries = map[string]tokenEntry{}
	c.mu.Unlock()
}
