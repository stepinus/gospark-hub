// Package store owns the SQLite schema and all SQL queries for users,
// sessions, API keys and usage accounting. Time values are stored as UTC
// text "YYYY-MM-DD HH:MM:SS" to stay byte-compatible with the upstream Hub.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

const TimeFmt = "2006-01-02 15:04:05"

// User mirrors the users table.
type User struct {
	ID           int64
	Email        string
	Name         string
	PasswordHash string
	Role         string
	Status       string
	APIKey       string
	CreatedAt    string
	ReviewedAt   sql.NullString
	LastLoginAt  sql.NullString
}

// Usage is the aggregated usage shape returned by UsageSummary.
type Usage struct {
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	LastUsedAt       *string `json:"last_used_at"`
	Requests30d      int64   `json:"requests_30d"`
	Tokens30d        int64   `json:"tokens_30d"`
	Requests24h      int64   `json:"requests_24h"`
	Tokens24h        int64   `json:"tokens_24h"`
}

// ModelUsage is per-model usage returned by TopModels.
type ModelUsage struct {
	Model       string `json:"model"`
	Requests    int64  `json:"requests"`
	TotalTokens int64  `json:"total_tokens"`
}

// Storage is the per-user media footprint.
type Storage struct {
	Bytes int64 `json:"bytes"`
	Files int64 `json:"files"`
}

// UsageEvent is a single row to append to usage_events.
type UsageEvent struct {
	UserID           int64
	Model            string
	Endpoint         string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Source           string
}

// cache is a tiny TTL cache for credential resolution.
type cache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	ttl     time.Duration
}

type cacheEntry struct {
	at   time.Time
	user *User
}

func newCache(ttl time.Duration) *cache {
	return &cache{entries: map[string]cacheEntry{}, ttl: ttl}
}

func (c *cache) get(key string) (*User, bool) {
	c.mu.RLock()
	ent, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || time.Since(ent.at) > c.ttl {
		return nil, false
	}
	return ent.user, true
}

func (c *cache) set(key string, u *User) {
	c.mu.Lock()
	c.entries[key] = cacheEntry{at: time.Now(), user: u}
	c.mu.Unlock()
}

func (c *cache) delete(key string) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

func (c *cache) clear() {
	c.mu.Lock()
	c.entries = map[string]cacheEntry{}
	c.mu.Unlock()
}

// DB wraps the SQLite connection and credential caches.
type DB struct {
	db           *sql.DB
	mu           sync.Mutex // serialises create-user positional rule
	sessionCache *cache
	apiKeyCache  *cache
}

