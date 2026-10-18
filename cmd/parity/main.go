// parity compares a live upstream Spark AI Hub (Python) against sahd (Go) and
// reports per-endpoint differences. It is the acceptance gate for the port:
// same paths, same JSON keys, same types, same values where values are stable.
//
//	go run ./cmd/parity -upstream http://127.0.0.1:9020 -ours http://127.0.0.1:9120
//
// Both sides need an account; the first registered account becomes admin, so on
// a fresh database the harness registers one and logs in. Volatile fields
// (timestamps, ids, keys, token counts) are compared by shape only.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

type caseSpec struct {
	name   string
	method string
	path   string
	body   string
	// shape compares structure only: key sets and types, not values.
	shape bool
}

var cases = []caseSpec{
	{name: "state", method: "GET", path: "/api/auth/state"},
	{name: "me", method: "GET", path: "/api/auth/me"},
	{name: "my-usage", method: "GET", path: "/api/auth/me/usage"},
	{name: "users", method: "GET", path: "/api/admin/users"},
	{name: "recipes", method: "GET", path: "/api/recipes"},
	{name: "recipe-web", method: "GET", path: "/api/recipes/comfyui-spark"},
	{name: "recipe-api", method: "GET", path: "/api/recipes/ollama-openwebui"},
	{name: "recipe-unknown", method: "GET", path: "/api/recipes/does-not-exist"},
	{name: "launch-plan", method: "GET", path: "/api/recipes/comfyui-spark/launch-plan"},
	{name: "compose", method: "GET", path: "/api/recipes/comfyui-spark/compose"},
	{name: "build-status", method: "GET", path: "/api/recipes/comfyui-spark/build-status"},
	{name: "containers", method: "GET", path: "/api/containers"},
	{name: "connect", method: "GET", path: "/api/system/connect"},
	{name: "metrics", method: "GET", path: "/api/system/metrics", shape: true},
	{name: "hf-token", method: "GET", path: "/api/system/hf-token"},
	{name: "hf-access", method: "GET", path: "/api/system/hf-access/comfyui-spark", shape: true},
	{name: "media", method: "GET", path: "/api/media"},
	{name: "sah-version", method: "GET", path: "/sah/version"},
	{name: "spa-index", method: "GET", path: "/"},
}

type side struct {
	name   string
	base   string
	client *http.Client
}

func newSide(name, base string) *side {
	jar, _ := cookiejar.New(nil)
	return &side{name: name, base: strings.TrimRight(base, "/"), client: &http.Client{
		Jar:     jar,
		Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

func (s *side) do(method, path, body string) (int, []byte, http.Header, error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, s.base+path, rd)
	if err != nil {
		return 0, nil, nil, err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	return resp.StatusCode, b, resp.Header, err
}

// login registers the first account on a fresh Hub, otherwise signs in.
func (s *side) login(email, password string) error {
	payload, _ := json.Marshal(map[string]string{
		"email": email, "password": password, "name": "Parity",
	})
	code, _, _, err := s.do("POST", "/api/auth/register", string(payload))
	if err != nil {
		return fmt.Errorf("%s: register: %w", s.name, err)
	}
	if code == http.StatusOK || code == http.StatusCreated {
		return nil
	}
	code, body, _, err := s.do("POST", "/api/auth/login",
		fmt.Sprintf(`{"email":%q,"password":%q}`, email, password))
	if err != nil {
		return fmt.Errorf("%s: login: %w", s.name, err)
	}
	if code != http.StatusOK {
		return fmt.Errorf("%s: login %d: %s", s.name, code, truncate(body, 200))
	}
	return nil
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

var volatileKeys = map[string]bool{
	"id": true, "created_at": true, "updated_at": true, "reviewed_at": true,
	"last_login_at": true, "last_used_at": true, "expires_at": true, "ts": true,
	"api_key": true, "token": true, "access_token": true, "session": true,
	"timestamp": true, "uptime": true, "uptime_seconds": true, "boot_time": true,
}

var portRe = regexp.MustCompile(`:\d{2,5}`)
var keyRe = regexp.MustCompile(`sah-[A-Za-z0-9_-]{8,}`)
var repoRe = regexp.MustCompile(`[^"\s]*/registry/`)

// values differ by design (9000 upstream vs 9100 ours), so the port is
// not part of the comparison.
func maskPort(s string) string { return portRe.ReplaceAllString(s, ":<port>") }

// maskValue hides the parts that legitimately differ between two instances:
// their own addresses, their own API keys and where the registry lives on disk.
func maskValue(s string) string {
	s = keyRe.ReplaceAllString(s, "sah-<key>")
	s = portRe.ReplaceAllString(s, ":<port>")
	return repoRe.ReplaceAllString(s, "<repo>/registry/")
}

// normalize replaces volatile values with a type marker so ids and clocks do
// not count as differences, while the shape still must match.
func normalize(v any, key string) any {
	if key == "port" {
		return "<port>"
	}
	if volatileKeys[key] {
		switch v.(type) {
		case nil:
			return nil
		case string:
			return "<string>"
		case float64:
			return "<number>"
		}
	}
	if s, ok := v.(string); ok {
		return maskValue(s)
	}
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalize(val, k)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = normalize(t[i], key)
		}
		return out
	}
	return v
}

// shapeOf keeps only the structure: maps become sorted key lists of shapes,
// scalars become their type name, list contents collapse to the shape of the
// first element (Hubs ship heterogeneous lists only for media).
func shapeOf(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]any, 0, len(keys)*2)
		for _, k := range keys {
			out = append(out, k, shapeOf(t[k]))
		}
		return out
	case []any:
		if len(t) == 0 {
			return []any{"<empty>"}
		}
		return []any{shapeOf(t[0])}
	case string:
		return "<string>"
	case float64:
		return "<number>"
	case bool:
		return "<bool>"
	case nil:
		return "<null>"
	}
	return fmt.Sprintf("<%T>", v)
}

