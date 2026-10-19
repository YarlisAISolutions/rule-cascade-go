package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every tool gets the commands, the code rule and the agent tools in its own format.
func TestAgentInstallEveryToolInItsOwnFormat(t *testing.T) {
	dir := t.TempDir()
	stack := []string{"agent", "install", "--for", "all", "--stack", "java,ts,python,go,other"}
	if status, out, errs := in(t, dir, "", stack...); status != 0 {
		t.Fatalf("%d %s %s", status, out, errs)
	}
	for path, wants := range map[string][]string{
		// commands, each with the user's argument where the tool puts it
		".claude/commands/rcas-capture.md":          {"description: ", "argument-hint: ", "$ARGUMENTS (when nothing is given: the whole project)"},
		".gemini/commands/rcas-capture.toml":        {"description = ", "prompt = '''", "{{args}}"},
		".opencode/commands/rcas-capture.md":        {"description: ", "$ARGUMENTS"},
		".factory/commands/rcas-review.md":          {"argument-hint: ", "$ARGUMENTS"},
		".junie/commands/rcas-capture.md":           {"allowPromptArgument: true", "$prompt"},
		".tabnine/agent/commands/rcas-capture.toml": {"prompt = '''", "{{args}}"},
		".kilo/commands/rcas-capture.md":            {"description: ", "what the user named"},
		".devin/workflows/rcas-capture.md":          {"# rcas-capture: ", "what the user named"},
		".github/prompts/rcas-capture.prompt.md":    {"agent: agent", "${input:scope:"},
		// the command as a portable skill, for the tools without commands
		".agents/skills/rules-cascade-capture/SKILL.md": {"name: rules-cascade-capture", "what the user named"},
		".kiro/skills/rules-cascade-capture/SKILL.md":   {"name: rules-cascade-capture"},
		"AGENTS.md": {"`rules-cascade-capture`"},
		// instructions scoped to the application code of every language
		".claude/rules/rules-cascade-code.md":                     {"paths:\n  - \"**/*.java\"", `"**/*.py"`, `"**/*.go"`, `"**/*.cs"`},
		".claude/rules/rules-cascade.md":                          {"paths:\n  - \"**/*.ruleset.yaml\""},
		".cursor/rules/rules-cascade-code.mdc":                    {"globs: [", `"**/*.rs"`, "alwaysApply: false"},
		".kiro/steering/rules-cascade-code.md":                    {"inclusion: fileMatch", "fileMatchPattern: ["},
		".devin/rules/rules-cascade-code.md":                      {"trigger: glob", `globs: "**/*.java,`},
		".clinerules/rules-cascade-code.md":                       {"paths: ["},
		".github/instructions/rules-cascade-code.instructions.md": {"applyTo: "},
		// sub-agents restricted to what each role needs
		".opencode/agents/rcas-analyst.md": {"permission:\n  edit: deny\n  bash: deny"},
		".opencode/agents/rcas-author.md":  {"permission:\n  edit: deny"},
		".factory/droids/rcas-reviewer.md": {"tools: read-only", `mcpServers: ["rules-cascade"]`},
		".gemini/agents/rcas-analyst.md":   {`tools: ["read_file", "grep_search", "mcp_rules-cascade_*"]`},
		".kiro/agents/rcas-analyst.md":     {`tools: ["read", "@rules-cascade"]`},
		".kiro/agents/rcas-tester.md":      {`"write", "shell", "@rules-cascade"`},
		".junie/agents/rcas-reviewer.md":   {`tools: ["Read", "Glob", "Grep"]`, `mcpServers: ["rules-cascade"]`},
		".augment/agents/rcas-analyst.md":  {"name: rcas-analyst"},
	} {
		got := readFile(t, filepath.Join(dir, filepath.FromSlash(path)))
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("%s lacks %q:\n%s", path, want, got)
			}
		}
		if !strings.Contains(got, managedMarker) {
			t.Errorf("%s is not marked rcas-managed", path)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".augment", "commands")); err == nil {
		t.Error("Auggie reads .claude/commands: no second copy when claude is chosen")
	}
	if _, err := os.Stat(filepath.Join(dir, ".agents", "skills", "rules-cascade-rcas-review")); err == nil {
		t.Error("the review command has a skill already")
	}
	_, out, _ := in(t, dir, "", stack...)
	files := 0
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(line); strings.HasPrefix(line, "  ") && len(fields) == 2 {
			files++
			if fields[0] != "unchanged" {
				t.Errorf("a second install changed %s: %s", fields[1], fields[0])
			}
		}
	}
	if files < 90 {
		t.Errorf("a second install listed only %d files:\n%s", files, out)
	}

	// without claude, Auggie gets its own commands
	other := t.TempDir()
	in(t, other, "", "agent", "install", "--for", "augment", "--stack", "ts")
	if got := readFile(t, filepath.Join(other, ".augment", "commands", "rcas-capture.md")); !strings.Contains(got, "argument-hint: ") {
		t.Errorf("augment command:\n%s", got)
	}
}
