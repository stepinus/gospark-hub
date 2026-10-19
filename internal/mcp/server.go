package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"gospark-hub/internal/auth"
	"gospark-hub/internal/config"
	"gospark-hub/internal/media"
	"gospark-hub/internal/registry"
	"gospark-hub/internal/store"
)

// Deps is what the MCP server needs from the application.
type Deps struct {
	Config   config.Config
	Store    *store.DB
	Registry *registry.Cache
	Logger   *log.Logger
	// Runtime answers installed/running/ready and starts or stops a recipe.
	// Defaults to the docker CLI; a test supplies a fake.
	Runtime Runtime
	// App resolves a recipe's internal URL. Defaults to the front door.
	App App
	// HTTP is the client used to reach apps and fetch URLs. Defaults to a
	// client with no overall timeout (renders take minutes).
	HTTP *http.Client
}

// Server is the MCP endpoint and the media tools behind it.
type Server struct {
	cfg        config.Config
	store      *store.DB
	reg        *registry.Cache
	log        *log.Logger
	rt         Runtime
	app        App
	http       *http.Client
	media      *media.MediaStore
	jobs       *jobManager
	videoJobs  *videoJobs
	startMu    sync.Mutex
	serverInfo ServerInfo
}

// New builds the MCP server.
func New(deps Deps) *Server {
	if deps.Logger == nil {
		deps.Logger = log.Default()
	}
	if deps.App == nil {
		deps.App = ProxyApp{PublicPort: deps.Config.PublicPort, DataDir: deps.Config.DataDir}
	}
	if deps.HTTP == nil {
		deps.HTTP = &http.Client{Timeout: 0}
	}
	if deps.Runtime == nil {
		if deps.Store != nil {
			deps.Runtime = NewDockerRuntime(deps.Config, deps.Store, deps.Registry, deps.App, deps.Logger)
		} else {
			deps.Runtime = NilRuntime{}
		}
	}
	return &Server{
		cfg:        deps.Config,
		store:      deps.Store,
		reg:        deps.Registry,
		log:        deps.Logger,
		rt:         deps.Runtime,
		app:        deps.App,
		http:       deps.HTTP,
		media:      media.NewStore(deps.Config, deps.Store),
		jobs:       newJobManager(),
		videoJobs:  newVideoJobs(),
		serverInfo: ServerInfoFor(deps.Config.BaseDir),
	}
}

// Media returns the shared media store, so the file routes use the same
// ownership rules the MCP tools do.
func (s *Server) Media() *media.MediaStore { return s.media }

// --- HTTP transport ---------------------------------------------------------

// POST is upstream mcp_post: one JSON-RPC message per request, no session.
func (s *Server) POST(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, rpcErr(nil, -32700, "Parse error"))
		return
	}
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcErr(nil, -32700, "Parse error"))
		return
	}
	msg, ok := raw.(map[string]any)
	if !ok || msg["jsonrpc"] != "2.0" {
		writeJSON(w, http.StatusBadRequest, rpcErr(nil, -32600, "Expected one JSON-RPC 2.0 message"))
		return
	}

	id, hasID := msg["id"]
	methodRaw, hasMethod := msg["method"]
	if !hasID || !hasMethod {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	method, _ := methodRaw.(string)
	params, _ := msg["params"].(map[string]any)
	if params == nil {
		params = map[string]any{}
	}

	switch method {
	case "initialize":
		asked, _ := params["protocolVersion"].(string)
		version := ProtocolVersions[0]
		for _, v := range ProtocolVersions {
			if v == asked {
				version = asked
				break
			}
		}
		writeJSON(w, http.StatusOK, result(id, initializeResult{
			ProtocolVersion: version,
			Capabilities:    capabilities{Tools: toolCapability{ListChanged: false}},
			ServerInfo:      s.serverInfo,
			Instructions:    Instructions(),
		}))
	case "ping":
		writeJSON(w, http.StatusOK, result(id, map[string]any{}))
	case "tools/list":
		writeJSON(w, http.StatusOK, result(id, toolsListResult{Tools: ToolsJSON()}))
	case "tools/call":
		s.toolsCall(w, r, id, params)
	default:
		writeJSON(w, http.StatusOK, rpcErr(id, -32601, fmt.Sprintf("Method not found: %s", method)))
	}
}

// NoStream answers GET/DELETE /mcp with 405 and Allow: POST.
func (s *Server) NoStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "POST")
	w.WriteHeader(http.StatusMethodNotAllowed)
}

