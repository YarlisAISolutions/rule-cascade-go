// Package mcp is a Model Context Protocol server over standard input and output: JSON-RPC 2.0, one
// message per line. It knows the protocol (initialize, ping, tools, resources); the tools
// themselves are registered by the caller. Nothing but protocol messages is written to the output:
// logs go to the log writer.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

// ProtocolVersions are the protocol revisions this server speaks, newest first.
var ProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// Tool is one tool: its description, the JSON Schema of its arguments, and the function.
type Tool struct {
	Name        string
	Title       string
	Description string
	InputSchema map[string]any
	ReadOnly    bool
	// Call returns the result as a value (sent as JSON text and as structured content), or an
	// error, which the client sees as a tool error (isError), not a protocol error.
	Call func(args map[string]any) (any, error)
}

// Resource is one readable document.
type Resource struct {
	URI         string
	Name        string
	Description string
	MimeType    string
	Read        func() (string, error)
}

// Server holds the tools and resources and serves them.
type Server struct {
	Name, Version string
	Instructions  string
	Log           io.Writer

	mu        sync.Mutex
	tools     map[string]*Tool
	order     []string
	resources func() []Resource
}

// NewServer returns a server with no tools.
func NewServer(name, version string) *Server {
	return &Server{Name: name, Version: version, tools: map[string]*Tool{}, Log: io.Discard}
}

// AddTool registers a tool; a second tool with the same name replaces the first.
func (s *Server) AddTool(t Tool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tools[t.Name]; !ok {
		s.order = append(s.order, t.Name)
	}
	s.tools[t.Name] = &t
}

// SetResources sets the function that lists the resources (they can change while serving).
func (s *Server) SetResources(list func() []Resource) { s.resources = list }

// Tools returns the registered tools in registration order.
func (s *Server) Tools() []Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Tool, 0, len(s.order))
	for _, name := range s.order {
		out = append(out, *s.tools[name])
	}
	return out
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// JSON-RPC error codes.
const (
	ParseError     = -32700
	InvalidRequest = -32600
	MethodNotFound = -32601
	InvalidParams  = -32602
	InternalError  = -32603
)

// Serve reads requests from in and writes responses to out until in ends. Requests are answered
// in order; a batch (a JSON array) is answered with an array.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	reader := bufio.NewReaderSize(in, 1<<20)
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if reply := s.Handle(bytes.TrimSpace(line)); reply != nil {
				if werr := enc.Encode(reply); werr != nil {
					return werr
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Handle answers one message (or batch); it returns nil for notifications.
func (s *Server) Handle(message []byte) any {
	if len(message) > 0 && message[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(message, &batch); err != nil {
			return response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{ParseError, "invalid JSON"}}
		}
		if len(batch) == 0 {
			return response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{InvalidRequest, "empty batch"}}
		}
		var replies []any
		for _, m := range batch {
			if r := s.handleOne(m); r != nil {
				replies = append(replies, r)
			}
		}
		if len(replies) == 0 {
			return nil
		}
		return replies
	}
	if r := s.handleOne(message); r != nil {
		return r
	}
	return nil
}

func (s *Server) handleOne(message []byte) *response {
	var req request
	if !json.Valid(message) {
		return &response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{ParseError, "invalid JSON"}}
	}
	if err := json.Unmarshal(message, &req); err != nil {
		return &response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{InvalidRequest, "a request must be a JSON object"}}
	}
	notification := len(req.ID) == 0
	if req.JSONRPC != "2.0" || req.Method == "" {
		if notification {
			return nil
		}
		return &response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{InvalidRequest, "not a JSON-RPC 2.0 request"}}
	}
	result, rerr := s.dispatch(req)
	if notification {
		return nil
	}
	if rerr != nil {
		return &response{JSONRPC: "2.0", ID: req.ID, Error: rerr}
	}
	return &response{JSONRPC: "2.0", ID: req.ID, Result: result}
}

