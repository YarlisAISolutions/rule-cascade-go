package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	rulecascade "rulescascade.com/go"
)

const repository = "../../../.."

// invoke runs the command line in process.
func invoke(t *testing.T, stdin string, args ...string) (status int, stdout, stderr string) {
	t.Helper()
	var out, errs bytes.Buffer
	status = run(args, strings.NewReader(stdin), &out, &errs)
	return status, out.String(), errs.String()
}

func canonical(t *testing.T, v any) string {
	t.Helper()
	text, err := rulecascade.Canonical(v)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func readJSON(t *testing.T, path string) any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	v, err := rulecascade.ParseJSON(data)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

// A plain scalar means what the YAML 1.2 core schema says, never what YAML 1.1 said.
func TestYAMLCoreSchema(t *testing.T) {
	for _, c := range []struct {
		yaml     string
		want     string // canonical JSON
		problems int
	}{
		{"a: 1", `{"a":1}`, 0},
		{"a: -0", `{"a":0}`, 0},
		{"a: +5", `{"a":5}`, 0},
		{"a: 1.50", `{"a":1.5}`, 0},
		{"a: .5", `{"a":0.5}`, 0},
		{"a: 1.5e+3", `{"a":1500}`, 0},
		{"a: 0x1F", `{"a":31}`, 0},
		{"a: 12345678901234567890123", `{"a":12345678901234568000000}`, 0}, // the nearest double
		{"a: 1.0.0", `{"a":"1.0.0"}`, 0},
		{"a: true\nb: True\nc: FALSE", `{"a":true,"b":true,"c":false}`, 0},
		{"a: null\nb:\nc: Null", `{"a":null,"b":null,"c":null}`, 0},
		{"a: '012'\nb: \"no\"\nc: '2026-10-03'", `{"a":"012","b":"no","c":"2026-10-03"}`, 0},
		{"a: |\n  one\n  two\nb: >\n  folded\n  text\n", `{"a":"one\ntwo\n","b":"folded text\n"}`, 0},
		{"- a\n- {b: [1, 2], c: {}}\n- []", `["a",{"b":[1,2],"c":{}},[]]`, 0},
		{"200: ok\ntrue: 1", `{"200":"ok","true":1}`, 0},
		{"a: ~\nb: 0x1F\nc: 1.5e+3\nd: .5\ne: 1.\nf: +1\ng: 007\nh: y", `{"a":null,"b":31,"c":1500,"d":0.5,"e":1,"f":1,"g":7,"h":"y"}`, 0},
		// JSON has only string keys: a key that is not a string is written as JSON writes its value
		{"0x1F: a\nTrue: b\n1.50: c\nNull: d", `{"1.5":"c","31":"a","null":"d","true":"b"}`, 0},
		{"# only a comment\n", `null`, 0},
		{"", `null`, 0},
		// read the YAML 1.2 way, and reported because a YAML 1.1 parser reads them differently
		{"a: 012", `{"a":12}`, 1},
		{"a: 089", `{"a":89}`, 1},
		{"a: 0o17", `{"a":15}`, 1},
		{"a: 1e3", `{"a":1000}`, 1},
		{"a: 1.5e3", `{"a":1500}`, 1},
		{"a: yes\nb: No\nc: on\nd: OFF\ne: off", `{"a":"yes","b":"No","c":"on","d":"OFF","e":"off"}`, 5},
		{"on: 1", `{"on":1}`, 1},
		{"a: -.5", `{"a":-0.5}`, 1},
		{"a: 010", `{"a":10}`, 1},
		{"a: =", `{"a":"="}`, 1},
		{"a: 2026-10-03", `{"a":"2026-10-03"}`, 1},
		{"a: 12:30", `{"a":"12:30"}`, 1},
		{"a: 1_000", `{"a":"1_000"}`, 1},
		{"a: 0b101", `{"a":"0b101"}`, 1},
		{"a: -0x1F", `{"a":"-0x1F"}`, 1},
	} {
		value, problems, err := parseYAML([]byte(c.yaml))
		if err != nil {
			t.Errorf("%q: %v", c.yaml, err)
			continue
		}
		if got := canonical(t, value); got != c.want || len(problems) != c.problems {
			t.Errorf("%q: got %s with %d problem(s) %v, want %s with %d", c.yaml, got, len(problems), problems, c.want, c.problems)
		}
		for _, p := range problems {
			if p.Code != "YAML_NOT_PORTABLE" {
				t.Errorf("%q: problem code %s", c.yaml, p.Code)
			}
		}
	}
	for _, bad := range []string{"a: .inf", "a: .NaN", "a: 1.0e+999", "a: [1, 2", "? [1, 2]\n: x"} {
		if value, _, err := parseYAML([]byte(bad)); err == nil {
			t.Errorf("%q was read as %v", bad, value)
		}
	}
}

// What specification section 12 forbids is reported.
func TestYAMLNotPortable(t *testing.T) {
	for name, text := range map[string]string{
		"anchors and aliases":   "a: &x 1\nb: *x\n",
		"an anchor on a key":    "&k a: 1\n",
		"a tag on a scalar":     "a: !!str 1\n",
		"a tag on a mapping":    "a: !thing {b: 1}\n",
		"a merge key":           "a: {b: 1}\nc:\n  <<: {d: 2}\n",
		"two documents":         "a: 1\n---\nb: 2\n",
		"a byte order mark":     "\xEF\xBB\xBFa: 1\n",
		"a repeated key":        "a: 1\na: 2\n",
		"an unquoted timestamp": "a: 2026-10-03T09:00:00Z\n",
	} {
		_, problems, err := parseYAML([]byte(text))
		if err != nil {
			t.Errorf("%s: %v", name, err)
		} else if len(problems) == 0 {
			t.Errorf("%s: nothing was reported", name)
		}
	}
}

// The fixtures of the conformance suite were produced from the example contracts by the
// reference implementation's YAML reader. Reading the same files here must give the same values.
func TestExamplesReadLikeTheFixtures(t *testing.T) {
	var files []string
	for _, pattern := range []string{"examples/contracts/*.yaml", "examples/catalog/*.yaml", "conformance/sources/*.yaml"} {
		found, _ := filepath.Glob(filepath.Join(repository, filepath.FromSlash(pattern)))
		files = append(files, found...)
	}
	if len(files) < 8 {
		t.Fatalf("expected the example contracts, found %v", files)
	}
	for _, path := range files {
		value, problems, err := readDocument(path)
		if err != nil || len(problems) > 0 {
			t.Errorf("%s: %v %v", path, err, problems)
			continue
		}
		fixture := filepath.Join(repository, "conformance", "fixtures", strings.TrimSuffix(filepath.Base(path), ".yaml")+".json")
		if !rulecascade.Equal(value, readJSON(t, fixture)) {
			t.Errorf("%s is not read as %s", path, fixture)
		}
	}
}

func TestCheckExamples(t *testing.T) {
	status, stdout, stderr := invoke(t, "", "check",
		filepath.Join(repository, "examples/contracts/acme-org-base.ruleset.yaml"),
		filepath.Join(repository, "examples/contracts/payments-transfer.ruleset.yaml"),
		filepath.Join(repository, "examples/catalog/customer-onboarding.ruleset.yaml"),
		filepath.Join(repository, "conformance/sources/edge-fail.ruleset.yaml"),
		filepath.Join(repository, "conformance/sources/edge-priority.ruleset.yaml"))
	if status != 0 || stderr != "" || strings.Contains(stdout, "FAIL") || strings.Count(stdout, "golden tests, 0 failed") != 5 {
		t.Fatalf("status %d\n%s%s", status, stdout, stderr)
	}
	// The organisation base is small and stable; the other rulesets grow, so only their pass lines are counted.
	for _, want := range []string{"acme.org.base@1.2.0  sha256:4dff42ddd5da...  3 rules (2 client-safe), 2 params",
		"  3 golden tests, 0 failed"} {
		if !strings.Contains(stdout, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, stdout)
		}
	}
}

