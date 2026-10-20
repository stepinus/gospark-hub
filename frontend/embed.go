// Package frontend embeds the built SPA so the daemon binary is self-contained:
// a release build carries its own UI and needs no frontend/ directory beside it.
//
// The embedded tree is whatever `frontend/dist` held at compile time — run
// `npm run build` before `go build` to ship a fresh UI. The directory always
// exists in the checkout (`.gitkeep` is tracked) so a plain `go build ./...`
// still compiles on a fresh clone, with the daemon falling back to disk.
package frontend

import "embed"

//go:embed all:dist
var Dist embed.FS
