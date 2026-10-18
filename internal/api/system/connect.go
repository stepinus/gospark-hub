package system

import (
	"gospark-hub/internal/auth"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gospark-hub/internal/sah"
)

// connectInfo is the JSON returned by GET /api/system/connect.
type connectInfo struct {
	Hostname       string             `json:"hostname"`
	Port           int                `json:"port"`
	Origin         string             `json:"origin"`
	ExternalOrigin bool               `json:"external_origin"`
	Primary        string             `json:"primary"`
	Candidates     []connectCandidate `json:"candidates"`
	ClientIP       string             `json:"client_ip"`
	ClientLocal    bool               `json:"client_local"`
	LocalURL       string             `json:"local_url"`
	LocalURLs      []string           `json:"local_urls"`
	Agents         []sah.Agent        `json:"agents"`
	Commands       connectCommands    `json:"commands"`
}

type connectCandidate struct {
	Host        string `json:"host"`
	URL         string `json:"url"`
	Kind        string `json:"kind"`
	Scope       string `json:"scope"`
	Note        string `json:"note"`
	Recommended bool   `json:"recommended"`
}

type connectCommands struct {
	Install string  `json:"install"`
	SetHub  string  `json:"set_hub"`
	SetKey  *string `json:"set_key"`
}

var safeOrigin = regexp.MustCompile(`^https?://(?:[A-Za-z0-9._\-]+|\[[0-9A-Fa-f:.]+\])(?::[0-9]{1,5})?$`)

var scopeByKind = map[string]string{
	"mdns":      "lan",
	"ip":        "lan",
	"tailscale": "vpn",
	"origin":    "remote",
}

func (s *server) handleConnect(w http.ResponseWriter, r *http.Request) {
	apiKey := apiKeyFromRequest(r)
	info := computeConnectInfo(
		s.deps.Config.PublicPort,
		apiKey,
		requestOrigin(r),
		clientIP(r),
		s.agents(),
	)
	writeJSON(w, http.StatusOK, info)
}

// agents reads the integration registry out of the shipped sah CLI, the same
// source upstream uses, so this page can never list a client sah lacks.
func (s *server) agents() []sah.Agent {
	path := filepath.Join(s.deps.Config.BaseDir, "sah", "sah")
	list := sah.Load(path)
	if list == nil {
		return []sah.Agent{}
	}
	return list
}

func computeConnectInfo(port int, apiKey, origin, callerIP string, agents []sah.Agent) connectInfo {
	hostname := shortHostname()
	candidates := []connectCandidate{}

	add := func(host, kind, note string, recommended bool) {
		if host == "" {
			return
		}
		url := fmt.Sprintf("http://%s:%d", host, port)
		for _, c := range candidates {
			if c.URL == url {
				return
			}
		}
		candidates = append(candidates, connectCandidate{
			Host:        host,
			URL:         url,
			Kind:        kind,
			Scope:       scopeByKind[kind],
			Note:        note,
			Recommended: recommended,
		})
	}

	add(fmt.Sprintf("%s.local", hostname), "mdns",
		"Stable on your LAN — survives IP changes, works offline.", true)

	if ts := tailscaleInfo(); ts != nil && ts.DNS != "" {
		add(ts.DNS, "tailscale",
			"Works on your LAN and remotely (requires Tailscale on the client).", false)
	}

	if ip := primaryLANIP(); ip != "" {
		add(ip, "ip",
			"Reachable now, but this address changes when the server moves networks.", false)
	}

	origin = strings.TrimRight(origin, "/")
	external := origin != "" && !containsURL(candidates, origin)
	if external {
		for i := range candidates {
			candidates[i].Recommended = false
		}
		candidates = append([]connectCandidate{{
			Host:        strings.TrimPrefix(origin, requestSchemeFromOrigin(origin)+"://"),
			URL:         origin,
			Kind:        "origin",
			Scope:       "remote",
			Note:        "The address you are using right now — works wherever this page loaded from.",
			Recommended: true,
		}}, candidates...)
	}

	primary := fmt.Sprintf("http://localhost:%d", port)
	if len(candidates) > 0 {
		primary = candidates[0].URL
	}
	installFrom := origin
	if installFrom == "" {
		installFrom = primary
	}

	local := []connectCandidate{}
	for _, c := range candidates {
		if c.Scope == "lan" {
			local = append(local, c)
		}
	}
	localURL := ""
	localURLs := []string{}
	for _, c := range local {
		localURLs = append(localURLs, c.URL)
		if localURL == "" && c.Kind == "ip" {
			localURL = c.URL
		}
	}

	install := fmt.Sprintf("curl -fsSL %s/sah/install.sh | sh", installFrom)
	if apiKey != "" {
		install = fmt.Sprintf("curl -fsSL %s/sah/install.sh | sh -s -- --key %s", installFrom, apiKey)
	}
	setKey := (*string)(nil)
	if apiKey != "" {
		// The page shows a ready-to-paste line, not a bare key.
		line := fmt.Sprintf("sah set-key %s", apiKey)
		setKey = &line
	}

	return connectInfo{
		Hostname:       hostname,
		Port:           port,
		Origin:         origin,
		ExternalOrigin: external,
		Primary:        primary,
		Candidates:     candidates,
		ClientIP:       callerIP,
		ClientLocal:    isLocalClient(callerIP),
		LocalURL:       localURL,
		LocalURLs:      localURLs,
		Agents:         agents,
		Commands: connectCommands{
			Install: install,
			SetHub:  fmt.Sprintf("sah set-hub %s", primary),
			SetKey:  setKey,
		},
	}
}

