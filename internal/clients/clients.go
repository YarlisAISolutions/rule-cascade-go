// Package clients writes the MCP server entry of rcas into the configuration of AI coding tools.
// It merges: other servers and settings in the file are kept as they are, in their order, and an
// existing entry under the same name is replaced only when asked.
package clients

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	rulecascade "rulescascade.com/go"
)

// Client is one AI coding tool rcas can register with.
type Client struct {
	Name  string // the argument of 'rcas mcp install'
	Title string
	// CLI is the tool's own command that registers a server ("claude", "codex"), tried first.
	CLI string
	// Project and User are the configuration files, relative to the project directory and to the
	// home directory; "" when the tool has no such file.
	Project, User string
	// Key is the member that holds the servers: mcpServers, or servers for VS Code.
	Key string
	// Format is "json", or "toml" for Codex.
	Format string
}

// All is every client 'rcas mcp install' writes, in the order 'all' installs them. Paths and keys
// follow each tool's documentation (October 2026; docs/agents.md has the sources). Tools whose only
// MCP configuration is interactive (Goose, the Cline UI) or lives in repository settings (the Copilot
// cloud agent) are covered by --print.
var All = []Client{
	{Name: "claude", Title: "Claude Code", CLI: "claude", Project: ".mcp.json", Key: "mcpServers", Format: "json"},
	{Name: "codex", Title: "OpenAI Codex CLI and IDE extension", CLI: "codex", User: ".codex/config.toml", Key: "mcp_servers", Format: "toml"},
	{Name: "cursor", Title: "Cursor", Project: ".cursor/mcp.json", User: ".cursor/mcp.json", Key: "mcpServers", Format: "json"},
	{Name: "vscode", Title: "VS Code (GitHub Copilot agent mode)", Project: ".vscode/mcp.json", Key: "servers", Format: "json"},
	{Name: "copilot-cli", Title: "GitHub Copilot CLI", CLI: "copilot", Project: ".mcp.json", User: ".copilot/mcp-config.json", Key: "mcpServers", Format: "json"},
	{Name: "gemini", Title: "Gemini CLI", Project: ".gemini/settings.json", User: ".gemini/settings.json", Key: "mcpServers", Format: "json"},
	{Name: "kiro", Title: "Kiro", Project: ".kiro/settings/mcp.json", User: ".kiro/settings/mcp.json", Key: "mcpServers", Format: "json"},
	{Name: "devin", Title: "Devin Desktop and Devin CLI (formerly Windsurf)", Project: ".devin/mcp_config.json", User: ".config/devin/mcp_config.json", Key: "mcpServers", Format: "json"},
	{Name: "windsurf", Title: "Windsurf (the configuration file before Devin Desktop)", User: ".codeium/windsurf/mcp_config.json", Key: "mcpServers", Format: "json"},
	{Name: "junie", Title: "JetBrains Junie", Project: ".junie/mcp/mcp.json", User: ".junie/mcp/mcp.json", Key: "mcpServers", Format: "json"},
	{Name: "cline", Title: "Cline", User: ".cline/data/settings/cline_mcp_settings.json", Key: "mcpServers", Format: "json"},
	{Name: "opencode", Title: "OpenCode", Project: "opencode.json", User: ".config/opencode/opencode.json", Key: "mcp", Format: "json"},
	{Name: "kilo", Title: "Kilo Code", Project: "kilo.json", Key: "mcp", Format: "json"},
	{Name: "factory", Title: "Factory Droid", Project: ".factory/mcp.json", User: ".factory/mcp.json", Key: "mcpServers", Format: "json"},
	{Name: "amp", Title: "Amp", Project: ".amp/settings.json", User: ".config/amp/settings.json", Key: "amp.mcpServers", Format: "json"},
	{Name: "zed", Title: "Zed", Project: ".zed/settings.json", User: ".config/zed/settings.json", Key: "context_servers", Format: "json"},
	{Name: "warp", Title: "Warp", Project: ".warp/.mcp.json", User: ".warp/.mcp.json", Key: "mcpServers", Format: "json"},
	{Name: "augment", Title: "Augment Code (Auggie)", User: ".augment/settings.json", Key: "mcpServers", Format: "json"},
	{Name: "amazonq", Title: "Amazon Q Developer", Project: ".amazonq/mcp.json", User: ".aws/amazonq/mcp.json", Key: "mcpServers", Format: "json"},
	{Name: "muse", Title: "Meta Muse Code", User: ".config/muse/settings.json", Key: "mcp_servers", Format: "json"},
}

// Find returns the client with that name.
func Find(name string) (Client, bool) {
	for _, c := range All {
		if c.Name == name {
			return c, true
		}
	}
	return Client{}, false
}

// Names lists the client names.
func Names() []string {
	out := make([]string, len(All))
	for i, c := range All {
		out[i] = c.Name
	}
	return out
}

// Server is the entry to write: the name and the command line that starts 'rcas mcp'.
type Server struct {
	Name    string
	Command string
	Args    []string
}

