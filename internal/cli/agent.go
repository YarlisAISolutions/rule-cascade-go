package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"rulescascade.com/go/internal/assets"
)

const agentHelp = `install [--for <tools>] [--stack <languages>] [--print] [--force] [--dry-run]
list

Writes the instructions AI coding agents read, so they find business rules in the code, write
rulesets with golden tests, check them, submit them as proposals instead of editing the rules, and
enforce them in the project's own languages.

Every install writes the portable part, which most tools read as it is:

  AGENTS.md                        a managed block (the rest of the file is kept)
  .agents/skills/<skill>/SKILL.md  the skills (the Agent Skills format): rules-cascade, -review,
                                   -migrate, -capture, and one per language of the project

and, for each chosen tool, what it needs on top, in its own format: instructions scoped to the
rulesets and to the application code of the project's languages, the commands /rcas-capture and
/rcas-review, and the sub-agents rcas-analyst, -author, -reviewer and -tester with only the tools
their role needs ('{program} agent list' shows every file):

  claude    CLAUDE.md, .claude/skills/, .claude/agents/, .claude/commands/, .claude/rules/
  codex     .codex/agents/rcas-*.toml (commands: the skills)
  copilot   .github/instructions/, .github/prompts/rcas-*.prompt.md, .github/agents/rcas-*.agent.md
  cursor    .cursor/rules/rules-cascade.mdc and rules-cascade-code.mdc, .cursor/agents/
  gemini    GEMINI.md (a managed block), .gemini/commands/rcas-*.toml, .gemini/agents/
  kiro      .kiro/steering/ (rulesets and code), .kiro/skills/, .kiro/agents/
  devin     .devin/rules/ (rulesets and code), .devin/workflows/ (formerly Windsurf)
  junie     .junie/agents/, .junie/commands/
  cline     .clinerules/ (rulesets and code), .cline/skills/
  opencode  .opencode/agents/, .opencode/commands/
  factory   .factory/droids/, .factory/commands/
  amazonq   .amazonq/rules/rules-cascade.md
  tabnine   .tabnine/guidelines/rules-cascade.md, .tabnine/agent/commands/rcas-*.toml
  kilo      .kilo/commands/
  augment   .augment/agents/, .augment/commands/ (unless claude: Auggie reads .claude/commands)
  muse, amp, goose, warp, zed   AGENTS.md and .agents/skills/ only

--for takes a comma-separated list, or all. Without it: agents.tools in rcas.yaml, else the tools
the project already shows signs of (a .cursor/ folder, CLAUDE.md, .kiro/ ...).
--stack picks the language skills: ts, python, java, go, other (comma-separated). Without it: the
languages of rcas.yaml, else the languages found in the project (package.json, pom.xml, go.mod ...).

Managed blocks are replaced on every run. Other files are refreshed while they carry the line
'rcas-managed'; delete that line to keep your own edits. Files without it are written only when
missing, or with --force. Pair it with '{program} mcp install <tool>' so the agent can call the tools
the instructions name.
`

// agentTool is an AI coding tool 'rcas agent install' can write for.
type agentTool struct {
	Name, Title string
	// Detect lists files and directories whose presence shows that a project uses the tool.
	Detect []string
	// Reads says what the tool picks up of the portable part, for 'agent list'.
	Reads string
}

// agentTools is every tool, in the order 'all' writes them. The conventions were checked against
// each tool's documentation in October 2026; docs/agents.md has the sources.
var agentTools = []agentTool{
	{"claude", "Claude Code", []string{"CLAUDE.md", ".claude", ".mcp.json"}, "AGENTS.md through CLAUDE.md; skills only in .claude/skills"},
	{"codex", "OpenAI Codex (CLI, IDE, app)", []string{".codex", "AGENTS.override.md"}, "AGENTS.md, .agents/skills"},
	{"copilot", "GitHub Copilot (VS Code, cloud agent, CLI)", []string{".github/copilot-instructions.md", ".github/instructions", ".github/agents", ".github/prompts", ".vscode/mcp.json"}, "AGENTS.md, .agents/skills, .claude/agents"},
	{"cursor", "Cursor", []string{".cursor", ".cursorrules"}, "AGENTS.md, .agents/skills, .claude/agents"},
	{"gemini", "Gemini CLI", []string{"GEMINI.md", ".gemini"}, ".agents/skills; not AGENTS.md by default"},
	{"kiro", "Kiro (IDE and CLI)", []string{".kiro"}, "AGENTS.md; skills only in .kiro/skills"},
	{"devin", "Devin Desktop and Devin CLI (formerly Windsurf)", []string{".devin", ".windsurf", ".windsurfrules"}, "AGENTS.md, .agents/skills"},
	{"junie", "JetBrains Junie", []string{".junie"}, "AGENTS.md, .agents/skills"},
	{"cline", "Cline", []string{".clinerules", ".cline"}, "AGENTS.md; skills in .cline/skills or .claude/skills"},
	{"opencode", "OpenCode", []string{"opencode.json", "opencode.jsonc", ".opencode"}, "AGENTS.md, .agents/skills"},
	{"factory", "Factory Droid", []string{".factory"}, "AGENTS.md, .agents/skills"},
	{"amazonq", "Amazon Q Developer (superseded by Kiro)", []string{".amazonq"}, "rules in .amazonq/rules"},
	{"tabnine", "Tabnine", []string{".tabnine", "TABNINE.md"}, "guidelines in .tabnine/guidelines; .agents/skills (CLI)"},
	{"muse", "Meta Muse Code", []string{".muse"}, "AGENTS.md, .agents/skills"},
	{"amp", "Amp", []string{".amp"}, "AGENTS.md, .agents/skills"},
	{"goose", "Goose", []string{".goosehints", ".goose"}, "AGENTS.md, .agents/skills"},
	{"warp", "Warp", []string{"WARP.md", ".warp"}, "AGENTS.md, .agents/skills"},
	{"zed", "Zed", []string{".zed", ".rules"}, "AGENTS.md (unless .rules or another rules file exists), .agents/skills"},
	{"kilo", "Kilo Code", []string{".kilo", ".kilocode", "kilo.json", "kilo.jsonc"}, "AGENTS.md, .agents/skills"},
	{"augment", "Augment Code (Auggie)", []string{".augment", ".augment-guidelines"}, "AGENTS.md, .agents/skills"},
}

