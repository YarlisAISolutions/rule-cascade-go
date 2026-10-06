package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"rules.sdods.com/go/internal/clients"
)

// finding of doctor: ok, warn or fail, with what to do.
type diagnosis struct {
	Check  string `json:"check"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

func (c *cli) doctorCommand(args []string) int {
	flags, positional, err := parseArgs(args, nil, []string{"--json"})
	if err != nil || len(positional) > 0 {
		c.usage("doctor")
		return exitUsage
	}
	var out []diagnosis
	add := func(check, status, detail, fix string) { out = append(out, diagnosis{check, status, detail, fix}) }

	exe, _ := os.Executable()
	add("command", "ok", fmt.Sprintf("%s %s on %s/%s (%s)", c.program, engineVersion(), runtime.GOOS, runtime.GOARCH, exe), "")
	if path, err := exec.LookPath("rcas"); err != nil {
		add("PATH", "warn", "rcas is not on PATH", "add the install directory to PATH, or use npx -y @rules-cascade/cli")
	} else {
		add("PATH", "ok", path, "")
	}
	if os.Getenv("RCAS_NO_NETWORK") == "" {
		latest, err := latestVersion()
		switch {
		case err != nil:
			add("latest version", "warn", "could not reach rules.sdods.com: "+err.Error(), "set RCAS_NO_NETWORK=1 to skip this check offline")
		case latest != engineVersion() && engineVersion() != "":
			add("latest version", "warn", "this is "+engineVersion()+"; the latest release is "+latest, "to change, see https://rules.sdods.com/get-started/install/")
		default:
			add("latest version", "ok", latest, "")
		}
	}

	cfg, err := c.loadConfig()
	switch {
	case errors.Is(err, errNoConfig):
		add("rcas.yaml", "warn", "no project configuration found", c.program+" init")
	case err != nil:
		add("rcas.yaml", "fail", err.Error(), "fix the file; https://rules.sdods.com/reference/project-config/")
	default:
		add("rcas.yaml", "ok", cfg.Path, "")
		files := findRulesets(cfg.RulesDir(), cfg.Rules.Include)
		if len(files) == 0 {
			add("rulesets", "warn", "none under "+cfg.RulesDir(), c.program+" init, or set rules.dir")
		} else {
			bad := 0
			for _, f := range files {
				if c.checkFile(f).bad() > 0 {
					bad++
				}
			}
			if bad > 0 {
				add("rulesets", "fail", fmt.Sprintf("%d of %d do not pass check", bad, len(files)), c.program+" check")
			} else {
				add("rulesets", "ok", fmt.Sprintf("%d pass check", len(files)), "")
			}
		}
		if data, err := os.ReadFile(filepath.Join(cfg.Root, ".gitignore")); err != nil || !strings.Contains(string(data), ".rcas") {
			add(".gitignore", "warn", ".rcas/ is not ignored", "add .rcas/ to .gitignore")
		} else {
			add(".gitignore", "ok", ".rcas/ is ignored", "")
		}
		if store, err := c.store(); err == nil {
			if pending, _ := store.List("pending"); len(pending) > 0 {
				add("proposals", "warn", fmt.Sprintf("%d waiting for review", len(pending)), c.program+" proposals list")
			}
		}
	}

	root, _ := c.projectRoot()
	home, _ := os.UserHomeDir()
	found := 0
	for _, client := range clients.All {
		for _, scope := range []string{"project", "user"} {
			path, err := clients.Locate(client, scope, root, home, os.Getenv("CODEX_HOME"))
			if err != nil {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			if strings.Contains(string(data), "rules-cascade") || strings.Contains(string(data), "@rules-cascade/cli") {
				found++
				add("MCP: "+client.Name, "ok", path, "")
			}
		}
	}
	if found == 0 {
		add("MCP", "warn", "no AI tool is configured to use the rules-cascade MCP server", c.program+" mcp install claude (or codex, cursor, windsurf, gemini, vscode)")
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); err == nil {
		data, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
		if strings.Contains(string(data), "rcas:begin") {
			add("agent instructions", "ok", "AGENTS.md", "")
		} else {
			add("agent instructions", "warn", "AGENTS.md has no Rule Cascade section", c.program+" agent install")
		}
	} else {
		add("agent instructions", "warn", "no AGENTS.md", c.program+" agent install")
	}

	status := exitOK
	for _, d := range out {
		if d.Status == "fail" {
			status = exitFindings
		}
	}
	if flags["--json"] != "" {
		c.printJSON(map[string]any{"ok": status == exitOK, "checks": out})
		return status
	}
	for _, d := range out {
		mark := map[string]string{"ok": "ok  ", "warn": "WARN", "fail": "FAIL"}[d.Status]
		fmt.Fprintf(c.stdout, "%s  %-20s %s\n", mark, d.Check, d.Detail)
		if d.Fix != "" {
			fmt.Fprintf(c.stdout, "      %-20s -> %s\n", "", d.Fix)
		}
	}
	return status
}

// latestVersion reads the version the site publishes as latest.
func latestVersion() (string, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("https://rules.sdods.com/download/latest/VERSION")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 100))
	return strings.TrimSpace(string(data)), err
}
