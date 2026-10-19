package oauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gospark-hub/internal/store"
)

// loopbackHosts are the only plain-http redirect hosts accepted, matching
// upstream LOOPBACK_HOSTS (native clients on the user's own machine).
func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost", "[::1]", "::1":
		return true
	}
	return false
}

// ValidRedirectURI reports whether uri is https anywhere, or plain http on a
// loopback address. A fragment or a missing hostname disqualifies it.
func ValidRedirectURI(uri string) bool {
	u, err := url.Parse(uri)
	if err != nil {
		return false
	}
	if u.Fragment != "" || u.Hostname() == "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	return u.Scheme == "http" && isLoopbackHost(u.Hostname())
}

// RedirectMatches is exact matching, except a loopback redirect may use any
// port (RFC 8252 §7.3): host, path and query still have to agree.
func RedirectMatches(registered []string, given string) bool {
	for _, uri := range registered {
		if uri == given {
			return true
		}
	}
	g, err := url.Parse(given)
	if err != nil {
		return false
	}
	if g.Scheme != "http" || !isLoopbackHost(g.Hostname()) {
		return false
	}
	host := strings.ToLower(g.Hostname())
	for _, uri := range registered {
		r, err := url.Parse(uri)
		if err != nil {
			continue
		}
		if r.Scheme == "http" && strings.ToLower(r.Hostname()) == host &&
			r.Path == g.Path && r.RawQuery == g.RawQuery {
			return true
		}
	}
	return false
}

func (s *Service) maxClients() int {
	if s.MaxClients > 0 {
		return s.MaxClients
	}
	return MaxClients
}

// RegisterClient performs RFC 7591 dynamic registration of a public client.
// Only redirect_uris, token_endpoint_auth_method and client_name are read; every
// other member is ignored, as upstream does.
func (s *Service) RegisterClient(meta map[string]any) (map[string]any, error) {
	uris, ok := stringList(meta["redirect_uris"])
	if !ok || len(uris) == 0 {
		return nil, newError("invalid_redirect_uri", "redirect_uris must be a non-empty list of URLs.")
	}
	for _, uri := range uris {
		if !ValidRedirectURI(uri) {
			return nil, newError("invalid_redirect_uri",
				fmt.Sprintf("%s must be https, or http on a loopback address.", uri))
		}
	}

	method := "none"
	if v, present := meta["token_endpoint_auth_method"]; present && v != nil {
		s, isString := v.(string)
		if !isString {
			return nil, newError("invalid_client_metadata",
				"Only public clients are supported (token_endpoint_auth_method=none).")
		}
		if s != "" {
			method = s
		}
	}
	if method != "none" {
		return nil, newError("invalid_client_metadata",
			"Only public clients are supported (token_endpoint_auth_method=none).")
	}

	name := truncateRunes(stringValue(meta["client_name"]), 100)

	var count int64
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM oauth_clients").Scan(&count); err != nil {
		return nil, err
	}
	if count >= int64(s.maxClients()) {
		return nil, newErrorStatus("invalid_client_metadata",
			"This Hub has too many registered clients.", http.StatusServiceUnavailable)
	}

	random, err := store.NewRandomToken(18)
	if err != nil {
		return nil, err
	}
	clientID := "sah-client-" + random

	encoded, err := json.Marshal(uris)
	if err != nil {
		return nil, err
	}
	if _, err := s.DB.Exec(
		"INSERT INTO oauth_clients (client_id, client_name, redirect_uris) VALUES (?, ?, ?)",
		clientID, name, string(encoded),
	); err != nil {
		return nil, err
	}

	return map[string]any{
		"client_id":                  clientID,
		"client_id_issued_at":        time.Now().Unix(),
		"client_name":                name,
		"redirect_uris":              uris,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}, nil
}

