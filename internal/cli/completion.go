package cli

import (
	"fmt"
	"strings"
)

func (c *cli) completionCommand(args []string) int {
	if len(args) != 1 {
		c.usage("completion")
		return exitUsage
	}
	names := make([]string, 0, len(commands))
	for _, cmd := range commands {
		names = append(names, cmd.name)
	}
	words := strings.Join(names, " ")
	p := c.program
	fn := strings.ReplaceAll(p, "-", "_")
	sub := map[string]string{
		"mcp":       "install --root --read-only --list-tools " + strings.Join(clientNames(), " ") + " all --scope --print --file --command --name --force",
		"agent":     "install list --for " + strings.Join(agentToolNames(), " ") + " all --stack ts python java go other --print --force --dry-run",
		"proposals": "list show accept reject --status --json --diff --force --reason",
		"init":      "--name --id-prefix --lang --from --agent --mcp --ci --force --dry-run",
		"compile":   "--all -o --output",
		"check":     "--json", "test": "--json",
		"derive":     "--schema --pointer --id --entity --scope --version --title --tests --codes-from -o --check --propose",
		"analyze":    "--format --include --exclude --max-files --derive -o",
		"evaluate":   "--bundle --manifest --channel --conformance-operators",
		"manifest":   "--bundle --manifest --channel -o",
		"completion": "bash zsh fish powershell",
		"help":       words,
	}
	switch args[0] {
	case "bash":
		fmt.Fprintf(c.stdout, "# bash completion for %[1]s\n_%[2]s() {\n  local cur=${COMP_WORDS[COMP_CWORD]} cmd=${COMP_WORDS[1]}\n  if [ \"$COMP_CWORD\" -eq 1 ]; then\n    COMPREPLY=($(compgen -W %[3]q -- \"$cur\")); return\n  fi\n  case \"$cmd\" in\n", p, fn, words)
		for _, name := range sortedKeys(sub) {
			fmt.Fprintf(c.stdout, "    %s) COMPREPLY=($(compgen -W %q -- \"$cur\")) ;;\n", name, sub[name])
		}
		fmt.Fprintf(c.stdout, "  esac\n  [ ${#COMPREPLY[@]} -eq 0 ] && COMPREPLY=($(compgen -f -- \"$cur\"))\n}\ncomplete -o default -F _%s %s\n", fn, p)
	case "zsh":
		fmt.Fprintf(c.stdout, "#compdef %[1]s\n_%[2]s() {\n  if (( CURRENT == 2 )); then\n    compadd -- %[3]s\n    return\n  fi\n  case $words[2] in\n", p, fn, words)
		for _, name := range sortedKeys(sub) {
			fmt.Fprintf(c.stdout, "    %s) compadd -- %s; _files ;;\n", name, sub[name])
		}
		fmt.Fprintf(c.stdout, "    *) _files ;;\n  esac\n}\ncompdef _%s %s\n", fn, p)
	case "fish":
		fmt.Fprintf(c.stdout, "# fish completion for %s\ncomplete -c %s -n __fish_use_subcommand -f -a %q\n", p, p, words)
		for _, name := range sortedKeys(sub) {
			fmt.Fprintf(c.stdout, "complete -c %s -n '__fish_seen_subcommand_from %s' -a %q\n", p, name, sub[name])
		}
	case "powershell":
		fmt.Fprintf(c.stdout, "# PowerShell completion for %s\nRegister-ArgumentCompleter -Native -CommandName '%s','%s.exe' -ScriptBlock {\n  param($wordToComplete, $commandAst, $cursorPosition)\n  $words = $commandAst.CommandElements | ForEach-Object { $_.ToString() }\n  $subs = @{\n", p, p, p)
		for _, name := range sortedKeys(sub) {
			fmt.Fprintf(c.stdout, "    '%s' = '%s'\n", name, sub[name])
		}
		fmt.Fprintf(c.stdout, "  }\n  if ($words.Count -le 2 -and -not ($words.Count -eq 2 -and $wordToComplete -eq '')) { $candidates = '%s' } elseif ($subs.ContainsKey($words[1])) { $candidates = $subs[$words[1]] } else { return }\n  $candidates.Split(' ') | Where-Object { $_ -like \"$wordToComplete*\" } | ForEach-Object { [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_) }\n}\n", words)
	default:
		c.usage("completion")
		return exitUsage
	}
	return exitOK
}
