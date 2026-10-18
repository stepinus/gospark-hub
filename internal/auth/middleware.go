// Package auth provides session/API-key middleware, password hashing helpers,
// and the dependency types used by the auth route package.
package auth

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"

	"gospark-hub/internal/config"
	"gospark-hub/internal/store"
)

// TokenResolver converts a Bearer token into a user. It is implemented by the
// OAuth service; until that service is ready a no-op stub can be used.
type TokenResolver interface {
	Resolve(token string) (*store.User, error)
}

// NoopResolver always returns nil.
type NoopResolver struct{}

func (NoopResolver) Resolve(string) (*store.User, error) { return nil, nil }

// Deps is what auth middleware and handlers need from the application.
type Deps struct {
	Store         *store.DB
	Config        config.Config
	Logger        *log.Logger
	TokenResolver TokenResolver
}

// context keys for request-scoped auth state.
type ctxKey string

const (
	userCtxKey   ctxKey = "auth_user"
	sourceCtxKey ctxKey = "auth_source"
)

var (
	publicAPIPaths = map[string]bool{
		"/api/auth/state":    true,
		"/api/auth/login":    true,
		"/api/auth/register": true,
		"/api/auth/logout":   true,
	}
	guardedPrefixes = []string{"/api", "/ws", "/v1", "/mcp"}
	apiKeyPrefixes  = []string{"/v1", "/mcp"}
	apiKeyPaths     = map[string]bool{"/api/system/connect": true}
	mcpPrefix       = "/mcp"
	safeOriginRE    = regexp.MustCompile(`^https?://(?:[A-Za-z0-9._\-]+|\[[0-9A-Fa-f:.]+\])(?::[0-9]{1,5})?$`)
)

// Middleware wraps a handler with the upstream auth gate.
type Middleware struct {
	Deps
}

// NewMiddleware builds an auth middleware from dependencies.
func NewMiddleware(deps Deps) *Middleware {
	return &Middleware{Deps: deps}
}