func TestCheckReportsProblems(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "broken.ruleset.yaml")
	text := `ruleCascade: 1.0.0
kind: RuleSet
metadata: { id: t.broken, version: 1.0.0, title: Broken }
scope: [ { level: organization, id: t } ]
entities:
  Thing: { schema: { $ref: "./missing.json#/Thing" } }
rules:
  - id: thing.name.required
    kind: validation
    target: { entity: Thing, field: /name }
    operations: [create]
    enabled: yes
    assert: { op: exists, args: [ { var: data.name } ] }
    severity: error
    finding: { code: T-001, message: thing.name }
`
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	status, stdout, _ := invoke(t, "", "check", path)
	if status != 1 {
		t.Errorf("status %d", status)
	}
	for _, want := range []string{"LOAD FAILED", "YAML_NOT_PORTABLE: line 12: quote 'yes'", "SCHEMA_INVALID: rules/0/enabled"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in:\n%s", want, stdout)
		}
	}
	// with the scalar quoted properly the schema reference and the message are what is left
	fixed := strings.Replace(text, "enabled: yes", "enabled: true", 1)
	if err := os.WriteFile(path, []byte(fixed), 0o644); err != nil {
		t.Fatal(err)
	}
	status, stdout, _ = invoke(t, "", "check", path)
	if status != 1 || !strings.Contains(stdout, "SCHEMA_REF_UNRESOLVED") || !strings.Contains(stdout, "MESSAGE_MISSING: thing.name.required") {
		t.Errorf("status %d:\n%s", status, stdout)
	}
	if status, _, stderr := invoke(t, "", "compile", path); status != 1 || !strings.Contains(stderr, "MESSAGE_MISSING") {
		t.Errorf("compile: status %d: %s", status, stderr)
	}
}

