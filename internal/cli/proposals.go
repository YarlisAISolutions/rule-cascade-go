package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	rulecascade "rules.sdods.com/go"
	"rules.sdods.com/go/internal/proposals"
)

const proposalsHelp = `A proposal is one or more ruleset files that an AI agent (through '{program} mcp') or
'{program} derive --propose' suggested. It waits in .rcas/proposals/ until a person decides:

  list [--status pending|invalid|accepted|rejected] [--json]
  show <id> [--diff]       the files, the problems check found, and with --diff the change to each file
  accept <id> [--force]    write the files into the project; refused when a target file changed since
                           the proposal was made, or when the proposal does not pass check (--force
                           overrides both)
  reject <id> [--reason <text>]

After accepting, run '{program} check' and commit the files like any other change.
`

// store opens the proposals of the project. Without an rcas.yaml the project root is the working
// directory.
func (c *cli) store() (*proposals.Store, error) {
	cfg, err := c.loadConfig()
	if err != nil {
		if !errors.Is(err, errNoConfig) {
			return nil, err
		}
		root, _ := filepath.Abs(c.workDir())
		return &proposals.Store{Dir: filepath.Join(root, ".rcas", "proposals"), Root: root, RulesDir: filepath.Join(root, "rules")}, nil
	}
	rules, _ := filepath.Abs(cfg.RulesDir())
	return &proposals.Store{Dir: cfg.ProposalsDir(), Root: cfg.Root, RulesDir: rules}, nil
}

// validate checks proposed ruleset files as they would be in the project, without writing them:
// each is loaded as if it were at its target path, with the rulesets already in that directory as
// its possible parents (the proposed ones replacing those with the same id) and its schema
// references resolved from there. It returns the problems of each ruleset, and notes that do not
// make a proposal invalid. Files that are not rulesets are not checked.
func (c *cli) validate(root string, contents map[string][]byte) (problems, notes map[string][]string, err error) {
	problems, notes = map[string][]string{}, map[string][]string{}
	type parsed struct {
		doc  any
		bad  []rulecascade.Problem
		err  error
		path string
	}
	byDir := map[string][]parsed{}
	for _, path := range sortedKeys(contents) {
		if !isRulesetFile(filepath.Base(path)) {
			continue
		}
		target, err := proposals.Within(root, path)
		if err != nil {
			return nil, nil, err
		}
		doc, bad, err := parseDocument(path, contents[path])
		byDir[filepath.Dir(target)] = append(byDir[filepath.Dir(target)], parsed{doc, bad, err, path})
	}
	for dir, files := range byDir {
		registry := c.directoryRegistry(dir)
		for _, f := range files {
			if f.err == nil {
				if id := text(member(member(f.doc, "metadata"), "id")); id != "" && member(f.doc, "kind") == "RuleSet" {
					registry[id] = f.doc
				}
			}
		}
		for _, f := range files {
			var list []string
			if f.err != nil {
				problems[f.path] = []string{"READ: " + f.err.Error()}
				continue
			}
			r := c.checkDocument(f.path, f.doc, f.bad, dir, registry)
			for _, p := range r.Problems {
				list = append(list, strings.TrimSpace(p.Code+": "+p.Rule+" "+p.Message))
			}
			if r.TestError != "" {
				list = append(list, "TESTS: "+r.TestError)
			}
			for _, fail := range r.Failures {
				list = append(list, "FAIL "+fail.Name+": "+strings.Join(fail.Reasons, "; "))
			}
			problems[f.path] = list
			if r.ID != "" && r.Tests == 0 {
				notes[f.path] = append(notes[f.path], "no golden tests: add a passing and a failing case for every rule before relying on it")
			}
		}
	}
	return problems, notes, nil
}

// propose validates and stores a proposal.
func (c *cli) propose(p *proposals.Proposal, contents map[string][]byte) (*proposals.Proposal, error) {
	return c.proposeAs(p, contents, true)
}

// proposeAs stores a proposal; with replace false (an agent's proposal) an existing id is refused.
// A generated id (derive-...) that was already decided gets a numeric suffix instead.
func (c *cli) proposeAs(p *proposals.Proposal, contents map[string][]byte, replace bool) (*proposals.Proposal, error) {
	store, err := c.store()
	if err != nil {
		return nil, err
	}
	for path := range contents {
		if _, err := store.Allowed(path); err != nil {
			return nil, err
		}
	}
	if replace && strings.HasPrefix(p.ID, "derive-") {
		base := p.ID
		for n := 2; ; n++ {
			old, err := store.Get(p.ID)
			if err != nil || (old.Status != proposals.Accepted && old.Status != proposals.Rejected) {
				break
			}
			p.ID = fmt.Sprintf("%s-%d", base, n)
		}
	}
	problems, notes, err := c.validate(store.Root, contents)
	if err != nil {
		return nil, err
	}
	p.Files = nil
	for path := range contents {
		p.Files = append(p.Files, proposals.File{Path: path, Problems: problems[path], Notes: notes[path]})
	}
	create := store.Create
	if !replace {
		create = store.CreateNew
	}
	if err := create(p, contents); err != nil {
		return nil, err
	}
	return p, nil
}

