package cli

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"rulescascade.com/go/internal/analyze"
	"rulescascade.com/go/internal/proposals"
)

const analyzeHelp = `Scans a code base for what business rules can be made from:

  - API schemas: OpenAPI 3 documents and JSON Schemas, with their schema names. '{program} derive'
    turns each into a baseline ruleset; with --derive this command does it for every schema found,
    as proposals.
  - decisions in code: validation libraries (Zod, Joi, Yup, class-validator, Pydantic, marshmallow,
    Django validators, Bean Validation, Spring validators, Go validate tags, ozzo-validation,
    FluentValidation, data annotations, Rails, Laravel, Rust validator), rule engines already in use,
    and hand-written checks that throw or return a validation error. Each with file:line.

It reads the code and never runs it. A hit is a candidate: what it decides, and whether it belongs
in a ruleset, is for a person or an AI agent reading the code (see '{program} agent install').

  --format md|json     md (default) for people, json for tools
  -o <file>            write the report to a file
  --derive             propose a ruleset for every schema found
  --include <glob>     only these paths (repeatable; ** matches directories)
  --exclude <glob>     not these (repeatable; also analyze.exclude of rcas.yaml)
  --max-files <n>      stop after n files (default 20000)
`

func (c *cli) analyze(path string, include, exclude []string, max int) *analyze.Report {
	if cfg, err := c.loadConfig(); err == nil {
		include = append(include, cfg.Analyze.Include...)
		exclude = append(exclude, cfg.Analyze.Exclude...)
		if max == 0 {
			max = cfg.Analyze.MaxFiles
		}
	}
	return analyze.Scan(path, analyze.Options{Include: include, Exclude: exclude, MaxFiles: max})
}

// repeated collects every value of a repeatable flag.
func repeated(args []string, names ...string) (values []string, rest []string) {
	for i := 0; i < len(args); i++ {
		name, value, hasValue := strings.Cut(args[i], "=")
		if contains(names, name) {
			if !hasValue && i+1 < len(args) {
				i++
				value = args[i]
			}
			values = append(values, value)
			continue
		}
		rest = append(rest, args[i])
	}
	return values, rest
}

func (c *cli) analyzeCommand(args []string) int {
	include, args := repeated(args, "--include")
	exclude, args := repeated(args, "--exclude")
	flags, positional, err := parseArgs(args, map[string]string{"--format": "format", "-o": "output", "--output": "output", "--max-files": "max"},
		[]string{"--derive"})
	if err != nil || len(positional) > 1 {
		if err != nil {
			fmt.Fprintf(c.stderr, "%s analyze: %v\n", c.program, err)
		}
		c.usage("analyze")
		return exitUsage
	}
	root := c.workDir()
	if len(positional) == 1 {
		root = c.path(positional[0])
	}
	max := 0
	if flags["max"] != "" {
		if max, err = strconv.Atoi(flags["max"]); err != nil || max <= 0 {
			fmt.Fprintf(c.stderr, "%s analyze: --max-files is a positive number\n", c.program)
			return exitUsage
		}
	}
	report := c.analyze(root, include, exclude, max)
	format := flags["format"]
	if format == "" {
		format = "md"
	}
	var out []byte
	switch format {
	case "md":
		out = []byte(report.Markdown())
	case "json":
		if flags["output"] == "" {
			return c.printJSON(report)
		}
		var b strings.Builder
		saved := c.stdout
		c.stdout = &b
		c.printJSON(report)
		c.stdout = saved
		out = []byte(b.String())
	default:
		fmt.Fprintf(c.stderr, "%s analyze: --format is md or json\n", c.program)
		return exitUsage
	}
	if flags["output"] != "" {
		if err := writeFileAtomic(c.outPath(flags["output"]), out, 0o644); err != nil {
			return c.fail(err)
		}
		fmt.Fprintf(c.stdout, "%d files, %d schemas, %d decisions in code -> %s\n", report.Files, len(report.Schemas), len(report.Hits), flags["output"])
	} else {
		c.stdout.Write(out)
	}
	if flags["--derive"] != "" {
		return c.deriveAll(root, report)
	}
	return exitOK
}

// deriveAll proposes one ruleset per schema found by analyze.
func (c *cli) deriveAll(root string, report *analyze.Report) int {
	prefix := "generated"
	if cfg, err := c.loadConfig(); err == nil && cfg.Project.IDPrefix != "" {
		prefix = cfg.Project.IDPrefix + ".generated"
	}
	rulesDir := c.rulesDirRel()
	projectRoot, _ := c.projectRoot()
	base := root
	if info, err := statDir(root); err == nil && !info {
		base = filepath.Dir(root)
	}
	status := exitOK
	made := 0
	seen := map[string]bool{}
	for _, s := range report.Schemas {
		if len(s.Names) == 0 {
			fmt.Fprintf(c.stdout, "  skipped  %s: no named schemas ($defs, definitions or components.schemas); derive it with --pointer\n", s.Path)
			continue
		}
		for _, name := range s.Names {
			entity := name
			if entity == "" {
				entity = strings.TrimSuffix(strings.TrimSuffix(filepath.Base(s.Path), filepath.Ext(s.Path)), ".schema")
			}
			kebab := slug(entity)
			if seen[kebab] { // the same schema name in two documents: keep both
				kebab = slug(strings.TrimSuffix(filepath.Base(s.Path), filepath.Ext(s.Path))) + "-" + kebab
			}
			seen[kebab] = true
			opts := deriveOptions{ID: prefix + "." + kebab}
			if s.Kind == "openapi" {
				opts.Schema = name
			} else if name != "" {
				opts.Pointer = "/$defs/" + name
				if !strings.Contains(readHead(filepath.Join(base, filepath.FromSlash(s.Path))), `"$defs"`) {
					opts.Pointer = "/definitions/" + name
				}
				opts.Entity = name
			}
			target := rulesDir + "/" + kebab + ".generated.ruleset.yaml"
			opts.Output = filepath.Join(projectRoot, filepath.FromSlash(target))
			res, err := runDerive(filepath.Join(base, filepath.FromSlash(s.Path)), opts)
			if err != nil {
				fmt.Fprintf(c.stderr, "  skipped %s %s: %v\n", s.Path, name, err)
				continue
			}
			contents := map[string][]byte{target: res.Ruleset}
			p, err := c.propose(&proposals.Proposal{ID: "derive-" + kebab, Title: "Baseline rules for " + entity + " from " + s.Path,
				Origin: "analyze", SourceRefs: []string{s.Path + "#" + name},
				Rationale: deriveRationale(res.Skipped)}, contents)
			if err != nil {
				fmt.Fprintf(c.stderr, "  could not propose %s: %v\n", entity, err)
				status = exitFindings
				continue
			}
			made++
			fmt.Fprintf(c.stdout, "  proposed %-36s %s (%s)\n", p.ID, entity, p.Status)
		}
	}
	if made > 0 {
		fmt.Fprintf(c.stdout, "%d proposal(s). Review: %s proposals list\n", made, c.program)
	}
	return status
}