func TestCompileManifestEvaluate(t *testing.T) {
	source := filepath.Join(repository, "examples/catalog/customer-onboarding.ruleset.yaml")
	published := readJSON(t, filepath.Join(repository, "conformance/bundles/acme.onboarding.customer.bundle.json"))

	status, stdout, stderr := invoke(t, "", "compile", source)
	if status != 0 {
		t.Fatalf("compile: status %d: %s", status, stderr)
	}
	compiled, err := rulecascade.ParseJSON([]byte(stdout))
	if err != nil || !rulecascade.Equal(compiled, published) {
		t.Fatalf("the compiled bundle is not the published one (%v)", err)
	}

	bundle := filepath.Join(t.TempDir(), "customer.bundle.json")
	status, stdout, stderr = invoke(t, "", "compile", source, "-o", bundle)
	if status != 0 || !strings.HasPrefix(stdout, "wrote "+bundle+"  acme.onboarding.customer@1.0.0  sha256:") {
		t.Fatalf("compile -o: status %d: %s%s", status, stdout, stderr)
	}
	if !rulecascade.Equal(readJSON(t, bundle), published) {
		t.Error("the bundle written with -o is not the published one")
	}

	for _, args := range [][]string{{source, "--channel", "client"}, {bundle, "--channel=server"}, {bundle}} {
		channel := "client" // the default, as in tools/rulecheck.py
		if len(args) > 1 {
			channel = strings.TrimPrefix(args[len(args)-1], "--channel=")
		}
		status, stdout, stderr = invoke(t, "", append([]string{"manifest"}, args...)...)
		manifest, err := rulecascade.ParseJSON([]byte(stdout))
		if status != 0 || err != nil || !rulecascade.Equal(manifest, member(member(published, "manifests"), channel)) {
			t.Errorf("manifest %v: status %d: %v %s", args, status, err, stderr)
		}
	}

	request := `{"entity": "Customer", "operation": "create", "data": {"fullName": "Maya Okafor", "email": "maya(at)example.com"}}`
	status, stdout, stderr = invoke(t, request, "evaluate", "--bundle", bundle, "--channel=client")
	result, err := rulecascade.ParseJSON([]byte(stdout))
	if status != 0 || err != nil || member(result, "decision") != "deny" || !strings.Contains(stdout, `"rule": "type.email.format"`) {
		t.Errorf("evaluate: status %d: %v\n%s%s", status, err, stdout, stderr)
	}
	requestFile := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(requestFile, []byte(request), 0o644); err != nil {
		t.Fatal(err)
	}
	if status, fromFile, _ := invoke(t, "", "evaluate", requestFile, "--bundle", bundle, "--channel", "client"); status != 0 || fromFile != stdout {
		t.Errorf("evaluate from a file: status %d", status)
	}
}

