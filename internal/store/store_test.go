package store

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func open(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func queryStrings(t *testing.T, db *DB, q string) [][2]string {
	t.Helper()
	rows, err := db.db.Query(q)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, [2]string{a, b})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func queryString(t *testing.T, db *DB, q string) string {
	t.Helper()
	var s string
	if err := db.db.QueryRow(q).Scan(&s); err != nil {
		t.Fatalf("query row: %v", err)
	}
	return s
}

func queryInt(t *testing.T, db *DB, q string) int64 {
	t.Helper()
	var n int64
	if err := db.db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("query int: %v", err)
	}
	return n
}

// TestSchemaMatchesContract asserts the 9 tables and 4 indexes from
// docs/contract/schema.sql exist with the exact names.
func TestSchemaMatchesContract(t *testing.T) {
	db := open(t)
	rows := queryStrings(t, db,
		"SELECT type, name FROM sqlite_master WHERE type IN ('table','index') AND name NOT LIKE 'sqlite_%'")
	got := map[string]bool{}
	for _, r := range rows {
		got[r[0]+":"+r[1]] = true
	}
	wantTables := []string{
		"installed_recipes", "users", "sessions", "usage_events",
		"oauth_clients", "oauth_codes", "oauth_tokens", "media", "media_links",
	}
	for _, name := range wantTables {
		if !got["table:"+name] {
			t.Errorf("missing table %s", name)
		}
	}
	wantIndexes := []string{
		"idx_sessions_user", "idx_usage_user_ts", "idx_oauth_tokens_user", "idx_media_user",
	}
	for _, name := range wantIndexes {
		if !got["index:"+name] {
			t.Errorf("missing index %s", name)
		}
	}
	if len(wantTables) != 9 {
		t.Fatalf("expected 9 contract tables, listed %d", len(wantTables))
	}
}

// TestTimestampsAreStrings proves created_at/expires_at are stored as the UTC
// text format the upstream compares lexically, not as RFC3339 or time.Time.
func TestTimestampsAreStrings(t *testing.T) {
	db := open(t)
	u, err := db.CreateUser("a@x.io", "A", "hash", "", "", "sah-key-1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`).MatchString(u.CreatedAt) {
		t.Fatalf("created_at not upstream format: %q", u.CreatedAt)
	}
	if _, err := db.CreateSession(u.ID, "ua", "127.0.0.1", 30); err != nil {
		t.Fatalf("session: %v", err)
	}
	exp := queryString(t, db, "SELECT expires_at FROM sessions LIMIT 1")
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`).MatchString(exp) {
		t.Fatalf("expires_at not upstream format: %q", exp)
	}
}

func TestFirstUserPositionalRule(t *testing.T) {
	db := open(t)
	first, err := db.CreateUser("a@x.io", "", "hash", "", "", "k1")
	if err != nil {
		t.Fatal(err)
	}
	if first.Role != "admin" || first.Status != "active" {
		t.Fatalf("first user want admin/active got %s/%s", first.Role, first.Status)
	}
	second, err := db.CreateUser("b@x.io", "", "hash", "", "", "k2")
	if err != nil {
		t.Fatal(err)
	}
	if second.Role != "user" || second.Status != "pending" {
		t.Fatalf("second user want user/pending got %s/%s", second.Role, second.Status)
	}
	// Explicit role/status bypass the positional rule.
	explicit, err := db.CreateUser("c@x.io", "", "hash", "admin", "active", "k3")
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Role != "admin" || explicit.Status != "active" {
		t.Fatalf("explicit role/status ignored: %s/%s", explicit.Role, explicit.Status)
	}
}

func TestDuplicateEmail(t *testing.T) {
	db := open(t)
	if _, err := db.CreateUser("a@x.io", "", "hash", "", "", "k1"); err != nil {
		t.Fatal(err)
	}
	_, err := db.CreateUser("A@X.IO", "", "hash", "", "", "k2")
	if err == nil || err.Error() != "An account with that email already exists." {
		t.Fatalf("want duplicate error, got %v", err)
	}
}

func TestDeleteCascades(t *testing.T) {
	db := open(t)
	u, _ := db.CreateUser("a@x.io", "", "hash", "", "", "k1")
	if _, err := db.CreateSession(u.ID, "", "", 30); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordUsage(UsageEvent{UserID: u.ID, Endpoint: "x", TotalTokens: 5}); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM sessions"); n != 0 {
		t.Fatalf("sessions not cascaded: %d", n)
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM usage_events"); n != 0 {
		t.Fatalf("usage not cascaded: %d", n)
	}
}

// TestOpenLiveUpstreamDatabase opens a copy of the real Hub database and reads
// a user through the same code path the daemon uses. Skipped when the live
// database is not present on this machine.
func TestOpenLiveUpstreamDatabase(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	live := filepath.Join(home, "spark-ai-hub", "data", "spark-ai-hub.db")
	if _, err := os.Stat(live); err != nil {
		t.Skip("live upstream database not present")
	}
	// Copy so InitSchema's idempotent run never touches the real file.
	src, err := os.ReadFile(live)
	if err != nil {
		t.Fatalf("read live db: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "live.db")
	if err := os.WriteFile(dst, src, 0o600); err != nil {
		t.Fatalf("write copy: %v", err)
	}
	db, err := Open(dst)
	if err != nil {
		t.Fatalf("open live db: %v", err)
	}
	defer db.Close()

	users, err := db.AllUsers()
	if err != nil {
		t.Fatalf("read users: %v", err)
	}
	if len(users) == 0 {
		t.Fatal("live db has no users")
	}
	u := users[0]
	if u.PasswordHash == "" || u.APIKey == "" {
		t.Fatalf("live user missing credentials: %+v", u)
	}
	if !regexp.MustCompile(`^pbkdf2_sha256\$\d+\$[0-9a-f]+\$[0-9a-f]+$`).MatchString(u.PasswordHash) {
		t.Fatalf("live password hash not pbkdf2 format: %q", u.PasswordHash)
	}
}