// Open opens the database, enabling foreign keys and creating the schema.
func Open(dbPath string) (*DB, error) {
	dsn := dbPath + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetConnMaxLifetime(0)
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	d := &DB{
		db:           db,
		sessionCache: newCache(15 * time.Second),
		apiKeyCache:  newCache(15 * time.Second),
	}
	if err := d.InitSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return d, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

// InitSchema creates tables and indexes from the embedded schema.sql.
func (d *DB) InitSchema() error {
	_, err := d.db.Exec(schemaSQL)
	return err
}

// Exec runs a raw SQL statement. Prefer typed helpers in production code.
func (d *DB) Exec(query string, args ...any) (sql.Result, error) {
	return d.db.Exec(query, args...)
}

// Query runs a raw SQL query. Prefer typed helpers in production code.
func (d *DB) Query(query string, args ...any) (*sql.Rows, error) {
	return d.db.Query(query, args...)
}

// QueryRow runs a raw SQL query returning one row. Prefer typed helpers in production code.
func (d *DB) QueryRow(query string, args ...any) *sql.Row {
	return d.db.QueryRow(query, args...)
}

func nowISO() string { return time.Now().UTC().Format(TimeFmt) }

// UserCount returns the total number of user rows.
func (d *DB) UserCount() (int64, error) {
	var n int64
	if err := d.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CreateUser inserts a new account. If role or status are empty, the upstream
// positional rule applies: the first account becomes admin/active; the rest
// are user/pending.
func (d *DB) CreateUser(email, name, passwordHash, role, status, apiKey string) (*User, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if role == "" || status == "" {
		n, err := d.UserCount()
		if err != nil {
			return nil, err
		}
		if role == "" {
			if n == 0 {
				role = "admin"
			} else {
				role = "user"
			}
		}
		if status == "" {
			if n == 0 {
				status = "active"
			} else {
				status = "pending"
			}
		}
	}

	var reviewedAt sql.NullString
	if status != "pending" {
		reviewedAt = sql.NullString{String: nowISO(), Valid: true}
	}

	res, err := d.db.Exec(
		`INSERT INTO users (email, name, password_hash, role, status, api_key, reviewed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		email, name, passwordHash, role, status, apiKey, reviewedAt,
	)
	if err != nil {
		if isUniqueViolation(err) || strings.Contains(err.Error(), "UNIQUE") {
			return nil, errors.New("An account with that email already exists.")
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return d.GetUserByID(id)
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed")
}

func scanUser(row *sql.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.Role, &u.Status,
		&u.APIKey, &u.CreatedAt, &u.ReviewedAt, &u.LastLoginAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &u, err
}

func scanUserRows(rows *sql.Rows) ([]*User, error) {
	var out []*User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.Role, &u.Status,
			&u.APIKey, &u.CreatedAt, &u.ReviewedAt, &u.LastLoginAt); err != nil {
			return nil, err
		}
		out = append(out, &u)
	}
	return out, rows.Err()
}

// GetUserByID fetches a user by primary key.
func (d *DB) GetUserByID(id int64) (*User, error) {
	return scanUser(d.db.QueryRow(
		"SELECT id, email, name, password_hash, role, status, api_key, created_at, reviewed_at, last_login_at FROM users WHERE id = ?", id))
}

// GetUserByEmail fetches a user by email, case-insensitive.
func (d *DB) GetUserByEmail(email string) (*User, error) {
	return scanUser(d.db.QueryRow(
		"SELECT id, email, name, password_hash, role, status, api_key, created_at, reviewed_at, last_login_at FROM users WHERE email = ? COLLATE NOCASE", email))
}

// UpdateUser patches a user. Allowed keys: email, name, role, status,
// password_hash, api_key, reviewed_at, last_login_at.
func (d *DB) UpdateUser(id int64, fields map[string]any) (*User, error) {
	allowed := map[string]bool{
		"email": true, "name": true, "role": true, "status": true,
		"password_hash": true, "api_key": true, "reviewed_at": true, "last_login_at": true,
	}
	if len(fields) == 0 {
		return d.GetUserByID(id)
	}

	if email, ok := fields["email"].(string); ok && email != "" {
		var dup int64
		if err := d.db.QueryRow(
			"SELECT id FROM users WHERE email = ? COLLATE NOCASE AND id != ?", email, id,
		).Scan(&dup); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		} else if err == nil {
			return nil, errors.New("An account with that email already exists.")
		}
	}

	cols := make([]string, 0, len(fields))
	vals := make([]any, 0, len(fields))
	for k, v := range fields {
		if !allowed[k] {
			continue
		}
		cols = append(cols, fmt.Sprintf("%s = ?", k))
		vals = append(vals, v)
	}
	if len(cols) == 0 {
		return d.GetUserByID(id)
	}
	vals = append(vals, id)
	if _, err := d.db.Exec(
		"UPDATE users SET "+strings.Join(cols, ", ")+" WHERE id = ?",
		vals...,
	); err != nil {
		return nil, err
	}
	d.InvalidateCaches()
	return d.GetUserByID(id)
}

// DeleteUser removes a user and cascades to sessions, usage, media and links.
func (d *DB) DeleteUser(id int64) error {
	_, err := d.db.Exec("DELETE FROM users WHERE id = ?", id)
	if err == nil {
		d.InvalidateCaches()
	}
	return err
}

// AdminCount returns the number of active administrators, optionally excluding one id.
func (d *DB) AdminCount(excludeID *int64) (int64, error) {
	var n int64
	q := "SELECT COUNT(*) FROM users WHERE role = 'admin' AND status = 'active'"
	args := []any{}
	if excludeID != nil {
		q += " AND id != ?"
		args = append(args, *excludeID)
	}
	if err := d.db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// TokenDigest returns the hex SHA-256 digest of a token.
func TokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateSession creates a browser session, updates last_login_at and returns the raw token.
func (d *DB) CreateSession(userID int64, userAgent, ip string, ttlDays int) (string, error) {
	token, err := NewRandomToken(32)
	if err != nil {
		return "", err
	}
	expires := time.Now().UTC().Add(time.Duration(ttlDays) * 24 * time.Hour).Format(TimeFmt)
	_, err = d.db.Exec(
		`INSERT INTO sessions (token_hash, user_id, expires_at, user_agent, ip)
		 VALUES (?, ?, ?, ?, ?)`,
		TokenDigest(token), userID, expires, truncate(userAgent, 256), truncate(ip, 64),
	)
	if err != nil {
		return "", err
	}
	_, err = d.db.Exec(
		"UPDATE users SET last_login_at = ? WHERE id = ?",
		nowISO(), userID,
	)
	if err != nil {
		return "", err
	}
	d.InvalidateCaches()
	return token, nil
}

// NewRandomToken returns a URL-safe base64 string encoding n random bytes.
// It matches the upstream secrets.token_urlsafe(n) shape used for sessions
// and API keys.
func NewRandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// DestroySession deletes one session by raw token.
func (d *DB) DestroySession(token string) error {
	_, err := d.db.Exec("DELETE FROM sessions WHERE token_hash = ?", TokenDigest(token))
	if err == nil {
		d.sessionCache.delete(TokenDigest(token))
	}
	return err
}

// DestroySessionsForUser deletes every session for a user.
func (d *DB) DestroySessionsForUser(userID int64) error {
	_, err := d.db.Exec("DELETE FROM sessions WHERE user_id = ?", userID)
	if err == nil {
		d.InvalidateCaches()
	}
	return err
}

// PurgeExpiredSessions removes sessions whose expires_at is in the past.
func (d *DB) PurgeExpiredSessions() error {
	_, err := d.db.Exec("DELETE FROM sessions WHERE expires_at < ?", nowISO())
	return err
}

// UserForSession resolves a raw cookie token to its active user.
func (d *DB) UserForSession(token string) (*User, error) {
	if token == "" {
		return nil, nil
	}
	digest := TokenDigest(token)
	if u, ok := d.sessionCache.get(digest); ok {
		return u, nil
	}

	row := d.db.QueryRow(
		`SELECT u.id, u.email, u.name, u.password_hash, u.role, u.status, u.api_key,
		        u.created_at, u.reviewed_at, u.last_login_at
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = ? AND s.expires_at > ?`,
		digest, nowISO(),
	)
	u, err := scanUser(row)
	if err != nil {
		return nil, err
	}
	if u != nil && u.Status != "active" {
		u = nil
	}
	if u != nil {
		_, _ = d.db.Exec("UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?", nowISO(), digest)
	}
	d.sessionCache.set(digest, u)
	return u, nil
}

// UserForAPIKey resolves a raw API key to its active user.
func (d *DB) UserForAPIKey(key string) (*User, error) {
	if key == "" {
		return nil, nil
	}
	if u, ok := d.apiKeyCache.get(key); ok {
		return u, nil
	}
	u, err := scanUser(d.db.QueryRow(
		"SELECT id, email, name, password_hash, role, status, api_key, created_at, reviewed_at, last_login_at FROM users WHERE api_key = ? AND status = 'active'", key))
	if err != nil {
		return nil, err
	}
	d.apiKeyCache.set(key, u)
	return u, nil
}

// InvalidateCaches drops all cached credential lookups.
func (d *DB) InvalidateCaches() {
	d.sessionCache.clear()
	d.apiKeyCache.clear()
}

// RecordUsage appends a usage_events row. It never propagates errors to the
// caller; failures are printed to the supplied logger by the caller.
func (d *DB) RecordUsage(ev UsageEvent) error {
	total := ev.TotalTokens
	if total == 0 {
		total = ev.PromptTokens + ev.CompletionTokens
	}
	_, err := d.db.Exec(
		`INSERT INTO usage_events
		 (user_id, model, endpoint, prompt_tokens, completion_tokens, total_tokens, source)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ev.UserID, ev.Model, ev.Endpoint, ev.PromptTokens, ev.CompletionTokens, total, ev.Source,
	)
	return err
}

// UsageSummary returns per-user aggregates. Pass nil for all users.
func (d *DB) UsageSummary(userID *int64) (map[int64]Usage, error) {
	where := ""
	args := []any{}
	if userID != nil {
		where = "WHERE user_id = ?"
		args = append(args, *userID)
	}
	rows, err := d.db.Query(
		`SELECT user_id,
		        COUNT(*) AS requests,
		        COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
		        COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
		        COALESCE(SUM(total_tokens), 0) AS total_tokens,
		        MAX(ts) AS last_used_at,
		        COALESCE(SUM(CASE WHEN ts >= datetime('now','-30 days') THEN 1 ELSE 0 END), 0) AS requests_30d,
		        COALESCE(SUM(CASE WHEN ts >= datetime('now','-30 days') THEN total_tokens ELSE 0 END), 0) AS tokens_30d,
		        COALESCE(SUM(CASE WHEN ts >= datetime('now','-1 day') THEN 1 ELSE 0 END), 0) AS requests_24h,
		        COALESCE(SUM(CASE WHEN ts >= datetime('now','-1 day') THEN total_tokens ELSE 0 END), 0) AS tokens_24h
		 FROM usage_events `+where+` GROUP BY user_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64]Usage{}
	for rows.Next() {
		var uid int64
		var u Usage
		var last sql.NullString
		if err := rows.Scan(&uid, &u.Requests, &u.PromptTokens, &u.CompletionTokens, &u.TotalTokens,
			&last, &u.Requests30d, &u.Tokens30d, &u.Requests24h, &u.Tokens24h); err != nil {
			return nil, err
		}
		if last.Valid && last.String != "" {
			u.LastUsedAt = &last.String
		}
		out[uid] = u
	}
	return out, rows.Err()
}

// TopModels returns the most-used models for a user, or for all users if id is nil.
func (d *DB) TopModels(userID *int64, limit int) ([]ModelUsage, error) {
	where := "WHERE model != ''"
	args := []any{}
	if userID != nil {
		where += " AND user_id = ?"
		args = append(args, *userID)
	}
	args = append(args, limit)
	rows, err := d.db.Query(
		`SELECT model, COUNT(*) AS requests, COALESCE(SUM(total_tokens), 0) AS total_tokens
		 FROM usage_events `+where+`
		 GROUP BY model ORDER BY requests DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelUsage
	for rows.Next() {
		var m ModelUsage
		if err := rows.Scan(&m.Model, &m.Requests, &m.TotalTokens); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if out == nil {
		out = []ModelUsage{} // upstream serialises an empty list, never null
	}
	return out, rows.Err()
}

// SpaceByUser returns an empty map. Media storage is implemented in a later phase.
func (d *DB) SpaceByUser() (map[int64]Storage, error) {
	return map[int64]Storage{}, nil
}

// AllUsers returns every user row.
func (d *DB) AllUsers() ([]*User, error) {
	rows, err := d.db.Query(
		"SELECT id, email, name, password_hash, role, status, api_key, created_at, reviewed_at, last_login_at FROM users")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserRows(rows)
}

// InstalledRecipes returns the set of slugs recorded in installed_recipes.
func (d *DB) InstalledRecipes(ctx context.Context) (map[string]struct{}, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT slug FROM installed_recipes")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		out[slug] = struct{}{}
	}
	return out, rows.Err()
}

// InstalledSlugs returns every installed slug in ascending order.
func (d *DB) InstalledSlugs(ctx context.Context) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT slug FROM installed_recipes ORDER BY slug")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		out = append(out, slug)
	}
	return out, rows.Err()
}

// UpsertInstalledRecipe records a successful install/launch. It mirrors the
// upstream INSERT OR REPLACE, which refreshes installed_at each time.
func (d *DB) UpsertInstalledRecipe(ctx context.Context, slug, composeProject string) error {
	_, err := d.db.ExecContext(ctx,
		"INSERT OR REPLACE INTO installed_recipes (slug, status, compose_project) VALUES (?, 'installed', ?)",
		slug, composeProject)
	return err
}

// DeleteInstalledRecipe removes the install marker for a slug.
func (d *DB) DeleteInstalledRecipe(ctx context.Context, slug string) error {
	_, err := d.db.ExecContext(ctx, "DELETE FROM installed_recipes WHERE slug = ?", slug)
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// MediaRow is one row from the media table.
type MediaRow struct {
	Name    string
	UserID  int64
	Model   string
	Seconds sql.NullFloat64
}

// RecordMedia inserts or replaces a media ownership row.
func (d *DB) RecordMedia(name string, userID int64, model string, seconds *float64) error {
	var sec any
	if seconds != nil {
		sec = *seconds
	}
	_, err := d.db.Exec(
		"INSERT OR REPLACE INTO media (name, user_id, model, seconds) VALUES (?, ?, ?, ?)",
		name, userID, model, sec,
	)
	return err
}

// MediaOwner returns the owner of a media file, if recorded.
func (d *DB) MediaOwner(name string) (int64, bool, error) {
	var owner sql.NullInt64
	row := d.db.QueryRow("SELECT user_id FROM media WHERE name = ?", name)
	if err := row.Scan(&owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return owner.Int64, owner.Valid, nil
}

// MediaRows returns every media row keyed by name.
func (d *DB) MediaRows() ([]MediaRow, error) {
	rows, err := d.db.Query("SELECT name, user_id, model, seconds FROM media")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MediaRow
	for rows.Next() {
		var r MediaRow
		if err := rows.Scan(&r.Name, &r.UserID, &r.Model, &r.Seconds); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteMedia removes the media row for a file.
func (d *DB) DeleteMedia(name string) error {
	_, err := d.db.Exec("DELETE FROM media WHERE name = ?", name)
	return err
}

// CreateMediaLink stores a new upload/download link.
func (d *DB) CreateMediaLink(tokenHash, kind string, userID int64, name string, expiresAt int64) error {
	_, err := d.db.Exec(
		"INSERT INTO media_links (token_hash, kind, user_id, name, expires_at) VALUES (?, ?, ?, ?, ?)",
		tokenHash, kind, userID, name, expiresAt,
	)
	return err
}

// ClaimMediaUpload reserves an upload link. Returns true if the reservation succeeded.
func (d *DB) ClaimMediaUpload(tokenHash string, usedAt int64) (bool, error) {
	res, err := d.db.Exec(
		"UPDATE media_links SET used_at = ? WHERE token_hash = ? AND kind = 'upload' AND used_at IS NULL AND expires_at > ?",
		usedAt, tokenHash, usedAt,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ReleaseMediaUpload un-reserves an upload link after a failed upload.
func (d *DB) ReleaseMediaUpload(tokenHash string) error {
	_, err := d.db.Exec(
		"UPDATE media_links SET used_at = NULL WHERE token_hash = ? AND kind = 'upload'",
		tokenHash,
	)
	return err
}

// MediaDownloadTarget returns the file name for a live download link.
func (d *DB) MediaDownloadTarget(tokenHash string, now int64) (string, error) {
	var name string
	err := d.db.QueryRow(
		"SELECT name FROM media_links WHERE token_hash = ? AND kind = 'download' AND expires_at > ?",
		tokenHash, now,
	).Scan(&name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return name, nil
}

// MediaLinkOwner resolves the active user bound to a link token.
func (d *DB) MediaLinkOwner(tokenHash string) (*User, error) {
	return scanUser(d.db.QueryRow(
		`SELECT u.id, u.email, u.name, u.password_hash, u.role, u.status, u.api_key,
		        u.created_at, u.reviewed_at, u.last_login_at
		 FROM media_links l JOIN users u ON u.id = l.user_id
		 WHERE l.token_hash = ? AND u.status = 'active'`, tokenHash))
}

// PurgeExpiredMediaLinks removes media_links rows whose expires_at is <= now.
func (d *DB) PurgeExpiredMediaLinks(now int64) error {
	_, err := d.db.Exec("DELETE FROM media_links WHERE expires_at <= ?", now)
	return err
}
