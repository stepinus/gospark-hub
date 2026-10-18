package modelproxy

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gospark-hub/internal/config"
)

const probeHeader = "X-Sah-Probe"

// ProbeHeaders returns the single probe header used to bypass forward_auth.
func ProbeHeaders(cfg config.Config) http.Header {
	h := make(http.Header)
	h.Set(probeHeader, ProbeToken(cfg.DataDir))
	return h
}

// ProbeToken returns the shared probe token, creating it if necessary.
func ProbeToken(dataDir string) string {
	p := filepath.Join(dataDir, "proxy-probe-token")
	b, err := os.ReadFile(p)
	if err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t
		}
	}
	t := newToken(32)
	_ = os.MkdirAll(dataDir, 0o755)
	_ = os.WriteFile(p, []byte(t), 0o600)
	return t
}

func newToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
