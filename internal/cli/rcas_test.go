package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"rules.sdods.com/go/internal/proposals"
)

func newProposal(id string) *proposals.Proposal {
	return &proposals.Proposal{ID: id, Title: id, Origin: "test"}
}

// in runs a command line in a directory, as rcas.
func in(t *testing.T, dir string, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errs bytes.Buffer
	status := Main("rcas", append([]string{"-C", dir}, args...), strings.NewReader(stdin), &out, &errs)
	return status, out.String(), errs.String()
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A new project passes check, compiles, and its bundle evaluates.
func TestInitCheckCompileEvaluate(t *testing.T) {
	dir := t.TempDir()
	if status, out, errs := in(t, dir, "", "init", "--name", "Acme Shop", "--lang", "ts,python,java,go,other", "--ci", "github"); status != 0 {
		t.Fatalf("init: %d\n%s\n%s", status, out, errs)
	}
	for _, f := range []string{"rcas.yaml", "rules/example.ruleset.yaml", "rules/order.schema.json", "rules/LOADING.md", ".rcas/.gitignore", ".github/workflows/rules.yml", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err != nil {
			t.Errorf("init did not write %s", f)
		}
	}
	if status, out, _ := in(t, dir, "", "check"); status != 0 || !strings.Contains(out, "3 golden tests, 0 failed") {
		t.Fatalf("check: %d\n%s", status, out)
	}
	if status, out, errs := in(t, dir, "", "compile", "--all"); status != 0 {
		t.Fatalf("compile --all: %d\n%s%s", status, out, errs)
	}
	bundle := filepath.Join(dir, "build", "rules", "acme.shop.example.bundle.json")
	status, out, errs := in(t, dir, `{"entity":"Order","operation":"create","data":{"quantity":11}}`, "evaluate", "--bundle", bundle, "-")
	if status != 0 || !strings.Contains(out, `"decision": "deny"`) {
		t.Fatalf("evaluate: %d\n%s%s", status, out, errs)
	}
	if _, err := os.Stat(filepath.Join(dir, "build", "rules", "acme.shop.example.client.manifest.json")); err != nil {
		t.Error("compile --all did not write the client manifest")
	}
	// a second init changes nothing and replaces nothing
	before := readFile(t, filepath.Join(dir, "rcas.yaml"))
	os.WriteFile(filepath.Join(dir, "rcas.yaml"), []byte(before+"# mine\n"), 0o644)
	if _, out, _ := in(t, dir, "", "init", "--name", "Acme Shop"); !strings.Contains(out, "kept") {
		t.Errorf("init over an edited file: %s", out)
	}
	if !strings.HasSuffix(readFile(t, filepath.Join(dir, "rcas.yaml")), "# mine\n") {
		t.Error("init replaced an edited rcas.yaml without --force")
	}
}

func TestInitRefusesABadPrefixAndDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	if status, _, _ := in(t, dir, "", "init", "--id-prefix", "Bad Prefix"); status != 2 {
		t.Errorf("a bad prefix: status %d", status)
	}
	if status, out, _ := in(t, dir, "", "init", "--dry-run"); status != 0 || !strings.Contains(out, "would be created") {
		t.Errorf("dry run: %d %s", status, out)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("--dry-run wrote %d entries", len(entries))
	}
}

func TestConfigIsFoundUpwardsAndValidated(t *testing.T) {
	dir := t.TempDir()
	in(t, dir, "", "init", "--name", "Example", "--ci", "none")
	sub := filepath.Join(dir, "src", "deep")
	os.MkdirAll(sub, 0o755)
	if status, out, _ := in(t, sub, "", "check"); status != 0 || !strings.Contains(out, "0 failed") {
		t.Errorf("check from a subdirectory: %d %s", status, out)
	}
	os.WriteFile(filepath.Join(dir, "rcas.yaml"), []byte("rcas: 1\nunknown: true\n"), 0o644)
	if status, _, errs := in(t, sub, "", "check"); status != exitConfig || !strings.Contains(errs, "unknown") {
		t.Errorf("an unknown key: %d %s", status, errs)
	}
	empty := t.TempDir()
	if status, _, _ := in(t, empty, "", "check"); status != exitUsage {
		t.Errorf("check with neither files nor rcas.yaml: %d", status)
	}
}

