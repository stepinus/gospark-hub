package mcp

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// App tells the media tools how to reach an installed recipe's container.
// Upstream reaches every app through the front door's probe route
// (proxy_service.internal_url), carrying X-Sah-Probe so forward_auth lets it
// past; a test supplies its own implementation pointing at httptest servers.
type App interface {
	// InternalURL is the base URL of recipe `slug`, no trailing slash.
	InternalURL(slug string) string
	// ProbeHeaders is the header set that gets the daemon past forward_auth.
	ProbeHeaders() map[string]string
}

// ProxyApp is upstream proxy_service: the front door on the public port.
type ProxyApp struct {
	PublicPort int
	DataDir    string
}

// InternalURL mirrors proxy_service.internal_url.
func (a ProxyApp) InternalURL(slug string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/run/%s", a.PublicPort, slug)
}

// ProbeHeaders mirrors proxy_service.probe_headers, minting and persisting the
// shared secret on first use exactly like probe_token().
func (a ProxyApp) ProbeHeaders() map[string]string {
	if a.DataDir == "" {
		return map[string]string{}
	}
	path := filepath.Join(a.DataDir, "proxy-probe-token")
	if raw, err := os.ReadFile(path); err == nil {
		if token := strings.TrimSpace(string(raw)); token != "" {
			return map[string]string{"X-Sah-Probe": token}
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return map[string]string{}
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	_ = os.WriteFile(path, []byte(token), 0o600)
	return map[string]string{"X-Sah-Probe": token}
}
