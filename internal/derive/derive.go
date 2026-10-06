// Package derive derives a baseline Rule Cascade ruleset from the constraints of a schema: an
// OpenAPI 3.1 component schema, or a JSON Schema (its root or a schema under $defs/definitions).
//
// It is the Go port of tools/openapi_rules.py, the `derive` subcommand of tools/rulecheck.py, and
// produces the same bytes for the same inputs. Each constraint (`required`, `type`, `enum`,
// `maxLength`, `pattern`, `minimum` ...) becomes a validation rule with a finding code
// GEN-<ENTITY>-NNN, a message and a field pointer; every constraint that is not derived is
// reported with the reason. docs/openapi.md has the table of what is derived and what is not.
package derive

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	rulecascade "rules.sdods.com/go"
)

// Options mirror the flags of `rulecheck derive`.
type Options struct {
	Schema    string   // OpenAPI component schema name (OpenAPI input), --schema
	Pointer   string   // JSON pointer for JSON Schema input, e.g. "/$defs/Order"; "" = root
	ID        string   // ruleset id, required, --id
	Entity    string   // entity name, --entity; default: the schema name (the last pointer segment, or the root's title)
	Scope     []string // "level:id" pairs, --scope; an element may also hold several pairs separated by commas
	Version   string   // --version; default 1.0.0
	Title     string   // --title; default "<entity> - baseline rules derived from the OpenAPI schema"
	TestsFile string   // --tests: a YAML or JSON file with a list of golden tests to attach
	CodesFrom string   // --codes-from: an earlier derivation, whose rules keep their finding codes

	// Output is where the ruleset will be written, as -o. Nothing is written: like the Python,
	// the paths in the ruleset (the entity's schema reference, the header) are relative to its
	// directory. Default: the current directory.
	Output string
	// Document is how the ruleset refers to the schema document. FromFile computes it from the
	// path and Output; FromDocument defaults to ./openapi.yaml (./schema.json for JSON Schema).
	Document string
}

// Result is a derivation.
type Result struct {
	Ruleset []byte   // YAML text, byte-identical to `rulecheck derive` for the same inputs
	Tests   []byte   // always nil: `--tests` attaches the golden tests to Ruleset, no tests file is produced
	Skipped []string // every constraint that was not derived: "skipped <pointer>: <reason>", as the Python reports
}

// DerivationError reports a request that cannot produce a ruleset at all (unknown schema, an id
// that is not a ruleset id ...). A derived ruleset that does not load is reported as an error
// wrapping a *rulecascade.LoadError instead.
type DerivationError struct{ Message string }

func (e *DerivationError) Error() string { return e.Message }

func failf(format string, args ...any) error {
	return &DerivationError{fmt.Sprintf(format, args...)}
}

// FromFile derives a ruleset from a YAML or JSON file: an OpenAPI document when it has an
// "openapi" member (Options.Schema names the component schema), a JSON Schema otherwise
// (Options.Pointer names the schema).
func FromFile(path string, opts Options) (*Result, error) {
	doc, err := readFile(path)
	if err != nil {
		return nil, err
	}
	if opts.Document == "" {
		opts.Document = relative(path, home(opts.Output))
	}
	return FromDocument(doc, opts)
}

