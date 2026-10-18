package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"gospark-hub/internal/store"
)

// Web constants mirror daemon/services/web_service.py.
const (
	searxngSlug    = "searxng"
	webMaxResults  = 20
	webDefaultRes  = 8
	webMaxPageSize = 5 * 1024 * 1024
	webMaxChars    = 80000
	webMaxRedirect = 5
)

const webUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

// search is upstream web_service.search.
func (s *Server) search(ctx context.Context, query string, maxResults *int) string {
	count := webDefaultRes
	if maxResults != nil {
		count = *maxResults
	}
	if count < 1 {
		count = 1
	}
	if count > webMaxResults {
		count = webMaxResults
	}
	form := url.Values{"q": {query}, "format": {"json"}}
	req, err := s.newAppRequest(ctx, http.MethodPost,
		s.app.InternalURL(searxngSlug)+"/search", strings.NewReader(form.Encode()))
	if err != nil {
		panic(webErrorf("Web search is unavailable: %v. If SearXNG is stopped, start it "+
			"with start_model (model \"searxng\"), then search again.", err))
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.http.Do(req)
	if err != nil {
		panic(webErrorf("Web search is unavailable: %v. If SearXNG is stopped, start it "+
			"with start_model (model \"searxng\"), then search again.", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		panic(webErrorf("Web search is unavailable: SearXNG is not running on the Spark "+
			"(HTTP %d). Start it with start_model (model \"searxng\"), then search again.", resp.StatusCode))
	}
	var data map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&data); err != nil {
		panic(webErrorf("SearXNG returned an unreadable answer (%v).", err))
	}
	return formatResults(data, count)
}

// formatResults is upstream web_service.format_results.
func formatResults(data map[string]any, count int) string {
	lines := []string{}
	for _, boxAny := range asList(data["infoboxes"]) {
		box, _ := boxAny.(map[string]any)
		text := strings.TrimSpace(str(box["content"]))
		link := ""
		for _, uAny := range asList(box["urls"]) {
			u, _ := uAny.(map[string]any)
			if l := str(u["url"]); l != "" {
				link = l
				break
			}
		}
		if text != "" {
			name := str(box["infobox"])
			if name == "" {
				name = "Summary"
			}
			line := name + ": " + text
			if link != "" {
				line += " (" + link + ")"
			}
			lines = append(lines, line, "")
		}
	}
	results := asList(data["results"])
	for i, itemAny := range results {
		if i >= count {
			break
		}
		item, _ := itemAny.(map[string]any)
		title := str(item["title"])
		link := str(item["url"])
		if title == "" {
			title = link
		}
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, title))
		lines = append(lines, "   "+link)
		if snippet := strings.TrimSpace(str(item["content"])); snippet != "" {
			lines = append(lines, "   "+snippet)
		}
		if date := str(item["publishedDate"]); date != "" {
			lines = append(lines, "   Published: "+truncate(date, 10))
		}
	}
	if len(lines) == 0 {
		var failed []string
		for _, pairAny := range asList(data["unresponsive_engines"]) {
			pair, _ := pairAny.([]any)
			if len(pair) == 2 {
				failed = append(failed, fmt.Sprintf("%v (%v)", pair[0], pair[1]))
			}
		}
		msg := "No results."
		if len(failed) > 0 {
			msg += " Engines that did not answer: " + strings.Join(failed, ", ") +
				". Try again in a few minutes, or rephrase."
		} else {
			msg += " Try a different query."
		}
		return msg
	}
	lines = append(lines, "")
	lines = append(lines, "Read a result in full with web_fetch. Cite the URLs you use.")
	return strings.Join(lines, "\n")
}

// fetch is upstream web_service.fetch: HTTPS only, public addresses, HTML to
// markdown, capped.
func (s *Server) fetch(ctx context.Context, rawURL string, user *store.User) (string, error) {
	current := rawURL
	for redirect := 0; redirect <= webMaxRedirect; redirect++ {
		parts, err := url.Parse(strings.TrimSpace(current))
		if err != nil || (parts.Scheme != "http" && parts.Scheme != "https") || parts.Hostname() == "" {
			return "", webErrorf("Only https:// URLs can be fetched (got %q).", current)
		}
		port := 80
		if parts.Scheme == "https" {
			port = 443
		}
		if parts.Port() != "" {
			fmt.Sscanf(parts.Port(), "%d", &port)
		}
		where := reach(parts.Hostname(), port, user)
		if where == "" {
			return "", webErrorf("%s is not a public address; only public sites can be fetched.", parts.Hostname())
		}
		if parts.Scheme == "http" && where == "public" {
			return "", webErrorf("Only https:// URLs can be fetched (got %q). Plain http is "+
				"refused so nothing travels unencrypted.", current)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return "", webErrorf("Fetching %s failed: %v", current, err)
		}
		req.Header.Set("User-Agent", webUserAgent)
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.5")
		resp, err := s.http.Do(req)
		if err != nil {
			return "", webErrorf("Fetching %s failed: %v", current, err)
		}
		if loc := resp.Header.Get("Location"); isRedirect(resp.StatusCode) && loc != "" {
			next, err := url.Parse(loc)
			if err == nil {
				if !next.IsAbs() {
					base, _ := url.Parse(current)
					next = base.ResolveReference(next)
				}
				current = next.String()
				resp.Body.Close()
				continue
			}
		}
		contentType := strings.ToLower(resp.Header.Get("Content-Type"))
		body, _ := readCapped(resp.Body, webMaxPageSize)
		status := resp.StatusCode
		resp.Body.Close()

		tooBig := len(body) > webMaxPageSize
		text := string(body)
		if strings.Contains(contentType, "html") {
			text = htmlToMarkdown(text, current)
		} else if !strings.HasPrefix(contentType, "text/") &&
			!strings.Contains(contentType, "json") && !strings.Contains(contentType, "xml") {
			return "", webErrorf("%s is %s, not a web page or text; web_fetch reads HTML and text only.",
				current, orUnknown(contentType))
		}
		cut := tooBig || len([]rune(text)) > webMaxChars
		out := fmt.Sprintf("Fetched %s (HTTP %d)\n\n%s", current, status, truncateRunes(text, webMaxChars))
		if cut {
			out += "\n\n(Content truncated. Fetch a more specific page for the rest.)"
		}
		return out, nil
	}
	return "", webErrorf("Too many redirects fetching %s.", rawURL)
}

