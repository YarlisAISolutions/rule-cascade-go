// Package proposals keeps the rulesets an AI agent or 'rcas derive --propose' suggested, until a
// person accepts or rejects them. A proposal never touches the project's files by itself: only
// Accept writes them, and 'rcas proposals accept' is the only caller of Accept.
//
// Layout: <dir>/<id>/proposal.json, and the proposed files under <dir>/<id>/files/, at the path
// they would have relative to the project root.
package proposals

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Statuses of a proposal.
const (
	Pending  = "pending"
	Invalid  = "invalid" // the proposed rulesets do not pass check; it can still be shown, not accepted without --force
	Accepted = "accepted"
	Rejected = "rejected"
)

// File is one proposed file.
type File struct {
	Path string `json:"path"` // relative to the project root, slash-separated
	// Before is the SHA-256 of the file when the proposal was made, or "" when it did not exist.
	// Accept refuses to overwrite a file that changed since, unless forced.
	Before   string   `json:"before"`
	Problems []string `json:"problems,omitempty"`
	Notes    []string `json:"notes,omitempty"` // advice that does not make the proposal invalid
}

// Proposal is the record of one proposal.
type Proposal struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Status     string     `json:"status"`
	Origin     string     `json:"origin"` // "mcp", "derive", "analyze"
	Rationale  string     `json:"rationale,omitempty"`
	SourceRefs []string   `json:"sourceRefs,omitempty"` // code or schema locations the rules come from
	Created    time.Time  `json:"created"`
	Decided    *time.Time `json:"decided,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	Files      []File     `json:"files"`
}

// Store is the proposals directory of one project.
type Store struct {
	Dir  string // the proposals directory
	Root string // the project root, which proposed paths are relative to
	// RulesDir is the only directory proposed files may be written to. Empty means Root/rules.
	RulesDir string
}

// proposable are the file names a proposal may contain: rulesets and the schemas they refer to.
var proposable = regexp.MustCompile(`(?i)\.(ruleset\.(ya?ml|json)|schema\.json)$`)

// Allowed checks that a proposed path is a ruleset or schema file inside the rules directory, in
// no hidden directory, and not inside the proposals directory. Create and Accept both apply it, so
// a proposal can never write configuration, version control or tool files.
func (s *Store) Allowed(rel string) (string, error) {
	target, err := Within(s.Root, rel)
	if err != nil {
		return "", err
	}
	for _, seg := range strings.Split(filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel))), "/") {
		if strings.HasPrefix(seg, ".") {
			return "", fmt.Errorf("%q: a proposal may not write hidden files or directories", rel)
		}
	}
	if !proposable.MatchString(target) {
		return "", fmt.Errorf("%q: a proposal may contain only *.ruleset.yaml, *.ruleset.yml, *.ruleset.json and *.schema.json files", rel)
	}
	rules := s.RulesDir
	if rules == "" {
		rules = filepath.Join(s.Root, "rules")
	}
	if r, err := filepath.Rel(rules, target); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q: a proposal may only write under the rules directory (%s)", rel, rules)
	}
	if r, err := filepath.Rel(s.Dir, target); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q is inside the proposals directory", rel)
	}
	return target, nil
}

var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,79}$`)

// ValidID reports whether id can name a proposal.
func ValidID(id string) bool { return validID.MatchString(id) && !strings.Contains(id, "..") }

// Hash is the hex SHA-256 of data.
func Hash(data []byte) string { s := sha256.Sum256(data); return hex.EncodeToString(s[:]) }

// Within joins a proposed path to base and refuses anything that would leave base.
func Within(base, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "\\") {
		return "", fmt.Errorf("%q: a proposed path must be relative to the project", rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q leaves the project", rel)
	}
	target := filepath.Join(base, clean)
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		realBase = base
	}
	for probe := target; ; probe = filepath.Dir(probe) {
		if _, err := os.Lstat(probe); err == nil {
			real, err := filepath.EvalSymlinks(probe)
			if err != nil {
				return "", err
			}
			r, err := filepath.Rel(realBase, real)
			if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("%q resolves outside the project", rel)
			}
			break
		}
		if filepath.Dir(probe) == probe || len(probe) <= len(base) {
			break
		}
	}
	return target, nil
}

// Create stores a new proposal. contents maps each proposed path to its content; problems, when
// not empty, makes the proposal Invalid. An existing proposal with the same id is replaced if it
// is still pending or invalid.
func (s *Store) Create(p *Proposal, contents map[string][]byte) error {
	return s.create(p, contents, true)
}

// CreateNew is Create for proposals from an agent: an existing proposal with the same id is never
// replaced, so what a person reviewed is what they accept.
func (s *Store) CreateNew(p *Proposal, contents map[string][]byte) error {
	return s.create(p, contents, false)
}

