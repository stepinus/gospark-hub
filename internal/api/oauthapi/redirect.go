package oauthapi

import (
	"net/http"
	"net/url"
	"strings"
)

// param is one query parameter added to a redirect. A nil value means "absent",
// which is how upstream keeps `state` out of the response when the client sent
// none.
type param struct {
	key   string
	value *string
}

// redirectWith appends params to a redirect_uri, keeping any query the client
// already registered. It mirrors urllib's parse_qsl/urlencode round trip:
// existing pairs stay in order, appended pairs follow, and a nil value is
// skipped.
func redirectWith(redirectURI string, params []param) string {
	base := redirectURI
	fragment := ""
	if i := strings.IndexByte(base, '#'); i >= 0 {
		fragment = base[i:]
		base = base[:i]
	}
	query := ""
	if i := strings.IndexByte(base, '?'); i >= 0 {
		query = base[i+1:]
		base = base[:i]
	}

	pairs := parseQueryOrdered(query)
	for _, p := range params {
		if p.value != nil {
			pairs = append(pairs, [2]string{p.key, *p.value})
		}
	}

	out := base
	if len(pairs) > 0 {
		out += "?" + encodeQuery(pairs)
	}
	return out + fragment
}

// parseQueryOrdered splits a raw query string into decoded pairs, preserving
// order and blank values.
func parseQueryOrdered(raw string) [][2]string {
	if raw == "" {
		return nil
	}
	var out [][2]string
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		key, value, _ := strings.Cut(part, "=")
		k, err := url.QueryUnescape(key)
		if err != nil {
			k = key
		}
		v, err := url.QueryUnescape(value)
		if err != nil {
			v = value
		}
		out = append(out, [2]string{k, v})
	}
	return out
}

func encodeQuery(pairs [][2]string) string {
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(p[0]))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(p[1]))
	}
	return b.String()
}

// redirectTo writes a body-less redirect, matching Starlette's RedirectResponse
// (empty body, explicit Location).
func redirectTo(w http.ResponseWriter, status int, location string) {
	w.Header().Set("Location", location)
	w.WriteHeader(status)
}