// toolAliases are earlier or alternative names.
var toolAliases = map[string]string{"windsurf": "devin", "vscode": "copilot", "q": "amazonq", "droid": "factory", "auggie": "augment"}

func agentToolNames() []string {
	out := make([]string, len(agentTools))
	for i, t := range agentTools {
		out[i] = t.Name
	}
	return out
}

func findAgentTool(name string) (agentTool, bool) {
	if real, ok := toolAliases[name]; ok {
		name = real
	}
	for _, t := range agentTools {
		if t.Name == name {
			return t, true
		}
	}
	return agentTool{}, false
}

// detectAgentTools lists the tools a project already shows signs of.
func detectAgentTools(base string) []string {
	var out []string
	for _, t := range agentTools {
		for _, marker := range t.Detect {
			if _, err := os.Stat(filepath.Join(base, filepath.FromSlash(marker))); err == nil {
				out = append(out, t.Name)
				break
			}
		}
	}
	return out
}

// languageMarkers are the files that show a project's languages, checked in the project directory
// and in the directories directly below it (a monorepo's services).
var languageMarkers = map[string][]string{
	"ts":     {"package.json", "tsconfig.json", "deno.json"},
	"python": {"pyproject.toml", "requirements.txt", "setup.py", "Pipfile"},
	"java":   {"pom.xml", "build.gradle", "build.gradle.kts", "build.sbt"},
	"go":     {"go.mod"},
	"other":  {"Cargo.toml", "composer.json", "Gemfile", "Package.swift", "mix.exs", "*.csproj", "*.sln", "*.fsproj"},
}

