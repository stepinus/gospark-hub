// Package mcp implements the Hub's MCP server: the Streamable HTTP transport
// in its stateless form at POST /mcp, the tool catalog, and the media tools
// that drive the installed recipe containers.
//
// The tool catalog (tools.json) and the initialize instructions
// (instructions.txt) are the upstream daemon/routers/mcp.py definitions
// verbatim, so tools/list and initialize answer byte for byte what an MCP
// client saw from the Python Hub.
package mcp

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
)

//go:embed tools.json
var toolsJSON []byte

//go:embed instructions.txt
var instructions string

// ProtocolVersions is upstream PROTOCOL_VERSIONS, newest first.
var ProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

// CheckWait is upstream CHECK_WAIT: how long a get_* / start_model call holds
// the socket open before answering "not done yet".
const CheckWait = 20

// KeepAliveSeconds is upstream KEEPALIVE_SECONDS for the SSE transport.
const KeepAliveSeconds = 10

// ServerInfo is MCP initialize's serverInfo, plus the Hub's icon when the
// frontend ships one.
type ServerInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
	Icons   []Icon `json:"icons,omitempty"`
}

// Icon is one MCP Implementation.icon (MCP 2025-11-25).
type Icon struct {
	Src      string   `json:"src"`
	MimeType string   `json:"mimeType"`
	Sizes    []string `json:"sizes"`
}

// ServerInfoFor builds the upstream SERVER_INFO, inlining the Hub's mark as a
// data URI when frontend/public/icon-128.png exists.
func ServerInfoFor(baseDir string) ServerInfo {
	info := ServerInfo{Name: "spark-ai-hub", Title: "Spark AI Hub", Version: "1.0.0"}
	iconPath := filepath.Join(baseDir, "frontend", "public", "icon-128.png")
	if raw, err := os.ReadFile(iconPath); err == nil {
		info.Icons = []Icon{{
			Src:      "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw),
			MimeType: "image/png",
			Sizes:    []string{"128x128"},
		}}
	}
	return info
}

// ToolsJSON returns the raw upstream tools array.
func ToolsJSON() json.RawMessage { return toolsJSON }

// Instructions returns the upstream initialize instructions text.
func Instructions() string { return instructions }

// toolNames is the set of known tool names, built from the embedded catalog.
var toolNames = func() map[string]bool {
	var tools []struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(toolsJSON, &tools)
	names := make(map[string]bool, len(tools))
	for _, t := range tools {
		names[t.Name] = true
	}
	return names
}()

// --- JSON-RPC envelopes -----------------------------------------------------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcErrorResponse struct {
	JSONRPC string   `json:"jsonrpc"`
	ID      any      `json:"id"`
	Error   rpcError `json:"error"`
}

type rpcResultResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result"`
}

// result makes a JSON-RPC success envelope.
func result(id any, value any) rpcResultResponse {
	return rpcResultResponse{JSONRPC: "2.0", ID: id, Result: value}
}

// rpcErr makes a JSON-RPC error envelope.
func rpcErr(id any, code int, message string) rpcErrorResponse {
	return rpcErrorResponse{JSONRPC: "2.0", ID: id, Error: rpcError{Code: code, Message: message}}
}

// initializeResult is the initialize reply, field order matching upstream.
type initializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    capabilities `json:"capabilities"`
	ServerInfo      ServerInfo   `json:"serverInfo"`
	Instructions    string       `json:"instructions"`
}

type capabilities struct {
	Tools toolCapability `json:"tools"`
}

type toolCapability struct {
	ListChanged bool `json:"listChanged"`
}

// toolsListResult wraps the embedded tools array without re-encoding it.
type toolsListResult struct {
	Tools json.RawMessage `json:"tools"`
}

// toolResult is a tools/call outcome.
type toolResult struct {
	Content           []any `json:"content"`
	StructuredContent any   `json:"structuredContent,omitempty"`
	IsError           bool  `json:"isError"`
}

// textBlock and imageBlock are MCP content blocks.
type textBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type imageBlock struct {
	Type     string `json:"type"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

// textResult is upstream _text.
func textResult(text string, isError bool) toolResult {
	return toolResult{Content: []any{textBlock{Type: "text", Text: text}}, IsError: isError}
}

// orderedMap serialises keys in insertion order, so tool results keep the
// upstream field order byte for byte.
type orderedMap struct {
	keys []string
	vals map[string]any
}

func newOrderedMap() *orderedMap {
	return &orderedMap{vals: map[string]any{}}
}

func (o *orderedMap) Set(key string, value any) *orderedMap {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = value
	return o
}

func (o *orderedMap) MarshalJSON() ([]byte, error) {
	out := make([]byte, 0, 64)
	out = append(out, '{')
	for i, k := range o.keys {
		if i > 0 {
			out = append(out, ',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		out = append(out, kb...)
		out = append(out, ':')
		vb, err := json.Marshal(o.vals[k])
		if err != nil {
			return nil, err
		}
		out = append(out, vb...)
	}
	return append(out, '}'), nil
}
