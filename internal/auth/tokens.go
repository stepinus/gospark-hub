package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"gospark-hub/internal/store"
)

const apiKeyPrefix = "sah-"

// NowISO returns the UTC timestamp format used everywhere in the database.
func NowISO() string { return time.Now().UTC().Format(store.TimeFmt) }

// TokenDigest returns the hex SHA-256 digest of a token.
func TokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewAPIKey creates a new API key in the upstream shape.
func NewAPIKey() (string, error) {
	raw, err := store.NewRandomToken(32)
	if err != nil {
		return "", err
	}
	return apiKeyPrefix + raw, nil
}

// NewSessionToken creates a new browser session token.
func NewSessionToken() (string, error) {
	return store.NewRandomToken(32)
}

// PublicUser returns the 8 upstream-visible fields for a user.
func PublicUser(u *store.User) map[string]any {
	if u == nil {
		return nil
	}
	m := map[string]any{
		"id":         u.ID,
		"email":      u.Email,
		"name":       u.Name,
		"role":       u.Role,
		"status":     u.Status,
		"created_at": u.CreatedAt,
	}
	if u.ReviewedAt.Valid {
		m["reviewed_at"] = u.ReviewedAt.String
	} else {
		m["reviewed_at"] = nil
	}
	if u.LastLoginAt.Valid {
		m["last_login_at"] = u.LastLoginAt.String
	} else {
		m["last_login_at"] = nil
	}
	return m
}