func TestEngineCommand(t *testing.T) {
	input := `{"id":1,"command":"version"}` + "\n\n" + `{"id":2,"command":"expression","expr":{"op":"x-luhn","args":["79927398713"]}}` + "\n"
	status, stdout, _ := invoke(t, input, "engine", "--conformance-operators")
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if status != 0 || len(lines) != 2 || !strings.Contains(lines[0], `"operators":["x-luhn","x-test-reverse","x-test-sum"]`) ||
		lines[1] != `{"id":2,"ok":true,"result":true}` {
		t.Errorf("status %d:\n%s", status, stdout)
	}
	// without the flag the operators are not built in and the rule fails closed
	_, stdout, _ = invoke(t, input, "engine")
	if !strings.Contains(stdout, `"operators":[]`) || !strings.Contains(stdout, `"code":"EVALUATION_ERROR"`) {
		t.Errorf("without operators:\n%s", stdout)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"nonsense"}, {"check"}, {"compile"}, {"compile", "a", "b"}, {"manifest"}, {"evaluate"},
		{"engine", "--nope"}, {"compile", "x.yaml", "-o"}, {"version", "extra"}} {
		if status, _, stderr := invoke(t, "", args...); status != 2 || stderr == "" {
			t.Errorf("%v: status %d, stderr %q", args, status, stderr)
		}
	}
	if status, stdout, _ := invoke(t, "", "version"); status != 0 || !strings.HasPrefix(stdout, "rcas "+rulecascade.Version) {
		t.Errorf("version: %d %q", status, stdout)
	}
	if status, _, stderr := invoke(t, "", "compile", "no-such-file.yaml"); status != 1 || stderr == "" {
		t.Errorf("a missing file: status %d, stderr %q", status, stderr)
	}
}

// Numbers enter as doubles: `check` reports the ones with more than 15 significant digits, and a
// number too large for a double is refused by every command.
func TestLongAndHugeNumbers(t *testing.T) {
	directory := t.TempDir()
	write := func(name, text string) string {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	const ruleset = `{"ruleCascade": "1.0.0", "kind": "RuleSet", "metadata": {"id": "t.numbers", "version": "1.0.0", "title": "Numbers"},
 "scope": [{"level": "organization", "id": "t"}],
 "params": {"list": {"type": "numberList", "default": [NUMBERS]}}, "rules": []}`
	long := write("long.ruleset.json", strings.Replace(ruleset, "NUMBERS", "0.1234567890123456, 12345678901234567890, 1.50, 123456789012345000000, 0.10000000000000000000001, -9007199254740993", 1))
	status, stdout, _ := invoke(t, "", "check", long)
	for _, want := range []string{
		"  NUMBER_NOT_PORTABLE: /params/list/default/0: 0.1234567890123456 has more than 15 significant digits; runtimes read it as the nearest double\n",
		"  NUMBER_NOT_PORTABLE: /params/list/default/1: 12345678901234567890 has more than 15 significant digits",
		"  NUMBER_NOT_PORTABLE: /params/list/default/5: -9007199254740993 has more than 15 significant digits",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in:\n%s", want, stdout)
		}
	}
	if status != 1 || strings.Count(stdout, "NUMBER_NOT_PORTABLE") != 3 {
		t.Errorf("status %d:\n%s", status, stdout)
	}
	// compile accepts the file and reads each number as its nearest double
	status, stdout, stderr := invoke(t, "", "compile", long)
	if status != 0 || !strings.Contains(stdout, "12345678901234567000") || !strings.Contains(stdout, "-9007199254740992") {
		t.Errorf("compile: status %d: %s%s", status, stdout, stderr)
	}

	for _, huge := range []string{"1e999", "-1e999", "1" + strings.Repeat("0", 400)} {
		path := write("huge.ruleset.json", strings.Replace(ruleset, "NUMBERS", huge, 1))
		for _, command := range []string{"check", "compile", "manifest"} {
			if status, stdout, stderr := invoke(t, "", command, path); status != 1 || !strings.Contains(stdout+stderr, "outside the range of a double") {
				t.Errorf("%s with %.10s: status %d: %s%s", command, huge, status, stdout, stderr)
			}
		}
	}
	yamlPath := write("huge.ruleset.yaml", "ruleCascade: 1.0.0\nkind: RuleSet\nmetadata: {id: t.numbers, version: 1.0.0, title: Numbers}\n"+
		"scope: [{level: organization, id: t}]\nparams: {big: {type: number, default: 1"+strings.Repeat("0", 400)+"}}\nrules: []\n")
	for _, command := range []string{"check", "compile"} {
		if status, stdout, stderr := invoke(t, "", command, yamlPath); status != 1 || !strings.Contains(stdout+stderr, "outside the range of a double") {
			t.Errorf("%s of a YAML file: status %d: %s%s", command, status, stdout, stderr)
		}
	}
	bundle := filepath.Join(repository, "conformance/bundles/acme.org.base.bundle.json")
	if status, _, stderr := invoke(t, `{"entity": "Transfer", "operation": "create", "data": {"amount": 1e999}}`, "evaluate", "--bundle", bundle); status != 1 || !strings.Contains(stderr, "outside the range of a double") {
		t.Errorf("evaluate: status %d: %s", status, stderr)
	}
}

