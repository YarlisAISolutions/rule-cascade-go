package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Defaults of the HTTP transport. Each one is an option of HTTPOptions (and a flag or an
// environment variable of 'rcas mcp').
const (
	DefaultPath           = "/mcp"
	DefaultMaxBodyBytes   = 4 << 20 // a request: rulesets and schemas sent inline fit easily
	DefaultMaxSSESessions = 256     // open streams of the deprecated HTTP+SSE transport
	DefaultKeepAlive      = 25 * time.Second
)

// HTTPOptions configures Handler.
type HTTPOptions struct {
	// Path is the Streamable HTTP endpoint (default /mcp).
	Path string
	// AllowedOrigins are the browser origins that may call the server ("*" for any). A request
	// without Origin (every non-browser client) is always allowed; a request from a loopback
	// origin is allowed when the server itself is reached on a loopback address.
	AllowedOrigins []string
	// Token, when set, is required as "Authorization: Bearer <token>".
	Token string
	// MaxBodyBytes caps a request body (default DefaultMaxBodyBytes).
	MaxBodyBytes int64
	// SSE serves the deprecated HTTP+SSE transport (revision 2024-11-05) at /sse and /messages.
	SSE bool
	// MaxSSESessions caps the open HTTP+SSE streams (default DefaultMaxSSESessions).
	MaxSSESessions int
	// KeepAlive is the interval of comment lines on open streams (default DefaultKeepAlive).
	KeepAlive time.Duration
	// Landing is the plain text answered to GET / (nothing when empty).
	Landing string
}

func (o *HTTPOptions) defaults() {
	if o.Path == "" {
		o.Path = DefaultPath
	}
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if o.MaxSSESessions <= 0 {
		o.MaxSSESessions = DefaultMaxSSESessions
	}
	if o.KeepAlive <= 0 {
		o.KeepAlive = DefaultKeepAlive
	}
}

// Handler serves the protocol over HTTP: Streamable HTTP at opts.Path for modern and legacy
// clients alike (no sessions: every POST is answered on its own), and, with opts.SSE, the
// deprecated HTTP+SSE transport. GET /health and GET /healthz answer ok (Cloud Run keeps /healthz
// for itself on public URLs).
func (s *Server) Handler(opts HTTPOptions) http.Handler {
	opts.defaults()
	h := &httpServer{s: s, opts: opts, sessions: map[string]*sseSession{}}
	mux := http.NewServeMux()
	mux.HandleFunc(opts.Path, h.guard(h.streamable))
	if opts.SSE {
		mux.HandleFunc("/sse", h.guard(h.sseStream))
		mux.HandleFunc("/messages", h.guard(h.sseMessage))
	}
	health := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "ok\n")
	}
	mux.HandleFunc("/health", health)
	mux.HandleFunc("/healthz", health)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" || opts.Landing == "" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, opts.Landing)
	})
	return mux
}

type httpServer struct {
	s    *Server
	opts HTTPOptions

	mu       sync.Mutex
	sessions map[string]*sseSession
}

type sseSession struct {
	out  chan []byte
	done chan struct{}
}

// The headers a browser client may send and read.
const (
	allowHeaders  = "Content-Type, Authorization, Accept, MCP-Protocol-Version, Mcp-Method, Mcp-Name, Mcp-Session-Id, Last-Event-ID"
	exposeHeaders = "Mcp-Session-Id, WWW-Authenticate"
)

