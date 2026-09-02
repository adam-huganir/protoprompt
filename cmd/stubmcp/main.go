// Command stubmcp is a dev-only stdio MCP server for protoprompt. It speaks
// newline-delimited JSON-RPC over stdin/stdout and exposes one tool, "echo",
// so tool calling can be exercised without a real MCP endpoint. It exits on
// stdin EOF, matching the client's graceful-close convention.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"os"
)

// request is the subset of a JSON-RPC frame we act on.
type request struct {
	ID     *int64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func main() {
	name := flag.String("name", "stubmcp", "server name reported by initialize")
	flag.Parse()

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := sc.Text()
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		if req.ID == nil {
			continue // notification; nothing to answer
		}
		var resp any
		switch req.Method {
		case "initialize":
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      *req.ID,
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": *name},
				},
			}
		case "tools/list":
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      *req.ID,
				"result": map[string]any{"tools": []map[string]any{{
					"name":        "echo",
					"description": "echoes its text argument",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"text": map[string]any{"type": "string"}},
					},
				}}},
			}
		case "tools/call":
			var params struct {
				Arguments struct {
					Text string `json:"text"`
				} `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &params)
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      *req.ID,
				"result":  map[string]any{"content": []map[string]any{{"type": "text", "text": "echo:" + params.Arguments.Text}}},
			}
		default:
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      *req.ID,
				"error":   map[string]any{"code": -32601, "message": "unknown method"},
			}
		}
		b, _ := json.Marshal(resp)
		os.Stdout.Write(append(b, '\n'))
	}
	// stdin EOF: graceful exit for the client's Close.
}