// toolsCall validates the call, runs it, and answers as JSON or SSE.
func (s *Server) toolsCall(w http.ResponseWriter, r *http.Request, id any, params map[string]any) {
	name, _ := params["name"].(string)
	if !toolNames[name] {
		writeJSON(w, http.StatusOK, rpcErr(id, -32602, fmt.Sprintf("Unknown tool: %s", name)))
		return
	}
	args, _ := params["arguments"].(map[string]any)
	if raw, ok := params["arguments"]; ok && raw != nil {
		if _, isMap := raw.(map[string]any); !isMap {
			writeJSON(w, http.StatusOK, rpcErr(id, -32602, "arguments must be an object"))
			return
		}
	}
	if args == nil {
		args = map[string]any{}
	}

	user := auth.CurrentUser(r)
	origin := requestOrigin(r)
	s.recordUsage(r, name, args)

	if !strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream") {
		out := s.callTool(r.Context(), name, args, origin, user, nil)
		writeJSON(w, http.StatusOK, result(id, out))
		return
	}

	// SSE: keepalives while the render runs, then the JSON-RPC result event.
	token := progressToken(params)
	progress := make(chan string, 16)
	done := make(chan toolResult, 1)
	go func() {
		done <- s.callTool(r.Context(), name, args, origin, user, func(msg string) {
			select {
			case progress <- msg:
			default:
			}
		})
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	count := 0
	ticker := time.NewTicker(KeepAliveSeconds * time.Second)
	defer ticker.Stop()
	for {
		select {
		case out := <-done:
			writeSSE(w, result(id, out))
			if flusher != nil {
				flusher.Flush()
			}
			return
		case msg := <-progress:
			if token != nil {
				count++
				writeSSE(w, map[string]any{
					"jsonrpc": "2.0", "method": "notifications/progress",
					"params": map[string]any{"progressToken": token, "progress": count, "message": msg},
				})
				if flusher != nil {
					flusher.Flush()
				}
			}
		case <-ticker.C:
			fmt.Fprint(w, ": keepalive\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func progressToken(params map[string]any) any {
	meta, _ := params["_meta"].(map[string]any)
	if meta == nil {
		return nil
	}
	return meta["progressToken"]
}

// writeSSE emits one "event: message" frame.
func writeSSE(w io.Writer, message any) {
	payload, err := json.Marshal(message)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
}

// --- artifact routes --------------------------------------------------------

// mediaSuffix maps a route kind to its file extension.
var mediaSuffix = map[string]string{
	"image": ".png", "video": ".mp4", "audio": ".wav", "mesh": ".glb",
}

// ServeMedia is upstream _media_file: a generated or uploaded file, for its
// owner only, with the same 401/404 and cache headers.
func (s *Server) ServeMedia(w http.ResponseWriter, r *http.Request, kind string) {
	suffix := mediaSuffix[kind]
	name := r.PathValue("name")
	user := auth.CurrentUser(r)

	if s.cfg.AuthEnabled && user == nil {
		if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, signInPage)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("WWW-Authenticate", `Bearer realm="Spark AI Hub"`)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"detail":"Authentication required: sign in to the Hub, or ask the MCP tools for a create_download link."}`)
		return
	}

	if !strings.HasSuffix(name, suffix) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	path := s.media.Find(name)
	if path == "" || !s.media.CanRead(name, user) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Private to one account, who can delete it at any time: no shared cache
	// may keep a copy. Matches upstream mcp._cache_headers.
	if isUploadPath(path) {
		w.Header().Set("Cache-Control", "private, max-age=3600")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	}
	w.Header().Set("Content-Type", mediaTypes[suffix])
	http.ServeFile(w, r, path)
}

const signInPage = `<!doctype html><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in · Spark AI Hub</title>
<body style="font:15px system-ui,sans-serif;max-width:480px;margin:15vh auto;padding:0 16px">
<p>This file is private. <a href="/">Sign in to Spark AI Hub</a> with the account that
made it, then open the link again.</p>`

// --- helpers ----------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// requestOrigin mirrors connect_service.request_origin: forwarded host/proto
// first, then the request's own.
func requestOrigin(r *http.Request) string {
	host := ""
	if fwd := r.Header.Get("Forwarded"); fwd != "" {
		first := strings.Split(fwd, ",")[0]
		for _, kv := range strings.Split(first, ";") {
			kv = strings.TrimSpace(kv)
			if strings.HasPrefix(strings.ToLower(kv), "host=") {
				host = strings.Trim(strings.TrimPrefix(kv, "host="), `"`)
				break
			}
		}
	}
	if host == "" {
		host = strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0])
	}
	if host == "" {
		host = r.Host
	}
	if host == "" {
		return ""
	}
	scheme := "http"
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
		v := strings.ToLower(strings.TrimSpace(strings.Split(fwd, ",")[0]))
		if v == "http" || v == "https" {
			scheme = v
		}
	} else if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + host
}
