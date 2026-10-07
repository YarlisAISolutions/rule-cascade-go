package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	rulecascade "rulescascade.com/go"
	"rulescascade.com/go/internal/assets"
	"rulescascade.com/go/internal/clients"
	"rulescascade.com/go/internal/mcp"
	"rulescascade.com/go/internal/proposals"
)

const mcpHelp = `Without arguments, serves the Model Context Protocol on standard input and output for an AI
coding tool. The project is the directory of the nearest rcas.yaml (or --root). The tools read
rulesets, check, compile, evaluate (always a dry run), derive and analyze; the only one that writes
is propose_ruleset, and it writes only under .rcas/proposals/ (none with --read-only). Nothing
reaches the rules until a person runs '{program} proposals accept'.

  --root <dir>      the project directory (default: the working directory the tool starts it in)
  --read-only       leave out propose_ruleset
  --list-tools      print the tools and exit

install <client>... registers the server with AI coding tools: claude, codex, cursor, vscode,
copilot-cli, gemini, kiro, devin, windsurf, junie, cline, opencode, kilo, factory, amp, zed, warp,
augment, amazonq, muse, or all. It uses the tool's own command where it runs one reliably (claude mcp
add, codex mcp add) and otherwise merges an entry into the tool's configuration file, in the shape
that tool expects, keeping everything else there. Goose ('goose configure') and the GitHub Copilot
cloud agent (repository settings) are configured by hand: --print shows the entry.

  --scope project|user   project files are committed and shared; user files apply to every project
                         (default: project where the tool has a project file)
  --print                print the configuration and the commands, change nothing
  --file                 write the file even when the tool's own command is available
  --command npx|binary   start the server with 'npx -y @rules-cascade/cli mcp' (the default: works
                         for everyone who clones the project) or with the path of this binary
  --name <name>          the server name (default rules-cascade)
  --force                replace an existing entry of that name that differs

Check it: '{program} mcp --list-tools', then ask the agent to list its MCP tools.
`

func clientNames() []string { return clients.Names() }

func (c *cli) mcpCommand(args []string) int {
	if len(args) > 0 && args[0] == "install" {
		return c.mcpInstall(args[1:])
	}
	flags, positional, err := parseArgs(args, map[string]string{"--root": "root"}, []string{"--read-only", "--list-tools"})
	if err != nil || len(positional) > 0 {
		if err != nil {
			fmt.Fprintf(c.stderr, "%s mcp: %v\n", c.program, err)
		}
		c.usage("mcp")
		return exitUsage
	}
	if flags["root"] != "" {
		c.dir = flags["root"]
	}
	server := c.mcpServer(flags["--read-only"] != "")
	if flags["--list-tools"] != "" {
		for _, t := range server.Tools() {
			mode := "read"
			if !t.ReadOnly {
				mode = "write"
			}
			fmt.Fprintf(c.stdout, "%-24s %-5s %s\n", t.Name, mode, firstSentence(t.Description))
		}
		return exitOK
	}
	server.Log = c.stderr
	if err := server.Serve(c.stdin, c.stdout); err != nil {
		fmt.Fprintf(c.stderr, "%s mcp: %v\n", c.program, err)
		return exitFindings
	}
	return exitOK
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}

// ------------------------------------------------------------------ the tools