func detectLanguages(base string) []string {
	dirs := []string{base}
	if entries, err := os.ReadDir(base); err == nil {
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() && !strings.HasPrefix(name, ".") && name != "node_modules" && name != "vendor" && name != "dist" && name != "build" && name != "target" {
				dirs = append(dirs, filepath.Join(base, name))
			}
		}
	}
	var out []string
	for _, lang := range languageNames() {
		found := false
		for _, dir := range dirs {
			for _, marker := range languageMarkers[lang] {
				if matches, _ := filepath.Glob(filepath.Join(dir, marker)); len(matches) > 0 {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if found {
			out = append(out, lang)
		}
	}
	return out
}

// languageSkill is the skill that enforces rules in a language.
var languageSkill = map[string]string{"ts": "rules-cascade-typescript", "python": "rules-cascade-python",
	"java": "rules-cascade-java", "go": "rules-cascade-go", "other": "rules-cascade-engine"}

// coreSkills are written for every project.
var coreSkills = []string{"rules-cascade", "rules-cascade-review", "rules-cascade-migrate"}

// skillsFor lists the skills for a project's languages: the core ones and one per language, or every
// language skill when no language is known.
func skillsFor(langs []string) []string {
	out := append([]string{}, coreSkills...)
	if len(langs) == 0 {
		langs = languageNames()
	}
	for _, l := range langs {
		if s := languageSkill[l]; s != "" && !contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

const (
	blockBegin = "<!-- rcas:begin (written by 'rcas agent install'; edit outside this block) -->"
	blockEnd   = "<!-- rcas:end -->"
	// managedMarker in a file means 'rcas agent install' may refresh it; delete the line to keep edits.
	managedMarker = "rcas-managed"
	managedLine   = "<!-- rcas-managed: 'rcas agent install' refreshes this file. Delete this line to keep your own edits. -->"
)

// upsertBlock puts block between the rcas markers of a document: it replaces an existing block,
// or appends one (creating the document with a title when there is none).
func upsertBlock(existing []byte, title, block string) ([]byte, error) {
	managed := blockBegin + "\n" + strings.TrimRight(block, "\n") + "\n" + blockEnd + "\n"
	crlf := strings.Contains(string(existing), "\r\n")
	text := strings.ReplaceAll(string(existing), "\r\n", "\n")
	begins, ends := strings.Count(text, blockBegin), strings.Count(text, blockEnd)
	var out string
	switch {
	case begins == 0 && ends == 0 && strings.TrimSpace(text) == "":
		out = "# " + title + "\n\n" + managed
	case begins == 0 && ends == 0:
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		out = text + "\n" + managed
	case begins == 1 && ends == 1 && strings.Index(text, blockBegin) < strings.Index(text, blockEnd):
		i := strings.Index(text, blockBegin)
		end := strings.Index(text, blockEnd) + len(blockEnd)
		if end < len(text) && text[end] == '\n' {
			end++
		}
		out = text[:i] + managed + text[end:]
	default:
		return nil, fmt.Errorf("the rcas:begin / rcas:end markers are unbalanced; fix them by hand, then run again")
	}
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return []byte(out), nil
}

// frontmatter splits a Markdown document into its YAML frontmatter fields (simple key: value lines)
// and its body.
func frontmatter(doc string) (map[string]string, string) {
	fields := map[string]string{}
	if !strings.HasPrefix(doc, "---\n") {
		return fields, doc
	}
	end := strings.Index(doc[4:], "\n---\n")
	if end < 0 {
		return fields, doc
	}
	for _, line := range strings.Split(doc[4:4+end], "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			fields[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return fields, strings.TrimLeft(doc[4+end+5:], "\n")
}

// withMarker puts the managed line after a document's frontmatter.
func withMarker(head, body string) string {
	return head + managedLine + "\n\n" + body
}

// yamlString quotes a value for a YAML frontmatter line.
func yamlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// codeGlobs are the application files of each language, for instructions that apply to code.
var codeGlobs = map[string][]string{
	"ts":     {"**/*.ts", "**/*.tsx", "**/*.js", "**/*.jsx", "**/*.vue", "**/*.svelte", "**/*.component.html"},
	"java":   {"**/*.java", "**/*.kt", "**/*.kts", "**/*.scala"},
	"python": {"**/*.py"},
	"go":     {"**/*.go"},
	// the languages that evaluate through rcas engine or rcas.wasm
	"other": {"**/*.cs", "**/*.razor", "**/*.cshtml", "**/*.rs", "**/*.php", "**/*.rb", "**/*.swift", "**/*.dart",
		"**/*.c", "**/*.cc", "**/*.cpp", "**/*.h", "**/*.hpp"},
}

// copilotTools are the tools a Copilot custom agent may use: VS Code tool sets and the rules-cascade
// MCP server. Roles that change nothing get no edit or terminal tools.
func copilotTools(r role) string {
	tools := []string{"read", "search", "rules-cascade/*"}
	switch {
	case r.ReadOnly:
	case r.Name == "rcas-author": // proposes through MCP, runs rcas check
		tools = append(tools, "execute")
	default:
		tools = append(tools, "edit", "execute")
	}
	return "tools: [" + quotedList(tools) + "]"
}

// agentCommand is one reusable prompt (templates/agents/commands): a slash command, a prompt file or a
// workflow, depending on the tool.
type agentCommand struct {
	Name, Description, Hint  string
	Arg, ArgHelp, ArgDefault string // the argument's name, what it is, and what nothing means
	When                     string // when a tool should pick the command written as a skill
	Body                     string // with {{args}} where the user's argument goes
}

// body is the prompt with the argument written the way the tool inserts it.
func (c agentCommand) body(args string) string { return strings.ReplaceAll(c.Body, "{{args}}", args) }

// withPlaceholder is the body for a tool that puts the user's text where a placeholder ($ARGUMENTS,
// {{args}}, $prompt) is: the placeholder, and what to do when it is empty.
func (c agentCommand) withPlaceholder(token string) string {
	return c.body(token + " (when nothing is given: " + c.ArgDefault + ")")
}

// plain is the body for a tool that has no placeholder and passes what the user typed as context.
func (c agentCommand) plain() string {
	return c.body("what the user named with the request (" + c.ArgHelp + "; when nothing is given: " + c.ArgDefault + ")")
}

// skillName is the portable skill a command is also written as, for the tools without commands.
func (c agentCommand) skillName() string {
	return "rules-cascade-" + strings.TrimPrefix(c.Name, "rcas-")
}

func agentCommands(fill func(string) string) []agentCommand {
	var out []agentCommand
	for _, file := range assets.Commands() {
		fields, body := frontmatter(fill(assets.Template("commands/" + file)))
		arg, help, _ := strings.Cut(unquote(fields["argument"]), "|")
		out = append(out, agentCommand{Name: fields["name"], Description: unquote(fields["description"]), Hint: unquote(fields["argument-hint"]),
			Arg: arg, ArgHelp: help, ArgDefault: unquote(fields["argument-default"]), When: unquote(fields["skill-when"]), Body: body})
	}
	return out
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(s[1 : len(s)-1])
	}
	return s
}

// splitFrontmatter returns a document's frontmatter, with its delimiters, and its body.
func splitFrontmatter(doc string) (string, string) {
	if strings.HasPrefix(doc, "---\n") {
		if end := strings.Index(doc[4:], "\n---\n"); end >= 0 {
			return doc[:4+end+5], strings.TrimLeft(doc[4+end+5:], "\n")
		}
	}
	return "", doc
}

// rulesetGlobs are the files a scoped rule applies to.
var rulesetGlobs = []string{"**/*.ruleset.yaml", "**/*.ruleset.yml", "**/*.ruleset.json", "rcas.yaml"}

// role is one sub-agent (templates/agents/roles), in a neutral form.
type role struct {
	Name, Description, ClaudeTools, Body string
	ReadOnly                             bool // analyst and reviewer change nothing
}

func roles(fill func(string) string) []role {
	var out []role
	for _, file := range assets.Roles() {
		fields, body := frontmatter(fill(assets.Template("roles/" + file)))
		name := fields["name"]
		out = append(out, role{Name: name, Description: fields["description"], ClaudeTools: fields["tools"], Body: body,
			ReadOnly: name == "rcas-analyst" || name == "rcas-reviewer"})
	}
	return out
}

// agentPlan is what an install writes: managed-block files (always merged) and owned files.
type agentPlan struct {
	files    []fileWrite
	always   map[string]bool
	legacy   map[string]bool // files an earlier rcas wrote, unchanged since: refreshed like managed ones
	problems []string
	tools    []string
	langs    []string
	skills   []string
}

// agentFiles computes what to write for the chosen tools and languages.
func (c *cli) agentFiles(base string, tools, langs []string) *agentPlan {
	p := &agentPlan{always: map[string]bool{}, legacy: map[string]bool{}, tools: tools, langs: langs, skills: skillsFor(langs)}
	rules, out := "rules", "build/rules"
	if cfg, err := c.loadConfig(); err == nil {
		if r, err := filepath.Rel(cfg.Root, cfg.RulesDir()); err == nil {
			rules = filepath.ToSlash(r)
		}
		if o, err := filepath.Rel(cfg.Root, cfg.OutDir()); err == nil {
			out = filepath.ToSlash(o)
		}
	}
	fill := strings.NewReplacer("{{rules}}", rules, "{{out}}", out).Replace

	var skillList strings.Builder
	for _, name := range p.skills {
		fields, _ := frontmatter(assets.Skill(name))
		desc := fields["description"]
		if i := strings.Index(desc, ". "); i > 0 {
			desc = desc[:i+1]
		}
		fmt.Fprintf(&skillList, "- `%s`: %s\n", name, desc)
	}
	for _, cmd := range agentCommands(fill) {
		if !contains(assets.Skills(), cmd.skillName()) {
			fmt.Fprintf(&skillList, "- `%s`: %s.\n", cmd.skillName(), cmd.Description)
		}
	}
	playbook := strings.Replace(fill(assets.Template("playbook.md")), "{{skills}}", strings.TrimRight(skillList.String(), "\n"), 1)
	scoped := fill(assets.Template("scoped.md"))

	block := func(path, title, content string) {
		existing, _ := os.ReadFile(filepath.Join(base, filepath.FromSlash(path)))
		merged, err := upsertBlock(existing, title, content)
		if err != nil {
			p.problems = append(p.problems, path+": "+err.Error())
			return
		}
		p.files = append(p.files, fileWrite{Path: path, Content: merged, KeepEOL: true})
		p.always[path] = true
	}
	add := func(path, content string) {
		p.files = append(p.files, fileWrite{Path: path, Content: []byte(content)})
	}
	cmds := agentCommands(fill)
	skills := func(dir string) {
		for _, name := range p.skills {
			fields, body := frontmatter(fill(assets.Skill(name)))
			head := "---\nname: " + fields["name"] + "\ndescription: " + fields["description"] + "\n---\n\n"
			add(dir+"/"+name+"/SKILL.md", withMarker(head, body))
		}
		// each command also as a skill, for the tools that run skills but have no commands
		for _, cmd := range cmds {
			if contains(p.skills, cmd.skillName()) || contains(assets.Skills(), cmd.skillName()) {
				continue // rules-cascade-review already is one
			}
			head := "---\nname: " + cmd.skillName() + "\ndescription: " + yamlString(cmd.Description+". "+cmd.When) + "\n---\n\n"
			add(dir+"/"+cmd.skillName()+"/SKILL.md", withMarker(head, cmd.plain()))
		}
	}
	// commandFiles writes every command for one tool: path from the name, file from the command.
	commandFiles := func(path func(name string) string, file func(cmd agentCommand) string) {
		for _, cmd := range cmds {
			add(path(cmd.Name), file(cmd))
		}
	}
	// mdCommand is a Markdown command with frontmatter lines and the user's text at token ("" = no placeholder).
	mdCommand := func(token string, fields ...func(cmd agentCommand) string) func(cmd agentCommand) string {
		return func(cmd agentCommand) string {
			body := cmd.plain()
			if token != "" {
				body = cmd.withPlaceholder(token)
			}
			var head []string
			for _, f := range fields {
				head = append(head, f(cmd))
			}
			return withMarker("---\n"+strings.Join(head, "\n")+"\n---\n\n", body)
		}
	}
	description := func(cmd agentCommand) string { return "description: " + yamlString(cmd.Description) }
	hint := func(cmd agentCommand) string { return "argument-hint: " + yamlString(cmd.Hint) }
	tomlCommand := func(cmd agentCommand) string {
		return "# rcas-managed: 'rcas agent install' refreshes this file. Delete this line to keep your own edits.\n" +
			"description = " + tomlQuote(cmd.Description) + "\nprompt = '''\n" + strings.ReplaceAll(cmd.withPlaceholder("{{args}}"), "'''", `"""`) + "'''\n"
	}
	var code []string
	for _, lang := range langs {
		code = append(code, codeGlobs[lang]...)
	}
	codeBody := fill(assets.Template("code.md"))
	// codeRule writes the instructions for application code, scoped to the code globs in the tool's format.
	codeRule := func(path, head string) {
		if len(code) > 0 {
			add(path, withMarker(head, codeBody))
		}
	}
	yamlList := func(key string, items []string) string {
		out := key + ":"
		for _, item := range items {
			out += "\n  - " + yamlString(item)
		}
		return out
	}
	// markdownAgents writes the roles as Markdown sub-agents; fields builds each one's frontmatter.
	markdownAgents := func(dir, suffix string, fields func(r role) []string) {
		for _, r := range roles(fill) {
			head := "---\n" + strings.Join(fields(r), "\n") + "\n---\n\n"
			add(dir+"/"+r.Name+suffix, withMarker(head, r.Body))
		}
	}
	nameDesc := func(r role) []string { return []string{"name: " + r.Name, "description: " + yamlString(r.Description)} }
	scopedRule := func(path, head string) { add(path, withMarker(head, scoped)) }
	has := func(tool string) bool { return contains(tools, tool) }

	block("AGENTS.md", "AGENTS.md", playbook)
	skills(".agents/skills")
	for _, tool := range tools {
		switch tool {
		case "claude":
			block("CLAUDE.md", "CLAUDE.md", "Business rules: follow the Rule Cascade section of @AGENTS.md. Sub-agents: rcas-analyst,\n"+
				"rcas-author, rcas-reviewer, rcas-tester (.claude/agents/). Skills: .claude/skills/rules-cascade*.\n")
			skills(".claude/skills")
			markdownAgents(".claude/agents", ".md", func(r role) []string {
				return []string{"name: " + r.Name, "description: " + r.Description, "tools: " + r.ClaudeTools}
			})
			commandFiles(func(n string) string { return ".claude/commands/" + n + ".md" }, mdCommand("$ARGUMENTS", description, hint))
			scopedRule(".claude/rules/rules-cascade.md", "---\n"+yamlList("paths", rulesetGlobs)+"\n---\n\n")
			codeRule(".claude/rules/rules-cascade-code.md", "---\n"+yamlList("paths", code)+"\n---\n\n")
		case "codex":
			for _, r := range roles(fill) {
				add(".codex/agents/"+r.Name+".toml", "# rcas-managed: 'rcas agent install' refreshes this file. Delete this line to keep your own edits.\n"+
					"name = "+tomlQuote(r.Name)+"\ndescription = "+tomlQuote(r.Description)+"\ndeveloper_instructions = '''\n"+
					strings.ReplaceAll(r.Body, "'''", `"""`)+"'''\n")
			}
		case "copilot":
			// rcas up to 1.0.0-alpha.6 kept the whole playbook in a block here; Copilot reads AGENTS.md itself
			if data, err := os.ReadFile(filepath.Join(base, ".github", "copilot-instructions.md")); err == nil && strings.Contains(string(data), blockBegin) {
				block(".github/copilot-instructions.md", "Copilot instructions", "Business rules: follow the Rule Cascade section of AGENTS.md; rulesets also get\n"+
					".github/instructions/rules-cascade.instructions.md.\n")
			}
			scopedRule(".github/instructions/rules-cascade.instructions.md", "---\nname: Rule Cascade rulesets\napplyTo: "+
				yamlString(strings.Join(rulesetGlobs, ","))+"\n---\n\n")
			codeRule(".github/instructions/rules-cascade-code.instructions.md", "---\nname: Rule Cascade in application code\napplyTo: "+
				yamlString(strings.Join(code, ","))+"\n---\n\n")
			for _, cmd := range cmds {
				add(".github/prompts/"+cmd.Name+".prompt.md", withMarker("---\nname: "+cmd.Name+"\ndescription: "+yamlString(cmd.Description)+
					"\nargument-hint: "+yamlString(cmd.Hint)+"\nagent: agent\ntools: ["+quotedList([]string{"read", "search", "rules-cascade/*"})+"]\n---\n\n",
					cmd.withPlaceholder("${input:"+cmd.Arg+":"+cmd.ArgHelp+"}")))
			}
			if !has("claude") { // Copilot also reads .claude/agents: one copy is enough
				markdownAgents(".github/agents", ".agent.md", func(r role) []string { return append(nameDesc(r), copilotTools(r)) })
			}
		case "cursor":
			scopedRule(".cursor/rules/rules-cascade.mdc", "---\ndescription: Business rules are Rule Cascade rulesets; how to find, write, test and propose them with rcas\n"+
				"globs: ["+quotedList(rulesetGlobs)+"]\nalwaysApply: false\n---\n\n")
			codeRule(".cursor/rules/rules-cascade-code.mdc", "---\ndescription: Business rules in application code come from Rule Cascade rulesets, not new checks\n"+
				"globs: ["+quotedList(code)+"]\nalwaysApply: false\n---\n\n")
			if !has("claude") { // Cursor also reads .claude/agents
				markdownAgents(".cursor/agents", ".md", func(r role) []string {
					return append(nameDesc(r), fmt.Sprintf("readonly: %t", r.ReadOnly))
				})
			}
		case "gemini":
			// Gemini CLI does not read AGENTS.md unless configured to: it gets the playbook itself.
			block("GEMINI.md", "GEMINI.md", playbook)
			commandFiles(func(n string) string { return ".gemini/commands/" + n + ".toml" }, tomlCommand)
			markdownAgents(".gemini/agents", ".md", func(r role) []string {
				if r.ReadOnly { // MCP tools are mcp_<server>_<tool>
					return append(nameDesc(r), "tools: ["+quotedList([]string{"read_file", "grep_search", "mcp_rules-cascade_*"})+"]")
				}
				return nameDesc(r)
			})
		case "kiro":
			scopedRule(".kiro/steering/rules-cascade.md", "---\ninclusion: fileMatch\nfileMatchPattern: ["+quotedList(rulesetGlobs)+"]\n---\n\n")
			codeRule(".kiro/steering/rules-cascade-code.md", "---\ninclusion: fileMatch\nfileMatchPattern: ["+quotedList(code)+"]\n---\n\n")
			skills(".kiro/skills")
			markdownAgents(".kiro/agents", ".md", func(r role) []string { // MCP tools are @<server>
				tools := []string{"read"}
				if r.Name == "rcas-author" {
					tools = append(tools, "shell")
				} else if !r.ReadOnly {
					tools = append(tools, "write", "shell")
				}
				return append(nameDesc(r), "tools: ["+quotedList(append(tools, "@rules-cascade"))+"]")
			})
		case "devin":
			scopedRule(".devin/rules/rules-cascade.md", "---\ntrigger: glob\nglobs: "+yamlString(strings.Join(rulesetGlobs, ","))+"\n---\n\n")
			codeRule(".devin/rules/rules-cascade-code.md", "---\ntrigger: glob\nglobs: "+yamlString(strings.Join(code, ","))+"\n---\n\n")
			commandFiles(func(n string) string { return ".devin/workflows/" + n + ".md" }, func(cmd agentCommand) string {
				return withMarker("", "# "+cmd.Name+": "+cmd.Description+"\n\n"+cmd.plain())
			})
		case "junie":
			markdownAgents(".junie/agents", ".md", func(r role) []string { // tools are tool groups
				tools := []string{"Read", "Glob", "Grep"}
				if r.Name == "rcas-author" {
					tools = append(tools, "Bash")
				} else if !r.ReadOnly {
					tools = append(tools, "Bash", "Write", "Edit")
				}
				return append(nameDesc(r), "tools: ["+quotedList(tools)+"]", "mcpServers: ["+quotedList([]string{"rules-cascade"})+"]")
			})
			commandFiles(func(n string) string { return ".junie/commands/" + n + ".md" }, mdCommand("$prompt", description,
				func(agentCommand) string { return "allowPromptArgument: true" }))
		case "cline":
			scopedRule(".clinerules/rules-cascade.md", "---\npaths: ["+quotedList(rulesetGlobs)+"]\n---\n\n")
			codeRule(".clinerules/rules-cascade-code.md", "---\npaths: ["+quotedList(code)+"]\n---\n\n")
			if !has("claude") { // Cline also reads .claude/skills
				skills(".cline/skills")
			}
		case "opencode":
			markdownAgents(".opencode/agents", ".md", func(r role) []string {
				fields := []string{"description: " + yamlString(r.Description), "mode: subagent"}
				switch {
				case r.ReadOnly:
					fields = append(fields, "permission:\n  edit: deny\n  bash: deny")
				case r.Name == "rcas-author": // proposes through MCP instead of editing rulesets
					fields = append(fields, "permission:\n  edit: deny")
				}
				return fields
			})
			commandFiles(func(n string) string { return ".opencode/commands/" + n + ".md" }, mdCommand("$ARGUMENTS", description))
		case "factory":
			markdownAgents(".factory/droids", ".md", func(r role) []string {
				if r.ReadOnly { // read-only tools, plus the rules-cascade MCP tools
					return append(nameDesc(r), "tools: read-only", "mcpServers: ["+quotedList([]string{"rules-cascade"})+"]")
				}
				return nameDesc(r)
			})
			commandFiles(func(n string) string { return ".factory/commands/" + n + ".md" }, mdCommand("$ARGUMENTS", description, hint))
		case "amazonq":
			add(".amazonq/rules/rules-cascade.md", withMarker("", playbook))
		case "tabnine":
			add(".tabnine/guidelines/rules-cascade.md", withMarker("", playbook))
			commandFiles(func(n string) string { return ".tabnine/agent/commands/" + n + ".toml" }, tomlCommand)
		case "kilo":
			commandFiles(func(n string) string { return ".kilo/commands/" + n + ".md" }, mdCommand("", description))
		case "augment":
			if !has("claude") { // Auggie also reads .claude/commands
				commandFiles(func(n string) string { return ".augment/commands/" + n + ".md" }, mdCommand("", description, hint))
			}
			markdownAgents(".augment/agents", ".md", nameDesc)
		}
	}
	legacy := legacyOutputs(fill)
	for _, f := range p.files {
		if old, ok := legacy[f.Path]; ok {
			if data, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(f.Path))); err == nil && normalizeLegacy(string(data)) == old {
				p.legacy[f.Path] = true
			}
		}
	}
	return p
}

// legacyOutputs is what rcas 1.0.0-alpha.3 to alpha.6 wrote outside managed blocks, by path, before
// files carried the rcas-managed line. A file still exactly like that was never edited, so a new
// install may refresh it.
func legacyOutputs(fill func(string) string) map[string]string {
	skill := fill(assets.Template("legacy/skill.md"))
	out := map[string]string{
		".agents/skills/rules-cascade/SKILL.md": skill,
		".claude/skills/rules-cascade/SKILL.md": skill,
		".cursor/rules/rules-cascade.mdc":       strings.Replace(assets.Template("legacy/cursor.mdc"), "{{playbook}}", fill(assets.Template("legacy/playbook.md")), 1),
	}
	for _, file := range assets.Roles() {
		out[".claude/agents/"+file] = fill(assets.Template("roles/" + file))
	}
	return out
}

// normalizeLegacy undoes what changed in rcas output without anyone editing it: line endings, and
// the site's earlier address.
func normalizeLegacy(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "rules.sdods.com", "rulescascade.com")
}

func tomlQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func quotedList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = yamlString(s)
	}
	return strings.Join(q, ", ")
}

