package oauthapi

import (
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"

	"gospark-hub/internal/store"
)

// pageStyle is the inline stylesheet of the sign-in and consent pages. It lives
// in the page, not in the SPA, so the CSP can be `default-src 'none'`.
const pageStyle = `:root{color-scheme:dark}
body{margin:0;min-height:100vh;display:grid;place-items:center;background:#0b0d10;
color:#e6e8eb;font:15px/1.5 system-ui,-apple-system,Segoe UI,sans-serif;padding:16px}
.card{width:100%;max-width:420px;background:#14171c;border:1px solid #262b33;border-radius:14px;padding:28px}
h1{font-size:20px;margin:0 0 6px} p{margin:8px 0;color:#aab1bb} b{color:#e6e8eb}
.brand{font-size:12px;letter-spacing:.08em;text-transform:uppercase;color:#76b900;margin-bottom:14px}
label{display:block;font-size:13px;color:#aab1bb;margin:14px 0 4px}
input{width:100%;box-sizing:border-box;padding:10px 12px;border-radius:8px;border:1px solid #2f3540;
background:#0b0d10;color:#e6e8eb;font:inherit}
.row{display:flex;gap:10px;margin-top:22px}
button{flex:1;padding:11px;border-radius:8px;border:1px solid #2f3540;background:#1c2128;color:#e6e8eb;
font:600 14px system-ui,sans-serif;cursor:pointer}
button.primary{background:#76b900;border-color:#76b900;color:#0b0d10}
.err{background:#3a1717;border:1px solid #6b2a2a;color:#ffb4b4;border-radius:8px;padding:8px 12px;margin-top:12px}
ul{margin:10px 0;padding-left:20px;color:#aab1bb} code{color:#e6e8eb}`

// writePage renders an HTML page with the upstream security headers.
func writePage(w http.ResponseWriter, title, body string, status int) {
	doc := strings.Join([]string{
		`<!doctype html><html lang="en"><head><meta charset="utf-8">`,
		`<meta name="viewport" content="width=device-width,initial-scale=1">`,
		`<title>`, html.EscapeString(title), ` · Spark AI Hub</title>`,
		`<style>`, pageStyle, `</style></head><body><main class="card">`,
		`<div class="brand">Spark AI Hub</div>`, body, `</main></body></html>`,
	}, "")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, doc)
}

// writeErrorPage is the "Can't connect" page: registration, redirect and
// session problems that must never be turned into a redirect.
func writeErrorPage(w http.ResponseWriter, status int, message string) {
	writePage(w, "Can't connect",
		"<h1>Can't connect</h1><p>"+html.EscapeString(message)+"</p>", status)
}

// writeLoginPage renders the consent page's sign-in step. A failed attempt
// keeps the pending request, so the person can try again on the same page.
func writeLoginPage(w http.ResponseWriter, requestID string, p *pending, errMessage string) {
	errBlock := ""
	if errMessage != "" {
		errBlock = `<div class="err">` + html.EscapeString(errMessage) + `</div>`
	}
	body := `
<h1>Sign in to continue</h1>
<p><b>` + html.EscapeString(p.clientName) + `</b> wants to connect to this Hub. Sign in with your Spark AI Hub account.</p>
` + errBlock + `
<form method="post" action="/oauth/authorize">
<input type="hidden" name="request_id" value="` + html.EscapeString(requestID) + `">
<input type="hidden" name="action" value="login">
<label for="email">Email</label><input id="email" name="email" type="email" autocomplete="username" required autofocus>
<label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required>
<div class="row"><button class="primary" type="submit">Sign in</button></div>
</form>`

	status := http.StatusOK
	if errMessage != "" {
		status = http.StatusUnauthorized
	}
	writePage(w, "Sign in", body, status)
}

// writeConsentPage renders the Allow/Deny decision.
func writeConsentPage(w http.ResponseWriter, requestID string, p *pending, user *store.User) {
	host := p.redirectURI
	if u, err := url.Parse(p.redirectURI); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	body := `
<h1>Allow ` + html.EscapeString(p.clientName) + `?</h1>
<p>Signed in as <b>` + html.EscapeString(user.Email) + `</b>.</p>
<p>It will be able to:</p>
<ul><li>Generate and edit images with the models running on this Hub</li>
<li>See which image models are installed</li></ul>
<p>Access is sent back to <code>` + html.EscapeString(host) + `</code>. It never gets your password or API key.</p>
<form method="post" action="/oauth/authorize">
<input type="hidden" name="request_id" value="` + html.EscapeString(requestID) + `">
<div class="row"><button type="submit" name="action" value="deny">Deny</button>
<button class="primary" type="submit" name="action" value="allow">Allow</button></div>
</form>`

	writePage(w, "Allow access", body, http.StatusOK)
}
