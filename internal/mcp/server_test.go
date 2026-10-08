package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testServer() *Server {
	s := NewServer("test", "1.0.0")
	s.Instructions = "use echo"
	s.AddTool(Tool{Name: "echo", ReadOnly: true, Description: "Echo the text.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}},
		Call: func(args map[string]any) (any, error) {
			if args["text"] == "fail" {
				return nil, errors.New("failed on purpose")
			}
			return map[string]any{"echo": args["text"]}, nil
		}})
	s.SetResources(func() []Resource {
		return []Resource{{URI: "test://a", Name: "a", MimeType: "text/plain", Read: func() (string, error) { return "A", nil }}}
	})
	s.AddResourceTemplate(ResourceTemplate{URITemplate: "test://t/{x}", Name: "t", MimeType: "text/plain"})
	s.ResolveResource = func(uri string) (*Resource, bool) {
		if x, ok := strings.CutPrefix(uri, "test://t/"); ok {
			return &Resource{URI: uri, MimeType: "text/plain", Read: func() (string, error) { return "T" + x, nil }}, true
		}
		return nil, false
	}
	s.AddPrompt(Prompt{Name: "greet", Arguments: []PromptArgument{{Name: "who", Required: true}},
		Get: func(a map[string]string) (string, error) { return "hello " + a["who"], nil }})
	s.Complete = func(refType, ref, argument, prefix string) []string {
		var out []string
		for _, v := range []string{"alpha", "beta", "alto"} {
			if strings.HasPrefix(v, prefix) {
				out = append(out, v)
			}
		}
		return out
	}
	return s
}

var modernMeta = map[string]any{metaProtocolVersion: "2026-07-28", metaClientCapabilities: map[string]any{}}

// ask sends one request and returns the decoded response.
func ask(t *testing.T, s *Server, method string, params map[string]any) map[string]any {
	t.Helper()
	msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	reply := s.Handle(msg)
	data, _ := json.Marshal(reply)
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return out
}

func modern(params map[string]any) map[string]any {
	out := map[string]any{"_meta": modernMeta}
	for k, v := range params {
		out[k] = v
	}
	return out
}

func result(t *testing.T, reply map[string]any) map[string]any {
	t.Helper()
	r, ok := reply["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", reply)
	}
	return r
}

func errorCode(reply map[string]any) float64 {
	e, _ := reply["error"].(map[string]any)
	code, _ := e["code"].(float64)
	return code
}

func TestDiscover(t *testing.T) {
	r := result(t, ask(t, testServer(), "server/discover", modern(nil)))
	if r["resultType"] != "complete" || r["instructions"] != "use echo" || r["cacheScope"] != "private" || r["ttlMs"] == nil {
		t.Errorf("discover: %v", r)
	}
	versions := r["supportedVersions"].([]any)
	if versions[0] != "2026-07-28" || len(versions) != len(ProtocolVersions) {
		t.Errorf("versions %v", versions)
	}
	caps := r["capabilities"].(map[string]any)
	for _, c := range []string{"tools", "resources", "prompts", "completions", "extensions"} {
		if caps[c] == nil {
			t.Errorf("capability %s missing: %v", c, caps)
		}
	}
	if info := r["_meta"].(map[string]any)[metaServerInfo].(map[string]any); info["name"] != "test" {
		t.Errorf("serverInfo %v", info)
	}
}

func TestModernRequestsNeedTheirMeta(t *testing.T) {
	s := testServer()
	if code := errorCode(ask(t, s, "server/discover", nil)); code != InvalidParams {
		t.Errorf("discover without _meta: %v", code)
	}
	noCaps := map[string]any{"_meta": map[string]any{metaProtocolVersion: "2026-07-28"}}
	if code := errorCode(ask(t, s, "tools/list", noCaps)); code != InvalidParams {
		t.Errorf("without client capabilities: %v", code)
	}
	reply := ask(t, s, "tools/list", map[string]any{"_meta": map[string]any{metaProtocolVersion: "1900-01-01", metaClientCapabilities: map[string]any{}}})
	if errorCode(reply) != UnsupportedProtocolVersion {
		t.Fatalf("unknown version: %v", reply)
	}
	data := reply["error"].(map[string]any)["data"].(map[string]any)
	if data["requested"] != "1900-01-01" || len(data["supported"].([]any)) != len(ProtocolVersions) {
		t.Errorf("data %v", data)
	}
	// removed in 2026-07-28, still served to legacy clients
	if code := errorCode(ask(t, s, "ping", modern(nil))); code != MethodNotFound {
		t.Errorf("modern ping: %v", code)
	}
	if r := ask(t, s, "ping", nil); r["error"] != nil {
		t.Errorf("legacy ping: %v", r)
	}
}