func (c *cli) agentCommand(args []string) int {
	if len(args) > 0 && args[0] == "list" {
		return c.agentList()
	}
	if len(args) == 0 || args[0] != "install" {
		c.usage("agent")
		return exitUsage
	}
	flags, positional, err := parseArgs(args[1:], map[string]string{"--for": "for", "--stack": "stack"}, []string{"--print", "--force", "--dry-run"})
	if err != nil || len(positional) > 0 {
		if err != nil {
			fmt.Fprintf(c.stderr, "%s agent install: %v\n", c.program, err)
		}
		c.usage("agent")
		return exitUsage
	}
	base, err := c.projectRoot()
	if err != nil {
		return c.fail(err)
	}
	tools, langs, why, err := c.agentChoice(base, flags["for"], flags["stack"])
	if err != nil {
		fmt.Fprintf(c.stderr, "%s agent install: %v\n", c.program, err)
		return exitUsage
	}
	p := c.agentFiles(base, tools, langs)
	for _, problem := range p.problems {
		fmt.Fprintf(c.stderr, "%s agent install: %s\n", c.program, problem)
	}
	if len(p.problems) > 0 {
		return exitFindings
	}
	if flags["--print"] != "" {
		for _, f := range p.files {
			fmt.Fprintf(c.stdout, "==> %s <==\n%s\n", f.Path, f.Content)
		}
		return exitOK
	}
	shown := strings.Join(tools, ",")
	if shown == "" {
		shown = "the portable files only"
	}
	stack := strings.Join(langs, ",")
	if stack == "" {
		stack = "all"
	}
	fmt.Fprintf(c.stdout, "%s agent install --for %s --stack %s (%s)\n", c.program, shown, stack, base)
	fmt.Fprintf(c.stdout, "  %s\n", why)
	var managed, owned []fileWrite
	for _, f := range p.files {
		existing, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(f.Path)))
		if p.always[f.Path] || p.legacy[f.Path] || (err == nil && strings.Contains(string(existing), managedMarker)) {
			managed = append(managed, f)
		} else {
			owned = append(owned, f)
		}
	}
	dry := flags["--dry-run"] != ""
	outcomes, err := writeFiles(base, managed, true, dry)
	if err == nil {
		var more []writeOutcome
		more, err = writeFiles(base, owned, flags["--force"] != "", dry)
		outcomes = append(outcomes, more...)
	}
	for i := range outcomes {
		if outcomes[i].Action == "overwritten" {
			outcomes[i].Action = "updated"
		}
	}
	sort.SliceStable(outcomes, func(i, j int) bool { return outcomes[i].Path < outcomes[j].Path })
	c.printOutcomes(outcomes)
	if err != nil {
		return c.fail(err)
	}
	if len(tools) < len(agentTools) {
		fmt.Fprintf(c.stdout, "\nOther tools: '%s agent list'; add one with --for <tool>, or list them under agents.tools in rcas.yaml.\n", c.program)
	}
	return exitOK
}