// FromDocument derives a ruleset from a parsed document: *rulecascade.Object as
// rulecascade.ParseJSON returns it, or plain Go values (map[string]any, []any, float64 ...); the
// members of a map[string]any are taken in sorted order, so the rule order may differ from the
// order the document was written in.
func FromDocument(doc any, opts Options) (*Result, error) {
	doc, err := normalize(doc)
	if err != nil {
		return nil, err
	}
	object, _ := doc.(*rulecascade.Object)
	openAPI := false
	if object != nil {
		_, openAPI = object.Get("openapi")
	}
	if object == nil {
		return nil, failf("the OpenAPI document must be an object")
	}
	at := home(opts.Output)

	var tests []any
	if opts.TestsFile != "" {
		read, err := readFile(opts.TestsFile)
		if err != nil {
			return nil, err
		}
		if o, ok := read.(*rulecascade.Object); ok {
			read, _ = o.Get("tests")
		}
		if read != nil {
			list, ok := read.([]any)
			if !ok {
				return nil, failf("%s must hold a list of tests", opts.TestsFile)
			}
			tests = list
		}
	}
	var codes *rulecascade.Object
	if opts.CodesFrom != "" {
		earlier, err := readFile(opts.CodesFrom)
		if err != nil {
			return nil, err
		}
		codes = rulecascade.NewObject()
		e, _ := earlier.(*rulecascade.Object)
		if e == nil {
			return nil, failf("%s does not hold a ruleset", opts.CodesFrom)
		}
		rules, _ := e.Get("rules")
		list, _ := rules.([]any)
		for _, r := range list {
			rule, _ := r.(*rulecascade.Object)
			if rule == nil {
				continue
			}
			if finding, ok := rule.Get("finding"); ok {
				id, _ := rule.Get("id")
				name, _ := id.(string)
				code, _ := getPath(finding, "code").(string)
				codes.Set(name, code)
			}
		}
	}
	var scope []any
	for _, element := range opts.Scope {
		for _, step := range strings.Split(element, ",") {
			level, id, _ := strings.Cut(step, ":")
			s := rulecascade.NewObject()
			s.Set("level", level)
			s.Set("id", id)
			scope = append(scope, s)
		}
	}

	r := request{doc: object, rulesetID: opts.ID, entity: opts.Entity, scope: scope, version: opts.Version,
		title: opts.Title, tests: tests, codes: codes, document: opts.Document}
	if r.version == "" {
		r.version = "1.0.0"
	}
	var location string // the schema, as the header names it
	if openAPI {
		if opts.Schema == "" {
			return nil, failf("an OpenAPI document needs the name of a component schema")
		}
		if r.document == "" {
			r.document = "./openapi.yaml"
		}
		r.kind = "OpenAPI"
		location = "/components/schemas/" + opts.Schema
	} else {
		if r.document == "" {
			r.document = "./schema.json"
		}
		r.kind = "JSON Schema"
		r.jsonSchema = true
		location = opts.Pointer
	}
	ruleset, skipped, err := derive(r, opts.Schema, opts.Pointer)
	if err != nil {
		return nil, err
	}
	// never hand over a ruleset that does not load
	loader := func(string) any { return object }
	if r.jsonSchema && opts.Pointer == "" {
		// The entity refers to the root ("file#"). The library's dereference reads "#" as the
		// member "", so the check sees the root there.
		root := rulecascade.NewObject()
		for _, k := range object.Keys() {
			v, _ := object.Get(k)
			root.Set(k, v)
		}
		root.Set("", object)
		loader = func(string) any { return root }
	}
	if _, err := rulecascade.Load(ruleset, map[string]any{}, loader); err != nil {
		var problems *rulecascade.LoadError
		if errors.As(err, &problems) {
			return nil, fmt.Errorf("the derived ruleset does not load: %w", err)
		}
		return nil, err
	}

	header := []string{"Baseline rules derived by `rulecheck derive` from", r.document + "#" + location,
		"Do not edit this file: change the OpenAPI description and derive it again."}
	if r.jsonSchema {
		header[2] = "Do not edit this file: change the JSON Schema and derive it again."
	}
	if opts.TestsFile != "" {
		header = append(header, "The golden tests are attached from "+relative(opts.TestsFile, at)+".")
	}
	result := &Result{Ruleset: []byte(toYAML(ruleset, header))}
	for _, s := range skipped {
		result.Skipped = append(result.Skipped, "skipped "+s.pointer+": "+s.reason)
	}
	return result, nil
}

// home is the directory the paths in a ruleset are relative to: the one of the output file.
func home(output string) string {
	if output == "" {
		wd, _ := os.Getwd()
		return realPath(wd)
	}
	return filepath.Dir(realPath(output))
}

// realPath is the absolute path with symbolic links resolved as far as they exist.
func realPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return filepath.Join(dir, filepath.Base(abs))
	}
	return abs
}

// relative is `path` as a ruleset next to `start` refers to it: POSIX separators, always starting
// with a dot.
func relative(path, start string) string {
	text, err := filepath.Rel(start, realPath(path))
	if err != nil {
		text = realPath(path)
	}
	text = filepath.ToSlash(text)
	if strings.HasPrefix(text, ".") {
		return text
	}
	return "./" + text
}

// ------------------------------------------------------------------ derivation

