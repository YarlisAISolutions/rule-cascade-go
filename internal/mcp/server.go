// Package mcp is a Model Context Protocol server. It knows the protocol (discovery, tools,
// resources, prompts, completions, subscriptions); the tools themselves are registered by the
// caller. Two transports carry it: standard input and output (Serve, one JSON-RPC message per line)
// and HTTP (Handler: Streamable HTTP, and the deprecated HTTP+SSE transport for older clients).
//
// It speaks both eras of the protocol on every transport. A modern request (revision 2026-07-28)
// carries its protocol version and client capabilities in _meta and is answered on its own; a
// legacy client (2025-11-25 and earlier) opens with initialize. Nothing is remembered between
// requests in either era, so one process serves any number of clients.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// ModernVersions are the stateless revisions (per-request _meta, server/discover), newest first.
var ModernVersions = []string{"2026-07-28"}

// LegacyVersions are the revisions that open with initialize, newest first.
var LegacyVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// ProtocolVersions are all the revisions this server speaks, newest first.
var ProtocolVersions = append(append([]string{}, ModernVersions...), LegacyVersions...)

// The reserved _meta keys of revision 2026-07-28.
const (
	metaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfo         = "io.modelcontextprotocol/serverInfo"
	metaSubscriptionID     = "io.modelcontextprotocol/subscriptionId"
)

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
	Title       string
	Description string
	MimeType    string
	Read        func() (string, error)
}

// ResourceTemplate describes a family of resources by an RFC 6570 URI template.
type ResourceTemplate struct {
	URITemplate string
	Name        string
	Title       string
	Description string
	MimeType    string
}

// PromptArgument is one argument of a prompt.
type PromptArgument struct {
	Name        string
	Description string
	Required    bool
}

// Prompt is a message template a person picks in the client (often as a slash command).
type Prompt struct {
	Name        string
	Title       string
	Description string
	Arguments   []PromptArgument
	// Get returns the text of the user message for these arguments.
	Get func(args map[string]string) (string, error)
}

// Server holds the tools, resources and prompts and serves them.
type Server struct {
	Name, Version string
	Title         string
	WebsiteURL    string
	Instructions  string
	Log           io.Writer
	// CacheScope is "private" (results describe one project) or "public" (the same for everyone,
	// shared caches may keep them); CacheTTL is how long a client may keep a list or a read.
	CacheScope string
	CacheTTL   time.Duration
	// ResolveResource reads a resource that is not listed but matches a template; nil for none.
	ResolveResource func(uri string) (*Resource, bool)
	// Complete suggests values for an argument of a prompt ("ref/prompt") or of a resource
	// template ("ref/resource"); nil for none.
	Complete func(refType, ref, argument, prefix string) []string

	mu        sync.Mutex
	tools     map[string]*Tool
	order     []string
	resources func() []Resource
	templates []ResourceTemplate
	prompts   []Prompt
}

// NewServer returns a server with no tools.
func NewServer(name, version string) *Server {
	return &Server{Name: name, Version: version, tools: map[string]*Tool{}, Log: io.Discard,
		CacheScope: "private", CacheTTL: time.Minute}
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

// AddResourceTemplate registers a resource template.
func (s *Server) AddResourceTemplate(t ResourceTemplate) { s.templates = append(s.templates, t) }

// AddPrompt registers a prompt.
func (s *Server) AddPrompt(p Prompt) { s.prompts = append(s.prompts, p) }

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

// Prompts returns the registered prompts.
func (s *Server) Prompts() []Prompt { return append([]Prompt{}, s.prompts...) }

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
	// status is the HTTP status this error is sent with on Streamable HTTP.
	status int
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// notification is a message from the server that needs no answer.
type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// JSON-RPC error codes, and the codes MCP defines.
const (
	ParseError                      = -32700
	InvalidRequest                  = -32600
	MethodNotFound                  = -32601
	InvalidParams                   = -32602
	InternalError                   = -32603
	ResourceNotFoundLegacy          = -32002 // 2025-11-25 and earlier; InvalidParams from 2026-07-28
	HeaderMismatch                  = -32020
	MissingRequiredClientCapability = -32021
	UnsupportedProtocolVersion      = -32022
)

// exchange is what the transport needs to know about one request while it is handled.
type exchange struct {
	ctx    context.Context
	modern bool
	// emit sends a notification that belongs to this request; nil when the transport cannot.
	emit func(any)
}

// pending is returned by dispatch for a request that is answered later (subscriptions/listen).
type pendingResult struct{}

// Serve reads requests from in and writes responses to out until in ends. Requests are answered
// in order; a batch (a JSON array) is answered with an array.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	reader := bufio.NewReaderSize(in, 1<<20)
	var wmu sync.Mutex
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	var werr error
	write := func(v any) {
		wmu.Lock()
		defer wmu.Unlock()
		if werr == nil {
			werr = enc.Encode(v)
		}
	}
	for {
		line, err := reader.ReadBytes('\n')
		if msg := bytes.TrimSpace(line); len(msg) > 0 {
			if reply := s.handle(context.Background(), msg, write); reply != nil {
				write(reply)
			}
			if werr != nil {
				return werr
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
	return s.handle(context.Background(), message, nil)
}

func (s *Server) handle(ctx context.Context, message []byte, emit func(any)) any {
	if len(message) > 0 && message[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(message, &batch); err != nil {
			return response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: ParseError, Message: "invalid JSON"}}
		}
		if len(batch) == 0 {
			return response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: InvalidRequest, Message: "empty batch"}}
		}
		var replies []any
		for _, m := range batch {
			if r := s.handleOne(ctx, m, emit); r != nil {
				replies = append(replies, r)
			}
		}
		if len(replies) == 0 {
			return nil
		}
		return replies
	}
	if r := s.handleOne(ctx, message, emit); r != nil {
		return r
	}
	return nil
}