func (c *cli) proposalsCommand(args []string) int {
	if len(args) == 0 {
		c.usage("proposals")
		return exitUsage
	}
	sub := args[0]
	flags, positional, err := parseArgs(args[1:], map[string]string{"--status": "status", "--reason": "reason"},
		[]string{"--json", "--diff", "--force"})
	if err != nil {
		fmt.Fprintf(c.stderr, "%s proposals: %v\n", c.program, err)
		return exitUsage
	}
	store, err := c.store()
	if err != nil {
		fmt.Fprintf(c.stderr, "%s proposals: %v\n", c.program, err)
		return exitConfig
	}
	switch {
	case sub == "list" && len(positional) == 0:
		list, err := store.List(flags["status"])
		if err != nil {
			return c.fail(err)
		}
		if flags["--json"] != "" {
			if list == nil {
				list = []*proposals.Proposal{}
			}
			return c.printJSON(list)
		}
		if len(list) == 0 {
			fmt.Fprintf(c.stdout, "no proposals in %s\n", store.Dir)
			return exitOK
		}
		for _, p := range list {
			fmt.Fprintf(c.stdout, "%-40s %-8s %-8s %s  %s\n", p.ID, p.Status, p.Origin, p.Created.Format("2006-01-02 15:04"), p.Title)
		}
		return exitOK
	case sub == "show" && len(positional) == 1:
		return c.showProposal(store, positional[0], flags["--diff"] != "", flags["--json"] != "")
	case sub == "accept" && len(positional) == 1:
		p, err := store.Accept(positional[0], flags["--force"] != "")
		if err != nil {
			fmt.Fprintf(c.stderr, "%s proposals accept: %v\n", c.program, err)
			return exitFindings
		}
		fmt.Fprintf(c.stdout, "accepted %s:\n", p.ID)
		for _, f := range p.Files {
			fmt.Fprintf(c.stdout, "  wrote %s\n", f.Path)
		}
		fmt.Fprintf(c.stdout, "Next: %s check, then commit the files.\n", c.program)
		return exitOK
	case sub == "reject" && len(positional) == 1:
		p, err := store.Reject(positional[0], flags["reason"])
		if err != nil {
			fmt.Fprintf(c.stderr, "%s proposals reject: %v\n", c.program, err)
			return exitFindings
		}
		fmt.Fprintf(c.stdout, "rejected %s\n", p.ID)
		return exitOK
	}
	c.usage("proposals")
	return exitUsage
}

func (c *cli) showProposal(store *proposals.Store, id string, diff, asJSON bool) int {
	p, err := store.Get(id)
	if err != nil {
		fmt.Fprintf(c.stderr, "%s proposals show: %v\n", c.program, err)
		return exitFindings
	}
	if asJSON {
		type file struct {
			proposals.File
			Content string `json:"content"`
		}
		files := []file{}
		for _, f := range p.Files {
			content, _ := store.Content(id, f.Path)
			files = append(files, file{f, string(content)})
		}
		return c.printJSON(map[string]any{"proposal": p, "files": files})
	}
	fmt.Fprintf(c.stdout, "%s  %s  (%s, %s)\n", p.ID, p.Title, p.Status, p.Origin)
	if p.Rationale != "" {
		fmt.Fprintf(c.stdout, "\n%s\n", p.Rationale)
	}
	if len(p.SourceRefs) > 0 {
		fmt.Fprintf(c.stdout, "\nFrom: %s\n", strings.Join(p.SourceRefs, ", "))
	}
	for _, f := range p.Files {
		state := "new file"
		if f.Before != "" {
			state = "replaces the existing file"
		}
		fmt.Fprintf(c.stdout, "\n== %s (%s)\n", f.Path, state)
		for _, problem := range f.Problems {
			fmt.Fprintf(c.stdout, "  PROBLEM %s\n", problem)
		}
		for _, note := range f.Notes {
			fmt.Fprintf(c.stdout, "  NOTE    %s\n", note)
		}
		content, err := store.Content(id, f.Path)
		if err != nil {
			return c.fail(err)
		}
		if diff && f.Before != "" {
			target, _ := proposals.Within(store.Root, f.Path)
			old, _ := os.ReadFile(target)
			fmt.Fprint(c.stdout, lineDiff(string(old), string(content)))
		} else {
			fmt.Fprint(c.stdout, string(content))
		}
	}
	return exitOK
}

// lineDiff is a plain line diff (longest common subsequence), enough to review a ruleset change.
func lineDiff(a, b string) string {
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	if len(x)*len(y) > 4_000_000 {
		return "(too large to diff; showing nothing)\n"
	}
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out strings.Builder
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			out.WriteString("  " + x[i] + "\n")
			i, j = i+1, j+1
		case i < len(x) && (j == len(y) || lcs[i+1][j] >= lcs[i][j+1]):
			out.WriteString("- " + x[i] + "\n")
			i++
		default:
			out.WriteString("+ " + y[j] + "\n")
			j++
		}
	}
	return out.String()
}

// sortedKeys returns the keys of a map in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
