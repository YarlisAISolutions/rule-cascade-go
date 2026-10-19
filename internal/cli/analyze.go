package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	rulecascade "rulescascade.com/go"
	"rulescascade.com/go/internal/analyze"
	"rulescascade.com/go/internal/proposals"
)

const analyzeHelp = `Scans a code base for what business rules can be made from:

  - API schemas: OpenAPI 3 documents and JSON Schemas, with their schema names. '{program} derive'
    turns each into a baseline ruleset; with --derive this command does it for every schema found,
    as proposals.
  - decisions in code, in TypeScript, JavaScript, Python, Java, Kotlin, Scala, Go, C#, Ruby, PHP,
    Rust, Swift, Dart, C and C++:
      validation libraries: Zod, Joi, Yup, class-validator, react-hook-form, Angular Validators,
        Pydantic, marshmallow, Django, Bean Validation, Spring, Go validate and binding tags,
        ozzo-validation, FluentValidation, data annotations, Rails, Laravel, Rust validator, Flutter
      authorization: Spring Security, NestJS roles, Django permissions, ASP.NET [Authorize],
        Laravel gates, Pundit and CanCanCan
      user interfaces: React, Angular, Vue and Svelte templates, Blazor, SwiftUI, Jetpack Compose
      rule engines already in use: Drools .drl, DMN, json-rules-engine, NRules, Grule ...
      plain code: checks that throw, raise, return Err or require; named limits (MAX_QUANTITY = 10,
        const MaxQuantity = 10, #define MAX_QUANTITY 10); a name compared with a number
        (amount > 10000); branches on a status (case "GOLD":, when :gold, Tier::Gold =>)
    Each with file:line and a confidence.

It reads the code and never runs it. A hit is a candidate: what it decides, and whether it belongs
in a ruleset, is for a person or an AI agent reading the code (see '{program} agent install').

Every path in the report is relative to the project (the directory of rcas.yaml). Each hit names
its module (the nearest directory with pom.xml, build.gradle, package.json, go.mod, *.csproj,
Cargo.toml, composer.json, Gemfile, Package.swift, pubspec.yaml, CMakeLists.txt ...). The same
name compared with a number in two languages, such as a Java, Python, Go or C# service and a React,
Angular, Vue or Flutter front end, is listed once as a candidate rule, with drift when the values
differ. A hit whose name a project
ruleset already uses (a param, or a target field) is marked with that ruleset.

  [path ...]               directories or files (default: the project)
  --module <path|name>     only this module (repeatable); see the Modules table for the names
  --feature <name>         only the globs of analyze.features.<name> in rcas.yaml (repeatable)
  --include <glob>         only these paths (repeatable; ** matches directories)
  --exclude <glob>         not these (repeatable; also analyze.exclude of rcas.yaml)
  --changed <git-ref>      only the files changed since the merge base with <git-ref>, for pull requests
  --baseline <file>        leave out the hits recorded in this file (also analyze.baseline)
  --write-baseline <file>  record every hit found, so the next run shows only new ones
  --min-confidence <0..1>  leave out weaker hits (default 0; RCAS_ANALYZE_MIN_CONFIDENCE)
  --max-files <n>          stop after n files (default 20000; RCAS_ANALYZE_MAX_FILES)
  --max-hits <n>           stop after n hits (default 5000; RCAS_ANALYZE_MAX_HITS)
  --format md|json|sarif   md (default) for people, json for tools, sarif for GitHub code scanning
  -o <file>                write the report to a file
  --derive                 propose a ruleset for every schema found
`

// analyzeRequest is what to scan, from flags or MCP arguments.
type analyzeRequest struct {
	Paths                               []string // resolved; empty = the project
	Include, Exclude, Modules, Features []string
	Changed, Baseline, WriteBaseline    string
	MaxFiles, MaxHits                   int
	MinConfidence                       *float64
}