func schema(required []string, props map[string]any) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func strs(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

func argString(args map[string]any, name string) string { s, _ := args[name].(string); return s }

func argStrings(args map[string]any, name string) []string {
	var out []string
	switch v := args[name].(type) {
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
	case string:
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// projectFiles resolves paths given by an agent against the project root and refuses any outside.
func (c *cli) projectFiles(paths []string) ([]string, error) {
	root, err := c.projectRoot()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range paths {
		if filepath.IsAbs(p) {
			if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
				p = rel
			}
		}
		full, err := proposals.Within(root, p)
		if err != nil {
			return nil, err
		}
		out = append(out, full)
	}
	return out, nil
}

// rulesetIndex maps ruleset id -> file, for the rulesets of the project.
func (c *cli) rulesetIndex() (map[string]string, error) {
	cfg, err := c.loadConfig()
	var dir string
	var include []string
	if err == nil {
		dir, include = cfg.RulesDir(), cfg.Rules.Include
	} else if errors.Is(err, errNoConfig) {
		root, _ := c.projectRoot()
		dir = root
	} else {
		return nil, err
	}
	index := map[string]string{}
	for _, path := range findRulesets(dir, include) {
		doc, _, err := readDocument(path)
		if err != nil {
			continue
		}
		if id := text(member(member(doc, "metadata"), "id")); id != "" {
			index[id] = path
		}
	}
	return index, nil
}

func (c *cli) rulesetByID(id string) (*rulecascade.RuleSet, error) {
	index, err := c.rulesetIndex()
	if err != nil {
		return nil, err
	}
	path, ok := index[id]
	if !ok {
		return nil, fmt.Errorf("no ruleset %q in the project; known: %s", id, strings.Join(sortedKeys(index), ", "))
	}
	quiet := &cli{program: c.program, stdin: c.stdin, stdout: c.stderr, stderr: c.stderr, dir: c.dir, configPath: c.configPath}
	return quiet.loadFile(path)
}

// toAny converts a library value (with *Object members) to plain JSON values.
func toAny(v any) (any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	return out, json.Unmarshal(data, &out)
}

func (c *cli) mcpServer(readOnly bool) *mcp.Server {
	s := mcp.NewServer("rules-cascade", engineVersion())
	s.Instructions = "Rule Cascade: business rules as YAML rulesets, checked and compiled by rcas and evaluated identically in every language. " +
		"Find decisions in code with analyze, derive baseline rules from API schemas with derive, write the rest following get_authoring_guide, " +
		"run check until it is clean, then submit with propose_ruleset. Never edit ruleset files directly; a person accepts proposals with 'rcas proposals accept <id>'. " +
		"evaluate_rules is a dry run and changes nothing."
	quiet := &cli{program: c.program, stdin: c.stdin, stdout: c.stderr, stderr: c.stderr, dir: c.dir, configPath: c.configPath}
	if cfg, err := quiet.loadConfig(); err == nil {
		readOnly = readOnly || cfg.MCP.ReadOnly
	}
	if root, err := quiet.projectRoot(); err == nil {
		quiet.confine = root
		if real, err := filepath.EvalSymlinks(root); err == nil {
			quiet.confine = real
		}
	}

	s.AddTool(mcp.Tool{Name: "list_rules", ReadOnly: true,
		Description: "List the business rules that apply to an operation on an entity, with severity and plain-language titles. Call this before proposing a change so the user hears the constraints up front.",
		InputSchema: schema([]string{"ruleset", "entity"}, map[string]any{
			"ruleset": str("Ruleset id, e.g. acme.payments.transfer."), "entity": str("Entity name, e.g. Transfer."),
			"operation": map[string]any{"type": []any{"string", "null"}, "description": "create, read, update, delete, list or a custom action. Null for all."}}),
		Call: func(args map[string]any) (any, error) {
			rs, err := quiet.rulesetByID(argString(args, "ruleset"))
			if err != nil {
				return nil, err
			}
			mf, err := rs.Manifest("server")
			if err != nil {
				return nil, err
			}
			entity, operation := argString(args, "entity"), argString(args, "operation")
			rules := []any{}
			for _, r := range list(member(mf, "rules")) {
				if text(member(member(r, "target"), "entity")) != entity {
					continue
				}
				ops := list(member(r, "operations"))
				if operation != "" && !containsAny(ops, operation) {
					continue
				}
				item := map[string]any{"id": member(r, "id"), "title": member(r, "title"), "kind": member(r, "kind"),
					"severity": member(r, "severity"), "operations": ops, "field": member(member(r, "target"), "field"),
					"enforcement": member(r, "enforcement")}
				if acc := member(r, "acceptance"); acc != nil && member(acc, "allowed") == true {
					item["acceptableBy"] = member(acc, "roles")
				}
				rules = append(rules, item)
			}
			return toAny(map[string]any{"ruleset": rs.ID(), "version": rs.Version(), "rules": rules})
		}})

	s.AddTool(mcp.Tool{Name: "evaluate_rules", ReadOnly: true,
		Description: "Check a proposed operation against the business rules without changing anything (a dry run on the project's source rulesets). Returns decision (allow or deny), findings with severity and field pointers, effects and commands.",
		InputSchema: schema([]string{"ruleset", "entity", "operation", "data_json"}, map[string]any{
			"ruleset": str("Ruleset id."), "entity": str("Entity name."), "operation": str("create, read, update, delete, list or a custom action."),
			"data_json":        str("The proposed entity state as a JSON object, serialised to a string."),
			"original_json":    map[string]any{"type": []any{"string", "null"}, "description": "The stored state as a JSON string, for update, delete and custom actions."},
			"channel":          map[string]any{"type": "string", "enum": []any{"server", "client"}, "description": "Default server."},
			"actor_roles":      strs("Roles of a hypothetical actor, to see how acceptance rules behave. A test value only: in production the actor always comes from authentication."),
			"resolutions_json": map[string]any{"type": []any{"string", "null"}, "description": "Answers to findings, as a JSON array of {rule, type: acknowledge|accept-risk, justification}."},
		}),
		Call: func(args map[string]any) (any, error) {
			rs, err := quiet.rulesetByID(argString(args, "ruleset"))
			if err != nil {
				return nil, err
			}
			request := map[string]any{"entity": argString(args, "entity"), "operation": argString(args, "operation")}
			parse := func(name string) (any, error) {
				raw := argString(args, name)
				if raw == "" {
					return nil, nil
				}
				v, err := rulecascade.ParseJSON([]byte(raw))
				if err != nil {
					return nil, fmt.Errorf("%s is not valid JSON: %v", name, err)
				}
				return v, nil
			}
			for name, key := range map[string]string{"data_json": "data", "original_json": "original", "resolutions_json": "resolutions"} {
				v, err := parse(name)
				if err != nil {
					return nil, err
				}
				if v != nil {
					request[key] = v
				}
			}
			if roles := argStrings(args, "actor_roles"); roles != nil {
				r := make([]any, len(roles))
				for i, role := range roles {
					r[i] = role
				}
				request["actor"] = map[string]any{"id": "mcp-dry-run", "roles": r}
			}
			channel := argString(args, "channel")
			if channel == "" {
				channel = "server"
			}
			result, err := rs.Evaluate(request, channel, rulecascade.ConformanceOperators())
			if err != nil {
				return nil, err
			}
			return toAny(result)
		}})

	s.AddTool(mcp.Tool{Name: "explain_rule", ReadOnly: true,
		Description: "Return one rule in full: its target, condition, severity, enforcement, acceptance, messages, and where in the hierarchy it was defined or overridden.",
		InputSchema: schema([]string{"ruleset", "rule_id"}, map[string]any{"ruleset": str("Ruleset id."), "rule_id": str("Rule id.")}),
		Call: func(args map[string]any) (any, error) {
			rs, err := quiet.rulesetByID(argString(args, "ruleset"))
			if err != nil {
				return nil, err
			}
			mf, err := rs.Manifest("server")
			if err != nil {
				return nil, err
			}
			for _, r := range list(member(mf, "rules")) {
				if text(member(r, "id")) == argString(args, "rule_id") {
					return toAny(r)
				}
			}
			return nil, fmt.Errorf("no rule %q in %s", argString(args, "rule_id"), rs.ID())
		}})

	checkTool := func(name, desc string) {
		s.AddTool(mcp.Tool{Name: name, ReadOnly: true, Description: desc,
			InputSchema: schema(nil, map[string]any{
				"paths":   strs("Ruleset files or directories, relative to the project. Default: every ruleset of the project."),
				"content": str("Optional: the YAML or JSON of a draft ruleset to check without writing it. Checked next to the project's rulesets in rules.dir (so its parent is found)."),
			}),
			Call: func(args map[string]any) (any, error) {
				if draft := argString(args, "content"); draft != "" {
					return quiet.checkDraft(draft)
				}
				var files []string
				if paths := argStrings(args, "paths"); len(paths) > 0 {
					resolved, err := quiet.projectFiles(paths)
					if err != nil {
						return nil, err
					}
					files = quiet.expand(resolved)
				} else {
					found, status := quiet.configuredRulesets(name)
					if status != exitOK {
						return nil, errors.New("no rulesets found: pass paths, or create rcas.yaml with 'rcas init'")
					}
					files = found
				}
				reports := []*fileReport{}
				ok := true
				for _, f := range files {
					r := quiet.checkFile(f)
					ok = ok && r.bad() == 0
					reports = append(reports, r)
				}
				return toAny(map[string]any{"ok": ok, "files": reports})
			}})
	}
	checkTool("check", "Lint rulesets, load them (schema, inheritance, override policies, static checks) and run their golden tests. Run it until it reports ok before proposing.")
	checkTool("test", "Run the golden tests of rulesets (the same checks as check; named for test runs).")

	s.AddTool(mcp.Tool{Name: "compile", ReadOnly: true,
		Description: "Compile one ruleset and return a summary of its bundle (id, version, checksum, rule counts) and, if asked, the bundle itself. Writes nothing.",
		InputSchema: schema([]string{"path"}, map[string]any{"path": str("The ruleset file, relative to the project."),
			"include_bundle": map[string]any{"type": "boolean", "description": "Return the whole bundle (can be large)."}}),
		Call: func(args map[string]any) (any, error) {
			files, err := quiet.projectFiles([]string{argString(args, "path")})
			if err != nil {
				return nil, err
			}
			rs, err := quiet.loadFile(files[0])
			if err != nil {
				return nil, loadErr(err)
			}
			out := map[string]any{"id": rs.ID(), "version": rs.Version(), "checksum": rs.Checksum(), "channels": rs.Channels()}
			if args["include_bundle"] == true {
				out["bundle"] = rs.Bundle()
			}
			return toAny(out)
		}})

	s.AddTool(mcp.Tool{Name: "manifest", ReadOnly: true,
		Description: "Return the client or server manifest of a ruleset: what a browser (client) or a backend (server) receives.",
		InputSchema: schema([]string{"path"}, map[string]any{"path": str("The ruleset file, relative to the project."),
			"channel": map[string]any{"type": "string", "enum": []any{"client", "server"}}}),
		Call: func(args map[string]any) (any, error) {
			files, err := quiet.projectFiles([]string{argString(args, "path")})
			if err != nil {
				return nil, err
			}
			rs, err := quiet.loadFile(files[0])
			if err != nil {
				return nil, loadErr(err)
			}
			channel := argString(args, "channel")
			if channel == "" {
				channel = "client"
			}
			mf, err := rs.Manifest(channel)
			if err != nil {
				return nil, err
			}
			return toAny(mf)
		}})

	s.AddTool(mcp.Tool{Name: "derive", ReadOnly: true,
		Description: "Derive a baseline validation ruleset (required, enum, length, pattern, range) from an OpenAPI component schema or a JSON Schema. Returns the YAML and what could not be derived; writes nothing. Submit it with propose_ruleset.",
		InputSchema: schema([]string{"source", "id"}, map[string]any{
			"source":  str("The OpenAPI document or JSON Schema file, relative to the project."),
			"id":      str("The ruleset id to give it, e.g. acme.payments.transfer-generated."),
			"schema":  str("OpenAPI: the component schema name."),
			"pointer": str("JSON Schema: a JSON pointer to the entity schema, e.g. /$defs/Order. Default: the root."),
			"entity":  str("The entity name (default: the schema name)."),
			"scope":   strs("level:id pairs, e.g. organization:acme."),
		}),
		Call: func(args map[string]any) (any, error) {
			files, err := quiet.projectFiles([]string{argString(args, "source")})
			if err != nil {
				return nil, err
			}
			root, _ := quiet.projectRoot()
			res, err := runDerive(files[0], deriveOptions{Schema: argString(args, "schema"), Pointer: argString(args, "pointer"),
				ID: argString(args, "id"), Entity: argString(args, "entity"), Scope: argStrings(args, "scope"),
				Output: filepath.Join(root, quiet.rulesDirRel(), slug(argString(args, "id"))+".ruleset.yaml")})
			if err != nil {
				return nil, err
			}
			return map[string]any{"ruleset_yaml": string(res.Ruleset), "skipped": res.Skipped,
				"suggested_path": quiet.rulesDirRel() + "/" + slug(argString(args, "id")) + ".ruleset.yaml"}, nil
		}})

	s.AddTool(mcp.Tool{Name: "analyze", ReadOnly: true,
		Description: "Inventory an existing code base: API schemas (OpenAPI, JSON Schema) and validation code (Zod, Joi, class-validator, Pydantic, marshmallow, Bean Validation, Go validate tags, FluentValidation, Rails validates, hand-written if/throw checks) with file:line. Each hit is a candidate rule; read the code around it.",
		InputSchema: schema(nil, map[string]any{"path": str("Directory or file, relative to the project. Default: the project."),
			"include": strs("Glob patterns to include."), "exclude": strs("Glob patterns to exclude."),
			"max_files": map[string]any{"type": "integer", "description": "Stop after this many files (default 20000)."}}),
		Call: func(args map[string]any) (any, error) {
			path := argString(args, "path")
			if path == "" {
				path = "."
			}
			files, err := quiet.projectFiles([]string{path})
			if err != nil {
				return nil, err
			}
			max := 0
			if f, ok := args["max_files"].(float64); ok {
				max = int(f)
			}
			return toAny(quiet.analyze(files[0], argStrings(args, "include"), argStrings(args, "exclude"), max))
		}})

	if !readOnly {
		s.AddTool(mcp.Tool{Name: "propose_ruleset", ReadOnly: false,
			Description: "Submit new or changed ruleset files for a person to review. Writes only under .rcas/proposals/<id>/; the project's files change only when a person runs 'rcas proposals accept <id>'. Each ruleset is checked; a proposal that does not pass is stored as invalid with its problems, so run check first.",
			InputSchema: schema([]string{"id", "title", "files"}, map[string]any{
				"id":    str("Proposal id: lower case, digits, '.', '_' or '-', e.g. transfer-limits-from-service."),
				"title": str("One line: what the proposal does."),
				"files": map[string]any{"type": "array", "description": "The files, with paths relative to the project: *.ruleset.yaml/.yml/.json or *.schema.json files under the rules directory (anything else is refused).",
					"items": schema([]string{"path", "content"}, map[string]any{"path": str("e.g. rules/payments-transfer.ruleset.yaml"), "content": str("The whole file.")})},
				"rationale":   str("Why: the decisions in the code these rules capture, and anything the reviewer must check."),
				"source_refs": strs("Where the rules come from: file:line of the code or schema."),
			}),
			Call: func(args map[string]any) (any, error) {
				contents := map[string][]byte{}
				items, _ := args["files"].([]any)
				for _, item := range items {
					m, _ := item.(map[string]any)
					path, content := argString(m, "path"), argString(m, "content")
					if path == "" {
						return nil, errors.New("every file needs a path")
					}
					contents[filepath.ToSlash(path)] = lf([]byte(content))
				}
				p, err := quiet.proposeAs(&proposals.Proposal{ID: argString(args, "id"), Title: argString(args, "title"), Origin: "mcp",
					Rationale: argString(args, "rationale"), SourceRefs: argStrings(args, "source_refs")}, contents, false)
				if err != nil {
					return nil, err
				}
				return toAny(map[string]any{"proposal": p, "next": fmt.Sprintf("Tell the user: rcas proposals show %s, then rcas proposals accept %s", p.ID, p.ID)})
			}})
	}

	s.AddTool(mcp.Tool{Name: "list_proposals", ReadOnly: true, Description: "List proposals and their status (pending, invalid, accepted, rejected).",
		InputSchema: schema(nil, map[string]any{"status": str("Filter by status.")}),
		Call: func(args map[string]any) (any, error) {
			store, err := quiet.store()
			if err != nil {
				return nil, err
			}
			list, err := store.List(argString(args, "status"))
			if err != nil {
				return nil, err
			}
			if list == nil {
				list = []*proposals.Proposal{}
			}
			return toAny(map[string]any{"proposals": list})
		}})
	s.AddTool(mcp.Tool{Name: "get_proposal", ReadOnly: true, Description: "Return one proposal with the content of its files and the problems check found.",
		InputSchema: schema([]string{"id"}, map[string]any{"id": str("Proposal id.")}),
		Call: func(args map[string]any) (any, error) {
			store, err := quiet.store()
			if err != nil {
				return nil, err
			}
			p, err := store.Get(argString(args, "id"))
			if err != nil {
				return nil, err
			}
			files := map[string]string{}
			for _, f := range p.Files {
				data, _ := store.Content(p.ID, f.Path)
				files[f.Path] = string(data)
			}
			return toAny(map[string]any{"proposal": p, "contents": files})
		}})

	s.AddTool(mcp.Tool{Name: "get_spec_section", ReadOnly: true,
		Description: "Return a section of the Rule Cascade specification by number (\"4.4\" portable patterns, \"5\" loading, \"8\" evaluation, \"13\" engine protocol) or by words. Without a section, the list of headings.",
		InputSchema: schema(nil, map[string]any{"section": str("Section number or words.")}),
		Call:        func(args map[string]any) (any, error) { return docSection("specification", argString(args, "section")) }})
	s.AddTool(mcp.Tool{Name: "get_operator_reference", ReadOnly: true,
		Description: "Return the operator reference of the expression language (specification 4.3), or the part about one operator.",
		InputSchema: schema(nil, map[string]any{"operator": str("An operator, e.g. lte, matches, all. Omit for the whole list.")}),
		Call: func(args map[string]any) (any, error) {
			doc, _ := assets.Doc("specification")
			section, _ := assets.Section(doc, "4.3")
			op := argString(args, "operator")
			if op == "" {
				return section, nil
			}
			var hits []string
			for _, line := range strings.Split(section, "\n") {
				if strings.Contains(line, "`"+op+"`") {
					hits = append(hits, line)
				}
			}
			if cookbook, ok := assets.Doc("cookbook"); ok {
				for _, line := range strings.Split(cookbook, "\n") {
					if strings.Contains(line, "op: "+op+",") || strings.Contains(line, "`"+op+"`") {
						hits = append(hits, line)
						if len(hits) > 40 {
							break
						}
					}
				}
			}
			if len(hits) == 0 {
				return nil, fmt.Errorf("operator %q is not in the specification; call without operator for the list", op)
			}
			return strings.Join(hits, "\n"), nil
		}})
	s.AddTool(mcp.Tool{Name: "get_authoring_guide", ReadOnly: true,
		Description: "Return a guide: authoring (the MUST/SHOULD rules for rulesets), naming, enforcement (from API to UI), openapi (derivation and bindings) or cookbook (rules to copy). With section, only that part.",
		InputSchema: schema([]string{"topic"}, map[string]any{
			"topic":   map[string]any{"type": "string", "enum": []any{"authoring", "naming", "enforcement", "openapi", "cookbook"}},
			"section": str("Optional section number or words, e.g. \"10\" or \"golden tests\".")}),
		Call: func(args map[string]any) (any, error) {
			return docSection(argString(args, "topic"), argString(args, "section"))
		}})

	s.SetResources(func() []mcp.Resource {
		var out []mcp.Resource
		for _, topic := range assets.Topics() {
			topic := topic
			out = append(out, mcp.Resource{URI: "rcas://docs/" + topic, Name: topic, MimeType: "text/markdown",
				Description: "Rule Cascade " + topic,
				Read: func() (string, error) {
					doc, _ := assets.Doc(topic)
					return doc, nil
				}})
		}
		if index, err := quiet.rulesetIndex(); err == nil {
			for _, id := range sortedKeys(index) {
				path := index[id]
				out = append(out, mcp.Resource{URI: "rcas://rulesets/" + id, Name: id, MimeType: "text/yaml",
					Description: "Source of ruleset " + id,
					Read: func() (string, error) {
						data, err := os.ReadFile(path)
						return string(data), err
					}})
			}
		}
		return out
	})
	return s
}

func containsAny(list []any, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func loadErr(err error) error {
	var failure *rulecascade.LoadError
	if errors.As(err, &failure) {
		var lines []string
		for _, p := range failure.Problems {
			lines = append(lines, strings.TrimSpace(p.Code+": "+p.Rule+" "+p.Message))
		}
		return errors.New("the ruleset does not load:\n" + strings.Join(lines, "\n"))
	}
	return err
}

func docSection(topic, section string) (any, error) {
	doc, ok := assets.Doc(topic)
	if !ok {
		return nil, fmt.Errorf("unknown topic %q (%s)", topic, strings.Join(assets.Topics(), ", "))
	}
	if section == "" {
		if topic == "specification" {
			return "Sections:\n" + strings.Join(assets.Headings(doc), "\n"), nil
		}
		return doc, nil
	}
	text, ok := assets.Section(doc, section)
	if !ok {
		return nil, fmt.Errorf("no section %q; the sections are:\n%s", section, strings.Join(assets.Headings(doc), "\n"))
	}
	return text, nil
}

// checkDraft checks ruleset content that is not written anywhere, next to the project's rulesets.
func (c *cli) checkDraft(content string) (any, error) {
	root, err := c.projectRoot()
	if err != nil {
		return nil, err
	}
	dir := "rules"
	if cfg, err := c.loadConfig(); err == nil {
		if rel, err := filepath.Rel(root, cfg.RulesDir()); err == nil {
			dir = filepath.ToSlash(rel)
		}
	}
	name := dir + "/.draft.ruleset.yaml"
	if strings.HasPrefix(strings.TrimSpace(content), "{") {
		name = dir + "/.draft.ruleset.json"
	}
	problems, notes, err := c.validate(root, map[string][]byte{name: lf([]byte(content))})
	if err != nil {
		return nil, err
	}
	p := problems[name]
	if p == nil {
		p = []string{}
	}
	return map[string]any{"ok": len(p) == 0, "problems": p, "notes": notes[name]}, nil
}

// ------------------------------------------------------------------ mcp install

func (c *cli) mcpInstall(args []string) int {
	flags, names, err := parseArgs(args, map[string]string{"--scope": "scope", "--command": "command", "--name": "name"},
		[]string{"--print", "--file", "--force"})
	if err != nil || len(names) == 0 {
		if err != nil {
			fmt.Fprintf(c.stderr, "%s mcp install: %v\n", c.program, err)
		}
		fmt.Fprintf(c.stderr, "usage: %s mcp install <%s|all>... [--scope project|user] [--print] [--file] [--command npx|binary] [--force]\n",
			c.program, strings.Join(clientNames(), "|"))
		return exitUsage
	}
	var targets []clients.Client
	for _, n := range names {
		for _, name := range splitList(n, clientNames()) {
			client, ok := clients.Find(name)
			if !ok {
				fmt.Fprintf(c.stderr, "%s mcp install: unknown client %q (%s, all)\n", c.program, name, strings.Join(clientNames(), ", "))
				return exitUsage
			}
			targets = append(targets, client)
		}
	}
	server := clients.Server{Name: "rules-cascade", Command: "npx", Args: []string{"-y", "@rules-cascade/cli", "mcp"}}
	if flags["name"] != "" {
		server.Name = flags["name"]
	}
	switch flags["command"] {
	case "", "npx":
		if runtime.GOOS == "windows" {
			// npx is a .cmd script on Windows, which the tools cannot start directly
			server.Command, server.Args = "cmd", append([]string{"/c", "npx"}, server.Args...)
		}
	case "binary":
		exe, err := os.Executable()
		if err != nil {
			return c.fail(err)
		}
		server.Command, server.Args = exe, []string{"mcp"}
	default:
		fmt.Fprintf(c.stderr, "%s mcp install: --command is npx or binary\n", c.program)
		return exitUsage
	}
	root, err := c.projectRoot()
	if err != nil {
		return c.fail(err)
	}
	home, _ := os.UserHomeDir()
	status := exitOK
	for _, client := range targets {
		scope := flags["scope"]
		if scope == "" {
			scope = clients.DefaultScope(client)
		}
		// the tool's own command first: it knows scopes the file writer does not (claude --scope user, local)
		cliCmd := clients.CLIArgs(client, scope, server)
		if cliCmd != nil && flags["--print"] == "" && flags["--file"] == "" && (client.Name == "claude" || client.Name == "codex") {
			if _, err := exec.LookPath(cliCmd[0]); err == nil {
				if done := c.runClientCLI(client, cliCmd, server.Name, scope, root, flags["--force"] != ""); done {
					continue
				}
			}
		}
		path, err := clients.Locate(client, scope, root, home, os.Getenv("CODEX_HOME"))
		if err != nil && !(flags["--print"] != "" && cliCmd != nil) {
			fmt.Fprintf(c.stderr, "%s: %v\n", client.Name, err)
			status = exitFindings
			continue
		}
		if flags["--print"] != "" {
			if path != "" {
				fmt.Fprintf(c.stdout, "# %s (%s scope): add to %s\n%s", client.Title, scope, path, clients.Snippet(client, server))
			}
			if cliCmd != nil {
				fmt.Fprintf(c.stdout, "# or run:\n%s\n", shellJoin(cliCmd))
			}
			fmt.Fprintln(c.stdout)
			continue
		}
		t, err := clients.Plan(client, path, scope, server, flags["--force"] != "")
		if err != nil {
			fmt.Fprintf(c.stderr, "%s: %v\n", client.Name, err)
			status = exitFindings
			continue
		}
		switch {
		case t.Same:
			fmt.Fprintf(c.stdout, "%-9s unchanged  %s\n", client.Name, path)
		case t.Exists && !t.Changed:
			fmt.Fprintf(c.stdout, "%-9s kept       %s has a different %q entry; --force replaces it\n", client.Name, path, server.Name)
		default:
			if err := clients.Write(t); err != nil {
				fmt.Fprintf(c.stderr, "%s: %v\n", client.Name, err)
				status = exitFindings
				continue
			}
			verb := "added"
			if t.Exists {
				verb = "replaced"
			}
			fmt.Fprintf(c.stdout, "%-9s %-10s %s\n", client.Name, verb, path)
		}
	}
	return status
}

// runClientCLI registers the server with the tool's own command. It reports false when the
// command failed in a way the file writer can handle instead.
func (c *cli) runClientCLI(client clients.Client, cmd []string, name, scope, dir string, force bool) bool {
	run := func(args []string) (string, error) {
		command := exec.Command(args[0], args[1:]...)
		command.Dir = dir
		out, err := command.CombinedOutput()
		return string(out), err
	}
	out, err := run(cmd)
	if err != nil && strings.Contains(strings.ToLower(out), "already exists") {
		if !force {
			fmt.Fprintf(c.stdout, "%-9s kept       %s already has a %q server; --force replaces it\n", client.Name, client.Title, name)
			return true
		}
		remove := []string{cmd[0], "mcp", "remove"}
		if client.Name == "claude" {
			remove = append(remove, "-s", scope)
		}
		remove = append(remove, name)
		if _, err := run(remove); err == nil {
			out, err = run(cmd)
		}
	}
	if err != nil {
		fmt.Fprintf(c.stderr, "%-9s '%s' failed (%v): %s; writing the file instead\n", client.Name, shellJoin(cmd), err, strings.TrimSpace(out))
		return false
	}
	fmt.Fprintf(c.stdout, "%-9s added      with '%s'\n", client.Name, shellJoin(cmd))
	return true
}

func shellJoin(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\"'$`\\|&;<>()*?[]{}~") {
			out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		} else {
			out[i] = a
		}
	}
	return strings.Join(out, " ")
}
