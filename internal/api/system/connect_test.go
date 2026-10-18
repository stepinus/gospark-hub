package system

import (
	"crypto/tls"
	"net/http"
	"strings"
	"testing"
)

func TestParseTailscaleStatus(t *testing.T) {
	const fixture = `{
  "Self": {
    "DNSName": "spark-hub.tail1234.ts.net.",
    "TailscaleIPs": ["100.64.0.1"],
    "Online": true
  }
}`
	ts := parseTailscaleStatus([]byte(fixture))
	if ts == nil {
		t.Fatal("expected parsed tailscale info")
	}
	if ts.DNS != "spark-hub.tail1234.ts.net" {
		t.Fatalf("dns: got %q want %q", ts.DNS, "spark-hub.tail1234.ts.net")
	}
	if len(ts.IPs) != 1 || ts.IPs[0] != "100.64.0.1" {
		t.Fatalf("ips: got %v", ts.IPs)
	}
	if !ts.Online {
		t.Fatal("expected online")
	}
}

func TestParseTailscaleStatusInvalid(t *testing.T) {
	if ts := parseTailscaleStatus([]byte("not json")); ts != nil {
		t.Fatal("expected nil for invalid json")
	}
	if ts := parseTailscaleStatus([]byte(`{"Self":{}}`)); ts != nil {
		t.Fatal("expected nil when no dns/ips")
	}
}

func TestParseForwarded(t *testing.T) {
	cases := []struct {
		in   string
		want map[string]string
	}{
		{
			in:   `for=192.0.2.1;host="example.com";proto=https`,
			want: map[string]string{"for": "192.0.2.1", "host": "example.com", "proto": "https"},
		},
		{
			in:   `host=foo, for=1.2.3.4`,
			want: map[string]string{"host": "foo"},
		},
	}
	for _, tc := range cases {
		got := parseForwarded(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("parseForwarded(%q) = %v, want %v", tc.in, got, tc.want)
		}
		for k, v := range tc.want {
			if got[k] != v {
				t.Fatalf("parseForwarded(%q)[%q] = %q, want %q", tc.in, k, got[k], v)
			}
		}
	}
}

func TestRequestOrigin(t *testing.T) {
	cases := []struct {
		name   string
		header http.Header
		host   string
		tls    bool
		want   string
	}{
		{
			name:   "plain host",
			header: http.Header{},
			host:   "hub.local:9100",
			want:   "http://hub.local:9100",
		},
		{
			name: "forwarded host and proto",
			header: http.Header{
				"Forwarded": []string{`host="example.com";proto=https`},
			},
			host: "hub.local:9100",
			want: "https://example.com",
		},
		{
			name: "x-forwarded headers",
			header: http.Header{
				"X-Forwarded-Host":  []string{"tunnel.example.com"},
				"X-Forwarded-Proto": []string{"https"},
			},
			host: "10.0.0.1:9100",
			want: "https://tunnel.example.com",
		},
		{
			name:   "tls without forwarded",
			header: http.Header{},
			host:   "hub.local",
			tls:    true,
			want:   "https://hub.local",
		},
		{
			name:   "forged host rejected",
			header: http.Header{},
			host:   "evil<host",
			want:   "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &http.Request{
				Host:   tc.host,
				Header: tc.header,
			}
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			got := requestOrigin(req)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestComputeConnectInfoExternalOrigin(t *testing.T) {
	info := computeConnectInfo(9100, "sah-test-key", "https://tunnel.example.com", "203.0.113.4", nil)
	if !info.ExternalOrigin {
		t.Fatal("expected external_origin true")
	}
	if !strings.HasPrefix(info.Primary, "https://") {
		t.Fatalf("expected primary to keep https origin, got %q", info.Primary)
	}
	if !strings.Contains(info.Commands.Install, "--key sah-test-key") {
		t.Fatalf("install command should include api key: %q", info.Commands.Install)
	}
	if info.Commands.SetKey == nil || *info.Commands.SetKey != "sah set-key sah-test-key" {
		t.Fatal("set_key should contain api key")
	}
	foundOrigin := false
	for _, c := range info.Candidates {
		if c.Kind == "origin" && c.Recommended {
			foundOrigin = true
		}
	}
	if !foundOrigin {
		t.Fatalf("expected an origin candidate recommended: %+v", info.Candidates)
	}
}

func TestClientIP(t *testing.T) {
	req := &http.Request{
		RemoteAddr: "10.0.0.1:1234",
		Header:     http.Header{"X-Forwarded-For": []string{"203.0.113.1, 192.0.2.1"}},
	}
	if got := clientIP(req); got != "203.0.113.1" {
		t.Fatalf("clientIP = %q, want 203.0.113.1", got)
	}
}