func parseRequest(message []byte) (request, *response) {
	var req request
	if !json.Valid(message) {
		return req, &response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: ParseError, Message: "invalid JSON", status: 400}}
	}
	if err := json.Unmarshal(message, &req); err != nil {
		return req, &response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: InvalidRequest, Message: "a request must be a JSON object", status: 400}}
	}
	return req, nil
}

func (s *Server) handleOne(ctx context.Context, message []byte, emit func(any)) *response {
	req, bad := parseRequest(message)
	if bad != nil {
		return bad
	}
	notification := len(req.ID) == 0
	if req.JSONRPC != "2.0" || req.Method == "" {
		if notification {
			return nil
		}
		return &response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: InvalidRequest, Message: "not a JSON-RPC 2.0 request", status: 400}}
	}
	result, rerr := s.dispatch(req, &exchange{ctx: ctx, emit: emit})
	if notification {
		return nil
	}
	if rerr != nil {
		return &response{JSONRPC: "2.0", ID: req.ID, Error: rerr}
	}
	if _, wait := result.(pendingResult); wait {
		return nil
	}
	return &response{JSONRPC: "2.0", ID: req.ID, Result: result}
}

// requestMeta returns the _meta of the params, or nil.
func requestMeta(params map[string]any) map[string]any {
	m, _ := params["_meta"].(map[string]any)
	return m
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// era decides how a request is served: modern when its _meta names a modern revision, legacy
// otherwise. It returns an error for a modern request that is malformed or names a revision this
// server does not speak.
func era(method string, params map[string]any) (modern bool, rerr *rpcError) {
	meta := requestMeta(params)
	version, hasVersion := meta[metaProtocolVersion].(string)
	modernOnly := method == "server/discover" || method == "subscriptions/listen"
	if !hasVersion {
		if modernOnly {
			return true, &rpcError{Code: InvalidParams, Message: "missing _meta " + metaProtocolVersion, status: 400}
		}
		return false, nil
	}
	if method == "initialize" {
		return false, nil
	}
	if contains(LegacyVersions, version) && !modernOnly {
		return false, nil
	}
	if !contains(ModernVersions, version) {
		return true, &rpcError{Code: UnsupportedProtocolVersion, Message: "Unsupported protocol version", status: 400,
			Data: map[string]any{"supported": ProtocolVersions, "requested": version}}
	}
	if _, ok := meta[metaClientCapabilities].(map[string]any); !ok {
		return true, &rpcError{Code: InvalidParams, Message: "missing _meta " + metaClientCapabilities, status: 400}
	}
	return true, nil
}

func (s *Server) dispatch(req request, ex *exchange) (result any, rerr *rpcError) {
	defer func() {
		if p := recover(); p != nil {
			fmt.Fprintf(s.Log, "mcp: %s panicked: %v\n", req.Method, p)
			result, rerr = nil, &rpcError{Code: InternalError, Message: fmt.Sprintf("internal error in %s", req.Method)}
		}
	}()
	params := map[string]any{}
	if len(req.Params) > 0 && string(req.Params) != "null" {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &rpcError{Code: InvalidParams, Message: "params must be an object", status: 400}
		}
	}
	modern, rerr := era(req.Method, params)
	if rerr != nil {
		return nil, rerr
	}
	ex.modern = modern
	out, rerr := s.method(req, params, ex)
	if rerr != nil || out == nil {
		return nil, rerr
	}
	if modern {
		out["resultType"] = "complete"
		meta, _ := out["_meta"].(map[string]any)
		if meta == nil {
			meta = map[string]any{}
		}
		meta[metaServerInfo] = s.info()
		out["_meta"] = meta
	}
	if _, wait := out["pending"]; wait {
		return pendingResult{}, nil
	}
	return out, nil
}