// agent install writes managed blocks into existing files without touching the rest, and is
// idempotent.
func TestAgentInstall(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Our agents\n\nKeep this.\n"), 0o644)
	if status, out, errs := in(t, dir, "", "agent", "install"); status != 0 {
		t.Fatalf("%d %s %s", status, out, errs)
	}
	agents := readFile(t, filepath.Join(dir, "AGENTS.md"))
	if !strings.HasPrefix(agents, "# Our agents\n\nKeep this.\n") || !strings.Contains(agents, blockBegin) || strings.Count(agents, blockBegin) != 1 {
		t.Errorf("AGENTS.md:\n%s", agents)
	}
	for _, f := range []string{"CLAUDE.md", "GEMINI.md", ".github/copilot-instructions.md", ".cursor/rules/rules-cascade.mdc",
		".claude/agents/rcas-author.md", ".claude/skills/rules-cascade/SKILL.md", ".agents/skills/rules-cascade/SKILL.md"} {
		data := readFile(t, filepath.Join(dir, filepath.FromSlash(f)))
		if strings.Contains(data, "{{") {
			t.Errorf("%s has an unfilled placeholder", f)
		}
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, "CLAUDE.md")), "@AGENTS.md") {
		t.Error("CLAUDE.md does not import AGENTS.md")
	}
	// again: nothing changes
	_, out, _ := in(t, dir, "", "agent", "install")
	if strings.Contains(out, "created") || strings.Contains(out, "updated") {
		t.Errorf("a second install changed files:\n%s", out)
	}
	if strings.Count(readFile(t, filepath.Join(dir, "AGENTS.md")), blockBegin) != 1 {
		t.Error("a second install added a second block")
	}
	if status, _, _ := in(t, dir, "", "agent", "install", "--for", "emacs"); status != 2 {
		t.Error("an unknown tool was accepted")
	}
}

// mcp install merges: other servers stay, an existing entry is kept unless --force.
func TestMCPInstallMerges(t *testing.T) {
	dir := t.TempDir()
	in(t, dir, "", "init", "--name", "Example", "--ci", "none")
	os.MkdirAll(filepath.Join(dir, ".cursor"), 0o755)
	os.WriteFile(filepath.Join(dir, ".cursor", "mcp.json"), []byte(`{"mcpServers":{"other":{"command":"x"}},"keep":1}`), 0o644)
	if status, out, errs := in(t, dir, "", "mcp", "install", "cursor", "vscode", "gemini", "--file"); status != 0 {
		t.Fatalf("%d %s %s", status, out, errs)
	}
	var cursor map[string]any
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, ".cursor", "mcp.json"))), &cursor); err != nil {
		t.Fatal(err)
	}
	servers := cursor["mcpServers"].(map[string]any)
	if servers["other"] == nil || servers["rules-cascade"] == nil || cursor["keep"] == nil {
		t.Errorf("cursor config after merge: %v", cursor)
	}
	vscode := readFile(t, filepath.Join(dir, ".vscode", "mcp.json"))
	if !strings.Contains(vscode, `"servers"`) || !strings.Contains(vscode, `"stdio"`) {
		t.Errorf("vscode: %s", vscode)
	}
	_, out, _ := in(t, dir, "", "mcp", "install", "cursor", "--file")
	if !strings.Contains(out, "unchanged") {
		t.Errorf("second install: %s", out)
	}
	_, out, _ = in(t, dir, "", "mcp", "install", "cursor", "--file", "--name", "rules-cascade", "--command", "binary")
	if !strings.Contains(out, "kept") {
		t.Errorf("a different entry was not kept: %s", out)
	}
	if _, out, _ = in(t, dir, "", "mcp", "install", "cursor", "--file", "--command", "binary", "--force"); !strings.Contains(out, "replaced") {
		t.Errorf("--force: %s", out)
	}
	os.WriteFile(filepath.Join(dir, ".gemini", "settings.json"), []byte("// comment\n{}"), 0o644)
	if status, _, errs := in(t, dir, "", "mcp", "install", "gemini", "--file", "--force"); status == 0 || !strings.Contains(errs, "comments") {
		t.Errorf("a file with comments: %d %s", status, errs)
	}
}