// GetClient resolves a registered client id, or a Client ID Metadata Document
// when the id is an https URL. An unknown client is (nil, nil).
func (s *Service) GetClient(clientID string) (*Client, error) {
	if clientID == "" {
		return nil, nil
	}
	if strings.HasPrefix(clientID, "https://") {
		return s.fetchMetadataDocument(clientID), nil
	}

	var (
		c     Client
		uris  string
		name  string
		query = "SELECT client_id, client_name, redirect_uris FROM oauth_clients WHERE client_id = ?"
	)
	err := s.DB.QueryRow(query, clientID).Scan(&c.ClientID, &name, &uris)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.ClientName = name
	if err := json.Unmarshal([]byte(uris), &c.RedirectURIs); err != nil {
		return nil, err
	}
	return &c, nil
}

// fetchMetadataDocument returns the cached document for clientID, fetching it
// on a miss. Negative answers are cached for CIMDTTL too, so a dead metadata
// URL is not retried on every token request.
func (s *Service) fetchMetadataDocument(clientID string) *Client {
	s.cimdMu.Lock()
	hit, ok := s.cimd[clientID]
	s.cimdMu.Unlock()
	if ok && time.Since(hit.at) < CIMDTTL*time.Second {
		return hit.client
	}

	client, _ := s.fetchCIMD(clientID) // failures are negative cache entries

	s.cimdMu.Lock()
	s.cimd[clientID] = cimdEntry{at: time.Now(), client: client}
	s.cimdMu.Unlock()
	return client
}

func (s *Service) fetchCIMD(clientID string) (*Client, error) {
	u, err := url.Parse(clientID)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return nil, nil
	}
	port := 443
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	// The daemon fetches whatever URL a stranger names, so it must never be a
	// way to reach the daemon itself, the LAN or a cloud metadata address.
	if !s.allowCIMDHost(u.Hostname(), port) {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return nil, nil
	}
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, nil
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, CIMDMaxBytes+1))
	if err != nil || resp.StatusCode != http.StatusOK || len(raw) > CIMDMaxBytes {
		return nil, nil
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil
	}
	if id, _ := doc["client_id"].(string); id != clientID {
		return nil, nil
	}
	uris, ok := stringList(doc["redirect_uris"])
	if !ok || len(uris) == 0 {
		return nil, nil
	}
	for _, uri := range uris {
		if !ValidRedirectURI(uri) {
			return nil, nil
		}
	}
	name := stringValue(doc["client_name"])
	if name == "" {
		name = u.Hostname()
	}
	return &Client{
		ClientID:     clientID,
		ClientName:   truncateRunes(name, 100),
		RedirectURIs: uris,
	}, nil
}

func (s *Service) httpClient() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (s *Service) allowCIMDHost(host string, port int) bool {
	if s.AllowCIMDHost != nil {
		return s.AllowCIMDHost(host, port)
	}
	return isPublicHost(host)
}

// isPublicHost is web_service.is_public: every address the host resolves to
// must be a global internet address.
func isPublicHost(host string) bool {
	addrs, err := net.LookupIP(host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, ip := range addrs {
		if !isGlobalIP(ip) {
			return false
		}
	}
	return true
}

// reservedNets are IPv4 blocks Python's ipaddress marks as non-global but that
// Go's IsPrivate does not cover (shared address space, documentation, benchmark
// and reserved ranges).
var reservedNets = func() []*net.IPNet {
	cidrs := []string{
		"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "255.255.255.255/32",
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

func isGlobalIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		for _, n := range reservedNets {
			if n.Contains(ip4) {
				return false
			}
		}
		return true
	}
	// IPv6 documentation and 6to4 relay anycast are not global either.
	for _, c := range []string{"2001:db8::/32", "2002::/16"} {
		if _, n, err := net.ParseCIDR(c); err == nil && n.Contains(ip) {
			return false
		}
	}
	return true
}

// stringList converts a JSON array (or a []string) into a list of strings. It
// reports false when the value is not a list or any element is not a string.
func stringList(v any) ([]string, bool) {
	switch list := v.(type) {
	case []string:
		return list, true
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}

// stringValue renders a JSON scalar the way str(value or "") would: nil and
// the zero values become "", strings pass through, everything else is printed.
func stringValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if !t {
			return ""
		}
		return "true"
	case float64:
		if t == 0 {
			return ""
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