func TestBothEras(t *testing.T) {
	s := testServer()
	init := result(t, ask(t, s, "initialize", map[string]any{"protocolVersion": "2025-06-18"}))
	if init["protocolVersion"] != "2025-06-18" || init["resultType"] != nil {
		t.Errorf("initialize: %v", init)
	}
	if v := result(t, ask(t, s, "initialize", map[string]any{"protocolVersion": "2026-07-28"}))["protocolVersion"]; v != LegacyVersions[0] {
		t.Errorf("initialize with a modern version negotiates the newest legacy one, got %v", v)
	}
	for _, params := range []map[string]any{nil, modern(nil)} {
		isModern := params != nil
		tools := result(t, ask(t, s, "tools/list", params))
		if (tools["resultType"] == "complete") != isModern || (tools["ttlMs"] != nil) != isModern {
			t.Errorf("modern=%v tools/list: %v", isModern, tools)
		}
		ann := tools["tools"].([]any)[0].(map[string]any)["annotations"].(map[string]any)
		if ann["readOnlyHint"] != true || ann["idempotentHint"] != true {
			t.Errorf("annotations %v", ann)
		}
		call := result(t, ask(t, s, "tools/call", merge(params, map[string]any{"name": "echo", "arguments": map[string]any{"text": "hi"}})))
		if call["structuredContent"].(map[string]any)["echo"] != "hi" {
			t.Errorf("call %v", call)
		}
		failed := result(t, ask(t, s, "tools/call", merge(params, map[string]any{"name": "echo", "arguments": map[string]any{"text": "fail"}})))
		if failed["isError"] != true {
			t.Errorf("a failing tool is a tool error: %v", failed)
		}
		missing := result(t, ask(t, s, "tools/call", merge(params, map[string]any{"name": "echo", "arguments": map[string]any{}})))
		if missing["isError"] != true {
			t.Errorf("a missing argument is a tool error: %v", missing)
		}
		read := result(t, ask(t, s, "resources/read", merge(params, map[string]any{"uri": "test://t/7"})))
		if read["contents"].([]any)[0].(map[string]any)["text"] != "T7" {
			t.Errorf("template read %v", read)
		}
		want := float64(ResourceNotFoundLegacy)
		if isModern {
			want = InvalidParams
		}
		if code := errorCode(ask(t, s, "resources/read", merge(params, map[string]any{"uri": "test://none"}))); code != want {
			t.Errorf("modern=%v not found: %v, want %v", isModern, code, want)
		}
	}
}

func merge(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func TestPromptsTemplatesCompletion(t *testing.T) {
	s := testServer()
	prompts := result(t, ask(t, s, "prompts/list", modern(nil)))["prompts"].([]any)
	if len(prompts) != 1 || prompts[0].(map[string]any)["name"] != "greet" {
		t.Errorf("prompts %v", prompts)
	}
	got := result(t, ask(t, s, "prompts/get", modern(map[string]any{"name": "greet", "arguments": map[string]any{"who": "you"}})))
	if text := got["messages"].([]any)[0].(map[string]any)["content"].(map[string]any)["text"]; text != "hello you" {
		t.Errorf("prompt text %v", text)
	}
	if code := errorCode(ask(t, s, "prompts/get", modern(map[string]any{"name": "greet"}))); code != InvalidParams {
		t.Errorf("a required prompt argument: %v", code)
	}
	templates := result(t, ask(t, s, "resources/templates/list", modern(nil)))["resourceTemplates"].([]any)
	if len(templates) != 1 {
		t.Errorf("templates %v", templates)
	}
	c := result(t, ask(t, s, "completion/complete", modern(map[string]any{"ref": map[string]any{"type": "ref/prompt", "name": "greet"},
		"argument": map[string]any{"name": "who", "value": "al"}})))["completion"].(map[string]any)
	if values := c["values"].([]any); len(values) != 2 || c["hasMore"] != false {
		t.Errorf("completion %v", c)
	}
}

// On standard input and output, subscriptions/listen is acknowledged and stays open.
func TestServeListen(t *testing.T) {
	listen, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 5, "method": "subscriptions/listen",
		"params": modern(map[string]any{"notifications": map[string]any{"toolsListChanged": true}})})
	var out strings.Builder
	in := string(listen) + "\n" + `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":5}}` + "\n"
	if err := testServer().Serve(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "notifications/subscriptions/acknowledged") || !strings.Contains(lines[0], `"io.modelcontextprotocol/subscriptionId":5`) {
		t.Errorf("listen wrote %q", out.String())
	}
}