// guard checks Origin (DNS rebinding) and the token, and answers CORS preflights.
func (h *httpServer) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			if !h.originAllowed(origin, r) {
				writeJSON(w, http.StatusForbidden, response{JSONRPC: "2.0", Error: &rpcError{Code: InvalidRequest, Message: "origin not allowed: " + origin}})
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Expose-Headers", exposeHeaders)
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "POST, GET, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", allowHeaders)
				w.Header().Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		if h.opts.Token != "" {
			given := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if subtle.ConstantTimeCompare([]byte(given), []byte(h.opts.Token)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="rcas mcp"`)
				writeJSON(w, http.StatusUnauthorized, response{JSONRPC: "2.0", Error: &rpcError{Code: InvalidRequest, Message: "a bearer token is required"}})
				return
			}
		}
		next(w, r)
	}
}

func (h *httpServer) originAllowed(origin string, r *http.Request) bool {
	for _, allowed := range h.opts.AllowedOrigins {
		if allowed == "*" || strings.EqualFold(strings.TrimRight(allowed, "/"), origin) {
			return true
		}
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return isLoopback(u.Hostname()) && isLoopback(hostOnly(r.Host))
}

func hostOnly(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host
	}
	return strings.Trim(hostport, "[]")
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// readBody reads a request body within the limit; it has answered the request when it fails.
func (h *httpServer) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, response{JSONRPC: "2.0", Error: &rpcError{Code: InvalidRequest, Message: "Content-Type must be application/json"}})
		return nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.opts.MaxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, response{JSONRPC: "2.0", Error: &rpcError{Code: InvalidRequest,
				Message: fmt.Sprintf("the request is larger than %d bytes", h.opts.MaxBodyBytes)}})
			return nil, false
		}
		writeJSON(w, http.StatusBadRequest, response{JSONRPC: "2.0", Error: &rpcError{Code: ParseError, Message: err.Error()}})
		return nil, false
	}
	return bytes.TrimSpace(body), true
}

// streamable is the Streamable HTTP endpoint.
func (h *httpServer) streamable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// no standalone stream and no sessions: GET and DELETE are not offered
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, response{JSONRPC: "2.0", Error: &rpcError{Code: InvalidRequest, Message: "use POST"}})
		return
	}
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}
	if len(body) > 0 && body[0] == '[' {
		// a batch: legacy revisions 2025-03-26 and earlier only
		reply := h.s.handle(r.Context(), body, nil)
		if reply == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, reply)
		return
	}
	req, bad := parseRequest(body)
	if bad != nil {
		writeJSON(w, http.StatusBadRequest, bad)
		return
	}
	params := map[string]any{}
	_ = json.Unmarshal(req.Params, &params)
	modern, eraErr := era(req.Method, params)
	if eraErr == nil {
		if modern {
			eraErr = checkHeaders(r, req, params)
		} else if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !contains(ProtocolVersions, v) {
			eraErr = &rpcError{Code: InvalidRequest, Message: "unsupported MCP-Protocol-Version " + v, status: 400,
				Data: map[string]any{"supported": ProtocolVersions, "requested": v}}
		}
	}
	if eraErr != nil {
		writeJSON(w, statusOf(eraErr), response{JSONRPC: "2.0", ID: req.ID, Error: eraErr})
		return
	}
	if req.Method == "subscriptions/listen" && len(req.ID) > 0 {
		h.listen(w, r, body)
		return
	}
	reply := h.s.handleOne(r.Context(), body, nil)
	if reply == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	status := http.StatusOK
	if reply.Error != nil {
		status = statusOf(reply.Error)
	}
	writeJSON(w, status, reply)
}

func statusOf(e *rpcError) int {
	if e.status != 0 {
		return e.status
	}
	return http.StatusOK
}

// checkHeaders validates the headers a modern request mirrors from its body.
func checkHeaders(r *http.Request, req request, params map[string]any) *rpcError {
	mismatch := func(format string, a ...any) *rpcError {
		return &rpcError{Code: HeaderMismatch, Message: "Header mismatch: " + fmt.Sprintf(format, a...), status: 400}
	}
	version, _ := requestMeta(params)[metaProtocolVersion].(string)
	if got := r.Header.Get("MCP-Protocol-Version"); got != version {
		return mismatch("MCP-Protocol-Version header value %q does not match body value %q", got, version)
	}
	if got := r.Header.Get("Mcp-Method"); got != req.Method {
		return mismatch("Mcp-Method header value %q does not match body value %q", got, req.Method)
	}
	var want string
	switch req.Method {
	case "tools/call", "prompts/get":
		want, _ = params["name"].(string)
	case "resources/read":
		want, _ = params["uri"].(string)
	default:
		return nil
	}
	raw := r.Header.Get("Mcp-Name")
	if raw == "" {
		return mismatch("the Mcp-Name header is required for %s", req.Method)
	}
	got, ok := decodeHeaderValue(raw)
	if !ok {
		return mismatch("the Mcp-Name header is not valid")
	}
	if got != want {
		return mismatch("Mcp-Name header value %q does not match body value %q", got, want)
	}
	return nil
}

// decodeHeaderValue undoes the =?base64?...?= encoding of a mirrored header value.
func decodeHeaderValue(v string) (string, bool) {
	if strings.HasPrefix(v, "=?base64?") && strings.HasSuffix(v, "?=") && len(v) >= len("=?base64??=") {
		data, err := base64.StdEncoding.DecodeString(v[len("=?base64?") : len(v)-2])
		if err != nil {
			return "", false
		}
		return string(data), true
	}
	for _, c := range v {
		if (c < 0x20 && c != '\t') || c > 0x7e {
			return "", false
		}
	}
	return v, true
}

func startStream(w http.ResponseWriter) (http.Flusher, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, response{JSONRPC: "2.0", Error: &rpcError{Code: InternalError, Message: "streaming is not supported here"}})
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return flusher, true
}

// listen answers subscriptions/listen with a stream that stays open until the client closes it.
func (h *httpServer) listen(w http.ResponseWriter, r *http.Request, body []byte) {
	flusher, ok := startStream(w)
	if !ok {
		return
	}
	var mu sync.Mutex
	emit := func(v any) {
		data, _ := json.Marshal(v)
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
		flusher.Flush()
	}
	if reply := h.s.handleOne(r.Context(), body, emit); reply != nil {
		emit(reply)
		return
	}
	h.keepAlive(r.Context(), nil, func() {
		mu.Lock()
		defer mu.Unlock()
		io.WriteString(w, ":\n\n")
		flusher.Flush()
	})
}

func (h *httpServer) keepAlive(ctx context.Context, done <-chan struct{}, ping func()) {
	ticker := time.NewTicker(h.opts.KeepAlive)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			ping()
		}
	}
}

// sseStream is the stream of the deprecated HTTP+SSE transport: the first event names the URL to
// POST messages to, and every answer arrives on this stream.
func (h *httpServer) sseStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "use GET", http.StatusMethodNotAllowed)
		return
	}
	h.mu.Lock()
	if len(h.sessions) >= h.opts.MaxSSESessions {
		h.mu.Unlock()
		http.Error(w, "too many open streams; use the Streamable HTTP endpoint "+h.opts.Path, http.StatusServiceUnavailable)
		return
	}
	id := newSessionID()
	session := &sseSession{out: make(chan []byte, 16), done: make(chan struct{})}
	h.sessions[id] = session
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.sessions, id)
		h.mu.Unlock()
		close(session.done)
	}()
	flusher, ok := startStream(w)
	if !ok {
		return
	}
	fmt.Fprintf(w, "event: endpoint\ndata: /messages?sessionId=%s\n\n", id)
	flusher.Flush()
	ticker := time.NewTicker(h.opts.KeepAlive)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-session.out:
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
			flusher.Flush()
		case <-ticker.C:
			io.WriteString(w, ":\n\n")
			flusher.Flush()
		}
	}
}

// sseMessage takes a message of the deprecated HTTP+SSE transport; the answer goes to the stream.
func (h *httpServer) sseMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	h.mu.Lock()
	session := h.sessions[r.URL.Query().Get("sessionId")]
	h.mu.Unlock()
	if session == nil {
		http.Error(w, "unknown or closed session: open /sse first", http.StatusNotFound)
		return
	}
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}
	send := func(v any) {
		data, err := json.Marshal(v)
		if err != nil {
			return
		}
		select {
		case session.out <- data:
		case <-session.done:
		case <-r.Context().Done():
		}
	}
	if reply := h.s.handle(r.Context(), body, send); reply != nil {
		send(reply)
	}
	w.WriteHeader(http.StatusAccepted)
	io.WriteString(w, "Accepted")
}

func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