// Target says where a client's configuration is and what it should contain.
type Target struct {
	Client  Client
	Path    string
	Scope   string // project or user
	Before  []byte // nil when the file does not exist
	After   []byte
	Exists  bool // an entry with the server's name was already there
	Same    bool // ... and it is the one we would write
	Changed bool // After differs from Before
}

// Locate returns the file a client reads for a scope. home is the user's home directory; for
// Codex, codexHome (CODEX_HOME) replaces ~/.codex when it is set.
func Locate(c Client, scope, projectDir, home, codexHome string) (string, error) {
	switch scope {
	case "project":
		if c.Project == "" {
			return "", fmt.Errorf("%s has no project configuration; use --scope user", c.Title)
		}
		return filepath.Join(projectDir, filepath.FromSlash(c.Project)), nil
	case "user", "local":
		if scope == "local" {
			return "", fmt.Errorf("the local scope exists only through the tool's own command (%s mcp add -s local)", c.CLI)
		}
		if c.User == "" {
			return "", fmt.Errorf("%s has no user configuration file rcas can write; use --scope project", c.Title)
		}
		if c.Name == "codex" && codexHome != "" {
			return filepath.Join(codexHome, "config.toml"), nil
		}
		return filepath.Join(home, filepath.FromSlash(c.User)), nil
	}
	return "", fmt.Errorf("scope is project or user, not %q", scope)
}

// DefaultScope is project when the client has a project file, user otherwise.
func DefaultScope(c Client) string {
	if c.Project != "" {
		return "project"
	}
	return "user"
}

// Plan computes the new content of a client's configuration file without writing it.
func Plan(c Client, path, scope string, s Server, force bool) (*Target, error) {
	t := &Target{Client: c, Path: path, Scope: scope}
	if data, err := os.ReadFile(path); err == nil {
		t.Before = data
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	var err error
	if c.Format == "toml" {
		t.After, t.Exists, t.Same, err = mergeTOML(t.Before, c.Key, s, force)
	} else {
		t.After, t.Exists, t.Same, err = mergeJSON(t.Before, c, s, force)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	t.Changed = !bytes.Equal(t.Before, t.After)
	return t, nil
}

// Write writes a planned target, creating its directory. It writes through a symbolic link to its
// target, keeps the file's permissions (configuration files often hold keys, mode 0600), and keeps
// CRLF line endings when the file had them.
func Write(t *Target) error {
	if !t.Changed {
		return nil
	}
	path := t.Path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	data := t.After
	if bytes.Contains(t.Before, []byte("\r\n")) {
		data = bytes.ReplaceAll(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rcas-*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), mode)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		os.Remove(tmp.Name())
	}
	return werr
}

// entry is the server entry of a JSON configuration, in the shape the client expects.
func entry(c Client, s Server) *rulecascade.Object {
	e := rulecascade.NewObject()
	args := make([]any, len(s.Args))
	for i, a := range s.Args {
		args[i] = a
	}
	switch c.Name {
	case "opencode", "kilo":
		// one command array, "local" for a process
		e.Set("type", "local")
		e.Set("command", append([]any{s.Command}, args...))
		e.Set("enabled", true)
		return e
	case "muse":
		e.Set("transport", "stdio")
	case "vscode", "factory":
		e.Set("type", "stdio")
	}
	e.Set("command", s.Command)
	e.Set("args", args)
	return e
}

// newFile is the content of a configuration file the client needs before any entry.
func newFile(c Client) *rulecascade.Object {
	root := rulecascade.NewObject()
	switch c.Name {
	case "muse":
		root.Set("schema_version", 1)
	case "opencode":
		root.Set("$schema", "https://opencode.ai/config.json")
	}
	return root
}

func mergeJSON(before []byte, c Client, s Server, force bool) (after []byte, exists, same bool, err error) {
	root := newFile(c)
	if len(bytes.TrimSpace(before)) > 0 {
		doc, err := rulecascade.ParseJSON(before)
		if err != nil {
			return nil, false, false, fmt.Errorf("not plain JSON (%v); comments are not supported: add the entry by hand (rcas mcp install --print)", err)
		}
		o, ok := doc.(*rulecascade.Object)
		if !ok {
			return nil, false, false, fmt.Errorf("the file is not a JSON object")
		}
		root = o
	}
	servers := rulecascade.NewObject()
	if v, ok := root.Get(c.Key); ok {
		o, isObject := v.(*rulecascade.Object)
		if !isObject {
			return nil, false, false, fmt.Errorf("%s is not an object", c.Key)
		}
		servers = o
	}
	want := entry(c, s)
	if old, ok := servers.Get(s.Name); ok {
		exists = true
		same = rulecascade.Equal(old, want)
		if !same && !force {
			// keep the existing entry: the user may have edited it
			return before, true, false, nil
		}
	}
	if !same {
		servers.Set(s.Name, want)
		root.Set(c.Key, servers)
	}
	if same && before != nil {
		return before, exists, same, nil
	}
	out, err := rulecascade.MarshalIndent(root, "  ")
	if err != nil {
		return nil, false, false, err
	}
	return append(out, '\n'), exists, same, nil
}

// tomlString quotes a string as a TOML basic string.
func tomlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`).Replace(s) + `"`
}

