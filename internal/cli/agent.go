package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"rulescascade.com/go/internal/assets"
)

const agentHelp = `install [--for <tools>] [--print] [--force] [--dry-run]

Writes the instructions AI coding tools read, so they find business rules in the code, write
rulesets with golden tests, check them, and submit them as proposals instead of editing the rules:

  every tool   AGENTS.md (a managed block; the rest of the file is kept)
  claude       CLAUDE.md (imports AGENTS.md), .claude/agents/rcas-{analyst,author,reviewer,tester}.md,
               .claude/skills/rules-cascade/SKILL.md
  codex        .agents/skills/rules-cascade/SKILL.md
  cursor       .cursor/rules/rules-cascade.mdc
  copilot      .github/copilot-instructions.md (a managed block)
  gemini       GEMINI.md (a managed block)

--for takes a comma-separated list or all (the default). The managed blocks are replaced on every
run; other files are written only when missing, or with --force. Pair it with
'{program} mcp install <tool>' so the agent can call the tools the instructions name.
`

var agentTools = []string{"claude", "codex", "cursor", "copilot", "gemini"}

const (
	blockBegin = "<!-- rcas:begin (written by 'rcas agent install'; edit outside this block) -->"
	blockEnd   = "<!-- rcas:end -->"
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

// agentFiles computes what to write for the chosen tools. Managed-block files are returned with
// their merged content and always=true (they replace only their own block).
func (c *cli) agentFiles(base string, tools []string) (files []fileWrite, always map[string]bool, problems []string) {
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
	playbook := fill(assets.Template("playbook.md"))
	always = map[string]bool{}
	block := func(path, title, content string) {
		existing, _ := os.ReadFile(filepath.Join(base, filepath.FromSlash(path)))
		merged, err := upsertBlock(existing, title, content)
		if err != nil {
			problems = append(problems, path+": "+err.Error())
			return
		}
		files = append(files, fileWrite{Path: path, Content: merged, KeepEOL: true})
		always[path] = true
	}
	block("AGENTS.md", "Agent instructions", playbook)
	for _, tool := range tools {
		switch tool {
		case "claude":
			block("CLAUDE.md", "CLAUDE.md", "Business rules: follow the Rule Cascade section of @AGENTS.md. Sub-agents: rcas-analyst,\n"+
				"rcas-author, rcas-reviewer, rcas-tester (.claude/agents/). Skill: rules-cascade.\n")
			for _, name := range assets.ClaudeAgents() {
				files = append(files, fileWrite{Path: ".claude/agents/" + name, Content: []byte(fill(assets.Template("claude-agents/" + name)))})
			}
			files = append(files, fileWrite{Path: ".claude/skills/rules-cascade/SKILL.md", Content: []byte(fill(assets.Template("skill.md")))})
		case "codex":
			files = append(files, fileWrite{Path: ".agents/skills/rules-cascade/SKILL.md", Content: []byte(fill(assets.Template("skill.md")))})
		case "cursor":
			mdc := strings.Replace(assets.Template("cursor.mdc"), "{{playbook}}", playbook, 1)
			files = append(files, fileWrite{Path: ".cursor/rules/rules-cascade.mdc", Content: []byte(mdc)})
		case "copilot":
			block(".github/copilot-instructions.md", "Copilot instructions", playbook)
		case "gemini":
			block("GEMINI.md", "GEMINI.md", playbook)
		}
	}
	return files, always, problems
}

func (c *cli) agentCommand(args []string) int {
	if len(args) == 0 || args[0] != "install" {
		c.usage("agent")
		return exitUsage
	}
	flags, positional, err := parseArgs(args[1:], map[string]string{"--for": "for"}, []string{"--print", "--force", "--dry-run"})
	if err != nil || len(positional) > 0 {
		if err != nil {
			fmt.Fprintf(c.stderr, "%s agent install: %v\n", c.program, err)
		}
		c.usage("agent")
		return exitUsage
	}
	tools := agentTools
	if f := flags["for"]; f != "" {
		tools = splitList(f, agentTools)
	}
	for _, t := range tools {
		if !contains(agentTools, t) {
			fmt.Fprintf(c.stderr, "%s agent install: unknown tool %q (%s, all)\n", c.program, t, strings.Join(agentTools, ", "))
			return exitUsage
		}
	}
	base, err := c.projectRoot()
	if err != nil {
		return c.fail(err)
	}
	files, always, problems := c.agentFiles(base, tools)
	for _, p := range problems {
		fmt.Fprintf(c.stderr, "%s agent install: %s\n", c.program, p)
	}
	if len(problems) > 0 {
		return exitFindings
	}
	if flags["--print"] != "" {
		for _, f := range files {
			fmt.Fprintf(c.stdout, "==> %s <==\n%s\n", f.Path, f.Content)
		}
		return exitOK
	}
	fmt.Fprintf(c.stdout, "%s agent install --for %s (%s)\n", c.program, strings.Join(tools, ","), base)
	var managed, owned []fileWrite
	for _, f := range files {
		if always[f.Path] {
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
		if outcomes[i].Action == "overwritten" && always[outcomes[i].Path] {
			outcomes[i].Action = "updated"
		}
	}
	c.printOutcomes(outcomes)
	if err != nil {
		return c.fail(err)
	}
	return exitOK
}

// projectRoot is the directory of rcas.yaml, or the working directory when there is none.
func (c *cli) projectRoot() (string, error) {
	if cfg, err := c.loadConfig(); err == nil {
		return cfg.Root, nil
	}
	return filepath.Abs(c.workDir())
}
