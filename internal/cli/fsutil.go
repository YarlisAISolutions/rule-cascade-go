package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomic writes a file through a temporary file in the same directory and a rename, so a
// reader never sees half of it. Missing directories are created.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real // write through a link to its target, keep the link
	}
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm() // keep the permissions of a file that exists
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	// os.Rename replaces an existing file on every platform Go supports, Windows included.
	return os.Rename(name, path)
}

// lf converts CRLF line endings to LF: every file rcas writes has LF endings on every platform.
func lf(data []byte) []byte { return bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")) }

// fileWrite is one file a scaffolding command creates.
type fileWrite struct {
	Path    string // relative to the base directory, slash-separated
	Content []byte
	Mode    os.FileMode
	KeepEOL bool // the content already has the line endings it should keep (CRLF of an edited file)
}

// writeOutcome says what happened to one file.
type writeOutcome struct {
	Path   string `json:"path"`
	Action string `json:"action"` // created, overwritten, unchanged, kept (exists and differs; --force replaces it), would create...
}

// writeFiles writes files under base. An existing file is left alone unless force is set; one with
// the same content is reported unchanged. With dryRun nothing is written.
func writeFiles(base string, files []fileWrite, force, dryRun bool) ([]writeOutcome, error) {
	var out []writeOutcome
	for _, f := range files {
		target, err := safeJoin(base, f.Path)
		if err != nil {
			return out, err
		}
		content := f.Content
		if !f.KeepEOL {
			content = lf(content)
		}
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		action := "created"
		if existing, err := os.ReadFile(target); err == nil {
			switch {
			case bytes.Equal(existing, content):
				out = append(out, writeOutcome{f.Path, "unchanged"})
				continue
			case !force:
				out = append(out, writeOutcome{f.Path, "kept (exists and differs; --force replaces it)"})
				continue
			}
			action = "overwritten"
		}
		if dryRun {
			out = append(out, writeOutcome{f.Path, "would be " + action})
			continue
		}
		if err := writeFileAtomic(target, content, mode); err != nil {
			return out, err
		}
		out = append(out, writeOutcome{f.Path, action})
	}
	return out, nil
}

// safeJoin joins a relative, slash-separated path to base and refuses one that would leave base:
// an absolute path, "..", or a symbolic link inside base that points outside it.
func safeJoin(base, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" || (len(rel) > 0 && (rel[0] == '/' || rel[0] == '\\')) {
		return "", fmt.Errorf("%q: only a relative path is allowed", rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || len(clean) >= 3 && clean[:3] == ".."+string(filepath.Separator) {
		return "", fmt.Errorf("%q leaves the project directory", rel)
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	target := filepath.Join(absBase, clean)
	// Resolve the deepest existing ancestor: a symbolic link must not lead outside base.
	realBase, err := filepath.EvalSymlinks(absBase)
	if err != nil {
		realBase = absBase
	}
	probe := target
	for {
		if _, err := os.Lstat(probe); err == nil {
			real, err := filepath.EvalSymlinks(probe)
			if err != nil {
				return "", err
			}
			if r, err := filepath.Rel(realBase, real); err != nil || r == ".." || len(r) >= 3 && r[:3] == ".."+string(filepath.Separator) {
				return "", fmt.Errorf("%q resolves outside the project directory", rel)
			}
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe || len(parent) < len(absBase) {
			break
		}
		probe = parent
	}
	return target, nil
}

func (c *cli) printOutcomes(outcomes []writeOutcome) {
	for _, o := range outcomes {
		fmt.Fprintf(c.stdout, "  %-12s %s\n", o.Action, o.Path)
	}
}