func TestMCPInstallCodexTOML(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", home)
	original := "model = \"x\"\n\n[mcp_servers.other]\ncommand = \"foo\"\n\n[profiles.a]\nmodel = \"y\"\n"
	os.WriteFile(filepath.Join(home, "config.toml"), []byte(original), 0o644)
	if status, out, errs := in(t, dir, "", "mcp", "install", "codex", "--file"); status != 0 {
		t.Fatalf("%d %s %s", status, out, errs)
	}
	got := readFile(t, filepath.Join(home, "config.toml"))
	if !strings.HasPrefix(got, original) || !strings.Contains(got, "[mcp_servers.rules-cascade]\ncommand = ") {
		t.Errorf("config.toml:\n%s", got)
	}
	in(t, dir, "", "mcp", "install", "codex", "--file", "--command", "binary", "--force")
	got = readFile(t, filepath.Join(home, "config.toml"))
	if strings.Count(got, "[mcp_servers.rules-cascade]") != 1 || !strings.Contains(got, "[profiles.a]\nmodel = \"y\"") || strings.Contains(got, `"@rules-cascade/cli"`) {
		t.Errorf("config.toml after --force:\n%s", got)
	}
}

func TestMCPInstallPrint(t *testing.T) {
	dir := t.TempDir()
	status, out, _ := in(t, dir, "", "mcp", "install", "all", "--print")
	if status != 0 {
		t.Fatal(status)
	}
	for _, want := range []string{"Claude Code", "claude mcp add -s project rules-cascade --", "[mcp_servers.rules-cascade]", "Cursor", "Windsurf", "Gemini CLI", `"servers"`} {
		if !strings.Contains(out, want) {
			t.Errorf("--print lacks %q", want)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Error("--print wrote files")
	}
}

// The MCP server answers the protocol, checks and evaluates, and propose_ruleset writes only a
// proposal; accept is a person's command.
func TestMCPServer(t *testing.T) {
	dir := t.TempDir()
	in(t, dir, "", "init", "--name", "Example", "--ci", "none")
	content := strings.Replace(readFile(t, filepath.Join(dir, "rules", "example.ruleset.yaml")), "version: 0.1.0", "version: 0.2.0", 1)
	call := func(id int, name string, args map[string]any) string {
		msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		return string(msg)
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		call(3, "check", map[string]any{}),
		call(4, "evaluate_rules", map[string]any{"ruleset": "example.example", "entity": "Order", "operation": "create", "data_json": `{"quantity":11}`}),
		call(5, "propose_ruleset", map[string]any{"id": "bump", "title": "Bump", "files": []any{map[string]any{"path": "rules/example.ruleset.yaml", "content": content}}}),
		call(6, "propose_ruleset", map[string]any{"id": "escape", "title": "x", "files": []any{map[string]any{"path": "../x.ruleset.yaml", "content": "x"}}}),
		call(7, "get_spec_section", map[string]any{"section": "13"}),
		call(8, "evaluate_rules", map[string]any{"ruleset": "example.example"}),
		`{"jsonrpc":"2.0","id":9,"method":"no/such"}`,
		`{oops`,
	}, "\n") + "\n"
	status, out, errs := in(t, dir, input, "mcp", "--root", dir)
	if status != 0 || errs != "" {
		t.Fatalf("%d %s", status, errs)
	}
	replies := map[float64]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not JSON on stdout: %q", line)
		}
		id, _ := m["id"].(float64)
		replies[id] = m
	}
	if v := replies[1]["result"].(map[string]any)["protocolVersion"]; v != "2025-03-26" {
		t.Errorf("negotiated %v", v)
	}
	tools := replies[2]["result"].(map[string]any)["tools"].([]any)
	if len(tools) < 15 {
		t.Errorf("%d tools", len(tools))
	}
	text := func(id float64) (string, bool) {
		r := replies[id]["result"].(map[string]any)
		isErr, _ := r["isError"].(bool)
		return r["content"].([]any)[0].(map[string]any)["text"].(string), isErr
	}
	if s, isErr := text(3); isErr || !strings.Contains(s, `"ok": true`) {
		t.Errorf("check: %s", s)
	}
	if s, isErr := text(4); isErr || !strings.Contains(s, `"decision": "deny"`) {
		t.Errorf("evaluate: %s", s)
	}
	if s, isErr := text(5); isErr || !strings.Contains(s, `"status": "pending"`) {
		t.Errorf("propose: %s", s)
	}
	if s, isErr := text(6); !isErr || !strings.Contains(s, "leaves the project") {
		t.Errorf("escape: %s", s)
	}
	if s, _ := text(7); !strings.Contains(s, "## 13. Engine protocol") {
		t.Errorf("spec 13: %.80s", s)
	}
	if s, isErr := text(8); !isErr || !strings.Contains(s, "missing required") {
		t.Errorf("missing args: %s", s)
	}
	if replies[9]["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Error("unknown method")
	}
	if replies[0]["error"].(map[string]any)["code"].(float64) != -32700 {
		t.Error("parse error")
	}
	// the rules did not change; accepting does
	if strings.Contains(readFile(t, filepath.Join(dir, "rules", "example.ruleset.yaml")), "0.2.0") {
		t.Fatal("propose_ruleset wrote into the project")
	}
	if status, out, errs := in(t, dir, "", "proposals", "accept", "bump"); status != 0 {
		t.Fatalf("accept: %s %s", out, errs)
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, "rules", "example.ruleset.yaml")), "0.2.0") {
		t.Error("accept did not write")
	}
	if status, _, _ := in(t, dir, "", "proposals", "accept", "bump"); status == 0 {
		t.Error("accepted twice")
	}
	// read-only leaves out propose_ruleset
	_, out, _ = in(t, dir, "", "mcp", "--read-only", "--list-tools")
	if strings.Contains(out, "propose_ruleset") {
		t.Error("--read-only lists propose_ruleset")
	}
}

