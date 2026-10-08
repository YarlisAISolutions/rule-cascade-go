package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// post sends a body to the handler (no network) and returns the status and the decoded reply.
func post(t *testing.T, h http.Handler, body string, headers map[string]string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8765/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("not JSON (%d): %q", w.Code, w.Body.String())
		}
	}
	return w.Code, out
}

func modernBody(method string, params map[string]any) string {
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": modern(params)})
	return string(data)
}

func modernHeaders(method, name string) map[string]string {
	h := map[string]string{"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": method}
	if name != "" {
		h["Mcp-Name"] = name
	}
	return h
}

func TestStreamableModern(t *testing.T) {
	h := testServer().Handler(HTTPOptions{})
	status, reply := post(t, h, modernBody("tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"text": "x"}}), modernHeaders("tools/call", "echo"))
	if status != 200 || result(t, reply)["resultType"] != "complete" {
		t.Fatalf("%d %v", status, reply)
	}
	// a name outside plain ASCII is sent base64-encoded
	encoded := "=?base64?" + base64.StdEncoding.EncodeToString([]byte("echo")) + "?="
	if status, reply := post(t, h, modernBody("tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"text": "x"}}), modernHeaders("tools/call", encoded)); status != 200 {
		t.Errorf("encoded Mcp-Name: %d %v", status, reply)
	}
	for _, tc := range []struct {
		name    string
		headers map[string]string
	}{
		{"wrong name", modernHeaders("tools/call", "other")},
		{"no name", modernHeaders("tools/call", "")},
		{"wrong method", modernHeaders("tools/list", "echo")},
		{"wrong version", map[string]string{"MCP-Protocol-Version": "2025-06-18", "Mcp-Method": "tools/call", "Mcp-Name": "echo"}},
	} {
		status, reply := post(t, h, modernBody("tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"text": "x"}}), tc.headers)
		if status != 400 || errorCode(reply) != HeaderMismatch {
			t.Errorf("%s: %d %v", tc.name, status, reply)
		}
	}
	if status, reply := post(t, h, modernBody("no/such", nil), modernHeaders("no/such", "")); status != 404 || errorCode(reply) != MethodNotFound {
		t.Errorf("unknown method: %d %v", status, reply)
	}
	bad := strings.Replace(modernBody("tools/list", nil), "2026-07-28", "1900-01-01", 1)
	if status, reply := post(t, h, bad, map[string]string{"MCP-Protocol-Version": "1900-01-01", "Mcp-Method": "tools/list"}); status != 400 || errorCode(reply) != UnsupportedProtocolVersion {
		t.Errorf("unsupported version: %d %v", status, reply)
	}
}

func TestStreamableLegacy(t *testing.T) {
	h := testServer().Handler(HTTPOptions{})
	status, reply := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`, nil)
	if status != 200 || result(t, reply)["protocolVersion"] != "2025-03-26" {
		t.Fatalf("%d %v", status, reply)
	}
	if status, _ := post(t, h, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, map[string]string{"MCP-Protocol-Version": "2025-03-26"}); status != 202 {
		t.Errorf("notification: %d", status)
	}
	if status, reply := post(t, h, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, map[string]string{"MCP-Protocol-Version": "2025-11-25"}); status != 200 || reply["result"] == nil {
		t.Errorf("legacy tools/list: %d %v", status, reply)
	}
	if status, _ := post(t, h, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, map[string]string{"MCP-Protocol-Version": "1900-01-01"}); status != 400 {
		t.Errorf("unknown version header: %d", status)
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/mcp", nil))
		if w.Code != 405 || w.Header().Get("Allow") != "POST" {
			t.Errorf("%s: %d", method, w.Code)
		}
	}
}

func TestHTTPGuards(t *testing.T) {
	h := testServer().Handler(HTTPOptions{Token: "secret", MaxBodyBytes: 200, AllowedOrigins: []string{"https://app.example"}})
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	if status, _ := post(t, h, ping, nil); status != 401 {
		t.Errorf("no token: %d", status)
	}
	auth := map[string]string{"Authorization": "Bearer secret"}
	if status, _ := post(t, h, ping, auth); status != 200 {
		t.Errorf("token: %d", status)
	}
	if status, _ := post(t, h, ping, map[string]string{"Authorization": "Bearer secret", "Origin": "https://evil.example"}); status != 403 {
		t.Errorf("foreign origin: %d", status)
	}
	if status, _ := post(t, h, ping, map[string]string{"Authorization": "Bearer secret", "Origin": "https://app.example"}); status != 200 {
		t.Errorf("allowed origin: %d", status)
	}
	// the server is reached on 127.0.0.1, so a loopback page (an inspector) may call it
	if status, _ := post(t, h, ping, map[string]string{"Authorization": "Bearer secret", "Origin": "http://localhost:6274"}); status != 200 {
		t.Errorf("loopback origin: %d", status)
	}
	if status, _ := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"`+strings.Repeat("x", 300)+`"}}`, auth); status != 413 {
		t.Errorf("too large: %d", status)
	}
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(ping))
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Errorf("content type: %d", w.Code)
	}
	for _, path := range []string{"/health", "/healthz"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 200 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
}

// streamRecorder is a ResponseWriter that may be read while the handler writes.
type streamRecorder struct {
	mu     sync.Mutex
	header http.Header
	body   strings.Builder
	code   int
}

func (s *streamRecorder) Header() http.Header { return s.header }
func (s *streamRecorder) WriteHeader(code int) {
	s.mu.Lock()
	s.code = code
	s.mu.Unlock()
}
func (s *streamRecorder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.body.Write(p)
}
func (s *streamRecorder) Flush() {}
func (s *streamRecorder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.body.String()
}

func waitFor(t *testing.T, rec *streamRecorder, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if out := rec.String(); strings.Contains(out, want) {
			return out
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no %q in %q", want, rec.String())
	return ""
}

func TestListenOverHTTP(t *testing.T) {
	h := testServer().Handler(HTTPOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(modernBody("subscriptions/listen", map[string]any{"notifications": map[string]any{}}))).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	for k, v := range modernHeaders("subscriptions/listen", "") {
		r.Header.Set(k, v)
	}
	rec := &streamRecorder{header: http.Header{}}
	done := make(chan struct{})
	go func() { h.ServeHTTP(rec, r); close(done) }()
	waitFor(t, rec, "notifications/subscriptions/acknowledged")
	if rec.header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("content type %q", rec.header.Get("Content-Type"))
	}
	cancel()
	<-done
}

// The deprecated HTTP+SSE transport: the stream names the endpoint, answers arrive on the stream.
func TestLegacySSE(t *testing.T) {
	h := testServer().Handler(HTTPOptions{SSE: true})
	ctx, cancel := context.WithCancel(context.Background())
	rec := &streamRecorder{header: http.Header{}}
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sse", nil).WithContext(ctx))
		close(done)
	}()
	out := waitFor(t, rec, "event: endpoint")
	endpoint := strings.TrimSpace(strings.SplitN(strings.SplitN(out, "data: ", 2)[1], "\n", 2)[0])
	r := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Errorf("message: %d", w.Code)
	}
	waitFor(t, rec, `"protocolVersion":"2024-11-05"`)
	cancel()
	<-done
	r = httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"ping"}`))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Errorf("a closed session: %d", w.Code)
	}
}