func containsURL(candidates []connectCandidate, url string) bool {
	for _, c := range candidates {
		if c.URL == url {
			return true
		}
	}
	return false
}

func requestSchemeFromOrigin(origin string) string {
	if strings.HasPrefix(origin, "https://") {
		return "https"
	}
	return "http"
}

func apiKeyFromRequest(r *http.Request) string {
	// The page asks about the account it is signed in as; that account's own
	// key is the one the install line must carry.
	if u := auth.CurrentUser(r); u != nil && u.APIKey != "" {
		return u.APIKey
	}
	if v := r.Header.Get("X-Api-Key"); v != "" {
		return v
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}

func clientIP(r *http.Request) string {
	for _, h := range []string{"cf-connecting-ip", "x-forwarded-for", "x-real-ip"} {
		if v := r.Header.Get(h); v != "" {
			v = strings.TrimSpace(strings.Split(v, ",")[0])
			if v != "" {
				return v
			}
		}
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

func requestOrigin(r *http.Request) string {
	scheme := requestScheme(r)
	host := forwardedHost(r)
	if host == "" {
		host = r.Host
	}
	if host == "" {
		return ""
	}
	origin := fmt.Sprintf("%s://%s", scheme, host)
	if !safeOrigin.MatchString(origin) {
		return ""
	}
	return origin
}

func requestScheme(r *http.Request) string {
	fwd := parseForwarded(r.Header.Get("forwarded"))
	if proto := fwd["proto"]; proto != "" {
		return proto
	}
	if proto := r.Header.Get("x-forwarded-proto"); proto != "" {
		proto = strings.ToLower(strings.TrimSpace(strings.Split(proto, ",")[0]))
		if proto == "http" || proto == "https" {
			return proto
		}
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func forwardedHost(r *http.Request) string {
	fwd := parseForwarded(r.Header.Get("forwarded"))
	if h := fwd["host"]; h != "" {
		return h
	}
	if h := r.Header.Get("x-forwarded-host"); h != "" {
		first, _, _ := strings.Cut(h, ",")
		return strings.TrimSpace(first)
	}
	return ""
}

func parseForwarded(header string) map[string]string {
	out := make(map[string]string)
	if header == "" {
		return out
	}
	first, _, _ := strings.Cut(header, ",")
	for part := range strings.SplitSeq(first, ";") {
		key, val, _ := strings.Cut(strings.TrimSpace(part), "=")
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.Trim(strings.TrimSpace(val), `"`)
		if key != "" && val != "" {
			out[key] = val
		}
	}
	return out
}

func shortHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "spark-ai-hub"
	}
	name := strings.Split(h, ".")[0]
	name = strings.TrimSpace(name)
	if name == "" {
		return "spark-ai-hub"
	}
	return name
}

func primaryLANIP() string {
	conn, err := net.DialTimeout("udp", "8.8.8.8:80", 2*time.Second)
	if err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && !addr.IP.IsLoopback() {
			return addr.IP.String()
		}
	}
	ips, err := net.LookupIP(shortHostname())
	if err == nil {
		for _, ip := range ips {
			if ipv4 := ip.To4(); ipv4 != nil && !ipv4.IsLoopback() {
				return ipv4.String()
			}
		}
	}
	return ""
}

func localNetworks() []*net.IPNet {
	out := []*net.IPNet{}
	cmd := exec.Command("ip", "-o", "-4", "addr", "show")
	b, err := cmd.Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "inet" && i+1 < len(fields) {
				_, ipnet, err := net.ParseCIDR(fields[i+1])
				if err == nil && ipnet != nil && !ipnet.IP.IsLoopback() {
					if ipv4 := ipnet.IP.To4(); ipv4 != nil {
						ipnet.IP = ipv4
						out = append(out, ipnet)
					}
				}
			}
		}
	}
	return out
}

func isLocalClient(ip string) bool {
	if ip == "" {
		return false
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	if parsed.IsLoopback() {
		return true
	}
	if parsed.To4() == nil {
		return false
	}
	for _, n := range localNetworks() {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

type tsInfo struct {
	DNS    string
	IPs    []string
	Online bool
}

func tailscaleInfo() *tsInfo {
	cmd := exec.Command("tailscale", "status", "--json")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseTailscaleStatus(out)
}

func parseTailscaleStatus(data []byte) *tsInfo {
	var payload struct {
		Self struct {
			DNSName      string   `json:"DNSName"`
			TailscaleIPs []string `json:"TailscaleIPs"`
			Online       bool     `json:"Online"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil
	}
	dns := strings.TrimSuffix(payload.Self.DNSName, ".")
	if dns == "" && len(payload.Self.TailscaleIPs) == 0 {
		return nil
	}
	return &tsInfo{
		DNS:    dns,
		IPs:    payload.Self.TailscaleIPs,
		Online: payload.Self.Online,
	}
}