func TestProposalConflictAndInvalid(t *testing.T) {
	dir := t.TempDir()
	in(t, dir, "", "init", "--name", "Example", "--ci", "none")
	c := &cli{program: "rcas", stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, dir: dir}
	path := "rules/example.ruleset.yaml"
	original := readFile(t, filepath.Join(dir, path))
	if _, err := c.propose(newProposal("change"), map[string][]byte{path: []byte(strings.Replace(original, "0.1.0", "0.3.0", 1))}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, path), []byte(original+"\n"), 0o644) // someone edited it meanwhile
	if status, _, errs := in(t, dir, "", "proposals", "accept", "change"); status == 0 || !strings.Contains(errs, "changed since") {
		t.Errorf("conflict: %d %s", status, errs)
	}
	p, err := c.propose(newProposal("broken"), map[string][]byte{"rules/broken.ruleset.yaml": []byte("ruleCascade: 1.0.0\nkind: RuleSet\n")})
	if err != nil || p.Status != "invalid" {
		t.Fatalf("an invalid proposal: %v %+v", err, p)
	}
	if status, _, _ := in(t, dir, "", "proposals", "accept", "broken"); status == 0 {
		t.Error("an invalid proposal was accepted without --force")
	}
	if status, out, _ := in(t, dir, "", "proposals", "list", "--json"); status != 0 || !strings.Contains(out, `"broken"`) {
		t.Errorf("list: %s", out)
	}
	if status, _, _ := in(t, dir, "", "proposals", "reject", "change", "--reason", "no"); status != 0 {
		t.Error("reject")
	}
}

func TestSafeJoin(t *testing.T) {
	base := t.TempDir()
	for _, bad := range []string{"../x", "a/../../x", "/etc/passwd", ""} {
		if _, err := safeJoin(base, bad); err == nil {
			t.Errorf("safeJoin accepted %q", bad)
		}
	}
	if runtime.GOOS != "windows" {
		outside := t.TempDir()
		os.Symlink(outside, filepath.Join(base, "link"))
		if _, err := safeJoin(base, "link/x"); err == nil {
			t.Error("a symbolic link out of the directory was followed")
		}
	}
	if _, err := safeJoin(base, "rules/a.yaml"); err != nil {
		t.Error(err)
	}
}