var (
	operations = []string{"create", "update"}
	triggers   = []string{"blur", "submit"}
	segmentRE  = regexp.MustCompile(`^[A-Za-z0-9_-]+$`) // what a JSON Pointer segment of a ruleset may contain
	nameRE     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
	rulesetRE  = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z0-9][a-z0-9-]*)*$`)
	scopeIDRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	codeRE     = regexp.MustCompile(`^GEN-[A-Z0-9]+-([0-9]+)$`)
)

type typeName struct{ name, typeOf, words string }

// JSON Schema type -> what typeOf returns, how a message names it
var types = []typeName{{"string", "string", "text"}, {"number", "number", "a number"},
	{"integer", "number", "a whole number"}, {"boolean", "boolean", "true or false"}, {"array", "list", "a list"},
	{"object", "object", "an object"}}

func lookupType(name string) (typeName, bool) {
	for _, t := range types {
		if t.name == name {
			return t, true
		}
	}
	return typeName{}, false
}

// format -> message key, portable pattern. The patterns check the shape, not the calendar.
const dateRE = "[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])"

type format struct{ name, message, pattern string }

var formats = []format{
	{"email", "generated.formatEmail", "^[^@ ]+@[^@ ]+\\.[A-Za-z]{2,}$"},
	{"date", "generated.formatDate", "^" + dateRE + "$"},
	{"date-time", "generated.formatDateTime", "^" + dateRE + "T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](\\.[0-9]+)?" +
		"(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$"},
	{"uuid", "generated.formatUuid", "^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$"},
}

type pair struct{ key, text string }

var messages = []pair{
	{"generated.required", "{field} is required."},
	{"generated.type", "{field} must be {expected}."},
	{"generated.enum", "{field} must be one of: {allowed}."},
	{"generated.minLength", "{field} is too short: the minimum length is {min}."},
	{"generated.maxLength", "{field} is too long: the maximum length is {max}."},
	{"generated.pattern", "{field} must match the pattern {pattern}."},
	{"generated.formatEmail", "{field} must be an e-mail address."},
	{"generated.formatDate", "{field} must be a date written as YYYY-MM-DD."},
	{"generated.formatDateTime", "{field} must be a date and time written like 2026-10-03T09:00:00Z."},
	{"generated.formatUuid", "{field} must be a UUID."},
	{"generated.minimum", "{field} must be at least {min}."},
	{"generated.exclusiveMinimum", "{field} must be greater than {min}."},
	{"generated.maximum", "{field} must be at most {max}."},
	{"generated.exclusiveMaximum", "{field} must be less than {max}."},
	{"generated.multipleOf", "{field} must be a multiple of {factor}."},
	{"generated.minItems", "{field} has too few entries: the minimum is {min}."},
	{"generated.maxItems", "{field} has too many entries: the maximum is {max}."},
	{"generated.itemsType", "Every entry of {field} must be {expected}."},
}

// Constraints that are recognised and not derived, with the reason that is reported.
const (
	composition = "schema composition is not derived; write the rule by hand"
	objectShape = "a rule names the fields it checks and cannot enumerate the properties of an object"
)

var notDerived = []pair{
	{"const", "not derived; write a rule with eq"},
	{"uniqueItems", "Rule Cascade has no operator that compares the entries of a list with each other"},
	{"contains", "not derived; write a rule with some"}, {"minContains", "not derived"}, {"maxContains", "not derived"},
	{"prefixItems", "positional entries are not derived"},
	{"additionalProperties", objectShape}, {"unevaluatedProperties", objectShape}, {"patternProperties", objectShape},
	{"propertyNames", objectShape}, {"minProperties", objectShape}, {"maxProperties", objectShape},
	{"dependentRequired", "not derived; write a required rule with a when guard"},
	{"dependentSchemas", composition}, {"allOf", composition}, {"anyOf", composition}, {"oneOf", composition},
	{"not", composition}, {"if", composition}, {"then", composition}, {"else", composition},
}

func isNotDerived(key string) bool {
	for _, p := range notDerived {
		if p.key == key {
			return true
		}
	}
	return false
}

// Constraints on the value itself: reported when they sit on the entries of a list of scalars or
// next to a $ref.
var valueConstraints = map[string]bool{"enum": true, "minLength": true, "maxLength": true, "pattern": true,
	"format": true, "minimum": true, "exclusiveMinimum": true, "maximum": true, "exclusiveMaximum": true,
	"multipleOf": true, "minItems": true, "maxItems": true, "items": true, "properties": true, "required": true}

// ------------------------------------------------------------------ helpers

func obj(kv ...any) *rulecascade.Object {
	o := rulecascade.NewObject()
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

func call(op string, args ...any) *rulecascade.Object {
	return obj("op", op, "args", append([]any{}, args...))
}

func strings2any(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func get(v any, key string) (any, bool) {
	if o, ok := v.(*rulecascade.Object); ok {
		return o.Get(key)
	}
	return nil, false
}

func getPath(v any, key string) any { x, _ := get(v, key); return x }

func has(v any, key string) bool { _, ok := get(v, key); return ok }

func isNumber(v any) bool { _, ok := v.(json.Number); return ok }

// isCount reports a non-negative whole number.
func isCount(v any) bool {
	n, ok := v.(json.Number)
	return ok && isWhole(n) && !strings.HasPrefix(wholeText(n), "-")
}

// truthy is Python's truth value.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		return floatValue(x) != 0
	case []any:
		return len(x) > 0
	case *rulecascade.Object:
		return x.Len() > 0
	}
	return true
}

func count(n json.Number, noun string) string {
	text := wholeText(n)
	if text == "1" {
		return text + " " + noun
	}
	if strings.HasSuffix(noun, "y") {
		return text + " " + noun[:len(noun)-1] + "ies"
	}
	return text + " " + noun + "s"
}

// escape writes a property name as a JSON Pointer segment (RFC 6901).
func escape(segment string) string {
	return strings.ReplaceAll(strings.ReplaceAll(segment, "~", "~0"), "/", "~1")
}

var (
	lowerUpper = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	upperRun   = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
	separators = regexp.MustCompile(`[^A-Za-z0-9]+`)
)

// words are the words of a property name: fullName, full_name and full-name are all [full name].
func words(name string) []string {
	spaced := lowerUpper.ReplaceAllString(name, "${1} ${2}")
	spaced = upperRun.ReplaceAllString(spaced, "${1} ${2}")
	var out []string
	for _, w := range separators.Split(strings.ToLower(spaced), -1) {
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

func kebab(name string) string { return strings.Join(words(name), "-") }

// label is how messages and titles name a field: (beneficiary, swiftCode) is "Beneficiary swift code".
func label(names []string) string {
	var all []string
	for _, n := range names {
		all = append(all, words(n)...)
	}
	text := strings.Join(all, " ")
	if text == "" {
		return text
	}
	return strings.ToUpper(text[:1]) + text[1:]
}

// patternProblem is pattern_problem of the reference: "" when the pattern is in the portable
// subset of specification 4.4, otherwise why not. It asks the library, whose `matches` operator
// refuses a pattern outside the subset with the scanner's reason.
func patternProblem(pattern string) string {
	_, err := rulecascade.EvaluateExpression(call("matches", "", pattern), map[string]any{}, nil, nil)
	if err == nil {
		return ""
	}
	message := err.Error()
	prefix := fmt.Sprintf("pattern %q: ", pattern)
	if i := strings.Index(message, prefix); i >= 0 {
		return message[i+len(prefix):]
	}
	return message
}

// ------------------------------------------------------------------ the walk

// place is where a value sits: under the entity, or under an element of the list at forEach.
type place struct {
	forEach         string
	segments, names []string
}

func (p place) child(name string) place {
	return place{p.forEach, append(append([]string{}, p.segments...), name), append(append([]string{}, p.names...), name)}
}

// elements is the place of each element of the list that sits here.
func (p place) elements() place { return place{p.pointer(), nil, p.names} }

func (p place) pointer() string { return "/" + strings.Join(p.segments, "/") }

func (p place) root() string {
	if p.forEach != "" {
		return "item"
	}
	return "data"
}

func (p place) variable() *rulecascade.Object {
	return obj("var", strings.Join(append([]string{p.root()}, p.segments...), "."))
}

// parent is the object that holds this value, when it is not the entity itself.
func (p place) parent() *rulecascade.Object {
	if len(p.segments) > 1 || p.forEach != "" {
		return obj("var", strings.Join(append([]string{p.root()}, p.segments[:len(p.segments)-1]...), "."))
	}
	return nil
}

type skip struct{ pointer, reason string }

type derivation struct {
	document   *rulecascade.Object
	entity     string
	jsonSchema bool
	rules      []*rulecascade.Object
	skipped    []skip
	origin     map[string]string
	walking    []string
}

func (d *derivation) skip(pointer, reason string) {
	d.skipped = append(d.skipped, skip{pointer, reason})
}

func (d *derivation) isWalking(pointer string) bool {
	for _, w := range d.walking {
		if w == pointer {
			return true
		}
	}
	return false
}

// resolve follows local $refs. It returns the schema and its pointer, or nil after reporting why not.
func (d *derivation) resolve(schema any, pointer string) (*rulecascade.Object, string) {
	for {
		o, isObject := schema.(*rulecascade.Object)
		if !isObject || !has(o, "$ref") {
			break
		}
		ref, _ := o.Get("$ref")
		for _, key := range o.Keys() {
			if valueConstraints[key] || isNotDerived(key) || key == "type" {
				d.skip(pointer+"/"+key, "a constraint next to $ref is not derived; move it into the referenced schema")
			}
		}
		text, isString := ref.(string)
		root := d.jsonSchema && isString && text == "#"
		if !root && (!isString || !strings.HasPrefix(text, "#/")) {
			d.skip(pointer+"/$ref", pyStr(ref)+" is in another document; only local references are followed")
			return nil, ""
		}
		target := text[1:]
		if d.isWalking(target) {
			d.skip(pointer+"/$ref", text+" refers back to a schema that contains it; recursion is not derived")
			return nil, ""
		}
		var node any = d.document
		if !root {
			for _, part := range strings.Split(target[1:], "/") {
				var ok bool
				if node, ok = get(node, strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")); !ok {
					d.skip(pointer+"/$ref", text+" does not resolve")
					return nil, ""
				}
			}
		}
		schema, pointer = node, target
	}
	o, isObject := schema.(*rulecascade.Object)
	if !isObject {
		if schema == false {
			d.skip(pointer, "a false schema (the property must be absent) is not derived")
		}
		return nil, ""
	}
	return o, pointer
}

type arg struct {
	key   string
	value any
}

// rule records one validation rule. `source` is the JSON Pointer of the constraint it comes from.
func (d *derivation) rule(p place, constraint, source, title string, when, check any, message string, args ...arg) {
	parts := []string{kebab(d.entity)}
	for _, n := range p.names {
		parts = append(parts, kebab(n))
	}
	id := strings.Join(append(parts, constraint), ".")
	if earlier, taken := d.origin[id]; taken {
		d.skip(source, "its rule id "+id+" is already taken by "+earlier+"; two property names differ only in case or punctuation")
		return
	}
	d.origin[id] = source
	rule := obj("id", id, "kind", "validation", "title", strings.Replace(title, "{}", label(p.names), 1),
		"target", obj("entity", d.entity, "field", p.pointer()),
		"operations", strings2any(operations), "triggers", strings2any(triggers))
	if p.forEach != "" {
		rule.Set("forEach", p.forEach)
	}
	if when != nil {
		rule.Set("when", when)
	}
	findingArgs := obj("field", label(p.segments))
	for _, a := range args {
		findingArgs.Set(a.key, a.value)
	}
	rule.Set("assert", check)
	rule.Set("severity", "error")
	rule.Set("finding", obj("code", nil, "message", message, "args", findingArgs))
	rule.Set("x-generated-from", source)
	d.rules = append(d.rules, rule)
}

// typeCheck returns the expression that is true when `value` has one of the schema's types, and
// how a message names them.
func (d *derivation) typeCheck(value any, schema *rulecascade.Object, pointer string) (any, string) {
	declared, _ := schema.Get("type")
	all, isList := declared.([]any)
	if !isList {
		all = []any{declared}
	}
	var names []any
	for _, n := range all {
		if n != "null" { // the rule is guarded by exists, so null never reaches it
			names = append(names, n)
		}
	}
	var known []typeName
	for _, n := range names {
		text, ok := n.(string)
		t, found := lookupType(text)
		if !ok || !found {
			d.skip(pointer+"/type", pyRepr(declared)+" is not a JSON Schema type")
			return nil, ""
		}
		known = append(known, t)
	}
	if len(known) == 0 {
		return nil, ""
	}
	var checks []any
	var expected []string
	for _, t := range known {
		check := call("eq", call("typeOf", value), t.typeOf)
		if t.name == "integer" {
			check = call("and", check, call("eq", call("mod", value, json.Number("1")), json.Number("0")))
		}
		checks = append(checks, check)
		expected = append(expected, t.words)
	}
	if len(checks) == 1 {
		return checks[0], expected[0]
	}
	return call("or", checks...), strings.Join(expected, " or ")
}

func properties(schema *rulecascade.Object) (*rulecascade.Object, bool) {
	p, ok := getPath(schema, "properties").(*rulecascade.Object)
	return p, ok
}

func required(schema *rulecascade.Object) ([]any, bool) {
	r, ok := getPath(schema, "required").([]any)
	return r, ok
}

func indexOf(list []any, name string) int {
	for i, v := range list {
		if v == name {
			return i
		}
	}
	return -1
}

// object derives the rules for the properties of an object schema.
func (d *derivation) object(schema *rulecascade.Object, pointer string, p place) {
	props, _ := properties(schema)
	if props == nil {
		props = rulecascade.NewObject()
	}
	req, _ := required(schema)
	d.walking = append(d.walking, pointer)
	for _, name := range props.Keys() {
		raw, _ := props.Get(name)
		here := pointer + "/properties/" + escape(name)
		if !segmentRE.MatchString(name) || kebab(name) == "" {
			d.skip(here, "the property name "+pyRepr(name)+" cannot be written in a ruleset path (letters, digits, _ and - only)")
			continue
		}
		sub, at := d.resolve(raw, here)
		if sub == nil {
			continue
		}
		child := p.child(name)
		if i := indexOf(req, name); i >= 0 {
			source := pointer + "/required/" + strconv.Itoa(i)
			kinds, isList := getPath(sub, "type").([]any)
			if !isList {
				kinds = []any{getPath(sub, "type")}
			}
			if truthy(getPath(sub, "readOnly")) || truthy(getPath(raw, "readOnly")) {
				d.skip(source, name+" is readOnly: the server assigns it, so a request need not carry it")
			} else if indexOf(kinds, "null") >= 0 {
				d.skip(source, name+" may be null, and a rule cannot tell a null property from an absent one")
			} else {
				var inside any
				if parent := child.parent(); parent != nil {
					inside = call("eq", call("typeOf", parent), "object")
				}
				d.rule(child, "required", source, "{} is required", inside, call("exists", child.variable()),
					"generated.required")
			}
		}
		d.value(sub, at, child)
	}
	for i, name := range req {
		if text, ok := name.(string); !ok || !has(props, text) {
			d.skip(pointer+"/required/"+strconv.Itoa(i), pyStr(name)+" is required but not declared under properties, so "+
				"the entity schema does not know the path")
		}
	}
	d.walking = d.walking[:len(d.walking)-1]
}

func (d *derivation) notDerived(schema *rulecascade.Object, pointer string) {
	for _, nd := range notDerived {
		if v, ok := schema.Get(nd.key); ok && !(nd.key == "additionalProperties" && v == true) {
			d.skip(pointer+"/"+nd.key, nd.text)
		}
	}
}

// value derives the rules for the constraints a schema puts on one value.
func (d *derivation) value(schema *rulecascade.Object, pointer string, p place) {
	value := p.variable()
	present := call("exists", value)
	typeIs := func(kind string) any { return call("eq", call("typeOf", value), kind) }
	text, number, items := typeIs("string"), typeIs("number"), typeIs("list")
	source := func(keyword string) string { return pointer + "/" + keyword }

	if has(schema, "type") {
		if check, expected := d.typeCheck(value, schema, pointer); check != nil {
			d.rule(p, "type", source("type"), "{} is "+expected, present, check, "generated.type", arg{"expected", expected})
		}
	}
	if enum, ok := schema.Get("enum"); ok {
		var allowed []any
		list, _ := enum.([]any)
		structured := false
		for _, v := range list {
			if v != nil {
				allowed = append(allowed, v)
				structured = structured || isContainer(v)
			}
		}
		if len(allowed) == 0 || structured {
			d.skip(source("enum"), "only an enum of strings, numbers and booleans is derived")
		} else {
			d.rule(p, "enum", source("enum"), "{} is one of the allowed values", present,
				call("in", value, call("list", allowed...)), "generated.enum", arg{"allowed", call("list", allowed...)})
		}
	}
	for _, c := range []struct{ keyword, op, title string }{{"minLength", "gte", "{} has at least"},
		{"maxLength", "lte", "{} has at most"}} {
		if bound, ok := schema.Get(c.keyword); ok {
			if !isCount(bound) {
				d.skip(source(c.keyword), "the value is not a non-negative whole number")
				continue
			}
			d.rule(p, kebab(c.keyword), source(c.keyword), c.title+" "+count(bound.(json.Number), "character"), text,
				call(c.op, call("len", value), bound), "generated."+c.keyword, arg{c.keyword[:3], bound})
		}
	}
	if pattern, ok := schema.Get("pattern"); ok {
		why := "it is not a string"
		if s, isString := pattern.(string); isString {
			why = patternProblem(s)
		}
		if why != "" {
			d.skip(source("pattern"), "not a portable pattern (specification 4.4): "+why)
		} else {
			d.rule(p, "pattern", source("pattern"), "{} matches its pattern", text, call("matches", value, pattern),
				"generated.pattern", arg{"pattern", pattern})
		}
	}
	if name, ok := schema.Get("format"); ok {
		var found *format
		if s, isString := name.(string); isString {
			for i := range formats {
				if formats[i].name == s {
					found = &formats[i]
				}
			}
		}
		if found == nil {
			names := make([]string, len(formats))
			for i, f := range formats {
				names[i] = f.name
			}
			d.skip(source("format"), "format "+pyRepr(name)+" is not derived (derived: "+strings.Join(names, ", ")+")")
		} else {
			d.rule(p, "format", source("format"), "{} has the format "+found.name, text,
				call("matches", value, found.pattern), found.message)
		}
	}
	for _, c := range []struct{ keyword, op, title, arg string }{
		{"minimum", "gte", "{} is at least", "min"}, {"exclusiveMinimum", "gt", "{} is greater than", "min"},
		{"maximum", "lte", "{} is at most", "max"}, {"exclusiveMaximum", "lt", "{} is less than", "max"},
		{"multipleOf", "", "{} is a multiple of", "factor"}} {
		bound, ok := schema.Get(c.keyword)
		if !ok {
			continue
		}
		n, isNum := bound.(json.Number)
		switch {
		case bound == true || bound == false:
			d.skip(source(c.keyword), "the boolean form of OpenAPI 3.0 is not derived; OpenAPI 3.1 gives the bound as a number")
		case !isNum || (c.op == "" && !positive(n)):
			if c.op != "" {
				d.skip(source(c.keyword), "the value is not a number")
			} else {
				d.skip(source(c.keyword), "the value is not a positive number")
			}
		case len(strings.Trim(strings.ReplaceAll(strings.TrimLeft(canonNum(n), "-"), ".", ""), "0")) > 15:
			d.skip(source(c.keyword), "the value has more than 15 significant digits (specification 4.2)")
		default:
			var check any
			if c.op != "" {
				check = call(c.op, value, bound)
			} else {
				check = call("eq", call("mod", value, bound), json.Number("0"))
			}
			d.rule(p, kebab(c.keyword), source(c.keyword), c.title+" "+canonNum(n), number, check,
				"generated."+c.keyword, arg{c.arg, bound})
		}
	}
	for _, c := range []struct{ keyword, op, title string }{{"minItems", "gte", "{} has at least"},
		{"maxItems", "lte", "{} has at most"}} {
		if bound, ok := schema.Get(c.keyword); ok {
			if !isCount(bound) {
				d.skip(source(c.keyword), "the value is not a non-negative whole number")
				continue
			}
			d.rule(p, kebab(c.keyword), source(c.keyword), c.title+" "+count(bound.(json.Number), "entry"), items,
				call(c.op, call("len", value), bound), "generated."+c.keyword, arg{c.keyword[:3], bound})
		}
	}
	d.notDerived(schema, pointer)
	if raw, ok := schema.Get("items"); ok {
		d.elements(raw, source("items"), p, items)
	}
	if _, ok := properties(schema); ok {
		d.object(schema, pointer, p)
	} else if _, ok := required(schema); ok {
		d.object(schema, pointer, p)
	}
}

func positive(n json.Number) bool {
	if isWhole(n) {
		whole, ok := new(big.Int).SetString(strings.TrimPrefix(string(n), "+"), 10)
		return ok && whole.Sign() > 0
	}
	return floatValue(n) > 0
}

// elements derives the rules for the elements of the list at `p`.
func (d *derivation) elements(raw any, pointer string, p place, isList any) {
	schema, at := d.resolve(raw, pointer)
	if schema == nil {
		return
	}
	if has(schema, "type") {
		if check, expected := d.typeCheck(obj("var", "item"), schema, at); check != nil {
			d.rule(p, "items-type", at+"/type", "Every entry of {} is "+expected, isList, call("all", p.variable(), check),
				"generated.itemsType", arg{"expected", expected})
		}
	}
	_, hasProperties := properties(schema)
	_, hasRequired := required(schema)
	if hasProperties || hasRequired {
		if p.forEach != "" {
			d.skip(at, "a list of objects inside the entries of a list is not derived: forEach reaches one level")
			return
		}
		d.notDerived(schema, at)
		d.object(schema, at, p.elements())
		return
	}
	for _, key := range schema.Keys() {
		if valueConstraints[key] || isNotDerived(key) {
			d.skip(at+"/"+key, "apart from their type, constraints on list entries that are not objects are not derived; "+
				"write a rule with all")
		}
	}
}

// ------------------------------------------------------------------ the ruleset

type request struct {
	doc        *rulecascade.Object
	kind       string // "OpenAPI" or "JSON Schema", as messages name the document
	jsonSchema bool
	rulesetID  string
	entity     string
	scope      []any
	version    string
	title      string
	tests      []any
	codes      *rulecascade.Object // rule id -> finding code of an earlier derivation
	document   string              // the file part of the entity's schema reference
}

// derive is derive_with_report of the reference. For an OpenAPI document `schemaName` names the
// component schema; for a JSON Schema `pointer` names the schema ("" is the root).
func derive(r request, schemaName, pointer string) (*rulecascade.Object, []skip, error) {
	entity := r.entity
	var raw any
	if r.jsonSchema {
		if pointer != "" && !strings.HasPrefix(pointer, "/") {
			return nil, nil, failf("%s is not a JSON pointer (it starts with /, e.g. /$defs/Order)", pyRepr(pointer))
		}
		var node any = r.doc
		if pointer != "" {
			parts := strings.Split(pointer[1:], "/")
			for _, part := range parts {
				var ok bool
				if node, ok = get(node, strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")); !ok {
					return nil, nil, failf("#%s is not in the JSON Schema document", pointer)
				}
			}
			if entity == "" {
				last := parts[len(parts)-1]
				entity = strings.ReplaceAll(strings.ReplaceAll(last, "~1", "/"), "~0", "~")
			}
		} else if entity == "" {
			title, _ := r.doc.Get("title")
			entity, _ = title.(string)
			if !nameRE.MatchString(entity) {
				return nil, nil, failf("the root schema has no title that is an entity name; pass the entity name")
			}
		}
		raw = node
	} else if entity == "" {
		entity = schemaName
	}
	if !rulesetRE.MatchString(r.rulesetID) {
		return nil, nil, failf("%s is not a ruleset id (lower-case, dot-separated: acme.generated.customer)", pyRepr(r.rulesetID))
	}
	if !nameRE.MatchString(entity) {
		return nil, nil, failf("%s is not an entity name (letters, digits and _); pass entity=", pyRepr(entity))
	}
	scope := r.scope
	if len(scope) == 0 {
		scope = []any{obj("level", "organization", "id", strings.Split(r.rulesetID, ".")[0])}
	}
	for _, step := range scope {
		level, _ := getPath(step, "level").(string)
		id, _ := getPath(step, "id").(string)
		if !nameRE.MatchString(level) || !scopeIDRE.MatchString(id) {
			return nil, nil, failf("%s is not a scope step: level is a name, id is lower-case kebab-case", pyRepr(step))
		}
	}
	if !r.jsonSchema {
		schemas, _ := getPath(getPath(r.doc, "components"), "schemas").(*rulecascade.Object)
		var ok bool
		if schemas != nil {
			raw, ok = schemas.Get(schemaName)
		}
		if !ok {
			return nil, nil, failf("#/components/schemas/%s is not in the OpenAPI document", schemaName)
		}
		pointer = "/components/schemas/" + escape(schemaName)
	}
	d := &derivation{document: r.doc, entity: entity, jsonSchema: r.jsonSchema, origin: map[string]string{}}
	schema, at := d.resolve(raw, pointer)
	if schema == nil || !(getPath(schema, "type") == "object" || isObject(getPath(schema, "properties"))) {
		return nil, nil, failf("#%s is not an object schema with properties of its own", pointer)
	}
	d.notDerived(schema, at)
	d.object(schema, at, place{})

	// finding codes: GEN-<ENTITY>-NNN in document order; a rule that had a code keeps it
	prefix := "GEN-" + regexp.MustCompile(`[^A-Z0-9]`).ReplaceAllString(strings.ToUpper(entity), "") + "-"
	codes := map[string]string{}
	number := 0
	if r.codes != nil {
		for _, id := range r.codes.Keys() {
			v, _ := r.codes.Get(id)
			code, _ := v.(string)
			m := codeRE.FindStringSubmatch(code)
			if strings.HasPrefix(code, prefix) && m != nil {
				codes[id] = code
				if n, err := strconv.Atoi(m[1]); err == nil && n > number {
					number = n
				}
			}
		}
	}
	used := map[string]bool{}
	rules := make([]any, len(d.rules))
	for i, rule := range d.rules {
		id, _ := rule.Get("id")
		if _, ok := codes[id.(string)]; !ok {
			number++
			codes[id.(string)] = fmt.Sprintf("%s%03d", prefix, number)
		}
		finding, _ := rule.Get("finding")
		finding.(*rulecascade.Object).Set("code", codes[id.(string)])
		message, _ := finding.(*rulecascade.Object).Get("message")
		used[message.(string)] = true
		rules[i] = rule
	}
	en := rulecascade.NewObject()
	for _, m := range messages {
		if used[m.key] {
			en.Set(m.key, m.text)
		}
	}
	title := r.title
	if title == "" {
		title = entity + " - baseline rules derived from the " + r.kind + " schema"
		if r.jsonSchema {
			title = entity + " - baseline rules derived from the JSON Schema"
		}
	}
	ruleset := obj(
		"ruleCascade", "1.0.0",
		"kind", "RuleSet",
		"metadata", obj("id", r.rulesetID, "version", r.version, "title", title),
		"scope", scope,
		"entities", obj(entity, obj("schema", obj("$ref", r.document+"#"+pointer))),
		"rules", rules,
		"messages", obj("en", en),
	)
	if len(r.tests) > 0 {
		ruleset.Set("tests", r.tests)
	}
	return ruleset, d.skipped, nil
}

func isObject(v any) bool { _, ok := v.(*rulecascade.Object); return ok }
