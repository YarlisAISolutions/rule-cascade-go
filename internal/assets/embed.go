// Package assets holds what the rcas command carries with it: the agent instructions it installs,
// and copies of the specification and the guides it serves to AI agents. The docs/ copies are
// written by `python tools/rulecheck.py sync` from spec/ and docs/; never edit them here.
package assets

import (
	"embed"
	"io/fs"
	"sort"
	"strings"
)

//go:embed templates docs
var files embed.FS

// Template returns an agent template (templates/agents/<name>).
func Template(name string) string {
	data, err := files.ReadFile("templates/agents/" + name)
	if err != nil {
		panic("assets: no template " + name)
	}
	return string(data)
}

// Roles lists the agent roles (sub-agents), by file name: templates/agents/roles/<name>.md, written
// in Claude Code's format and converted for the other tools.
func Roles() []string {
	return list("templates/agents/roles", false)
}

// Skills lists the skills, by name: templates/agents/skills/<name>/SKILL.md.
func Skills() []string {
	return list("templates/agents/skills", true)
}

// Skill returns the SKILL.md of a skill.
func Skill(name string) string {
	return Template("skills/" + name + "/SKILL.md")
}

func list(dir string, dirs bool) []string {
	entries, _ := fs.ReadDir(files, dir)
	var out []string
	for _, e := range entries {
		if e.IsDir() == dirs {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// Docs maps a topic to the embedded document.
var Docs = map[string]string{
	"specification": "SPECIFICATION.md",
	"authoring":     "authoring-guidelines.md",
	"naming":        "naming-conventions.md",
	"enforcement":   "enforcement-guide.md",
	"openapi":       "openapi.md",
	"cookbook":      "cookbook.md",
}

// Doc returns an embedded document by topic.
func Doc(topic string) (string, bool) {
	name, ok := Docs[topic]
	if !ok {
		return "", false
	}
	data, err := files.ReadFile("docs/" + name)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// Topics lists the document topics.
func Topics() []string {
	out := make([]string, 0, len(Docs))
	for k := range Docs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Section returns one section of a Markdown document: the heading whose text starts with the
// given number ("4.4", "13") or contains the given words, down to the next heading of the same or
// a higher level.
func Section(doc, which string) (string, bool) {
	lines := strings.Split(doc, "\n")
	want := strings.ToLower(strings.TrimSpace(which))
	start, level := -1, 0
	inFence := false
	for i, line := range lines {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
		}
		if inFence || !strings.HasPrefix(line, "#") {
			continue
		}
		hashes := len(line) - len(strings.TrimLeft(line, "#"))
		title := strings.ToLower(strings.TrimSpace(line[hashes:]))
		if start >= 0 {
			if hashes <= level {
				return strings.Join(lines[start:i], "\n"), true
			}
			continue
		}
		if matches(title, want) {
			start, level = i, hashes
		}
	}
	if start >= 0 {
		return strings.Join(lines[start:], "\n"), true
	}
	return "", false
}

// matches says whether a heading is the one asked for: by number ("4", "4.4") or by words.
func matches(title, want string) bool {
	if want != "" && strings.Trim(want, "0123456789.") == "" {
		number := strings.TrimSuffix(want, ".")
		return strings.HasPrefix(title, number+" ") || strings.HasPrefix(title, number+". ")
	}
	return want != "" && strings.Contains(title, want)
}

// Headings lists the headings of a document, for a reader who does not know the section numbers.
func Headings(doc string) []string {
	var out []string
	inFence := false
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
		}
		if !inFence && (strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ")) {
			out = append(out, line)
		}
	}
	return out
}
