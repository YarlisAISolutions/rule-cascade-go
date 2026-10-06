package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"rules.sdods.com/go/internal/derive"
	"rules.sdods.com/go/internal/proposals"
)

const deriveHelp = `Builds a validation ruleset from the constraints of a schema: required members, types, enums,
lengths, patterns, ranges, item counts. Each rule gets golden tests. What cannot be expressed is
listed, not guessed. The output is deterministic: run it again after the schema changes and review
the difference.

  OpenAPI 3:    {program} derive api/openapi.yaml --schema Transfer --id acme.payments.transfer-generated
  JSON Schema:  {program} derive schemas/order.schema.json --pointer /$defs/Order --id acme.orders.generated

  -o <file>      write the ruleset (default: standard output)
  --check        compare with the file given by -o and fail when it is stale (for CI)
  --propose      store it as a proposal for review instead ('{program} proposals')
  --tests, --codes-from, --scope, --version, --title, --entity: as in the reference tool; see
  https://rules.sdods.com/reference/openapi/

Derived rulesets are generated files: change the schema and derive again; put hand-written rules
in a ruleset that extends the derived one.
`

type deriveOptions struct {
	Schema, Pointer, ID, Entity, Version, Title, TestsFile, CodesFrom string
	Scope                                                             []string
	// Output is where the ruleset will be: the entity's $ref is relative to its directory.
	Output string
}

func runDerive(path string, o deriveOptions) (*derive.Result, error) {
	if o.Pointer == "" && o.Schema == "" && !openapiRe.MatchString(readHead(path)) {
		// A ruleset that refers to the root of a schema document ("file#") does not load: the
		// entity reference must name a schema inside the document.
		return nil, fmt.Errorf("%s is a JSON Schema: name the entity schema with --pointer, e.g. /$defs/Order", path)
	}
	return derive.FromFile(path, derive.Options{Schema: o.Schema, Pointer: o.Pointer, ID: o.ID, Entity: o.Entity,
		Scope: o.Scope, Version: o.Version, Title: o.Title, TestsFile: o.TestsFile, CodesFrom: o.CodesFrom, Output: o.Output})
}

var openapiRe = regexp.MustCompile(`(?m)^\s*"?(openapi|swagger)"?\s*:`)

func statDir(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

// readHead returns the first 64 KiB of a file, for a cheap look at its shape.
func readHead(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, 64<<10))
	return string(data)
}

func (c *cli) deriveCommand(args []string) int {
	scopes, args := repeated(args, "--scope")
	flags, positional, err := parseArgs(args, map[string]string{
		"--schema": "schema", "--pointer": "pointer", "--id": "id", "--entity": "entity", "--version": "version",
		"--title": "title", "--tests": "tests", "--codes-from": "codes", "-o": "output", "--output": "output"},
		[]string{"--check", "--propose"})
	if err != nil || len(positional) != 1 || flags["id"] == "" {
		if err != nil {
			fmt.Fprintf(c.stderr, "%s derive: %v\n", c.program, err)
		} else if len(positional) == 1 {
			fmt.Fprintf(c.stderr, "%s derive: --id is required\n", c.program)
		}
		c.usage("derive")
		return exitUsage
	}
	if flags["--check"] != "" && flags["output"] == "" {
		fmt.Fprintf(c.stderr, "%s derive: --check needs -o, the file to compare with\n", c.program)
		return exitUsage
	}
	source := c.path(positional[0])
	output := c.outPath(flags["output"])
	target := output // where the ruleset will live, for the relative $ref
	if flags["--propose"] != "" && target == "" {
		if root, err := c.projectRoot(); err == nil {
			target = filepath.Join(root, c.rulesDirRel(), slug(flags["id"])+".ruleset.yaml")
		}
	}
	res, err := runDerive(source, deriveOptions{Schema: flags["schema"], Pointer: flags["pointer"], ID: flags["id"],
		Entity: flags["entity"], Version: flags["version"], Title: flags["title"], TestsFile: c.path(flags["tests"]),
		CodesFrom: c.path(flags["codes"]), Scope: scopes, Output: target})
	if err != nil {
		fmt.Fprintf(c.stderr, "%s derive: %v\n", c.program, err)
		return exitFindings
	}
	for _, s := range res.Skipped {
		fmt.Fprintf(c.stderr, "not derived: %s\n", s)
	}
	switch {
	case flags["--propose"] != "":
		root, err := c.projectRoot()
		if err != nil {
			return c.fail(err)
		}
		if abs, err := filepath.Abs(target); err == nil {
			if rel, err := filepath.Rel(root, abs); err == nil {
				target = rel
			}
		}
		target = filepath.ToSlash(target)
		contents := map[string][]byte{target: res.Ruleset}
		p, err := c.propose(&proposals.Proposal{ID: "derive-" + slug(flags["id"]), Title: "Derived " + flags["id"] + " from " + positional[0],
			Origin: "derive", SourceRefs: []string{positional[0] + "#" + flags["schema"] + flags["pointer"]},
			Rationale: deriveRationale(res.Skipped)}, contents)
		if err != nil {
			return c.fail(err)
		}
		fmt.Fprintf(c.stdout, "proposed %s (%s): %s proposals show %s\n", p.ID, p.Status, c.program, p.ID)
		return exitOK
	case flags["--check"] != "":
		current, err := os.ReadFile(output)
		if err != nil || !bytes.Equal(lf(current), res.Ruleset) {
			fmt.Fprintf(c.stderr, "%s is stale: run '%s derive' without --check to regenerate it\n", flags["output"], c.program)
			return exitFindings
		}
		fmt.Fprintf(c.stdout, "%s is up to date\n", flags["output"])
		return exitOK
	case output != "":
		if err := writeFileAtomic(output, res.Ruleset, 0o644); err != nil {
			return c.fail(err)
		}
		fmt.Fprintf(c.stdout, "wrote %s\n", flags["output"])
		return exitOK
	}
	c.stdout.Write(res.Ruleset)
	return exitOK
}

// rulesDirRel is the rules directory relative to the project root ("rules" without rcas.yaml).
func (c *cli) rulesDirRel() string {
	if cfg, err := c.loadConfig(); err == nil {
		if rel, err := filepath.Rel(cfg.Root, cfg.RulesDir()); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return "rules"
}

func deriveRationale(skipped []string) string {
	out := "Derived from the schema constraints; regenerate it when the schema changes. Hand-written rules belong in a ruleset that extends it."
	if len(skipped) > 0 {
		out += "\nNot derived (write these by hand if they matter):\n  " + strings.Join(skipped, "\n  ")
	}
	return out
}