// Handler returns the middleware as an http.Handler wrapper.
func (m *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.Config.AuthEnabled {
			next.ServeHTTP(w, r)
			return
		}

		user, source := m.resolveUser(r)
		if user != nil {
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, user))
		}
		r = r.WithContext(context.WithValue(r.Context(), sourceCtxKey, source))

		if user == nil && needsAuth(r.URL.Path) {
			m.deny(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CurrentUser returns the user attached to the request, if any.
func CurrentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(userCtxKey).(*store.User)
	return u
}

// AuthSource returns how the current user was resolved ("session", "api_key", "oauth").
func AuthSource(r *http.Request) string {
	s, _ := r.Context().Value(sourceCtxKey).(string)
	return s
}

func needsAuth(path string) bool {
	if publicAPIPaths[path] {
		return false
	}
	for _, p := range guardedPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

func apiKeyAllowed(path string) bool {
	for _, p := range apiKeyPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return apiKeyPaths[path]
}

func (m *Middleware) resolveUser(r *http.Request) (*store.User, string) {
	if c, err := r.Cookie(m.Config.SessionCookieName); err == nil && c.Value != "" {
		if u, err := m.Store.UserForSession(c.Value); err == nil && u != nil {
			return u, "session"
		}
	}

	key := bearer(r.Header)
	if key != "" {
		path := r.URL.Path
		if apiKeyAllowed(path) {
			if u, err := m.Store.UserForAPIKey(key); err == nil && u != nil {
				return u, "api_key"
			}
		}
		if strings.HasPrefix(path, mcpPrefix) && m.TokenResolver != nil {
			if u, err := m.TokenResolver.Resolve(key); err == nil && u != nil {
				return u, "oauth"
			}
		}
	}
	return nil, ""
}

func bearer(h http.Header) string {
	auth := strings.TrimSpace(h.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		tok := strings.TrimSpace(auth[7:])
		if tok != "" {
			return tok
		}
	}
	for _, name := range []string{"X-Api-Key", "Api-Key"} {
		if v := strings.TrimSpace(h.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

func (m *Middleware) deny(w http.ResponseWriter, r *http.Request) {
	if isWebsocket(r) {
		closeWebsocket(w, 1008)
		return
	}

	var body any
	status := http.StatusUnauthorized
	if strings.HasPrefix(r.URL.Path, "/v1") {
		body = map[string]any{
			"error": map[string]any{
				"type":    "authentication_error",
				"code":    "invalid_api_key",
				"message": "Missing or invalid API key. Sign in to Spark AI Hub and copy your key from Account, or run the sah installer again.",
			},
		}
	} else {
		body = map[string]any{"detail": "Authentication required."}
	}

	challenge := `Bearer realm="Spark AI Hub"`
	if strings.HasPrefix(r.URL.Path, mcpPrefix) {
		origin := requestOrigin(r)
		if origin == "" {
			scheme := requestScheme(r)
			host := r.Host
			origin = scheme + "://" + host
		}
		challenge += fmt.Sprintf(`, resource_metadata="%s/.well-known/oauth-protected-resource%s", scope="mcp"`, origin, mcpPrefix)
		if bearer(r.Header) != "" {
			challenge += `, error="invalid_token"`
		}
	}

	w.Header().Set("WWW-Authenticate", challenge)
	writeJSON(w, status, body)
}

func isWebsocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// closeWebsocket hijacks an upgrade request and sends a WebSocket close frame.
func closeWebsocket(w http.ResponseWriter, code int) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Authentication required.", http.StatusUnauthorized)
		return
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	// Server-to-client close frame: FIN=1, opcode=8, unmasked, payload = code.
	payload := make([]byte, 2)
	binary.BigEndian.PutUint16(payload, uint16(code))
	frame := append([]byte{0x88, byte(len(payload))}, payload...)
	_ = bufrw.WriteByte(frame[0])
	_ = bufrw.WriteByte(frame[1])
	_, _ = bufrw.Write(payload)
	_ = bufrw.Flush()
}

func requestScheme(r *http.Request) string {
	if fwd := r.Header.Get("Forwarded"); fwd != "" {
		parts := strings.Split(fwd, ",")
		for _, part := range parts {
			for _, kv := range strings.Split(part, ";") {
				kv = strings.TrimSpace(kv)
				if strings.HasPrefix(strings.ToLower(kv), "proto=") {
					v := strings.Trim(strings.TrimPrefix(kv, "proto="), `"`)
					v = strings.ToLower(strings.TrimSpace(v))
					if v == "http" || v == "https" {
						return v
					}
				}
			}
		}
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		v := strings.ToLower(strings.TrimSpace(strings.Split(proto, ",")[0]))
		if v == "http" || v == "https" {
			return v
		}
	}
	if r.URL.Scheme == "http" || r.URL.Scheme == "https" {
		return r.URL.Scheme
	}
	return "http"
}

func isHTTPS(r *http.Request, force bool) bool {
	if force {
		return true
	}
	return requestScheme(r) == "https"
}

func requestOrigin(r *http.Request) string {
	host := ""
	if fwd := r.Header.Get("Forwarded"); fwd != "" {
		first := strings.Split(fwd, ",")[0]
		for _, kv := range strings.Split(first, ";") {
			kv = strings.TrimSpace(kv)
			if strings.HasPrefix(strings.ToLower(kv), "host=") {
				host = strings.Trim(strings.TrimPrefix(kv, "host="), `"`)
				break
			}
		}
	}
	if host == "" {
		host = strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0])
	}
	if host == "" {
		host = r.Host
	}
	if host == "" {
		return ""
	}
	origin := requestScheme(r) + "://" + host
	if safeOriginRE.MatchString(origin) {
		return origin
	}
	return ""
}

func clientIP(r *http.Request) string {
	for _, h := range []string{"Cf-Connecting-Ip", "X-Forwarded-For", "X-Real-Ip"} {
		v := r.Header.Get(h)
		if v != "" {
			return strings.TrimSpace(strings.Split(v, ",")[0])
		}
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// RequireAdmin returns the current user only if they are an administrator.
func RequireAdmin(r *http.Request) (*store.User, error) {
	u := CurrentUser(r)
	if u == nil {
		return nil, errors.New("Authentication required.")
	}
	if u.Role != "admin" {
		return nil, errors.New("Only an administrator can do that.")
	}
	return u, nil
}

// ClientIP returns the caller's IP using the same forwarded-header precedence as upstream.
func ClientIP(r *http.Request) string { return clientIP(r) }

// SetSessionCookie sets the session cookie with the upstream attributes.
func SetSessionCookie(w http.ResponseWriter, r *http.Request, cfg config.Config, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cfg.SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   cfg.SessionTTLDays * 24 * 3600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(r, cfg.ForceSecureCookie),
	})
}

// ClearSessionCookie deletes the session cookie.
func ClearSessionCookie(w http.ResponseWriter, r *http.Request, cfg config.Config) {
	http.SetCookie(w, &http.Cookie{
		Name:     cfg.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(r, cfg.ForceSecureCookie),
	})
}