func (s *Server) dispatch(req request) (result any, rerr *rpcError) {
	defer func() {
		if p := recover(); p != nil {
			fmt.Fprintf(s.Log, "mcp: %s panicked: %v\n", req.Method, p)
			result, rerr = nil, &rpcError{InternalError, fmt.Sprintf("internal error in %s", req.Method)}
		}
	}()
	params := map[string]any{}
	if len(req.Params) > 0 && string(req.Params) != "null" {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &rpcError{InvalidParams, "params must be an object"}
		}
	}
	switch req.Method {
	case "initialize":
		version := ProtocolVersions[0]
		if asked, _ := params["protocolVersion"].(string); asked != "" {
			for _, v := range ProtocolVersions {
				if v == asked {
					version = asked
				}
			}
		}
		capabilities := map[string]any{"tools": map[string]any{"listChanged": false}}
		if s.resources != nil {
			capabilities["resources"] = map[string]any{"listChanged": false, "subscribe": false}
		}
		out := map[string]any{
			"protocolVersion": version,
			"capabilities":    capabilities,
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
		}
		if s.Instructions != "" {
			out["instructions"] = s.Instructions
		}
		return out, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		var list []map[string]any
		for _, t := range s.Tools() {
			item := map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.InputSchema}
			if t.Title != "" {
				item["title"] = t.Title
			}
			item["annotations"] = map[string]any{"readOnlyHint": t.ReadOnly, "destructiveHint": false, "openWorldHint": false}
			list = append(list, item)
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		name, _ := params["name"].(string)
		s.mu.Lock()
		tool := s.tools[name]
		s.mu.Unlock()
		if tool == nil {
			return nil, &rpcError{InvalidParams, fmt.Sprintf("unknown tool %q", name)}
		}
		args, _ := params["arguments"].(map[string]any)
		if args == nil {
			args = map[string]any{}
		}
		if missing := missingRequired(tool.InputSchema, args); len(missing) > 0 {
			return toolError(fmt.Sprintf("missing required argument(s): %s", strings.Join(missing, ", "))), nil
		}
		value, err := tool.Call(args)
		if err != nil {
			return toolError(err.Error()), nil
		}
		return toolResult(value), nil
	case "resources/list":
		var list []map[string]any
		if s.resources != nil {
			for _, r := range s.resources() {
				list = append(list, map[string]any{"uri": r.URI, "name": r.Name, "description": r.Description, "mimeType": r.MimeType})
			}
		}
		if list == nil {
			list = []map[string]any{}
		}
		return map[string]any{"resources": list}, nil
	case "resources/templates/list":
		return map[string]any{"resourceTemplates": []any{}}, nil
	case "resources/read":
		uri, _ := params["uri"].(string)
		if s.resources != nil {
			for _, r := range s.resources() {
				if r.URI == uri {
					text, err := r.Read()
					if err != nil {
						return nil, &rpcError{InternalError, err.Error()}
					}
					return map[string]any{"contents": []any{map[string]any{"uri": uri, "mimeType": r.MimeType, "text": text}}}, nil
				}
			}
		}
		return nil, &rpcError{-32002, fmt.Sprintf("resource not found: %s", uri)}
	case "prompts/list":
		return map[string]any{"prompts": []any{}}, nil
	case "logging/setLevel":
		return map[string]any{}, nil
	}
	if strings.HasPrefix(req.Method, "notifications/") && len(req.ID) == 0 {
		return nil, nil
	}
	return nil, &rpcError{MethodNotFound, fmt.Sprintf("method not found: %s", req.Method)}
}

func missingRequired(schema map[string]any, args map[string]any) []string {
	var missing []string
	required, _ := schema["required"].([]string)
	if required == nil {
		if list, ok := schema["required"].([]any); ok {
			for _, r := range list {
				if s, ok := r.(string); ok {
					required = append(required, s)
				}
			}
		}
	}
	for _, name := range required {
		if v, ok := args[name]; !ok || v == nil {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

func toolError(message string) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": message}}, "isError": true}
}

func toolResult(value any) map[string]any {
	if text, ok := value.(string); ok {
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return toolError("the result is not JSON: " + err.Error())
	}
	out := map[string]any{"content": []any{map[string]any{"type": "text", "text": string(data)}}}
	// structured content must be an object
	var structured map[string]any
	if json.Unmarshal(data, &structured) == nil && structured != nil {
		out["structuredContent"] = structured
	}
	return out
}