func (s *Server) info() map[string]any {
	info := map[string]any{"name": s.Name, "version": s.Version}
	if s.Title != "" {
		info["title"] = s.Title
	}
	if s.WebsiteURL != "" {
		info["websiteUrl"] = s.WebsiteURL
	}
	return info
}

func (s *Server) capabilities(modern bool) map[string]any {
	caps := map[string]any{"tools": map[string]any{"listChanged": false}}
	if s.resources != nil || len(s.templates) > 0 {
		caps["resources"] = map[string]any{"listChanged": false, "subscribe": false}
	}
	if len(s.prompts) > 0 {
		caps["prompts"] = map[string]any{"listChanged": false}
	}
	if s.Complete != nil {
		caps["completions"] = map[string]any{}
	}
	if modern {
		caps["resources"] = map[string]any{"listChanged": false}
		caps["extensions"] = map[string]any{}
	} else {
		caps["logging"] = map[string]any{}
	}
	return caps
}

// cacheable adds the freshness hints a modern client may cache a list or a read by.
func (s *Server) cacheable(out map[string]any, ex *exchange) map[string]any {
	if ex.modern {
		out["ttlMs"] = s.CacheTTL.Milliseconds()
		out["cacheScope"] = s.CacheScope
	}
	return out
}

func (s *Server) method(req request, params map[string]any, ex *exchange) (map[string]any, *rpcError) {
	switch req.Method {
	case "server/discover":
		out := map[string]any{"supportedVersions": ProtocolVersions, "capabilities": s.capabilities(true)}
		if s.Instructions != "" {
			out["instructions"] = s.Instructions
		}
		return s.cacheable(out, ex), nil
	case "initialize":
		// legacy: the newest legacy revision the client asks for, else the newest we speak
		version := LegacyVersions[0]
		if asked, _ := params["protocolVersion"].(string); contains(LegacyVersions, asked) {
			version = asked
		}
		out := map[string]any{"protocolVersion": version, "capabilities": s.capabilities(false), "serverInfo": s.info()}
		if s.Instructions != "" {
			out["instructions"] = s.Instructions
		}
		return out, nil
	case "ping", "logging/setLevel":
		if ex.modern {
			break // removed in 2026-07-28
		}
		return map[string]any{}, nil
	case "tools/list":
		list := []map[string]any{}
		for _, t := range s.Tools() {
			item := map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.InputSchema}
			if t.Title != "" {
				item["title"] = t.Title
			}
			annotations := map[string]any{"readOnlyHint": t.ReadOnly, "destructiveHint": false, "idempotentHint": t.ReadOnly, "openWorldHint": false}
			if t.Title != "" {
				annotations["title"] = t.Title
			}
			item["annotations"] = annotations
			list = append(list, item)
		}
		return s.cacheable(map[string]any{"tools": list}, ex), nil
	case "tools/call":
		name, _ := params["name"].(string)
		s.mu.Lock()
		tool := s.tools[name]
		s.mu.Unlock()
		if tool == nil {
			return nil, &rpcError{Code: InvalidParams, Message: fmt.Sprintf("unknown tool %q", name)}
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
		list := []map[string]any{}
		if s.resources != nil {
			for _, r := range s.resources() {
				list = append(list, describeResource(r))
			}
		}
		return s.cacheable(map[string]any{"resources": list}, ex), nil
	case "resources/templates/list":
		list := []map[string]any{}
		for _, t := range s.templates {
			item := map[string]any{"uriTemplate": t.URITemplate, "name": t.Name, "description": t.Description, "mimeType": t.MimeType}
			if t.Title != "" {
				item["title"] = t.Title
			}
			list = append(list, item)
		}
		return s.cacheable(map[string]any{"resourceTemplates": list}, ex), nil
	case "resources/read":
		uri, _ := params["uri"].(string)
		r := s.findResource(uri)
		if r == nil {
			code := InvalidParams
			if !ex.modern {
				code = ResourceNotFoundLegacy
			}
			return nil, &rpcError{Code: code, Message: fmt.Sprintf("resource not found: %s", uri), Data: map[string]any{"uri": uri}}
		}
		text, err := r.Read()
		if err != nil {
			return nil, &rpcError{Code: InternalError, Message: err.Error()}
		}
		return s.cacheable(map[string]any{"contents": []any{map[string]any{"uri": uri, "mimeType": r.MimeType, "text": text}}}, ex), nil
	case "prompts/list":
		list := []map[string]any{}
		for _, p := range s.prompts {
			item := map[string]any{"name": p.Name, "description": p.Description}
			if p.Title != "" {
				item["title"] = p.Title
			}
			var args []map[string]any
			for _, a := range p.Arguments {
				args = append(args, map[string]any{"name": a.Name, "description": a.Description, "required": a.Required})
			}
			if args != nil {
				item["arguments"] = args
			}
			list = append(list, item)
		}
		return s.cacheable(map[string]any{"prompts": list}, ex), nil
	case "prompts/get":
		name, _ := params["name"].(string)
		for _, p := range s.prompts {
			if p.Name != name {
				continue
			}
			args := map[string]string{}
			if given, ok := params["arguments"].(map[string]any); ok {
				for k, v := range given {
					if str, ok := v.(string); ok {
						args[k] = str
					}
				}
			}
			for _, a := range p.Arguments {
				if a.Required && args[a.Name] == "" {
					return nil, &rpcError{Code: InvalidParams, Message: fmt.Sprintf("prompt %s needs the argument %q", name, a.Name)}
				}
			}
			text, err := p.Get(args)
			if err != nil {
				return nil, &rpcError{Code: InvalidParams, Message: err.Error()}
			}
			return map[string]any{"description": p.Description, "messages": []any{
				map[string]any{"role": "user", "content": map[string]any{"type": "text", "text": text}}}}, nil
		}
		return nil, &rpcError{Code: InvalidParams, Message: fmt.Sprintf("unknown prompt %q", name)}
	case "completion/complete":
		if s.Complete == nil {
			break
		}
		ref, _ := params["ref"].(map[string]any)
		argument, _ := params["argument"].(map[string]any)
		refType, _ := ref["type"].(string)
		refName, _ := ref["name"].(string)
		if refType == "ref/resource" {
			refName, _ = ref["uri"].(string)
		}
		argName, _ := argument["name"].(string)
		prefix, _ := argument["value"].(string)
		values := s.Complete(refType, refName, argName, prefix)
		total := len(values)
		if len(values) > 100 {
			values = values[:100] // the protocol's limit per response
		}
		if values == nil {
			values = []string{}
		}
		return map[string]any{"completion": map[string]any{"values": values, "total": total, "hasMore": total > len(values)}}, nil
	case "subscriptions/listen":
		// Nothing this server lists changes while it runs, so it agrees to no notification type:
		// it acknowledges, then keeps the request open until the client closes it.
		if ex.emit != nil {
			ex.emit(acknowledgement(req.ID))
		}
		return map[string]any{"pending": true}, nil
	}
	if strings.HasPrefix(req.Method, "notifications/") && len(req.ID) == 0 {
		return nil, nil
	}
	return nil, &rpcError{Code: MethodNotFound, Message: fmt.Sprintf("method not found: %s", req.Method), status: 404}
}

// acknowledgement is the first message of a subscriptions/listen stream.
func acknowledgement(id json.RawMessage) notification {
	var subscription any
	_ = json.Unmarshal(id, &subscription)
	return notification{JSONRPC: "2.0", Method: "notifications/subscriptions/acknowledged",
		Params: map[string]any{"_meta": map[string]any{metaSubscriptionID: subscription}, "notifications": map[string]any{}}}
}

func describeResource(r Resource) map[string]any {
	item := map[string]any{"uri": r.URI, "name": r.Name, "description": r.Description, "mimeType": r.MimeType}
	if r.Title != "" {
		item["title"] = r.Title
	}
	return item
}

func (s *Server) findResource(uri string) *Resource {
	if s.resources != nil {
		for _, r := range s.resources() {
			if r.URI == uri {
				return &r
			}
		}
	}
	if s.ResolveResource != nil {
		if r, ok := s.ResolveResource(uri); ok {
			return r
		}
	}
	return nil
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
	// structured content must be an object for legacy clients
	var structured map[string]any
	if json.Unmarshal(data, &structured) == nil && structured != nil {
		out["structuredContent"] = structured
	}
	return out
}
