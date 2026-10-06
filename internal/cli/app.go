package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Exit statuses, the same for every command.
const (
	exitOK       = 0
	exitFindings = 1 // problems, failed tests, a failed evaluation or write
	exitUsage    = 2
	exitConfig   = 3 // rcas.yaml missing where it is needed, or invalid
)

// command is one entry of the command table: the help text and the function that runs it.
type command struct {
	name     string
	synopsis string // first line of the help: the arguments
	summary  string // one line for the command list
	help     string // the rest of `rcas help <name>`
	run      func(c *cli, args []string) int
}

var commands []command

func init() {
	core := func(name string) func(c *cli, args []string) int {
		return func(c *cli, args []string) int { return c.core(name, args) }
	}
	commands = []command{
		{"version", "[--json]", "print the version", "", core("version")},
		{"init", "[dir] [--name <name>] [--id-prefix <prefix>] [--lang ts,python,java,go,other] [--from <path>]\n          [--agent claude,codex,cursor,copilot,gemini|all] [--mcp <client>|all] [--ci github|none] [--force] [--dry-run]",
			"create rcas.yaml, a first ruleset with golden tests, and optionally agent files and MCP config", initHelp, (*cli).initCommand},
		{"check", "[file|dir]... [--json]", "lint, load and run the golden tests of each ruleset (default: the rules of rcas.yaml)", checkHelp, core("check")},
		{"test", "[file|dir]... [--json]", "the same as check; named for CI scripts that run tests", "", core("test")},
		{"compile", "<file> [-o out.bundle.json] | --all [-o dir]", "compile a ruleset, or every ruleset of the project, into portable JSON bundles", compileHelp, core("compile")},
		{"manifest", "<ruleset-or-bundle> | --bundle <bundle.json> | --manifest <manifest.json> [--channel client|server] [-o out.manifest.json]",
			"print or write a manifest (default: client, or the manifest's own)", "", core("manifest")},
		{"evaluate", "--bundle <bundle.json> | --manifest <manifest.json> [--channel server|client] [--conformance-operators] [request.json|-]",
			"evaluate one request, read from a file or standard input", "", core("evaluate")},
		{"engine", "[--conformance-operators]", "serve the JSON Lines engine protocol on standard input and output", "", core("engine")},
		{"derive", "<openapi.yaml|schema.json> --id <ruleset.id> [--schema <Name> | --pointer </$defs/X>] [--entity <Name>]\n          [--scope level:id]... [--version <v>] [--title <t>] [--tests <file>] [--codes-from <file>] [-o <file>] [--check] [--propose]",
			"derive a baseline validation ruleset from an OpenAPI component schema or a JSON Schema", deriveHelp, (*cli).deriveCommand},
		{"analyze", "[path] [--format md|json] [-o <file>] [--derive] [--include <glob>]... [--exclude <glob>]... [--max-files <n>]",
			"inventory the API schemas and validation code of an existing project", analyzeHelp, (*cli).analyzeCommand},
		{"mcp", "[--root <dir>] [--read-only] [--list-tools] | install <client>... [--scope project|user] [--print] [--file] [--command npx|binary] [--force]",
			"serve the Model Context Protocol on standard input and output, or register it with an AI coding tool", mcpHelp, (*cli).mcpCommand},
		{"agent", "install [--for claude,codex,cursor,copilot,gemini|all] [--print] [--force] [--dry-run]",
			"write agent instructions, sub-agents and skills for AI coding tools", agentHelp, (*cli).agentCommand},
		{"proposals", "list [--json] | show <id> [--diff] | accept <id> [--force] | reject <id> [--reason <text>]",
			"review the rulesets an agent or `derive --propose` proposed; accepting is the only way they reach the rules", proposalsHelp, (*cli).proposalsCommand},
		{"doctor", "[--json]", "check the installation, the project and the AI tool configuration", "", (*cli).doctorCommand},
		{"completion", "bash|zsh|fish|powershell", "print a shell completion script", completionHelp, (*cli).completionCommand},
		{"help", "[command]", "show help for a command", "", (*cli).helpCommand},
	}
}

func lookup(name string) *command {
	for i := range commands {
		if commands[i].name == name {
			return &commands[i]
		}
	}
	return nil
}