// agentChoice decides the tools and languages: the flags, then rcas.yaml, then what the project
// shows. why explains the choice in one line.
func (c *cli) agentChoice(base, forFlag, stackFlag string) (tools, langs []string, why string, err error) {
	cfg, _ := c.loadConfig()
	var reasons []string
	switch {
	case forFlag != "":
		tools = splitList(forFlag, agentToolNames())
		reasons = append(reasons, "tools from --for")
	case cfg != nil && len(cfg.Agents.Tools) > 0:
		tools = cfg.Agents.Tools
		reasons = append(reasons, "tools from agents.tools in rcas.yaml")
	default:
		tools = detectAgentTools(base)
		if len(tools) == 0 {
			reasons = append(reasons, "no AI tool configuration found: the portable files only")
		} else {
			reasons = append(reasons, "tools found in the project")
		}
	}
	for i, t := range tools {
		tool, ok := findAgentTool(t)
		if !ok {
			return nil, nil, "", fmt.Errorf("unknown tool %q (%s, all)", t, strings.Join(agentToolNames(), ", "))
		}
		tools[i] = tool.Name
	}
	switch {
	case stackFlag != "":
		langs = splitList(stackFlag, languageNames())
		reasons = append(reasons, "languages from --stack")
	case cfg != nil && len(cfg.Languages) > 0:
		langs = cfg.Languages
		reasons = append(reasons, "languages from rcas.yaml")
	default:
		langs = detectLanguages(base)
		if len(langs) == 0 {
			reasons = append(reasons, "no language found: every language skill")
		} else {
			reasons = append(reasons, "languages found in the project")
		}
	}
	for _, l := range langs {
		if _, ok := languages[l]; !ok {
			return nil, nil, "", fmt.Errorf("unknown language %q (%s)", l, strings.Join(languageNames(), ", "))
		}
	}
	return dedupe(tools), langs, strings.Join(reasons, "; "), nil
}