// baselineFile is what --write-baseline writes.
type baselineFile struct {
	Version      int      `json:"rcasAnalyzeBaseline"`
	Fingerprints []string `json:"fingerprints"`
}

func (c *cli) analyze(req analyzeRequest) (*analyze.Report, error) {
	cfg, cfgErr := c.loadConfig()
	if cfgErr != nil && !errors.Is(cfgErr, errNoConfig) {
		return nil, cfgErr
	}
	base, err := c.projectRoot()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range req.Paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, fmt.Errorf("%s: no such file or directory", p)
		}
		paths = append(paths, abs)
		if rel, err := filepath.Rel(base, abs); err != nil || strings.HasPrefix(rel, "..") {
			base = commonDir(base, abs)
		}
	}
	opt := analyze.Options{Include: req.Include, Exclude: req.Exclude, MaxFiles: req.MaxFiles, MaxHits: req.MaxHits}
	features := map[string][]string{}
	if cfg != nil {
		opt.Include = append(opt.Include, cfg.Analyze.Include...)
		opt.Exclude = append(opt.Exclude, cfg.Analyze.Exclude...)
		features = cfg.Analyze.Features
	}
	opt.Features = features
	if opt.MaxFiles, err = analyzeLimit(opt.MaxFiles, "RCAS_ANALYZE_MAX_FILES", cfg, func(c *Config) int { return c.Analyze.MaxFiles }, analyze.DefaultMaxFiles); err != nil {
		return nil, err
	}
	if opt.MaxHits, err = analyzeLimit(opt.MaxHits, "RCAS_ANALYZE_MAX_HITS", cfg, func(c *Config) int { return c.Analyze.MaxHits }, analyze.DefaultMaxHits); err != nil {
		return nil, err
	}
	switch {
	case req.MinConfidence != nil:
		opt.MinConfidence = *req.MinConfidence
	case os.Getenv("RCAS_ANALYZE_MIN_CONFIDENCE") != "":
		if opt.MinConfidence, err = parseConfidence(os.Getenv("RCAS_ANALYZE_MIN_CONFIDENCE")); err != nil {
			return nil, fmt.Errorf("RCAS_ANALYZE_MIN_CONFIDENCE: %v", err)
		}
	case cfg != nil && cfg.Analyze.MinConfidence != nil:
		opt.MinConfidence = *cfg.Analyze.MinConfidence
	default:
		opt.MinConfidence = analyze.DefaultMinConfidence
	}
	for _, name := range req.Features {
		globs, ok := features[name]
		if !ok {
			return nil, fmt.Errorf("no feature %q in analyze.features of rcas.yaml; known: %s", name, strings.Join(sortedKeys(features), ", "))
		}
		opt.Include = append(opt.Include, globs...)
	}
	if len(req.Modules) > 0 {
		modules := analyze.FindModules(base)
		for _, want := range req.Modules {
			found := false
			for _, m := range modules {
				if m.Path == filepath.ToSlash(strings.TrimSuffix(want, "/")) || filepath.Base(m.Path) == want {
					found = true
					if m.Path == "." {
						opt.Include = append(opt.Include, "**")
					} else {
						opt.Include = append(opt.Include, m.Path+"/**")
					}
				}
			}
			if !found {
				var names []string
				for _, m := range modules {
					names = append(names, m.Path)
				}
				return nil, fmt.Errorf("no module %q (a directory with pom.xml, build.gradle, package.json, go.mod ...); found: %s", want, strings.Join(names, ", "))
			}
		}
	}
	if req.Changed != "" {
		if opt.Only, err = changedFiles(base, req.Changed); err != nil {
			return nil, err
		}
	}
	baseline := req.Baseline
	if baseline == "" && cfg != nil && cfg.Analyze.Baseline != "" {
		if b := cfg.abs(cfg.Analyze.Baseline); fileExists(b) {
			baseline = b
		}
	}
	if baseline != "" && req.WriteBaseline == "" {
		if opt.Baseline, err = readBaseline(baseline); err != nil {
			return nil, err
		}
	}
	opt.Known = c.knownNames()
	report := analyze.Scan(base, paths, opt)
	if req.WriteBaseline != "" {
		b := baselineFile{Version: 1, Fingerprints: []string{}}
		seen := map[string]bool{}
		for _, h := range report.Hits {
			if !seen[h.Fingerprint] {
				seen[h.Fingerprint] = true
				b.Fingerprints = append(b.Fingerprints, h.Fingerprint)
			}
		}
		sort.Strings(b.Fingerprints)
		data, _ := json.MarshalIndent(b, "", "  ")
		if err := writeFileAtomic(req.WriteBaseline, append(data, '\n'), 0o644); err != nil {
			return nil, err
		}
	}
	return report, nil
}