// A client receives one manifest: the command writes it with `manifest -o` and evaluates it with
// `evaluate --manifest`, on the manifest's own channel.
func TestSingleManifest(t *testing.T) {
	bundle := filepath.Join(repository, "conformance/bundles/acme.payments.transfer.bundle.json")
	published := readJSON(t, bundle)
	directory := t.TempDir()
	client := filepath.Join(directory, "transfer.client.json")

	status, stdout, stderr := invoke(t, "", "manifest", bundle, "--channel", "client", "-o", client)
	if status != 0 || stdout != "wrote "+client+"  acme.payments.transfer@1.0.0  client manifest\n" {
		t.Fatalf("manifest -o: status %d: %s%s", status, stdout, stderr)
	}
	if !rulecascade.Equal(readJSON(t, client), member(member(published, "manifests"), "client")) {
		t.Error("the manifest written with -o is not the published client manifest")
	}
	for _, args := range [][]string{{"--manifest", client}, {"--bundle", bundle}, {"--bundle", bundle, "--channel", "client"}} {
		status, stdout, stderr = invoke(t, "", append([]string{"manifest"}, args...)...)
		manifest, err := rulecascade.ParseJSON([]byte(stdout))
		if status != 0 || err != nil || !rulecascade.Equal(manifest, member(member(published, "manifests"), "client")) {
			t.Errorf("manifest %v: status %d: %v %s", args, status, err, stderr)
		}
	}

	// blocked by a server-only rule: the client manifest does not know it
	const request = `{"entity": "Transfer", "operation": "create", "data": {"type": "international", "amount": 500, "currency": "USD",
		"memo": "gift", "beneficiary": {"name": "X", "country": "KP", "swiftCode": "ABCDKPPY"}}}`
	decision := func(args ...string) (int, any, string) {
		status, stdout, stderr := invoke(t, request, append([]string{"evaluate"}, args...)...)
		result, _ := rulecascade.ParseJSON([]byte(stdout))
		return status, member(result, "decision"), stderr
	}
	if status, got, stderr := decision("--manifest", client); status != 0 || got != "allow" {
		t.Errorf("evaluate --manifest: status %d, decision %v: %s", status, got, stderr)
	}
	if status, got, stderr := decision("--manifest", client, "--channel", "client"); status != 0 || got != "allow" {
		t.Errorf("evaluate --manifest --channel client: status %d, decision %v: %s", status, got, stderr)
	}
	if status, got, _ := decision("--bundle", bundle); status != 0 || got != "deny" {
		t.Errorf("evaluate --bundle: status %d, decision %v", status, got)
	}
	if status, _, stderr := decision("--manifest", client, "--channel", "server"); status != 1 || !strings.Contains(stderr, "has no server manifest") {
		t.Errorf("a client manifest evaluated as the server: status %d: %s", status, stderr)
	}
	if status, _, stderr := decision("--manifest", bundle); status != 1 || !strings.Contains(stderr, "MANIFEST_INVALID") {
		t.Errorf("a bundle given as a manifest: status %d: %s", status, stderr)
	}
	for _, args := range [][]string{{"evaluate"}, {"evaluate", "--bundle", bundle, "--manifest", client}, {"manifest", bundle, "--manifest", client}} {
		if status, _, _ := invoke(t, request, args...); status != 2 {
			t.Errorf("%v: status %d, want a usage error", args, status)
		}
	}
}

