package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const initHelp = `Creates, in dir (default: the current directory):

  rcas.yaml                       the project: rules directory, output directory, languages, sources
  rules/example.ruleset.yaml      a first ruleset with two rules and golden tests; '{program} check' passes
  rules/LOADING.md                how each chosen language loads the compiled bundle
  .rcas/                          proposals waiting for review (ignored by git)
  .github/workflows/rules.yml     CI: check and compile on every pull request (--ci github, the default
                                  when dir is inside a git repository)

  --name          the project name (default: the directory name)
  --id-prefix     the prefix of ruleset ids, for example acme.payments (default: from the name)
  --lang          languages that load the rules: ts, python, java, go, other (comma-separated)
  --from          an existing code base to analyze; schemas found there become proposals
  --agent         also write agent instructions for these AI tools ('{program} agent install')
  --mcp           also register the MCP server with these AI tools ('{program} mcp install')
  --ci            github or none
  --force         replace files that exist and differ
  --dry-run       print what would be written, write nothing

Existing files are never replaced without --force. Next: '{program} check', then '{program} compile --all'.
`

// language is a runtime that can load compiled rules.
type language struct {
	title   string
	install string
	load    string
}

var languages = map[string]language{
	"ts": {"TypeScript and JavaScript (Node.js, browsers, React Native)", "npm install @rules-cascade/core", "```ts\n" +
		"import { readFileSync } from 'node:fs';\n" +
		"import { RuleSet } from '@rules-cascade/core';\n\n" +
		"// Once, at start-up: a bundle that cannot be read is a load error, so do not serve without rules.\n" +
		"const rules = RuleSet.fromBundle(JSON.parse(readFileSync('{{out}}/{{id}}.bundle.json', 'utf8')));\n" +
		"const result = rules.evaluate({ entity: 'Order', operation: 'create', data: { quantity: 3 } }, 'server');\n" +
		"if (result.decision === 'deny') { /* return result.findings to the caller */ }\n" +
		"```\n\nIn a browser, load the client manifest (`{{id}}.client.manifest.json`), never the bundle."},
	"python": {"Python 3.10+", "pip install rule-cascade", "```python\n" +
		"import json\n" +
		"from rule_cascade import RuleSet\n\n" +
		"# Once, at start-up.\n" +
		"with open(\"{{out}}/{{id}}.bundle.json\", encoding=\"utf-8\") as f:\n" +
		"    rules = RuleSet.from_bundle(json.load(f))\n" +
		"result = rules.evaluate({\"entity\": \"Order\", \"operation\": \"create\", \"data\": {\"quantity\": 3}}, \"server\")\n" +
		"if result[\"decision\"] == \"deny\":\n" +
		"    ...  # return result[\"findings\"] to the caller\n" +
		"```"},
	"java": {"Java 17+ (Kotlin, Scala, Spring Boot)", "Maven: io.github.yarlisaisolutions:rule-cascade-core", "```java\n" +
		"import io.github.yarlisaisolutions.rulecascade.*;\n" +
		"import java.nio.file.*;\n" +
		"import java.util.Map;\n\n" +
		"// Once, at start-up.\n" +
		"RuleSet rules = RuleSet.fromBundle((Map<String, Object>) Json.parse(Files.readString(Path.of(\"{{out}}/{{id}}.bundle.json\"))));\n" +
		"EvaluationResult result = rules.evaluate(\n" +
		"        EvaluationRequest.builder(\"Order\", \"create\").data(Map.of(\"quantity\", 3)).build(), Channel.SERVER);\n" +
		"```"},
	"go": {"Go 1.22+", "go get rules.sdods.com/go", "```go\n" +
		"import rulecascade \"rules.sdods.com/go\"\n\n" +
		"// Once, at start-up.\n" +
		"data, err := os.ReadFile(\"{{out}}/{{id}}.bundle.json\")\n" +
		"doc, err := rulecascade.ParseJSON(data)\n" +
		"rules, err := rulecascade.FromBundle(doc) // a *rulecascade.LoadError: do not start\n" +
		"result, err := rules.Evaluate(map[string]any{\"entity\": \"Order\", \"operation\": \"create\",\n" +
		"\t\"data\": map[string]any{\"quantity\": 3}}, \"server\", nil)\n" +
		"```"},
	"other": {"Any other language (C#, Rust, PHP, Ruby, Swift, C++ ...)", "rcas, or rcas.wasm for a sandbox", "Start `rcas engine` once as a child process and write one JSON line per request to its standard\n" +
		"input; it answers with one JSON line. Or evaluate a single request:\n\n" +
		"```bash\n" +
		"echo '{\"entity\":\"Order\",\"operation\":\"create\",\"data\":{\"quantity\":3}}' | rcas evaluate --bundle {{out}}/{{id}}.bundle.json -\n" +
		"```\n\nThe protocol is in https://rules.sdods.com/usage/command-and-wasm/."},
}