func (s *Store) create(p *Proposal, contents map[string][]byte, replace bool) error {
	if !ValidID(p.ID) {
		return fmt.Errorf("proposal id %q: use lower-case letters, digits, '.', '_' and '-' (at most 80)", p.ID)
	}
	if len(contents) == 0 {
		return errors.New("a proposal needs at least one file")
	}
	if old, err := s.Get(p.ID); err == nil {
		if old.Status == Accepted || old.Status == Rejected {
			return fmt.Errorf("proposal %s was already %s; propose under a new id", p.ID, old.Status)
		}
		if !replace {
			return fmt.Errorf("proposal %s exists; propose under a new id (a reviewed proposal is never replaced)", p.ID)
		}
	}
	paths := make([]string, 0, len(contents))
	for path := range contents {
		if _, err := s.Allowed(path); err != nil {
			return err
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	dir := filepath.Join(s.Dir, p.ID)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	problems, notes := map[string][]string{}, map[string][]string{}
	for _, f := range p.Files {
		problems[f.Path], notes[f.Path] = f.Problems, f.Notes
	}
	p.Files = nil
	invalid := false
	for _, path := range paths {
		target, err := Within(s.Root, path)
		if err != nil {
			return err
		}
		before := ""
		if data, err := os.ReadFile(target); err == nil {
			before = Hash(data)
		}
		staged, err := Within(filepath.Join(dir, "files"), path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(staged, contents[path], 0o644); err != nil {
			return err
		}
		if len(problems[path]) > 0 {
			invalid = true
		}
		p.Files = append(p.Files, File{Path: filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))), Before: before, Problems: problems[path], Notes: notes[path]})
	}
	p.Status = Pending
	if invalid {
		p.Status = Invalid
	}
	if p.Created.IsZero() {
		p.Created = time.Now().UTC().Truncate(time.Second)
	}
	return s.save(p)
}

func (s *Store) save(p *Proposal) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Join(s.Dir, p.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "proposal.json"), append(data, '\n'), 0o644)
}

// Get reads one proposal.
func (s *Store) Get(id string) (*Proposal, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("no proposal %q", id)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, id, "proposal.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no proposal %q in %s", id, s.Dir)
		}
		return nil, err
	}
	p := &Proposal{}
	if err := json.Unmarshal(data, p); err != nil {
		return nil, fmt.Errorf("proposal %s: %w", id, err)
	}
	return p, nil
}

// Content reads the proposed content of one file of a proposal.
func (s *Store) Content(id, path string) ([]byte, error) {
	staged, err := Within(filepath.Join(s.Dir, id, "files"), path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(staged)
}

// List returns every proposal, newest first; status filters when not empty.
func (s *Store) List(status string) ([]*Proposal, error) {
	entries, err := os.ReadDir(s.Dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Proposal
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, err := s.Get(e.Name())
		if err != nil {
			continue
		}
		if status == "" || p.Status == status {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.After(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Conflict says that a target file changed after the proposal was made.
type Conflict struct{ Paths []string }

func (c *Conflict) Error() string {
	return "changed since the proposal was made: " + strings.Join(c.Paths, ", ") + " (review them; --force overwrites)"
}

// Accept writes the proposed files into the project and marks the proposal accepted. It refuses an
// invalid proposal and a file that changed since the proposal was made, unless force is set.
func (s *Store) Accept(id string, force bool) (*Proposal, error) {
	p, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	switch p.Status {
	case Accepted, Rejected:
		return nil, fmt.Errorf("proposal %s was already %s", id, p.Status)
	case Invalid:
		if !force {
			return nil, fmt.Errorf("proposal %s does not pass check (see 'rcas proposals show %s'); --force accepts it anyway", id, id)
		}
	}
	var conflicts []string
	for _, f := range p.Files {
		target, err := s.Allowed(f.Path)
		if err != nil {
			return nil, err
		}
		now := ""
		if data, err := os.ReadFile(target); err == nil {
			now = Hash(data)
		}
		if now != f.Before {
			conflicts = append(conflicts, f.Path)
		}
	}
	if len(conflicts) > 0 && !force {
		return nil, &Conflict{conflicts}
	}
	for _, f := range p.Files {
		content, err := s.Content(id, f.Path)
		if err != nil {
			return nil, err
		}
		target, _ := s.Allowed(f.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		tmp, err := os.CreateTemp(filepath.Dir(target), ".rcas-*.tmp")
		if err != nil {
			return nil, err
		}
		_, werr := tmp.Write(content)
		cerr := tmp.Close()
		if werr == nil {
			werr = cerr
		}
		if werr == nil {
			werr = os.Rename(tmp.Name(), target)
		}
		if werr != nil {
			os.Remove(tmp.Name())
			return nil, werr
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	p.Status, p.Decided = Accepted, &now
	return p, s.save(p)
}

// Reject marks a proposal rejected, keeping it for the record.
func (s *Store) Reject(id, reason string) (*Proposal, error) {
	p, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if p.Status == Accepted || p.Status == Rejected {
		return nil, fmt.Errorf("proposal %s was already %s", id, p.Status)
	}
	now := time.Now().UTC().Truncate(time.Second)
	p.Status, p.Reason, p.Decided = Rejected, reason, &now
	return p, s.save(p)
}
