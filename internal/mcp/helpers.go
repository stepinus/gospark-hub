package mcp

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
)

// allRecipes returns the catalog in deterministic order.
func (s *Server) allRecipes() ([]registry.Recipe, error) {
	if s.reg == nil {
		return nil, nil
	}
	return s.reg.All()
}

// installedSet is the set of installed recipe slugs.
func (s *Server) installedSet(ctx context.Context) (map[string]bool, error) {
	return s.rt.Installed(ctx)
}

// pickReady returns the index of the first candidate that is running and ready;
// else the upstream error that tells the agent what to start.
func (s *Server) pickReady(ctx context.Context, slugs []string, what string) (int, error) {
	var starting []string
	for i, slug := range slugs {
		running, err := s.rt.Running(ctx, slug)
		if err != nil {
			return 0, err
		}
		if running {
			if s.rt.Ready(slug) {
				return i, nil
			}
			starting = append(starting, slug)
		}
	}
	if len(starting) > 0 {
		return 0, imageErrorf("%s is still starting (loading weights). Call start_model "+
			"with model %s to wait until it is ready.", starting[0], starting[0])
	}
	if len(slugs) == 0 {
		return 0, imageErrorf("No %s is installed on the Spark. Install one from the Spark AI Hub first.", what)
	}
	return 0, imageErrorf("No %s is running on the Spark. Start %s with start_model first.",
		what, strings.Join(slugs, " or "))
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

func contains(list []string, want string) bool { return hasTag(list, want) }

// --- arg helpers ------------------------------------------------------------

func argString(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func argStringList(args map[string]any, key string) []string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	if s, ok := raw.(string); ok {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		return []string{strings.TrimSpace(s)}
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range list {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

// argInt mirrors upstream _int: empty/absent is nil, a non-integer is an error.
func argInt(args map[string]any, key string) *int {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		n, err := parseInt(v)
		if err != nil {
			panic(imageErrorf("'%s' must be an integer.", key))
		}
		return &n
	case float64:
		n := int(v)
		return &n
	case int:
		return &v
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			panic(imageErrorf("'%s' must be an integer.", key))
		}
		i := int(n)
		return &i
	}
	panic(imageErrorf("'%s' must be an integer.", key))
}

func parseInt(s string) (int, error) {
	n := 0
	neg := false
	for i, c := range s {
		if i == 0 && c == '-' {
			neg = true
			continue
		}
		if c < '0' || c > '9' {
			return 0, errNotInt
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		n = -n
	}
	return n, nil
}

var errNotInt = errStringType("not an integer")

type errStringType string

func (e errStringType) Error() string { return string(e) }

func randomSeed() int {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	return int(binary.BigEndian.Uint32(b[:]) & 0x7fffffff)
}

func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func userIDOf(user *store.User) int64 {
	if user == nil {
		return 0
	}
	return user.ID
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// readJSONBody reads a small JSON body.
func readJSONBody(r io.Reader, v any) error {
	body, err := io.ReadAll(io.LimitReader(r, 8<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

// doJSON posts a JSON body and decodes the response, returning the raw body too.
func (s *Server) doJSON(req *http.Request, out any) (int, []byte, error) {
	resp, err := s.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, body, err
	}
	if out != nil && len(body) > 0 {
		_ = json.Unmarshal(body, out)
	}
	return resp.StatusCode, body, nil
}

// newAppRequest builds a request to an app with the probe headers.
func (s *Server) newAppRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	for k, v := range s.app.ProbeHeaders() {
		req.Header.Set(k, v)
	}
	return req, nil
}