func languageNames() []string {
	names := make([]string, 0, len(languages))
	for name := range languages {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

var notSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slug turns a project name into an id segment: lower case, letters and digits, dash-separated.
func slug(name string) string {
	s := strings.Trim(notSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if s == "" {
		return "project"
	}
	if s[0] >= '0' && s[0] <= '9' {
		s = "p-" + s
	}
	return s
}

// splitList reads a comma-separated flag value; "all" expands to every choice.
func splitList(value string, all []string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(strings.ToLower(item))
		if item == "all" {
			return append([]string{}, all...)
		}
		if item != "" && !contains(out, item) {
			out = append(out, item)
		}
	}
	return out
}

func (c *cli) initCommand(args []string) int {
	flags, positional, err := parseArgs(args,
		map[string]string{"--name": "name", "--id-prefix": "prefix", "--lang": "lang", "--from": "from",
			"--agent": "agent", "--mcp": "mcp", "--ci": "ci"},
		[]string{"--force", "--dry-run"})
	if err != nil || len(positional) > 1 {
		if err != nil {
			fmt.Fprintf(c.stderr, "%s init: %v\n", c.program, err)
		}
		c.usage("init")
		return exitUsage
	}
	dir := c.workDir()
	if len(positional) == 1 {
		dir = c.path(positional[0])
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return c.fail(err)
	}
	name := flags["name"]
	if name == "" {
		name = filepath.Base(abs)
	}
	prefix := flags["prefix"]
	if prefix == "" {
		var segments []string
		for _, seg := range strings.Split(slug(name), "-") {
			if seg == "" {
				continue
			}
			if seg[0] >= '0' && seg[0] <= '9' {
				seg = "p" + seg
			}
			segments = append(segments, seg)
		}
		prefix = strings.Join(segments, ".")
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)*$`).MatchString(prefix) {
		fmt.Fprintf(c.stderr, "%s init: --id-prefix %q must be dot-separated lower-case segments, like acme.payments\n", c.program, prefix)
		return exitUsage
	}
	langs := splitList(flags["lang"], languageNames())
	for _, l := range langs {
		if _, ok := languages[l]; !ok {
			fmt.Fprintf(c.stderr, "%s init: unknown language %q (%s)\n", c.program, l, strings.Join(languageNames(), ", "))
			return exitUsage
		}
	}
	ci := flags["ci"]
	if ci == "" {
		ci = "none"
		if insideGit(abs) {
			ci = "github"
		}
	}
	if ci != "github" && ci != "none" {
		fmt.Fprintf(c.stderr, "%s init: --ci is github or none\n", c.program)
		return exitUsage
	}
	force, dryRun := flags["--force"] != "", flags["--dry-run"] != ""

	id := prefix + ".example"
	org := strings.SplitN(prefix, ".", 2)[0]
	files := []fileWrite{
		{Path: "rcas.yaml", Content: []byte(configTemplate(name, prefix, langs))},
		{Path: "rules/example.ruleset.yaml", Content: []byte(strings.NewReplacer("{{id}}", id, "{{org}}", org, "{{name}}", slug(name)).Replace(starterRuleset))},
		{Path: "rules/order.schema.json", Content: []byte(starterSchema)},
		{Path: "rules/LOADING.md", Content: []byte(loadingGuide(id, langs))},
		{Path: ".rcas/.gitignore", Content: []byte("# Proposals and caches of rcas: reviewed with 'rcas proposals', never committed.\n*\n")},
	}
	if ci == "github" {
		files = append(files, fileWrite{Path: ".github/workflows/rules.yml", Content: []byte(ciWorkflow)})
	}
	fmt.Fprintf(c.stdout, "%s init %s (%s, ids %s.*)\n", c.program, abs, name, prefix)
	outcomes, err := writeFiles(abs, files, force, dryRun)
	c.printOutcomes(outcomes)
	if err != nil {
		return c.fail(err)
	}
	if !dryRun {
		if added, err := ensureLine(filepath.Join(abs, ".gitignore"), ".rcas/"); err != nil {
			return c.fail(err)
		} else if added {
			fmt.Fprintf(c.stdout, "  %-12s %s\n", "updated", ".gitignore (.rcas/)")
		}
	}
	sub := &cli{program: c.program, stdin: c.stdin, stdout: c.stdout, stderr: c.stderr, dir: abs}
	status := exitOK
	if from := flags["from"]; from != "" {
		fmt.Fprintf(c.stdout, "\nAnalyzing %s\n", from)
		source, err := filepath.Abs(c.path(from)) // relative to where init was run, not to the new project
		if err != nil {
			return c.fail(err)
		}
		analyzeArgs := []string{source, "--derive", "--format", "md", "-o", filepath.Join(abs, ".rcas", "analysis.md")}
		if dryRun {
			analyzeArgs = []string{source, "--format", "md"}
		}
		if s := sub.analyzeCommand(analyzeArgs); s > status {
			status = s
		}
	}
	if agents := flags["agent"]; agents != "" {
		fmt.Fprintln(c.stdout)
		agentArgs := []string{"install", "--for", agents}
		if force {
			agentArgs = append(agentArgs, "--force")
		}
		if dryRun {
			agentArgs = append(agentArgs, "--dry-run")
		}
		if s := sub.agentCommand(agentArgs); s > status {
			status = s
		}
	}
	if clients := flags["mcp"]; clients != "" {
		fmt.Fprintln(c.stdout)
		mcpArgs := append([]string{"install"}, splitList(clients, clientNames())...)
		if dryRun {
			mcpArgs = append(mcpArgs, "--print")
		}
		if s := sub.mcpCommand(mcpArgs); s > status {
			status = s
		}
	}
	if !dryRun {
		next := ""
		if len(positional) == 1 {
			next = "  cd " + dir + "\n"
		}
		fmt.Fprintf(c.stdout, "\nNext:\n%s  %s check            # lint and run the golden tests\n  %s compile --all    # bundles in %s\n",
			next, c.program, c.program, "build/rules/")
		if flags["from"] != "" {
			fmt.Fprintf(c.stdout, "  %s proposals list   # rulesets derived from %s, waiting for review\n", c.program, flags["from"])
		}
	}
	return status
}

func insideGit(dir string) bool {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// ensureLine appends line to a text file (creating it) unless the file already has it.
func ensureLine(path, line string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	for _, l := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) == line || strings.TrimSpace(l) == strings.TrimSuffix(line, "/") {
			return false, nil
		}
	}
	eol := "\n"
	if strings.Contains(string(data), "\r\n") {
		eol = "\r\n"
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, eol...)
	}
	data = append(data, line+eol...)
	return true, writeFileAtomic(path, data, 0o644)
}

func configTemplate(name, prefix string, langs []string) string {
	l := "[]"
	if len(langs) > 0 {
		l = "[" + strings.Join(langs, ", ") + "]"
	}
	return fmt.Sprintf(`# rcas project configuration: https://rules.sdods.com/reference/project-config/
rcas: 1
project:
  name: %s
  idPrefix: %s          # ruleset ids start with this
rules:
  dir: rules            # where the *.ruleset.yaml files are
output:
  dir: build/rules      # 'rcas compile --all' writes <id>.bundle.json here
  manifests: [client]   # and <id>.client.manifest.json, for browsers and mobile apps
languages: %s
channels: [client, server]
sources:
  openapi: []           # - { path: api/openapi.yaml, schemas: [Order], idPrefix: %s.generated }
  jsonSchema: []        # - { path: schemas/order.schema.json, entity: Order }
analyze:
  exclude: ["**/test/**", "**/tests/**", "**/*.test.*", "**/*_test.go"]
proposals:
  dir: .rcas/proposals
`, yamlScalar(name), yamlScalar(prefix), l, prefix)
}

// yamlScalar quotes a string for YAML when it is not obviously a plain scalar.
func yamlScalar(s string) string {
	switch strings.ToLower(s) {
	case "true", "false", "null", "yes", "no", "on", "off", "y", "n", "~":
		return `"` + s + `"`
	}
	if regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 ._-]*$`).MatchString(s) && !strings.HasSuffix(s, " ") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func loadingGuide(id string, langs []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Loading the rules\n\n`rcas compile --all` writes `build/rules/%s.bundle.json` (and the client manifest).\n", id)
	b.WriteString("Every runtime loads the same bundle and gives the same answer. Load it once at start-up; evaluate per request.\n")
	if len(langs) == 0 {
		langs = languageNames()
	}
	for _, name := range langs {
		l := languages[name]
		fmt.Fprintf(&b, "\n## %s\n\nInstall: `%s`\n\n%s\n", l.title, l.install,
			strings.NewReplacer("{{out}}", "build/rules", "{{id}}", id).Replace(l.load))
	}
	b.WriteString("\nMore: https://rules.sdods.com/learn/languages/\n")
	return b.String()
}

// starterRuleset passes 'rcas check': one validation rule, one warning, and golden tests for both.
const starterRuleset = `# A first ruleset. Edit it, then run 'rcas check': it lints the file and runs the tests at the end.
# How to write rules: https://rules.sdods.com/reference/authoring-guidelines/
ruleCascade: 1.0.0
kind: RuleSet

metadata:
  id: {{id}}
  version: 0.1.0
  title: Example rules
  owner: "{{name}}"
  status: active

scope:
  - { level: organization, id: "{{org}}" }

entities:
  Order:
    schema: { $ref: "./order.schema.json#/$defs/Order" }

params:
  maxQuantity:
    type: integer
    default: 10
    overridePolicy: tighten-only
    tightenDirection: lower

rules:
  - id: order.quantity.max
    kind: validation
    title: An order has at most maxQuantity items
    target: { entity: Order, field: /quantity }
    operations: [create, update]
    triggers: [change, submit]
    when: { op: exists, args: [{ var: data.quantity }] }
    assert: { op: lte, args: [{ var: data.quantity }, { var: params.maxQuantity }] }
    severity: error
    finding:
      code: EXAMPLE-ORD-001
      message: order.quantityTooHigh
      args: { max: { var: params.maxQuantity } }

  - id: order.note.recommended
    kind: validation
    title: Large orders should carry a note
    target: { entity: Order, field: /note }
    operations: [create]
    triggers: [submit]
    when: { op: gte, args: [{ var: data.quantity }, 5] }
    assert: { op: exists, args: [{ var: data.note }] }
    severity: warning
    finding:
      code: EXAMPLE-ORD-002
      message: order.noteRecommended

messages:
  en:
    order.quantityTooHigh: "You can order at most {max} items."
    order.noteRecommended: "Add a note to an order of five items or more."

tests:
  - name: three items are allowed
    entity: Order
    operation: create
    given:
      data: { quantity: 3 }
    expect:
      decision: allow
      findings: []

  - name: eleven items are denied
    entity: Order
    operation: create
    given:
      data: { quantity: 11, note: rush }
    expect:
      decision: deny
      findings:
        - { rule: order.quantity.max, fields: [/quantity], message: You can order at most 10 items. }

  - name: six items without a note are allowed with a warning
    entity: Order
    operation: create
    given:
      data: { quantity: 6 }
    expect:
      decision: allow
      findings:
        - { rule: order.note.recommended }
`

// starterSchema is the entity the starter ruleset talks about. In a real project, point entities at
// the API's own schemas (an OpenAPI document or JSON Schema), so rules and API share one vocabulary.
const starterSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$defs": {
    "Order": {
      "type": "object",
      "properties": {
        "id": { "type": "string" },
        "quantity": { "type": "integer", "minimum": 1 },
        "note": { "type": "string", "maxLength": 500 }
      },
      "required": ["quantity"]
    }
  }
}
`

const ciWorkflow = `# Written by 'rcas init'. Lints every ruleset, runs its golden tests and compiles the bundles.
name: rules

on:
  pull_request:
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  rules:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - uses: actions/setup-node@v5
        with:
          node-version: 22
      - name: Check the rulesets and run their golden tests
        run: npx -y @rules-cascade/cli check
      - name: Compile the bundles
        run: npx -y @rules-cascade/cli compile --all
      - uses: actions/upload-artifact@v4
        with:
          name: rule-bundles
          path: build/rules/
`