var tomlHeader = regexp.MustCompile(`^\s*\[\[?\s*([^\]]+?)\s*\]\]?\s*(#.*)?$`)

// mergeTOML replaces or appends the table [<key>.<name>] in a TOML document. Only that table is
// touched: every other line is kept byte for byte.
func mergeTOML(before []byte, key string, s Server, force bool) (after []byte, exists, same bool, err error) {
	quotedArgs := make([]string, len(s.Args))
	for i, a := range s.Args {
		quotedArgs[i] = tomlString(a)
	}
	table := fmt.Sprintf("[%s.%s]\ncommand = %s\nargs = [%s]\n", key, tomlKey(s.Name), tomlString(s.Command), strings.Join(quotedArgs, ", "))
	text := strings.ReplaceAll(string(before), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	start, end := -1, len(lines)
	names := map[string]bool{key + "." + s.Name: true, key + `."` + s.Name + `"`: true, key + ".'" + s.Name + "'": true}
	// The entry may also be written as a key of [mcp_servers] (an inline table or dotted keys), or
	// as a dotted key at the top level; rewriting around it would make a duplicate key.
	inKey, current := regexp.MustCompile(`^\s*("?`+regexp.QuoteMeta(s.Name)+`"?)\s*(=|\.)`), ""
	topKey := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\.("?` + regexp.QuoteMeta(s.Name) + `"?)\s*(=|\.)`)
	for _, line := range lines {
		if m := tomlHeader.FindStringSubmatch(line); m != nil {
			current = strings.ReplaceAll(m[1], " ", "")
			continue
		}
		if (current == key && inKey.MatchString(line)) || (current == "" && topKey.MatchString(line)) {
			return nil, true, false, fmt.Errorf("%q is defined inline or with dotted keys; edit it by hand (rcas mcp install --print shows the entry)", s.Name)
		}
	}
	for i, line := range lines {
		m := tomlHeader.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		header := strings.ReplaceAll(m[1], " ", "")
		if start >= 0 {
			// a sub-table of ours ([mcp_servers.rules-cascade.env]) belongs to it
			if !strings.HasPrefix(header, key+"."+s.Name+".") {
				end = i
				break
			}
			continue
		}
		if names[header] {
			start = i
		}
	}
	if start >= 0 {
		// comments and blank lines before the next table belong to it, not to ours
		for end > start+1 {
			t := strings.TrimSpace(lines[end-1])
			if t != "" && !strings.HasPrefix(t, "#") {
				break
			}
			end--
		}
		exists = true
		old := strings.TrimRight(strings.Join(lines[start:end], "\n"), "\n") + "\n"
		same = old == table
		if same || !force {
			return before, true, same, nil
		}
		rest := strings.Join(lines[end:], "\n")
		head := strings.Join(lines[:start], "\n")
		if head != "" {
			head += "\n"
		}
		if rest != "" && !strings.HasPrefix(rest, "\n") {
			table += "\n"
		}
		return []byte(head + table + rest), true, false, nil
	}
	if strings.TrimSpace(text) == "" {
		return []byte(table), false, false, nil
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return []byte(text + "\n" + table), false, false, nil
}

func tomlKey(name string) string {
	if regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(name) {
		return name
	}
	return tomlString(name)
}

// Snippet is the configuration to add by hand, for --print.
func Snippet(c Client, s Server) string {
	if c.Format == "toml" {
		out, _, _, _ := mergeTOML(nil, c.Key, s, true)
		return string(out)
	}
	root := newFile(c)
	servers := rulecascade.NewObject()
	servers.Set(s.Name, entry(c, s))
	root.Set(c.Key, servers)
	out, _ := rulecascade.MarshalIndent(root, "  ")
	return string(out) + "\n"
}

// CLIArgs is the command line of the client's own CLI that registers the server, when it has one.
func CLIArgs(c Client, scope string, s Server) []string {
	switch c.Name {
	case "claude":
		return append([]string{"claude", "mcp", "add", "-s", scope, s.Name, "--", s.Command}, s.Args...)
	case "codex":
		return append([]string{"codex", "mcp", "add", s.Name, "--", s.Command}, s.Args...)
	case "gemini":
		// the -- is needed, or gemini reads -y as its own option
		return append([]string{"gemini", "mcp", "add", "-s", scope, s.Name, s.Command, "--"}, s.Args...)
	case "copilot-cli":
		return append([]string{"copilot", "mcp", "add", s.Name, "--", s.Command}, s.Args...)
	}
	return nil
}

// sortedNames is for messages.
func sortedNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