func dedupe(list []string) []string {
	var out []string
	for _, s := range list {
		if !contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// agentList prints every tool, whether the project uses it, and what install writes for it.
func (c *cli) agentList() int {
	base, err := c.projectRoot()
	if err != nil {
		return c.fail(err)
	}
	portable := map[string]bool{}
	for _, f := range c.agentFiles(base, nil, nil).files {
		portable[f.Path] = true
	}
	detected := detectAgentTools(base)
	fmt.Fprintf(c.stdout, "Every install writes AGENTS.md (a managed block) and .agents/skills/.\n\n")
	fmt.Fprintf(c.stdout, "%-9s %-6s %-48s %s\n", "TOOL", "IN USE", "ALSO WRITES", "READS OF THE PORTABLE PART")
	for _, t := range agentTools {
		var writes []string
		for _, f := range c.agentFiles(base, []string{t.Name}, nil).files {
			if !portable[f.Path] && !contains(writes, summaryPath(f.Path)) {
				writes = append(writes, summaryPath(f.Path))
			}
		}
		inUse := ""
		if contains(detected, t.Name) {
			inUse = "yes"
		}
		w := strings.Join(writes, ", ")
		if w == "" {
			w = "-"
		}
		fmt.Fprintf(c.stdout, "%-9s %-6s %-48s %s\n", t.Name, inUse, w, t.Reads)
	}
	langs := detectLanguages(base)
	fmt.Fprintf(c.stdout, "\nLanguages found: %s\nSkills for them: %s\n", orNone(langs), strings.Join(skillsFor(langs), ", "))
	return exitOK
}

// summaryPath shortens a file inside a skills or agents folder to the folder.
func summaryPath(path string) string {
	for _, dir := range []string{"/skills/", "/agents/", "/droids/"} {
		if i := strings.Index(path, dir); i >= 0 {
			return path[:i+len(dir)]
		}
	}
	return path
}

func orNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}

// projectRoot is the directory of rcas.yaml, or the working directory when there is none.
func (c *cli) projectRoot() (string, error) {
	if cfg, err := c.loadConfig(); err == nil {
		return cfg.Root, nil
	}
	return filepath.Abs(c.workDir())
}