// analyzeLimit picks a limit: the flag, else the environment variable, else rcas.yaml, else the default.
func analyzeLimit(flag int, env string, cfg *Config, fromConfig func(*Config) int, def int) (int, error) {
	if flag > 0 {
		return flag, nil
	}
	if cfg != nil && fromConfig(cfg) > 0 {
		def = fromConfig(cfg)
	}
	return envInt(env, def)
}

func parseConfidence(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v < 0 || v > 1 {
		return 0, fmt.Errorf("a confidence is a number between 0 and 1, not %q", s)
	}
	return v, nil
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func readBaseline(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("baseline %s: %v (create it with --write-baseline)", path, err)
	}
	var b baselineFile
	if err := json.Unmarshal(data, &b); err != nil || b.Version != 1 {
		return nil, fmt.Errorf("baseline %s is not a file written by --write-baseline", path)
	}
	out := map[string]bool{}
	for _, f := range b.Fingerprints {
		out[f] = true
	}
	return out, nil
}

// commonDir is the deepest directory that holds both a and b.
func commonDir(a, b string) string {
	for d := a; ; d = filepath.Dir(d) {
		if rel, err := filepath.Rel(d, b); err == nil && !strings.HasPrefix(rel, "..") {
			return d
		}
		if filepath.Dir(d) == d {
			return d
		}
	}
}