func TestAnalyzeFindsSchemasAndChecks(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"api/openapi.yaml":        "openapi: 3.1.0\ninfo: {title: x, version: 1}\ncomponents:\n  schemas:\n    Order:\n      type: object\n    Customer:\n      type: object\n",
		"src/order.ts":            "const Order = z.object({ qty: z.number().max(10) });\n",
		"src/model.py":            "class Order(BaseModel):\n    qty: int = Field(le=10)\n",
		"src/Order.java":          "  @NotNull @Size(max = 10) String id;\n",
		"src/order.go":            "type Order struct { Qty int `validate:\"max=10\"` }\n",
		"src/svc.go":              "if qty > 10 { return errors.New(\"quantity must be at most 10\") }\n",
		"node_modules/x/index.js": "z.object({})\n",
		"src/order_test.go":       "z.object({})\n",
	}
	for name, content := range files {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(filepath.FromSlash(name))), 0o755)
		os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(content), 0o644)
	}
	status, out, _ := in(t, dir, "", "analyze", ".", "--format", "json", "--exclude", "**/*_test.go")
	if status != 0 {
		t.Fatal(status)
	}
	for _, want := range []string{`"zod"`, `"pydantic"`, `"bean-validation"`, `"go-validator"`, `"hand-written"`, `"Order"`, `"Customer"`} {
		if !strings.Contains(out, want) {
			t.Errorf("analyze lacks %s", want)
		}
	}
	if strings.Contains(out, "node_modules") || strings.Contains(out, "order_test.go") {
		t.Error("analyze looked into node_modules or an excluded file")
	}
}

func TestProgramNameAndHelp(t *testing.T) {
	t.Setenv("RCAS_PROGRAM_NAME", "")
	for argv0, want := range map[string]string{"rcas": "rcas", "/usr/bin/rule-cascade": "rule-cascade", `C:\x\RULE-CASCADE.EXE`: "rule-cascade", "rcas-linux-amd64": "rcas", "rule-cascade-darwin-arm64": "rule-cascade"} {
		if got := ProgramName(argv0); got != want {
			t.Errorf("ProgramName(%q) = %q", argv0, got)
		}
	}
	t.Setenv("RCAS_PROGRAM_NAME", "rule-cascade")
	if ProgramName("rcas") != "rule-cascade" {
		t.Error("RCAS_PROGRAM_NAME is not honoured")
	}
	var out bytes.Buffer
	if Main("rule-cascade", []string{"version"}, nil, &out, &out); !strings.HasPrefix(out.String(), "rule-cascade ") {
		t.Errorf("version as rule-cascade: %q", out.String())
	}
	for _, cmd := range commands {
		var o, e bytes.Buffer
		if status := Main("rcas", []string{"help", cmd.name}, nil, &o, &e); status != 0 || !strings.Contains(o.String(), "usage: rcas "+cmd.name) {
			t.Errorf("help %s: %d %s", cmd.name, status, e.String())
		}
	}
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		var o bytes.Buffer
		if status := Main("rcas", []string{"completion", shell}, nil, &o, &o); status != 0 || !strings.Contains(o.String(), "proposals") {
			t.Errorf("completion %s", shell)
		}
	}
}

// Review findings: a proposal writes only rulesets and schemas under the rules directory.
func TestProposalPathsAreConfined(t *testing.T) {
	dir := t.TempDir()
	in(t, dir, "", "init", "--name", "Example", "--ci", "none")
	c := &cli{program: "rcas", stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, dir: dir}
	for _, bad := range []string{".git/config", ".git/hooks/pre-commit", ".mcp.json", "rcas.yaml", "package.json",
		"rules/.hidden.ruleset.yaml", ".rcas/proposals/x/proposal.json", "src/order.ruleset.yaml", "rules/notes.md"} {
		if _, err := c.proposeAs(newProposal("evil"), map[string][]byte{bad: []byte("x")}, false); err == nil {
			t.Errorf("a proposal of %s was accepted", bad)
		}
	}
	good := readFile(t, filepath.Join(dir, "rules", "example.ruleset.yaml"))
	if _, err := c.proposeAs(newProposal("ok"), map[string][]byte{"rules/sub/copy.ruleset.yaml": []byte(strings.Replace(good, "example.example", "example.copy", 1))}, false); err != nil {
		t.Errorf("a ruleset under rules/ was refused: %v", err)
	}
	// an agent cannot swap a proposal after it was reviewed
	if _, err := c.proposeAs(newProposal("ok"), map[string][]byte{"rules/sub/copy.ruleset.yaml": []byte("changed")}, false); err == nil {
		t.Error("an existing proposal was replaced by an agent")
	}
}

func TestInitQuotesYAMLKeywords(t *testing.T) {
	for _, name := range []string{"true", "null", "off"} {
		dir := filepath.Join(t.TempDir(), name)
		os.MkdirAll(dir, 0o755)
		if status, out, errs := in(t, dir, "", "init", "--ci", "none"); status != 0 {
			t.Fatalf("%s: init %d %s %s", name, status, out, errs)
		}
		if status, out, errs := in(t, dir, "", "check"); status != 0 {
			t.Errorf("%s: check %d %s %s", name, status, out, errs)
		}
	}
}