// Custom operators live in the host. When a ruleset needs one this command does not have, check
// says so: the rules that use it fail closed here.
func TestCheckNotesMissingOperators(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ops.ruleset.yaml")
	text := `ruleCascade: 1.0.0
kind: RuleSet
metadata: { id: t.ops, version: 1.0.0, title: Operators }
scope: [ { level: organization, id: t } ]
entities:
  Thing: { schema: { $ref: "./thing.json#/Thing" } }
operators:
  x-vat-id: { description: Checks a VAT identifier. }
  x-luhn: { description: Luhn check digit. }
rules:
  - id: thing.vat.valid
    kind: validation
    target: { entity: Thing, field: /vat }
    operations: [create]
    assert: { op: and, args: [ { op: x-vat-id, args: [ { var: data.vat } ] }, { op: x-luhn, args: [ { var: data.vat } ] } ] }
    severity: error
    finding: { code: T-001, message: thing.vat }
messages:
  en: { thing.vat: The VAT identifier is not valid. }
`
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "thing.json"), []byte(`{"Thing": {"type": "object", "properties": {"vat": {"type": "string"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	status, stdout, _ := invoke(t, "", "check", path)
	if status != 0 || !strings.Contains(stdout, "  NOTE  this command does not have the custom operators x-vat-id: ") || strings.Contains(stdout, "x-luhn") {
		t.Errorf("status %d:\n%s", status, stdout)
	}
}

// Nested aliases cannot make a short document expand without end; a few aliases still read.
func TestYAMLAliasBudget(t *testing.T) {
	bomb := "a0: &a0 [x, x, x, x, x, x, x, x, x, x]\n"
	for i := 1; i <= 9; i++ {
		refs := strings.TrimSuffix(strings.Repeat(fmt.Sprintf("*a%d, ", i-1), 10), ", ")
		bomb += fmt.Sprintf("a%d: &a%d [%s]\n", i, i, refs)
	}
	if _, _, err := parseYAML([]byte(bomb)); err == nil || !strings.Contains(err.Error(), "expand too much") {
		t.Fatalf("an alias bomb: %v", err)
	}
	value, _, err := parseYAML([]byte("base: &b {x: 1}\none: *b\ntwo: *b\n"))
	if err != nil || value == nil {
		t.Errorf("a few aliases: %v", err)
	}
}

func TestHideWorkspace(t *testing.T) {
	unix := []string{"/var/folders/x/T", "/private/var/folders/x/T"}
	windows := []string{`C:\Users\Jane Doe\AppData\Local\Temp`}
	for _, tc := range []struct {
		parents  []string
		in, want string
	}{
		{unix, `/private/var/folders/x/T/rcas-mcp-12/rules/a.ruleset.yaml: open /var/folders/x/T/rcas-mcp-12/rules/a.ruleset.yaml: no such file`,
			`rules/a.ruleset.yaml: open rules/a.ruleset.yaml: no such file`},
		{unix, `/var/folders/x/T/rcas-mcp-12/rules/rcas-mcp-7/my dir/a.ruleset.yaml: bad`, `rules/rcas-mcp-7/my dir/a.ruleset.yaml: bad`},
		{windows, `C:\Users\Jane Doe\AppData\Local\Temp\rcas-mcp-34\rules\my dir\a.ruleset.yaml: The system cannot find the file specified.`,
			`rules/my dir/a.ruleset.yaml: The system cannot find the file specified.`},
		{windows, `C:\Users\Jane Doe\AppData\Local\Temp\rcas-mcp-34\rules\rcas-mcp-12\a.ruleset.yaml: x`, `rules/rcas-mcp-12/a.ruleset.yaml: x`},
		{unix, `no rule "x" in a.b`, `no rule "x" in a.b`},
		{unix, `/var/folders/x/T/other: kept`, `/var/folders/x/T/other: kept`},
	} {
		if got := hideWorkspace(tc.in, tc.parents); got != tc.want {
			t.Errorf("%s\n got %s\nwant %s", tc.in, got, tc.want)
		}
	}
}
