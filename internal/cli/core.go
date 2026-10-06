// Package cli is the rcas command (also installed as rule-cascade): it checks, compiles and
// evaluates Rule Cascade rulesets, serves the engine protocol of the specification (section 13),
// scaffolds projects, derives rules from API schemas, and serves the Model Context Protocol for AI
// coding agents. This file holds the commands every engine shares; app.go dispatches.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	rulecascade "rules.sdods.com/go"
)

// BuildVersion is set by the command's main package from -ldflags "-X main.version=...".
var BuildVersion string

type cli struct {
	program        string // "rcas", or "rule-cascade" when invoked by that name
	stdin          io.Reader
	stdout, stderr io.Writer
	dir            string // -C: the directory relative paths and the config are resolved against
	configPath     string // --config
	// confine, when set (the MCP server), is the only directory schema and API documents may be
	// read from: an agent's draft cannot make the command read files outside the project.
	confine string
}

// readable reports whether a referenced document may be read.
func (c *cli) readable(path string) bool {
	if c.confine == "" {
		return true
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return true // it does not exist: nothing can be read from it
	}
	rel, err := filepath.Rel(c.confine, real)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// core runs the commands of the original rule-cascade command: version, check, compile, manifest,
// evaluate and engine. It returns -1 for a command it does not know.
func (c *cli) core(command string, rest []string) int {
	valued := map[string]string{}
	var switches []string
	switch command {
	case "compile":
		valued = map[string]string{"-o": "output", "--output": "output"}
		switches = []string{"--all"}
	case "manifest":
		valued = map[string]string{"--channel": "channel", "--bundle": "bundle", "--manifest": "manifest", "-o": "output", "--output": "output"}
	case "evaluate":
		valued = map[string]string{"--bundle": "bundle", "--manifest": "manifest", "--channel": "channel"}
		switches = []string{"--conformance-operators"}
	case "engine":
		switches = []string{"--conformance-operators"}
	case "version":
		switches = []string{"--json"}
	case "check", "test":
		switches = []string{"--json"}
	default:
		return -1
	}
	flags, files, err := parseArgs(rest, valued, switches)
	if err != nil {
		fmt.Fprintf(c.stderr, "%s %s: %v\n", c.program, command, err)
		return 2
	}
	// one source: a file argument (manifest only), --bundle or --manifest
	sources := 0
	for _, given := range []bool{command == "manifest" && len(files) == 1, flags["bundle"] != "", flags["manifest"] != ""} {
		if given {
			sources++
		}
	}
	var operators rulecascade.Operators
	if flags["--conformance-operators"] != "" {
		operators = rulecascade.ConformanceOperators()
	}
	switch {
	case command == "version" && len(files) == 0:
		if flags["--json"] != "" {
			return c.print(map[string]any{"program": c.program, "version": engineVersion(),
				"specification": rulecascade.SpecVersion, "bundleFormat": rulecascade.BundleVersion})
		}
		fmt.Fprintf(c.stdout, "%s %s (specification %s, bundle format %s)\n",
			c.program, engineVersion(), rulecascade.SpecVersion, rulecascade.BundleVersion)
		return 0
	case command == "check" || command == "test":
		if len(files) == 0 {
			found, status := c.configuredRulesets(command)
			if status != 0 {
				return status
			}
			files = found
		} else {
			files = c.expand(files)
		}
		return c.check(files, command == "test", flags["--json"] != "")
	case command == "compile" && flags["--all"] != "" && len(files) == 0:
		return c.compileAll(flags["output"])
	case command == "compile" && len(files) == 1 && flags["--all"] == "":
		return c.compile(c.path(files[0]), c.outPath(flags["output"]))
	case command == "manifest" && sources == 1 && len(files) <= 1:
		file := append(files, "")[0]
		if file != "" {
			file = c.path(file)
		}
		return c.manifest(file, c.path(flags["bundle"]), c.path(flags["manifest"]), flags["channel"], c.outPath(flags["output"]))
	case command == "evaluate" && sources == 1 && len(files) <= 1:
		request := append(files, "-")[0]
		if request != "-" {
			request = c.path(request)
		}
		return c.evaluate(c.path(flags["bundle"]), c.path(flags["manifest"]), flags["channel"], request, operators)
	case command == "engine" && len(files) == 0:
		engine := rulecascade.NewEngine(operators)
		engine.Version = engineVersion()
		if err := engine.Serve(c.stdin, c.stdout); err != nil {
			fmt.Fprintf(c.stderr, "%s engine: %v\n", c.program, err)
			return 1
		}
		return 0
	}
	c.usage(command)
	return 2
}

func engineVersion() string {
	if BuildVersion != "" {
		return BuildVersion
	}
	return rulecascade.Version
}

// parseArgs separates flags from file arguments. Flags may come before or after the files.
// valued maps each flag that takes a value to the name it is stored under; a switch is stored
// under its own name.
func parseArgs(args []string, valued map[string]string, switches []string) (flags map[string]string, files []string, err error) {
	flags = map[string]string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := strings.Cut(arg, "=")
		switch {
		case arg == "--":
			return flags, append(files, args[i+1:]...), nil
		case arg == "-" || !strings.HasPrefix(arg, "-"):
			files = append(files, arg)
		case valued[name] != "":
			if !hasValue {
				if i++; i >= len(args) {
					return nil, nil, fmt.Errorf("%s needs a value", name)
				}
				value = args[i]
			}
			flags[valued[name]] = value
		case contains(switches, arg):
			flags[arg] = "true"
		default:
			return nil, nil, fmt.Errorf("unknown option %s", arg)
		}
	}
	return flags, files, nil
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------ reading documents

// readDocument reads a YAML or JSON file as a JSON value. For YAML it also returns what makes the
// file not portable (specification section 12).
func readDocument(path string) (any, []rulecascade.Problem, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if strings.EqualFold(filepath.Ext(path), ".json") {
		value, err := rulecascade.ParseJSON(data)
		return value, nil, err
	}
	return parseYAML(data)
}

func object(v any) *rulecascade.Object { o, _ := v.(*rulecascade.Object); return o }

func member(v any, key string) any { m, _ := object(v).Get(key); return m }

func text(v any) string { s, _ := v.(string); return s }

func list(v any) []any { l, _ := v.([]any); return l }

// directoryRegistry returns the rulesets found in the *.ruleset.* files of a directory, by id.
func (c *cli) directoryRegistry(directory string) map[string]any {
	registry := map[string]any{}
	paths, _ := filepath.Glob(filepath.Join(directory, "*.ruleset.*"))
	sort.Strings(paths)
	for _, path := range paths {
		doc, _, err := readDocument(path)
		if err != nil {
			fmt.Fprintf(c.stderr, "warning: %s is not readable and is left out of the registry: %v\n", path, err)
			continue
		}
		if id, ok := member(member(doc, "metadata"), "id").(string); ok && member(doc, "kind") == "RuleSet" {
			registry[id] = doc
		}
	}
	return registry
}

// fileLoader resolves the schema documents entities refer to, relative to a directory.
func (c *cli) fileLoader(directory string) rulecascade.SchemaLoader {
	return func(file string) any {
		path := filepath.Join(directory, filepath.FromSlash(file))
		if !c.readable(path) {
			return nil
		}
		if _, err := os.Stat(path); err != nil {
			return nil
		}
		doc, _, err := readDocument(path)
		if err != nil {
			fmt.Fprintf(c.stderr, "warning: %s is not readable: %v\n", path, err)
			return nil
		}
		return doc
	}
}

// loadFile compiles a ruleset file. A YAML file that is not portable is refused: what it means
// would depend on the parser that reads it.
func (c *cli) loadFile(path string) (*rulecascade.RuleSet, error) {
	doc, problems, err := readDocument(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(problems) > 0 {
		return nil, &rulecascade.LoadError{Problems: problems}
	}
	directory := filepath.Dir(path)
	return rulecascade.Load(doc, c.directoryRegistry(directory), c.fileLoader(directory))
}

// fail prints why a file could not be used and returns the exit status.
func (c *cli) fail(err error) int {
	var failure *rulecascade.LoadError
	if errors.As(err, &failure) {
		for _, p := range failure.Problems {
			fmt.Fprintf(c.stderr, "%s: %s\n", p.Code, p.Message)
		}
	} else {
		fmt.Fprintf(c.stderr, "%s: %v\n", c.program, err)
	}
	return 1
}

func (c *cli) print(v any) int {
	out, err := rulecascade.MarshalIndent(v, "  ")
	if err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(c.stdout, "%s\n", out)
	return 0
}

// ------------------------------------------------------------------ commands

func (c *cli) compile(path, output string) int {
	rs, err := c.loadFile(path)
	if err != nil {
		return c.fail(err)
	}
	if output == "" {
		return c.print(rs.Bundle())
	}
	out, err := rulecascade.MarshalIndent(rs.Bundle(), "  ")
	if err == nil {
		err = os.WriteFile(output, append(out, '\n'), 0o644)
	}
	if err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(c.stdout, "wrote %s  %s@%s  %s\n", output, rs.ID(), rs.Version(), rs.Checksum())
	return 0
}

// source reads the ruleset an evaluator works on: a bundle, or one manifest on its own.
func (c *cli) source(bundlePath, manifestPath string) (*rulecascade.RuleSet, error) {
	path, read := bundlePath, rulecascade.FromBundle
	if bundlePath == "" {
		path, read = manifestPath, rulecascade.FromManifest
	}
	doc, _, err := readDocument(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return read(doc)
}

// manifest prints or writes one manifest of a ruleset file, a bundle or a manifest. Without
// --channel it is the client manifest, which is what is published to a browser or a mobile app,
// or the channel of the manifest that was given.
func (c *cli) manifest(path, bundlePath, manifestPath, channel, output string) int {
	var rs *rulecascade.RuleSet
	var err error
	if path == "" {
		rs, err = c.source(bundlePath, manifestPath)
	} else {
		var doc any
		doc, _, err = readDocument(path)
		if _, isBundle := object(doc).Get("ruleCascadeBundle"); err == nil && isBundle {
			rs, err = rulecascade.FromBundle(doc)
		} else if err == nil {
			rs, err = c.loadFile(path)
		}
	}
	if err != nil {
		return c.fail(err)
	}
	if channel == "" && contains(rs.Channels(), "client") {
		channel = "client"
	}
	mf, err := rs.Manifest(channel)
	if err != nil {
		return c.fail(err)
	}
	if output == "" {
		return c.print(mf)
	}
	out, err := rulecascade.MarshalIndent(mf, "  ")
	if err == nil {
		err = os.WriteFile(output, append(out, '\n'), 0o644)
	}
	if err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(c.stdout, "wrote %s  %s@%s  %s manifest\n", output, rs.ID(), rs.Version(), text(member(mf, "channel")))
	return 0
}

// evaluate evaluates one request against a bundle or a manifest. Without --channel it is the
// server's decision when the source has a server manifest, and otherwise the channel it has.
func (c *cli) evaluate(bundlePath, manifestPath, channel, requestPath string, operators rulecascade.Operators) int {
	rs, err := c.source(bundlePath, manifestPath)
	if err != nil {
		return c.fail(err)
	}
	var request any
	if requestPath == "-" {
		var data []byte
		if data, err = io.ReadAll(c.stdin); err == nil {
			request, err = rulecascade.ParseJSON(data)
		}
	} else {
		request, _, err = readDocument(requestPath)
	}
	if err != nil {
		return c.fail(fmt.Errorf("the request: %w", err))
	}
	result, err := rs.Evaluate(request, channel, operators)
	if err != nil {
		return c.fail(err)
	}
	return c.print(result)
}

// numberProblems lists the numbers the portable profile does not cover: those with more than 15
// significant digits (specification section 4.2). Runtimes read such a number as the nearest
// double, which is rarely what its author meant.
func numberProblems(node any, path string) []rulecascade.Problem {
	var out []rulecascade.Problem
	text := ""
	switch x := node.(type) {
	case *rulecascade.Object:
		for _, key := range x.Keys() {
			out = append(out, numberProblems(member(x, key), path+"/"+key)...)
		}
	case []any:
		for i, item := range x {
			out = append(out, numberProblems(item, fmt.Sprintf("%s/%d", path, i))...)
		}
	case float64:
		text = floatText(x)
	case json.Number:
		if whole, ok := new(big.Int).SetString(string(x), 10); ok {
			text = whole.String()
		} else if f, err := strconv.ParseFloat(string(x), 64); err == nil {
			text = floatText(f)
		}
	}
	mantissa, _, _ := strings.Cut(strings.ToLower(text), "e")
	if digits := strings.Trim(strings.NewReplacer("-", "", ".", "").Replace(mantissa), "0"); len(digits) > 15 {
		out = append(out, rulecascade.Problem{Code: "NUMBER_NOT_PORTABLE", Message: fmt.Sprintf(
			"%s: %s has more than 15 significant digits; runtimes read it as the nearest double", path, text)})
	}
	return out
}

// fileReport is what check found in one ruleset file. The text output and the JSON output (and
// the MCP tools) are both made from it.
type fileReport struct {
	Path        string                `json:"path"`
	ReadError   string                `json:"readError,omitempty"`
	ID          string                `json:"id,omitempty"`
	Version     string                `json:"version,omitempty"`
	Checksum    string                `json:"checksum,omitempty"`
	Rules       int                   `json:"rules"`
	ClientRules int                   `json:"clientRules"`
	Params      int                   `json:"params"`
	Problems    []rulecascade.Problem `json:"problems"`
	Missing     []string              `json:"missingOperators,omitempty"`
	Tests       int                   `json:"tests"`
	Failures    []testFailure         `json:"failures"`
	TestError   string                `json:"testError,omitempty"`
}

type testFailure struct {
	Name    string   `json:"name"`
	Reasons []string `json:"reasons"`
}

// bad counts what makes the file fail: problems, failed tests, and a read or test error.
func (r *fileReport) bad() int {
	n := len(r.Problems) + len(r.Failures)
	if r.ReadError != "" || r.TestError != "" {
		n++
	}
	return n
}

// checkFile lints a ruleset, loads it and runs its golden tests, like tools/rulecheck.py check.
// The golden tests run with the conformance operators registered.
func (c *cli) checkFile(path string) *fileReport {
	doc, problems, err := readDocument(path)
	if err != nil {
		return &fileReport{Path: path, ReadError: err.Error(), Problems: []rulecascade.Problem{}, Failures: []testFailure{}}
	}
	directory := filepath.Dir(path)
	return c.checkDocument(path, doc, problems, directory, c.directoryRegistry(directory))
}

// parseDocument reads YAML or JSON content as readDocument does, by the extension of name.
func parseDocument(name string, data []byte) (any, []rulecascade.Problem, error) {
	if strings.EqualFold(filepath.Ext(name), ".json") {
		value, err := rulecascade.ParseJSON(data)
		return value, nil, err
	}
	return parseYAML(data)
}

// checkDocument checks a parsed ruleset as if it were in directory, with the given registry of
// parents; check of a file and of a proposal (which is not written yet) both come here.
func (c *cli) checkDocument(path string, doc any, problems []rulecascade.Problem, directory string, registry map[string]any) *fileReport {
	r := &fileReport{Path: path, Problems: []rulecascade.Problem{}, Failures: []testFailure{}}
	problems = append(problems, numberProblems(doc, "")...)
	rs, err := rulecascade.Load(doc, registry, c.fileLoader(directory))
	var failure *rulecascade.LoadError
	switch {
	case errors.As(err, &failure):
		problems = append(problems, failure.Problems...)
	case err != nil:
		problems = append(problems, rulecascade.Problem{Code: "LOAD_FAILED", Message: err.Error()})
	}
	if rs != nil {
		problems = append(problems, c.bindingProblems(doc, directory)...)
		server, _ := rs.Manifest("server")
		client, _ := rs.Manifest("client")
		r.ID, r.Version, r.Checksum = rs.ID(), rs.Version(), rs.Checksum()
		r.Rules, r.ClientRules = len(list(member(server, "rules"))), len(list(member(client, "rules")))
		r.Params = object(member(server, "params")).Len()
	}
	r.Problems = append(r.Problems, problems...)
	if rs == nil {
		return r
	}
	r.Missing = rs.MissingOperators(rulecascade.ConformanceOperators())
	outcomes, err := rs.RunTests(member(doc, "tests"), rulecascade.ConformanceOperators())
	if err != nil {
		r.TestError = err.Error()
		return r
	}
	r.Tests = len(outcomes)
	for _, outcome := range outcomes {
		if len(outcome.Failures) > 0 {
			r.Failures = append(r.Failures, testFailure{Name: outcome.Name, Reasons: outcome.Failures})
		}
	}
	return r
}

// check prints the report of each file and returns 1 when any has a problem or a failed test.
func (c *cli) check(paths []string, testsOnly, asJSON bool) int {
	reports := make([]*fileReport, 0, len(paths))
	bad := 0
	for _, path := range paths {
		r := c.checkFile(path)
		bad += r.bad()
		reports = append(reports, r)
	}
	if asJSON {
		c.printJSON(map[string]any{"files": reports, "ok": bad == 0})
	} else {
		for _, r := range reports {
			c.printReport(r)
		}
	}
	if bad > 0 {
		return 1
	}
	return 0
}

func (c *cli) printReport(r *fileReport) {
	if r.ReadError != "" {
		fmt.Fprintf(c.stdout, "%s: LOAD FAILED\n  %s\n", r.Path, r.ReadError)
		return
	}
	if r.ID != "" {
		fmt.Fprintf(c.stdout, "%s@%s  %.19s...  %d rules (%d client-safe), %d params\n", r.ID, r.Version, r.Checksum,
			r.Rules, r.ClientRules, r.Params)
	} else {
		fmt.Fprintf(c.stdout, "%s: LOAD FAILED\n", r.Path)
	}
	for _, p := range r.Problems {
		if p.Rule != "" {
			fmt.Fprintf(c.stdout, "  %s: %s %s\n", p.Code, p.Rule, p.Message)
		} else {
			fmt.Fprintf(c.stdout, "  %s: %s\n", p.Code, p.Message)
		}
	}
	if r.ID == "" {
		return
	}
	if len(r.Missing) > 0 {
		// not a failure of the ruleset: the operators live in the host application
		fmt.Fprintf(c.stdout, "  NOTE  this command does not have the custom operators %s: rules that use them fail closed here; "+
			"run their golden tests in the test suite of the host that supplies them\n", strings.Join(r.Missing, ", "))
	}
	if r.TestError != "" {
		fmt.Fprintf(c.stdout, "  the golden tests could not run: %s\n", r.TestError)
		return
	}
	for _, f := range r.Failures {
		fmt.Fprintf(c.stdout, "  FAIL  %s\n", f.Name)
		for _, why := range f.Reasons {
			fmt.Fprintf(c.stdout, "        %s\n", why)
		}
	}
	fmt.Fprintf(c.stdout, "  %d golden tests, %d failed\n", r.Tests, len(r.Failures))
}

// bindingProblems checks that the ruleset's OpenAPI bindings and the API's x-rule-cascade tags
// agree.
func (c *cli) bindingProblems(doc any, directory string) []rulecascade.Problem {
	var out []rulecascade.Problem
	mismatch := func(format string, args ...any) {
		out = append(out, rulecascade.Problem{Code: "BINDING_MISMATCH", Message: fmt.Sprintf(format, args...)})
	}
	id, ownVersion := text(member(member(doc, "metadata"), "id")), text(member(member(doc, "metadata"), "version"))
	for _, b := range list(member(member(doc, "bindings"), "openapi")) {
		document, entity := text(member(b, "document")), text(member(b, "entity"))
		path := filepath.Join(directory, filepath.FromSlash(document))
		var api any
		var err error
		if c.readable(path) {
			api, _, err = readDocument(path)
		} else {
			err = fmt.Errorf("outside the project")
		}
		if err != nil {
			mismatch("%s not found", document)
			continue
		}
		root := member(api, "x-rule-cascade")
		if wanted, pinned := object(root).Get("version"); member(root, "ruleset") != id {
			mismatch("%s: root x-rule-cascade.ruleset is %q", document, text(member(root, "ruleset")))
		} else if pinned && !rulecascade.VersionSatisfies(ownVersion, text(wanted)) {
			mismatch("%s wants version %s", document, text(wanted))
		}
		operations := map[string]any{}
		paths := object(member(api, "paths"))
		for _, p := range paths.Keys() {
			item := object(member(paths, p))
			for _, method := range item.Keys() {
				if operationID, ok := member(member(item, method), "operationId").(string); ok {
					operations[operationID] = member(item, method)
				}
			}
		}
		bound := object(member(b, "operations"))
		for _, ruleOperation := range bound.Keys() {
			operationID := text(member(bound, ruleOperation))
			operation, ok := operations[operationID]
			if !ok {
				mismatch("operationId %s is not in %s", operationID, document)
			} else if !rulecascade.Equal(member(operation, "x-rule-cascade"), map[string]any{"entity": entity, "operation": ruleOperation}) {
				mismatch("%s: x-rule-cascade does not say %s/%s", operationID, entity, ruleOperation)
			}
		}
		used := map[string]bool{}
		for _, r := range list(member(doc, "rules")) {
			if member(member(r, "target"), "entity") == entity {
				for _, operation := range list(member(r, "operations")) {
					used[text(operation)] = true
				}
			}
		}
		var unbound []string
		for operation := range used {
			if _, ok := bound.Get(operation); !ok {
				unbound = append(unbound, operation)
			}
		}
		sort.Strings(unbound)
		for _, operation := range unbound {
			mismatch("operation '%s' on %s has no bound OpenAPI operation", operation, entity)
		}
	}
	return out
}