// changedFiles lists the files, relative to base, that differ from the merge base of HEAD and ref,
// committed or not, and the untracked files git does not ignore.
func changedFiles(base, ref string) (map[string]bool, error) {
	run := func(dir string, args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.Output()
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return "", fmt.Errorf("--changed %s: git %s: %s", ref, strings.Join(args, " "), strings.TrimSpace(string(exit.Stderr)))
			}
			return "", fmt.Errorf("--changed needs git: %v", err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	top, err := run(base, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	mergeBase, err := run(base, "merge-base", ref, "HEAD")
	if err != nil {
		return nil, err
	}
	changed, err := run(top, "diff", "--name-only", "--diff-filter=ACMR", mergeBase)
	if err != nil {
		return nil, err
	}
	untracked, err := run(top, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		realBase = base
	}
	out := map[string]bool{}
	for _, line := range strings.Split(changed+"\n"+untracked, "\n") {
		if line == "" {
			continue
		}
		if rel, err := filepath.Rel(realBase, filepath.Join(top, filepath.FromSlash(line))); err == nil && !strings.HasPrefix(rel, "..") {
			out[filepath.ToSlash(rel)] = true
		}
	}
	return out, nil
}

// knownNames maps the name keys the project's rulesets use (params, and the last segment of each
// rule's target fields) to the ruleset id, so analyze can tell which hits a ruleset already names.
func (c *cli) knownNames() map[string]string {
	index, err := c.rulesetIndex()
	if err != nil {
		return nil
	}
	known := map[string]string{}
	add := func(name, id string) {
		if k := analyze.Key(name); k != "" && known[k] == "" {
			known[k] = id
		}
	}
	for _, id := range sortedKeys(index) {
		doc, _, err := readDocument(index[id])
		if err != nil {
			continue
		}
		if params := object(member(doc, "params")); params != nil {
			for _, name := range params.Keys() {
				add(name, id)
			}
		}
		for _, rule := range list(member(doc, "rules")) {
			target := member(rule, "target")
			fields := append([]any{member(target, "field")}, list(member(target, "fields"))...)
			for _, f := range fields {
				if p := text(f); p != "" {
					add(p[strings.LastIndex(p, "/")+1:], id)
				}
			}
		}
	}
	return known
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
	modules, args := repeated(args, "--module")
	features, args := repeated(args, "--feature")
	flags, positional, err := parseArgs(args, map[string]string{"--format": "format", "-o": "output", "--output": "output",
		"--max-files": "max", "--max-hits": "maxHits", "--min-confidence": "minConfidence", "--changed": "changed",
		"--baseline": "baseline", "--write-baseline": "writeBaseline"}, []string{"--derive"})
	if err != nil {
		fmt.Fprintf(c.stderr, "%s analyze: %v\n", c.program, err)
		c.usage("analyze")
		return exitUsage
	}
	req := analyzeRequest{Include: include, Exclude: exclude, Modules: modules, Features: features, Changed: flags["changed"]}
	for _, p := range positional {
		req.Paths = append(req.Paths, c.path(p))
	}
	for flag, into := range map[string]*int{"max": &req.MaxFiles, "maxHits": &req.MaxHits} {
		if flags[flag] != "" {
			if *into, err = strconv.Atoi(flags[flag]); err != nil || *into <= 0 {
				fmt.Fprintf(c.stderr, "%s analyze: --max-files and --max-hits are positive numbers\n", c.program)
				return exitUsage
			}
		}
	}
	if flags["minConfidence"] != "" {
		v, err := parseConfidence(flags["minConfidence"])
		if err != nil {
			fmt.Fprintf(c.stderr, "%s analyze: --min-confidence: %v\n", c.program, err)
			return exitUsage
		}
		req.MinConfidence = &v
	}
	if flags["baseline"] != "" {
		req.Baseline = c.path(flags["baseline"])
	}
	if flags["writeBaseline"] != "" {
		req.WriteBaseline = c.path(flags["writeBaseline"])
	}
	format := flags["format"]
	if format == "" {
		format = "md"
	}
	if format != "md" && format != "json" && format != "sarif" {
		fmt.Fprintf(c.stderr, "%s analyze: --format is md, json or sarif\n", c.program)
		return exitUsage
	}
	report, err := c.analyze(req)
	if err != nil {
		fmt.Fprintf(c.stderr, "%s analyze: %v\n", c.program, err)
		return exitUsage
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
	case "sarif":
		if out, err = report.SARIF(rulecascade.Version); err != nil {
			return c.fail(err)
		}
		out = append(out, '\n')
	}
	if flags["output"] != "" {
		if err := writeFileAtomic(c.outPath(flags["output"]), out, 0o644); err != nil {
			return c.fail(err)
		}
		fmt.Fprintf(c.stdout, "%d files, %d schemas, %d decisions in code, %d in more than one language -> %s\n",
			report.Files, len(report.Schemas), len(report.Hits), len(report.Candidates), flags["output"])
	} else {
		c.stdout.Write(out)
	}
	if req.WriteBaseline != "" {
		fmt.Fprintf(c.stderr, "baseline: %d hits recorded in %s\n", len(report.Hits), flags["writeBaseline"])
	}
	if flags["--derive"] != "" {
		return c.deriveAll(report)
	}
	return exitOK
}

// deriveAll proposes one ruleset per schema found by analyze.
func (c *cli) deriveAll(report *analyze.Report) int {
	prefix := "generated"
	if cfg, err := c.loadConfig(); err == nil && cfg.Project.IDPrefix != "" {
		prefix = cfg.Project.IDPrefix + ".generated"
	}
	rulesDir := c.rulesDirRel()
	projectRoot, _ := c.projectRoot()
	base := filepath.FromSlash(report.Root)
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
