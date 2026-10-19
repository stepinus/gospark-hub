package oauth

import (
	"database/sql"
	"errors"
	"strings"

	"gospark-hub/internal/store"
)

// CreateCode stores a fresh authorization code (TTL 600 s) bound to the client,
// the signed-in user, the redirect URI and the PKCE challenge. Only the digest
// is written; the raw code goes back to the caller once.
func (s *Service) CreateCode(clientID string, userID int64, redirectURI, codeChallenge, resource, scope string) (string, error) {
	code, err := store.NewRandomToken(32)
	if err != nil {
		return "", err
	}
	_, err = s.DB.Exec(
		`INSERT INTO oauth_codes (code_hash, client_id, user_id, redirect_uri,
		                          code_challenge, resource, scope, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		store.TokenDigest(code), clientID, userID, redirectURI, codeChallenge,
		resource, scope, expiresISO(CodeTTL),
	)
	if err != nil {
		return "", err
	}
	return code, nil
}

type codeRow struct {
	ClientID      string
	UserID        int64
	RedirectURI   string
	CodeChallenge string
	Resource      string
	Scope         string
	ExpiresAt     string
}

// ExchangeCode redeems an authorization code for a token pair. The code is
// deleted before any check, so even a failed attempt burns it — upstream does
// the same on purpose.
func (s *Service) ExchangeCode(code, clientID, redirectURI, codeVerifier, resource string) (map[string]any, error) {
	digest := store.TokenDigest(code)

	var row codeRow
	err := s.DB.QueryRow(
		`SELECT client_id, user_id, redirect_uri, code_challenge, resource, scope, expires_at
		 FROM oauth_codes WHERE code_hash = ?`, digest,
	).Scan(&row.ClientID, &row.UserID, &row.RedirectURI, &row.CodeChallenge,
		&row.Resource, &row.Scope, &row.ExpiresAt)
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		return nil, err
	}

	res, execErr := s.DB.Exec("DELETE FROM oauth_codes WHERE code_hash = ?", digest)
	if execErr != nil {
		return nil, execErr
	}
	deleted, _ := res.RowsAffected()
	if missing || deleted == 0 {
		return nil, newError("invalid_grant", "The authorization code is invalid or was already used.")
	}
	if row.ExpiresAt < nowISO() {
		return nil, newError("invalid_grant", "The authorization code has expired.")
	}
	if row.ClientID != clientID || row.RedirectURI != redirectURI {
		return nil, newError("invalid_grant", "The code was issued to a different client or redirect_uri.")
	}
	if !pkceMatches(codeVerifier, row.CodeChallenge) {
		return nil, newError("invalid_grant", "PKCE verification failed.")
	}
	if resource != "" && strings.TrimRight(resource, "/") != row.Resource {
		return nil, newError("invalid_target", "resource does not match the authorization request.")
	}
	active, err := s.activeUser(row.UserID)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, newError("invalid_grant", "The account is no longer active.")
	}
	return s.issue(clientID, row.UserID, row.Resource, row.Scope)
}

type tokenRow struct {
	ClientID  string
	UserID    int64
	Resource  string
	Scope     string
	ExpiresAt string
}

// Refresh rotates a refresh token: the presented one is deleted first, so it is
// single-use even when the remaining checks fail.
func (s *Service) Refresh(refreshToken, clientID, resource string) (map[string]any, error) {
	digest := store.TokenDigest(refreshToken)

	var row tokenRow
	err := s.DB.QueryRow(
		`SELECT client_id, user_id, resource, scope, expires_at
		 FROM oauth_tokens WHERE token_hash = ? AND kind = 'refresh'`, digest,
	).Scan(&row.ClientID, &row.UserID, &row.Resource, &row.Scope, &row.ExpiresAt)
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		return nil, err
	}

	res, execErr := s.DB.Exec(
		"DELETE FROM oauth_tokens WHERE token_hash = ? AND kind = 'refresh'", digest)
	if execErr != nil {
		return nil, execErr
	}
	deleted, _ := res.RowsAffected()
	if missing || deleted == 0 || row.ExpiresAt < nowISO() {
		return nil, newError("invalid_grant", "The refresh token is invalid, expired or already used.")
	}
	if row.ClientID != clientID {
		return nil, newError("invalid_grant", "The refresh token was issued to a different client.")
	}
	if resource != "" && strings.TrimRight(resource, "/") != row.Resource {
		return nil, newError("invalid_target", "resource does not match the original grant.")
	}
	active, err := s.activeUser(row.UserID)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, newError("invalid_grant", "The account is no longer active.")
	}
	return s.issue(clientID, row.UserID, row.Resource, row.Scope)
}

// issue writes an access/refresh pair atomically and returns the token response.
func (s *Service) issue(clientID string, userID int64, resource, scope string) (map[string]any, error) {
	accessRandom, err := store.NewRandomToken(32)
	if err != nil {
		return nil, err
	}
	refreshRandom, err := store.NewRandomToken(32)
	if err != nil {
		return nil, err
	}
	access := "sah-oat-" + accessRandom
	refresh := "sah-ort-" + refreshRandom

	_, err = s.DB.Exec(
		`INSERT INTO oauth_tokens (token_hash, kind, client_id, user_id, resource, scope, expires_at)
		 VALUES (?, 'access', ?, ?, ?, ?, ?), (?, 'refresh', ?, ?, ?, ?, ?)`,
		store.TokenDigest(access), clientID, userID, resource, scope, expiresISO(AccessTTL),
		store.TokenDigest(refresh), clientID, userID, resource, scope, expiresISO(RefreshTTL),
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    AccessTTL,
		"refresh_token": refresh,
		"scope":         scope,
	}, nil
}

func (s *Service) activeUser(userID int64) (bool, error) {
	var status string
	err := s.DB.QueryRow("SELECT status FROM users WHERE id = ?", userID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status == "active", nil
}

// Resolve turns an OAuth access token into its account, or nil when the token is
// unknown, expired, not an access token, or its user is no longer active. It is
// the auth.TokenResolver implementation for /mcp.
func (s *Service) Resolve(token string) (*store.User, error) {
	if token == "" {
		return nil, nil
	}
	if s.tokens == nil {
		s.tokens = newTokenCache()
	}
	digest := store.TokenDigest(token)
	if u, ok := s.tokens.get(digest); ok {
		return u, nil
	}

	u, err := scanUser(s.DB.QueryRow(
		`SELECT u.id, u.email, u.name, u.password_hash, u.role, u.status, u.api_key,
		        u.created_at, u.reviewed_at, u.last_login_at
		 FROM oauth_tokens t JOIN users u ON u.id = t.user_id
		 WHERE t.token_hash = ? AND t.kind = 'access' AND t.expires_at > ?
		   AND u.status = 'active'`,
		digest, nowISO(),
	))
	if err != nil {
		return nil, err
	}
	s.tokens.set(digest, u)
	return u, nil
}

func scanUser(row *sql.Row) (*store.User, error) {
	var u store.User
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.Role, &u.Status,
		&u.APIKey, &u.CreatedAt, &u.ReviewedAt, &u.LastLoginAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// RevokeUserTokens deletes every OAuth token belonging to a user and drops the
// resolution cache.
func (s *Service) RevokeUserTokens(userID int64) (int64, error) {
	res, err := s.DB.Exec("DELETE FROM oauth_tokens WHERE user_id = ?", userID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	s.InvalidateCache()
	return n, nil
}

// PurgeExpired deletes expired authorization codes and tokens. The daemon's
// janitor calls it hourly, next to the session purge.
func (s *Service) PurgeExpired() error {
	now := nowISO()
	if _, err := s.DB.Exec("DELETE FROM oauth_codes WHERE expires_at < ?", now); err != nil {
		return err
	}
	_, err := s.DB.Exec("DELETE FROM oauth_tokens WHERE expires_at < ?", now)
	return err
}