// diff walks two normalized documents and collects up to max paths that differ.
func diff(a, b any, path string, out *[]string, max int) {
	if len(*out) >= max {
		return
	}
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			av, aHas := am[k]
			bv, bHas := bm[k]
			switch {
			case !aHas:
				*out = append(*out, fmt.Sprintf("%s.%s: только у нас", path, k))
			case !bHas:
				*out = append(*out, fmt.Sprintf("%s.%s: только у апстрима", path, k))
			default:
				diff(av, bv, path+"."+k, out, max)
			}
		}
		return
	}
	as, aok2 := a.([]any)
	bs, bok2 := b.([]any)
	if aok2 && bok2 {
		if len(as) != len(bs) {
			*out = append(*out, fmt.Sprintf("%s: длина %d у нас, %d у апстрима", path, len(as), len(bs)))
			return
		}
		for i := range as {
			diff(as[i], bs[i], fmt.Sprintf("%s[%d]", path, i), out, max)
		}
		return
	}
	if fmt.Sprintf("%v", a) != fmt.Sprintf("%v", b) {
		*out = append(*out, fmt.Sprintf("%s: %v ≠ %v", path, a, b))
	}
}

func main() {
	up := flag.String("upstream", "http://127.0.0.1:9020", "адрес апстрима")
	ours := flag.String("ours", "http://127.0.0.1:9120", "адрес нашего демона")
	email := flag.String("email", "parity@example.com", "учётка для входа")
	password := flag.String("password", "parity-password", "пароль")
	filter := flag.String("only", "", "подстрока пути: прогнать только совпавшие кейсы")
	maxDiff := flag.Int("max-diff", 6, "сколько расхождений печатать на кейс")
	flag.Parse()

	a := newSide("upstream", *up)
	b := newSide("sahd", *ours)
	for _, s := range []*side{a, b} {
		if err := s.login(*email, *password); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}

	failures := 0
	fmt.Printf("%-16s %-6s %s\n", "кейс", "итог", "детали")
	for _, c := range cases {
		if *filter != "" && !strings.Contains(c.path, *filter) {
			continue
		}
		ca, ba, ha, errA := a.do(c.method, c.path, c.body)
		cb, bb, hb, errB := b.do(c.method, c.path, c.body)
		if errA != nil || errB != nil {
			failures++
			fmt.Printf("%-16s DIFF   запрос: %v / %v\n", c.name, errA, errB)
			continue
		}
		if ca != cb {
			failures++
			fmt.Printf("%-16s DIFF   код %d у нас ≠ %d у апстрима (%s)\n", c.name, cb, ca, c.path)
			continue
		}
		ctA := ha.Get("Content-Type")
		ctB := hb.Get("Content-Type")
		if strings.HasPrefix(ctA, "application/json") != strings.HasPrefix(ctB, "application/json") {
			failures++
			fmt.Printf("%-16s DIFF   Content-Type %q ≠ %q\n", c.name, ctB, ctA)
			continue
		}
		if !strings.HasPrefix(ctA, "application/json") {
			// HTML/shell: compare only that both produced a body.
			if len(ba) == 0 && len(bb) != 0 || len(ba) != 0 && len(bb) == 0 {
				failures++
				fmt.Printf("%-16s DIFF   пустое тело с одной стороны\n", c.name)
			} else {
				fmt.Printf("%-16s ok     (не JSON, %d байт)\n", c.name, len(bb))
			}
			continue
		}
		var ja, jb any
		if err := json.Unmarshal(ba, &ja); err != nil {
			failures++
			fmt.Printf("%-16s DIFF   JSON апстрима: %v\n", c.name, err)
			continue
		}
		if err := json.Unmarshal(bb, &jb); err != nil {
			failures++
			fmt.Printf("%-16s DIFF   JSON наш: %v — %s\n", c.name, err, truncate(bb, 160))
			continue
		}
		if c.shape {
			sa, sb := shapeOf(ja), shapeOf(jb)
			var d []string
			diff(sa, sb, "$", &d, *maxDiff)
			if len(d) > 0 {
				failures++
				fmt.Printf("%-16s DIFF   структура\n", c.name)
				for _, line := range d {
					fmt.Println("                 ", line)
				}
			} else {
				fmt.Printf("%-16s ok     (структура)\n", c.name)
			}
			continue
		}
		var d []string
		diff(normalize(ja, ""), normalize(jb, ""), "$", &d, *maxDiff)
		if len(d) > 0 {
			failures++
			fmt.Printf("%-16s DIFF   %s\n", c.name, c.path)
			for _, line := range d {
				fmt.Println("                 ", line)
			}
		} else {
			fmt.Printf("%-16s ok\n", c.name)
		}
	}
	if failures > 0 {
		fmt.Printf("\nрасхождений: %d\n", failures)
		os.Exit(1)
	}
	fmt.Println("\nвсе кейсы совпали")
}