func TestAgentInstallKeepsCRLFAndRefusesBrokenMarkers(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Ours\r\n\r\nKeep.\r\n"), 0o644)
	in(t, dir, "", "agent", "install", "--for", "codex")
	got := readFile(t, filepath.Join(dir, "AGENTS.md"))
	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Error("CRLF file got LF line endings")
	}
	os.WriteFile(filepath.Join(dir, "GEMINI.md"), []byte(blockBegin+"\nuser text\n"), 0o644)
	if status, _, _ := in(t, dir, "", "agent", "install", "--for", "gemini"); status == 0 {
		t.Error("unbalanced markers were accepted")
	}
	if readFile(t, filepath.Join(dir, "GEMINI.md")) != blockBegin+"\nuser text\n" {
		t.Error("GEMINI.md was changed")
	}
}

func TestMCPInstallKeepsModeAndRefusesInlineTOML(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", home)
	cfg := filepath.Join(home, "config.toml")
	os.WriteFile(cfg, []byte("[mcp_servers]\nrules-cascade = { command = \"x\" }\n"), 0o600)
	if status, _, errs := in(t, dir, "", "mcp", "install", "codex", "--file", "--force"); status == 0 || !strings.Contains(errs, "inline") {
		t.Errorf("an inline table: %d %s", status, errs)
	}
	os.WriteFile(cfg, []byte("[other]\nx = 1\n"), 0o600)
	in(t, dir, "", "mcp", "install", "codex", "--file")
	if info, _ := os.Stat(cfg); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode became %v", info.Mode().Perm())
	}
	// a comment before the next table stays, and an identical entry is unchanged
	os.WriteFile(cfg, []byte("[mcp_servers.rules-cascade]\ncommand = \"npx\"\nargs = [\"-y\", \"@rules-cascade/cli\", \"mcp\"]\n\n# next\n[z]\n"), 0o600)
	if runtime.GOOS != "windows" {
		if _, out, _ := in(t, dir, "", "mcp", "install", "codex", "--file"); !strings.Contains(out, "unchanged") {
			t.Errorf("identical entry followed by a comment: %s", out)
		}
	}
}

func TestDeriveProposeRelativeOutput(t *testing.T) {
	dir := t.TempDir()
	in(t, dir, "", "init", "--name", "Example", "--ci", "none")
	os.WriteFile(filepath.Join(dir, "rules", "order.openapi.yaml"), []byte("openapi: 3.1.0\ninfo: {title: x, version: '1'}\npaths: {}\ncomponents:\n  schemas:\n    Order:\n      type: object\n      required: [qty]\n      properties:\n        qty: {type: integer, maximum: 10}\n"), 0o644)
	status, out, errs := in(t, dir, "", "derive", "rules/order.openapi.yaml", "--schema", "Order", "--id", "example.order", "-o", "rules/order.ruleset.yaml", "--propose")
	if status != 0 || !strings.Contains(out, "(pending)") {
		t.Fatalf("%d %s %s", status, out, errs)
	}
	_, out, _ = in(t, dir, "", "proposals", "list", "--json")
	if !strings.Contains(out, `"path": "rules/order.ruleset.yaml"`) {
		t.Errorf("proposed path: %s", out)
	}
}

func TestMCPReadsNoSchemaOutsideTheProject(t *testing.T) {
	outer := t.TempDir()
	dir := filepath.Join(outer, "proj")
	os.MkdirAll(dir, 0o755)
	in(t, dir, "", "init", "--name", "Example", "--ci", "none")
	os.WriteFile(filepath.Join(outer, "secret.json"), []byte(`{"$defs":{"x":{"type":"object","properties":{"quantity":{"type":"integer"},"note":{"type":"string"}}}}}`), 0o644)
	draft := strings.Replace(readFile(t, filepath.Join(dir, "rules", "example.ruleset.yaml")), "./order.schema.json#/$defs/Order", "../../secret.json#/$defs/x", 1)
	msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "check", "arguments": map[string]any{"content": draft}}})
	_, out, _ := in(t, dir, string(msg)+"\n", "mcp", "--root", dir)
	if !strings.Contains(out, "SCHEMA_REF_UNRESOLVED") {
		t.Errorf("a schema outside the project was read: %.300s", out)
	}
}