// ProgramName is the name the command was started as: rule-cascade when it was installed or
// invoked under that name (or RCAS_PROGRAM_NAME says so, as the npm shim does), rcas otherwise.
func ProgramName(argv0 string) string {
	if name := os.Getenv("RCAS_PROGRAM_NAME"); name == "rcas" || name == "rule-cascade" {
		return name
	}
	base := argv0
	if i := strings.LastIndexAny(base, `/\`); i >= 0 { // either separator: the name may come from another OS's shim
		base = base[i+1:]
	}
	base = strings.TrimSuffix(strings.ToLower(base), ".exe")
	if strings.HasPrefix(base, "rule-cascade") {
		return "rule-cascade"
	}
	return "rcas"
}

// Main runs one command line and returns the exit status.
func Main(program string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := &cli{program: program, stdin: stdin, stdout: stdout, stderr: stderr}
	for len(args) > 0 {
		arg := args[0]
		name, value, hasValue := strings.Cut(arg, "=")
		switch {
		case arg == "-h" || arg == "--help":
			return c.helpCommand(args[1:])
		case arg == "--version" || arg == "-V":
			return c.core("version", nil)
		case arg == "--no-color":
			os.Setenv("NO_COLOR", "1")
			args = args[1:]
			continue
		case name == "-C" || name == "--config":
			if !hasValue {
				if len(args) < 2 {
					fmt.Fprintf(stderr, "%s: %s needs a value\n", program, name)
					return exitUsage
				}
				value, args = args[1], args[1:]
			}
			if name == "-C" {
				c.dir = value
			} else {
				c.configPath = value
			}
			args = args[1:]
			continue
		}
		break
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, c.usageText())
		return exitUsage
	}
	if args[0] != "help" && len(args) > 1 && (args[1] == "--help" || args[1] == "-h") {
		return c.helpCommand(args[:1])
	}
	if cmd := lookup(args[0]); cmd != nil {
		return cmd.run(c, args[1:])
	}
	fmt.Fprintf(stderr, "%s: unknown command %q\n\n%s", program, args[0], c.usageText())
	return exitUsage
}

// run is Main for the in-process tests, as rcas.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return Main("rcas", args, stdin, stdout, stderr)
}

func (c *cli) usageText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "usage: %s [-C <dir>] [--config <rcas.yaml>] [--no-color] <command> [arguments]\n\n", c.program)
	for _, cmd := range commands {
		fmt.Fprintf(&b, "  %-11s %s\n", cmd.name, cmd.summary)
	}
	fmt.Fprintf(&b, "\nRun '%s help <command>' for its options. Documentation: https://rules.sdods.com/reference/cli/\n", c.program)
	fmt.Fprint(&b, "\nRulesets are YAML or JSON. Parents are looked up among the *.ruleset.* files next to the ruleset, and\nentity schemas are resolved relative to it.\n")
	return b.String()
}

// usage prints the synopsis of a command to standard error, for a usage error.
func (c *cli) usage(name string) {
	if cmd := lookup(name); cmd != nil {
		fmt.Fprintf(c.stderr, "usage: %s %s %s\n", c.program, cmd.name, cmd.synopsis)
		return
	}
	fmt.Fprint(c.stderr, c.usageText())
}

func (c *cli) helpCommand(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(c.stdout, c.usageText())
		return exitOK
	}
	cmd := lookup(args[0])
	if cmd == nil {
		fmt.Fprintf(c.stderr, "%s help: unknown command %q\n", c.program, args[0])
		return exitUsage
	}
	fmt.Fprintf(c.stdout, "usage: %s %s %s\n\n%s\n", c.program, cmd.name, cmd.synopsis, cmd.summary)
	if cmd.help != "" {
		fmt.Fprintf(c.stdout, "\n%s", strings.ReplaceAll(cmd.help, "{program}", c.program))
	}
	return exitOK
}

// ------------------------------------------------------------------ paths

// path resolves a path argument against -C.
func (c *cli) path(p string) string {
	if p == "" || p == "-" || filepath.IsAbs(p) || c.dir == "" {
		return p
	}
	return filepath.Join(c.dir, p)
}

// outPath resolves an output path; the empty string means standard output.
func (c *cli) outPath(p string) string { return c.path(p) }

// workDir is the directory the command works in: -C, or the current directory.
func (c *cli) workDir() string {
	if c.dir != "" {
		return c.dir
	}
	return "."
}

// expand replaces each directory argument with the rulesets found under it.
func (c *cli) expand(args []string) []string {
	var out []string
	for _, a := range args {
		p := c.path(a)
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			out = append(out, findRulesets(p, nil)...)
			continue
		}
		out = append(out, p)
	}
	return out
}

// configuredRulesets returns the rulesets of the project, for check and test without arguments.
func (c *cli) configuredRulesets(command string) ([]string, int) {
	cfg, err := c.loadConfig()
	if err != nil {
		fmt.Fprintf(c.stderr, "%s %s: %v\n", c.program, command, err)
		if os.IsNotExist(err) || strings.Contains(err.Error(), "no rcas.yaml") {
			c.usage(command)
			return nil, exitUsage
		}
		return nil, exitConfig
	}
	found := findRulesets(cfg.RulesDir(), cfg.Rules.Include)
	if len(found) == 0 {
		fmt.Fprintf(c.stderr, "%s %s: no *.ruleset.* files under %s\n", c.program, command, cfg.RulesDir())
		return nil, exitFindings
	}
	return found, exitOK
}

// findRulesets lists the ruleset files under a directory, sorted. include, when given, holds glob
// patterns relative to the directory (with ** for any number of directories).
func findRulesets(root string, include []string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if len(include) > 0 {
			for _, pattern := range include {
				if globMatch(pattern, rel) {
					out = append(out, p)
					break
				}
			}
		} else if isRulesetFile(d.Name()) {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func isRulesetFile(name string) bool {
	lower := strings.ToLower(name)
	for _, ext := range []string{".ruleset.yaml", ".ruleset.yml", ".ruleset.json"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// skipDir names the directories no command looks into: dependencies, build output, version control.
func skipDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", "vendor", "dist", "build", "target", "out", ".next",
		".venv", "venv", "__pycache__", ".rcas", ".idea", ".vscode", ".gradle", "bin", "obj", ".terraform":
		return true
	}
	return false
}

// globMatch matches a slash-separated path against a pattern in which * matches within one
// segment and ** matches any number of segments.
func globMatch(pattern, path string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(path, "/"))
}

func matchSegments(pattern, path []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			for i := 0; i <= len(path); i++ {
				if matchSegments(pattern[1:], path[i:]) {
					return true
				}
			}
			return false
		}
		if len(path) == 0 {
			return false
		}
		if ok, _ := filepath.Match(pattern[0], path[0]); !ok {
			return false
		}
		pattern, path = pattern[1:], path[1:]
	}
	return len(path) == 0
}

// printJSON writes any Go value as indented JSON (structs included), without HTML escaping.
func (c *cli) printJSON(v any) int {
	enc := json.NewEncoder(c.stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return c.fail(err)
	}
	return exitOK
}