func isRedirect(status int) bool {
	switch status {
	case 301, 302, 303, 307, 308:
		return true
	}
	return false
}

func orUnknown(s string) string {
	if s == "" {
		return "an unknown type"
	}
	return s
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// dropTagRE removes the content of page furniture that is never the article.
// Go's regexp has no backreferences, so the closing tag is spelled out.
var dropTagRE = regexp.MustCompile(`(?is)<(script|style|noscript|svg|nav|header|footer|aside|form|iframe|button)\b[^>]*>.*?</(script|style|noscript|svg|nav|header|footer|aside|form|iframe|button)\s*>`)

func init() {
	// Guard against a typo silently registering a regexp that never matches.
	if dropTagRE == nil {
		panic("mcp: invalid dropTagRE")
	}
}

var tagRE = regexp.MustCompile(`(?s)<[^>]*>|[^<]+`)
var titleRE = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
var hrefRE = regexp.MustCompile(`(?is)href\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))`)
var headingRE = regexp.MustCompile(`(?i)^h([1-6])$`)

// htmlToMarkdown is a small upstream-equivalent converter: it drops the page
// furniture, keeps headings, links, paragraphs and lists, and makes links
// absolute so the agent can web_fetch one as it stands.
func htmlToMarkdown(page, baseURL string) string {
	title := ""
	if m := titleRE.FindStringSubmatch(page); m != nil {
		title = strings.TrimSpace(html.UnescapeString(m[1]))
	}
	cleaned := dropTagRE.ReplaceAllString(page, "")

	var out strings.Builder
	linkHref := ""
	linkText := ""
	inLink := false
	appendText := func(s string) {
		if inLink {
			linkText += s
			return
		}
		out.WriteString(s)
	}
	for _, token := range tagRE.FindAllString(cleaned, -1) {
		if strings.HasPrefix(token, "<") {
			name, attrs := parseTag(token)
			switch {
			case name == "a":
				if href := hrefRE.FindStringSubmatch(attrs); href != nil {
					linkHref = firstNonEmpty(href[2], href[3], href[4])
					inLink = true
					linkText = ""
				}
			case name == "/a":
				if inLink {
					abs := absoluteURL(baseURL, linkHref)
					out.WriteString("[" + strings.TrimSpace(linkText) + "](" + abs + ")")
					inLink, linkHref, linkText = false, "", ""
				}
			case headingRE.MatchString(name):
				level := len(name) - 1
				out.WriteString("\n\n" + strings.Repeat("#", level) + " ")
			case name == "p" || name == "div" || name == "br" || name == "/p" ||
				name == "/div" || name == "li" || name == "/li" || name == "tr":
				out.WriteString("\n")
			case name == "li":
				out.WriteString("\n")
			}
			continue
		}
		appendText(html.UnescapeString(token))
	}
	if inLink {
		out.WriteString(strings.TrimSpace(linkText))
	}
	text := collapseBlankLines(out.String())
	text = strings.TrimSpace(text)
	if title != "" && !strings.HasPrefix(text, "# ") {
		text = "# " + title + "\n\n" + text
	}
	return text
}

func parseTag(token string) (string, string) {
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(token, "<"), ">"))
	inner = strings.TrimSuffix(inner, "/")
	fields := strings.Fields(inner)
	if len(fields) == 0 {
		return "", ""
	}
	name := strings.ToLower(fields[0])
	attrs := ""
	if idx := strings.Index(inner, " "); idx >= 0 {
		attrs = inner[idx+1:]
	}
	return name, attrs
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func absoluteURL(base, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return base
	}
	parsed, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	if parsed.IsAbs() {
		return ref
	}
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	return b.ResolveReference(parsed).String()
}

var blankLineRE = regexp.MustCompile(`\n{3,}`)

func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return blankLineRE.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
