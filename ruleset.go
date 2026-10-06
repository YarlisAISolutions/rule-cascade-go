package rulecascade

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Loading: schema validation, inheritance, load-time checks, checksum, manifests (specification
// sections 5 to 7). It is a port of ruleset.py of the reference implementation.

var (
	severityRank = map[string]int{"info": 0, "warning": 1, "error": 2}
	viewKeys     = []string{"page", "screen", "section", "component"}
	placeholder  = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)
)

// Problem is one reason a ruleset or a bundle was refused.
type Problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Rule    string `json:"rule,omitempty"` // the rule the problem is about, when there is one
}

// LoadError says that a ruleset must not be served.
type LoadError struct{ Problems []Problem }

func (e *LoadError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.Code + ": " + p.Message
	}
	return strings.Join(parts, "; ")
}

// Codes returns the code of every problem, in order.
func (e *LoadError) Codes() []string {
	codes := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		codes[i] = p.Code
	}
	return codes
}

func loadError(code, rule, format string, args ...any) *LoadError {
	return &LoadError{[]Problem{{Code: code, Message: fmt.Sprintf(format, args...), Rule: rule}}}
}

// SchemaLoader returns the parsed schema document an entity refers to, given the part of its $ref
// before the '#', or nil when there is no such document.
type SchemaLoader func(file string) any

// ------------------------------------------------------------------ resolve

func versionNumbers(v string) []string {
	v, _, _ = strings.Cut(v, "-")
	return strings.Split(v, ".")
}

// compareNumbers compares two runs of decimal digits as numbers of any size.
func compareNumbers(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return compareInts(len(a), len(b))
	}
	return strings.Compare(a, b)
}

// VersionSatisfies reports whether a version satisfies a range as `extends` writes it: "1.2.0"
// matches exactly, "^1.2.0" matches the same major version at 1.2.0 or later.
func VersionSatisfies(actual, wanted string) bool {
	a, w := versionNumbers(actual), versionNumbers(strings.TrimPrefix(wanted, "^"))
	order := compareInts(len(a), len(w))
	for i := 0; i < len(a) && i < len(w); i++ {
		if c := compareNumbers(a[i], w[i]); c != 0 {
			order = c
			break
		}
	}
	if strings.HasPrefix(wanted, "^") {
		return compareNumbers(a[0], w[0]) == 0 && order >= 0
	}
	return order == 0
}

func typeOK(kind string, value any) bool {
	listOf := func(test func(any) bool) bool {
		list, ok := value.([]any)
		for _, item := range list {
			ok = ok && test(item)
		}
		return ok
	}
	isString := func(v any) bool { _, ok := v.(string); return ok }
	switch kind {
	case "string":
		return isString(value)
	case "number":
		return isNum(value)
	case "integer":
		return isNum(value) && dec(value).isWhole()
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "stringList":
		return listOf(isString)
	case "numberList":
		return listOf(isNum)
	}
	return false
}

// loading holds what one call to Load reads from its caller: the registry and the entity schemas.
type loading struct {
	registry map[string]any
	loader   SchemaLoader
}

// resolve applies `extends` and `overrides`. It returns the resolved ruleset: the object the
// checksum is computed over, plus the checksum.
func (l *loading) resolve(doc any, seen []string) (*Object, error) {
	if problems := schemaProblems(doc); len(problems) > 0 {
		return nil, &LoadError{problems}
	}
	d := asObj(doc)
	me := d.obj("metadata").str("id")
	for _, id := range seen {
		if id == me {
			return nil, loadError("EXTENDS_CYCLE", "", "extends cycle through %s", me)
		}
	}
	origin := me + "@" + d.obj("metadata").str("version")

	params, messages, entities := NewObject(), NewObject(), NewObject()
	types, functions, operators := NewObject(), NewObject(), NewObject()
	var rules []any
	for _, x := range d.list("extends") {
		ext := asObj(x)
		name := ext.str("ruleset")
		parentDoc, err := normalize(l.registry[name])
		if err != nil {
			return nil, err
		}
		if parentDoc == nil {
			return nil, loadError("EXTENDS_NOT_FOUND", "", "%s: parent %s not found", me, name)
		}
		if problems := schemaProblems(parentDoc); len(problems) > 0 {
			return nil, &LoadError{problems}
		}
		parentVersion := asObj(parentDoc).obj("metadata").str("version")
		if !VersionSatisfies(parentVersion, ext.str("version")) {
			return nil, loadError("EXTENDS_VERSION_MISMATCH", "", "%s: parent %s is %s, need %s",
				me, name, parentVersion, ext.str("version"))
		}
		parent, err := l.resolve(parentDoc, append(seen[:len(seen):len(seen)], me))
		if err != nil {
			return nil, err
		}
		if pin, ok := ext.Get("checksum"); ok && pin != parent.get("checksum") {
			return nil, loadError("EXTENDS_CHECKSUM_MISMATCH", "", "%s: parent %s checksum differs", me, name)
		}
		parentScope, scope := asObj(parentDoc).list("scope"), d.list("scope")
		if len(scope) <= len(parentScope) || !equal(scope[:len(parentScope)], parentScope) {
			return nil, loadError("SCOPE_NOT_NARROWER", "", "%s: scope must extend the scope of %s", me, name)
		}
		inherit := func(key string) *Object { return asObj(clone(parent.get(key))) }
		params, messages, entities = inherit("params"), inherit("messages"), inherit("entities")
		types, functions, operators = inherit("types"), inherit("functions"), inherit("operators")
		rules, _ = clone(parent.get("rules")).([]any)
	}

	for _, name := range d.obj("params").names() {
		p := d.obj("params").obj(name)
		if params.has(name) {
			return nil, loadError("PARAM_REDEFINED", "", "%s: param %s is inherited; use overrides.params", me, name)
		}
		if !typeOK(p.str("type"), p.get("default")) {
			return nil, loadError("PARAM_TYPE_MISMATCH", "", "%s: default of param %s is not a %s", me, name, p.str("type"))
		}
		declared := p.copy()
		declared.Set("value", p.get("default"))
		declared.Set("origin", origin)
		params.Set(name, declared)
	}

	// Entities: a child may add field-type bindings but never change an inherited one,
	// otherwise it could quietly detach a field from the type rules of a parent.
	for _, name := range d.obj("entities").names() {
		ent := d.obj("entities").obj(name)
		inherited := entities.obj(name).obj("fieldTypes")
		fieldTypes := inherited.copy()
		for _, pointer := range ent.obj("fieldTypes").names() {
			typeName := ent.obj("fieldTypes").get(pointer)
			if bound, ok := inherited.Get(pointer); ok && bound != typeName {
				return nil, loadError("FIELD_TYPE_REBOUND", "", "%s: %s%s is bound to %v by a parent", me, name, pointer, bound)
			}
			fieldTypes.Set(pointer, typeName)
		}
		merged := entities.obj(name).copy()
		merged.update(ent)
		if fieldTypes.Len() > 0 {
			merged.Set("fieldTypes", fieldTypes)
		}
		entities.Set(name, merged)
	}
	types.update(d.obj("types"))
	operators.update(d.obj("operators"))
	for _, name := range d.obj("functions").names() {
		if functions.has(name) {
			// a parent's rules depend on the meaning of the parent's functions
			return nil, loadError("FUNCTION_REDEFINED", "", "%s: function %s is inherited and cannot be redefined", me, name)
		}
		fn := d.obj("functions").obj(name).copy()
		fn.Set("origin", origin)
		functions.Set(name, fn)
	}
	d.obj("messages").each(func(locale string, catalog any) {
		if !messages.has(locale) {
			messages.Set(locale, NewObject())
		}
		messages.obj(locale).update(asObj(catalog))
	})

	overrides := d.obj("overrides")
	for _, name := range overrides.obj("params").names() {
		value := overrides.obj("params").get(name)
		p := params.obj(name)
		if p == nil {
			return nil, loadError("PARAM_UNKNOWN", "", "%s: override of unknown param %s", me, name)
		}
		policy := p.str("overridePolicy")
		if policy == "locked" {
			return nil, loadError("PARAM_LOCKED", "", "%s: param %s is locked by %s", me, name, p.str("origin"))
		}
		if !typeOK(p.str("type"), value) {
			return nil, loadError("PARAM_TYPE_MISMATCH", "", "%s: override of param %s is not a %s", me, name, p.str("type"))
		}
		if policy == "tighten-only" && isNum(value) && isNum(p.get("value")) {
			direction := p.str("tightenDirection")
			if c := dec(value).cmp(dec(p.get("value"))); (direction == "lower" && c > 0) || (direction != "lower" && c < 0) {
				return nil, loadError("PARAM_LOOSENED", "", "%s: param %s may only move %s", me, name, direction)
			}
		}
		p.Set("value", value)
	}

	byID := map[string]*Object{}
	for _, r := range rules {
		byID[asObj(r).str("id")] = asObj(r)
	}
	for _, x := range overrides.list("rules") {
		o := asObj(x)
		r := byID[o.str("rule")]
		if r == nil {
			return nil, loadError("RULE_UNKNOWN", "", "%s: override of unknown rule %s", me, o.str("rule"))
		}
		id := r.str("id")
		policy := r.str("overridePolicy")
		if policy == "locked" {
			return nil, loadError("RULE_LOCKED", id, "%s: rule %s is locked by %s", me, id, r.str("origin"))
		}
		change := o.obj("set")
		if policy == "tighten-only" || policy == "" {
			lower, hasLower := severityRank[change.str("severity")]
			current, hasCurrent := severityRank[r.str("severity")]
			loosened := change.get("enabled") == false ||
				(hasLower && hasCurrent && lower < current) ||
				(truthy(change.obj("acceptance").get("allowed")) && !truthy(r.obj("acceptance").get("allowed"))) ||
				(change.str("acknowledgement") == "none" && r.str("acknowledgement") == "required")
			if loosened {
				return nil, loadError("RULE_LOOSENED", id, "%s: rule %s is tighten-only", me, id)
			}
		}
		r.update(change)
		r.Set("overriddenBy", origin)
	}

	for _, x := range d.list("rules") {
		id := asObj(x).str("id")
		if byID[id] != nil {
			return nil, loadError("RULE_DUPLICATE", id, "%s: duplicate rule id %s", me, id)
		}
		r := asObj(x).copy()
		r.Set("origin", origin)
		byID[id] = r
		rules = append(rules, r)
	}
	if rules == nil {
		rules = []any{}
	}

	resolved := NewObject()
	resolved.Set("ruleCascade", d.get("ruleCascade"))
	resolved.Set("id", me)
	resolved.Set("version", d.obj("metadata").get("version"))
	resolved.Set("scope", d.get("scope"))
	resolved.Set("conflictPolicy", orDefault(d, "conflictPolicy", "fail"))
	resolved.Set("defaultLocale", orDefault(d, "defaultLocale", "en"))
	resolved.Set("entities", entities)
	resolved.Set("types", types)
	resolved.Set("params", params)
	resolved.Set("functions", functions)
	resolved.Set("operators", operators)
	resolved.Set("rules", rules)
	resolved.Set("messages", messages)
	sum := sha256.Sum256([]byte(canonical(resolved)))
	resolved.Set("checksum", "sha256:"+hex.EncodeToString(sum[:]))
	return resolved, nil
}

func orDefault(o *Object, key string, fallback any) any {
	if v, ok := o.Get(key); ok {
		return v
	}
	return fallback
}

// ------------------------------------------------------------------ static checks

var collectionOps = map[string]bool{"all": true, "some": true, "none": true, "sum": true, "map": true, "filter": true}

// walkExprs visits an expression and everything nested in its args. bound is true where `item` is
// bound by an enclosing collection operator.
func walkExprs(node any, bound bool, visit func(node *Object, bound bool)) {
	o := asObj(node)
	if o == nil {
		return
	}
	visit(o, bound)
	for i, a := range o.list("args") {
		walkExprs(a, bound || (i == 1 && collectionOps[o.str("op")]), visit)
	}
}

// ruleExprs lists every expression a rule holds.
func ruleExprs(rule *Object) []any {
	var out []any
	values := func(o *Object) { o.each(func(_ string, v any) { out = append(out, v) }) }
	for _, key := range []string{"when", "assert"} {
		if e, ok := rule.Get(key); ok {
			out = append(out, e)
		}
	}
	values(rule.obj("finding").obj("args"))
	for _, a := range rule.list("assign") {
		out = append(out, asObj(a).get("value"))
	}
	for _, c := range rule.list("commands") {
		values(asObj(c).obj("payload"))
		out = append(out, asObj(c).list("idempotencyKey")...)
	}
	return out
}

// deref follows the fragment of a $ref ("#/components/schemas/Thing") through a document.
func deref(doc any, pointer string) (any, bool) {
	node := doc
	for _, part := range strings.Split(strings.TrimLeft(pointer, "#/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		var ok bool
		if node, ok = asObj(node).Get(part); !ok {
			return nil, false
		}
	}
	return node, true
}

// local follows $refs that stay inside the document.
func local(doc, schema any) any {
	for hops := 0; hops < 64; hops++ {
		ref := asObj(schema).str("$ref")
		if !strings.HasPrefix(ref, "#") {
			return schema
		}
		schema, _ = deref(doc, ref)
	}
	return nil
}

var openSchema = NewObject()

// schemaAt follows property names through a JSON Schema. It returns an empty schema where the
// schema says nothing about the path, and nil where the path is not in the schema.
func schemaAt(doc, schema any, segments []string) any {
	for _, seg := range segments {
		s := asObj(local(doc, schema))
		if s == nil {
			return openSchema
		}
		if s.str("type") == "array" || s.has("items") {
			if s = asObj(local(doc, orDefault(s, "items", openSchema))); s == nil {
				return openSchema
			}
			if schema = s; isDigits(seg) {
				continue
			}
		}
		properties := s.obj("properties")
		if properties == nil {
			return openSchema
		}
		var ok bool
		if schema, ok = properties.Get(seg); !ok {
			return nil
		}
	}
	return schema
}

func pointerSegments(pointer string) []string { return strings.Split(strings.Trim(pointer, "/"), "/") }

// usedNames collects the function names and custom operators reachable from the expressions,
// following function bodies.
func usedNames(exprs []any, functions *Object) (fns, ops map[string]bool) {
	fns, ops = map[string]bool{}, map[string]bool{}
	todo := append([]any(nil), exprs...)
	visit := func(node *Object, _ bool) {
		if name, ok := node.get("fn").(string); ok && !fns[name] {
			fns[name] = true
			if fn := functions.obj(name); fn != nil {
				todo = append(todo, fn.get("body"))
			}
		}
		if op := node.str("op"); strings.HasPrefix(op, "x-") {
			ops[op] = true
		}
	}
	for len(todo) > 0 {
		next := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		walkExprs(next, false, visit)
	}
	return fns, ops
}

// staticProblems runs the load-time checks beyond the schema. Without a loader the checks that
// need the entity schemas (PATH_UNKNOWN, SCHEMA_REF_UNRESOLVED) are skipped.
func (l *loading) staticProblems(rs *Object) ([]Problem, error) {
	var out []Problem
	add := func(code, rule, format string, args ...any) {
		out = append(out, Problem{Code: code, Message: fmt.Sprintf(format, args...), Rule: rule})
	}
	type entitySchema struct{ doc, schema any }
	entitySchemas := map[string]entitySchema{}
	functions, operators, types := rs.obj("functions"), rs.obj("operators"), rs.obj("types")
	entities, params := rs.obj("entities"), rs.obj("params")

	if l.loader != nil {
		for _, name := range entities.names() {
			ref := entities.obj(name).obj("schema").str("$ref")
			file, fragment, _ := strings.Cut(ref, "#")
			doc, err := normalize(l.loader(file))
			if err != nil {
				return nil, err
			}
			if schema, ok := deref(doc, "#"+fragment); ok {
				entitySchemas[name] = entitySchema{doc, schema}
			} else {
				add("SCHEMA_REF_UNRESOLVED", "", "entity %s: %s not found", name, ref)
			}
		}
	}

	for _, name := range entities.names() {
		es := entitySchemas[name]
		entities.obj(name).obj("fieldTypes").each(func(pointer string, typeName any) {
			if t, _ := typeName.(string); !types.has(t) {
				add("TYPE_UNKNOWN", "", "entity %s: %s is bound to undeclared type %s", name, pointer, t)
			}
			if es.schema != nil && schemaAt(es.doc, es.schema, pointerSegments(pointer)) == nil {
				add("PATH_UNKNOWN", "", "entity %s: fieldTypes %s is not in the schema", name, pointer)
			}
		})
	}

	// checks shared by rule expressions and function bodies
	checkCalls := func(node *Object, where, rule string) {
		op := node.str("op")
		if op == "matches" {
			var pattern any
			if args := node.list("args"); len(args) == 2 {
				pattern = args[1]
			}
			if p, ok := pattern.(string); !ok {
				add("PATTERN_NOT_PORTABLE", rule, "%s: the pattern of matches must be a literal", where)
			} else if _, err := compilePattern(p); err != nil {
				add("PATTERN_NOT_PORTABLE", rule, "%s: %v", where, err)
			}
		}
		if strings.HasPrefix(op, "x-") && !operators.has(op) {
			add("OPERATOR_UNDECLARED", rule, "%s: custom operator %s is not declared", where, op)
		}
		if name, ok := node.get("fn").(string); ok {
			if fn := functions.obj(name); fn == nil {
				add("FUNCTION_UNKNOWN", rule, "%s: unknown function %s", where, name)
			} else if len(node.list("args")) != len(fn.list("params")) {
				add("FUNCTION_ARITY", rule, "%s: function %s takes %d argument(s)", where, name, len(fn.list("params")))
			}
		}
	}
	varPath := func(node *Object) (root string, segments []string, ok bool) {
		path, ok := node.get("var").(string)
		if !ok {
			return "", nil, false
		}
		parts := strings.Split(path, ".")
		return parts[0], parts[1:], true
	}
	declared := func(names []any, name string) bool {
		for _, n := range names {
			if n == name {
				return true
			}
		}
		return false
	}

	// functions: scope, calls, recursion
	for _, name := range functions.names() {
		fn := functions.obj(name)
		where := "function " + name
		if expressionTooDeep(fn.get("body"), maxExpressionDepth) {
			add("EXPRESSION_TOO_DEEP", "", "%s: nested more than %d deep", where, maxExpressionDepth)
			continue
		}
		walkExprs(fn.get("body"), false, func(node *Object, bound bool) {
			checkCalls(node, where, "")
			root, segments, ok := varPath(node)
			if !ok {
				return
			}
			if root == "value" || root == "field" || (root == "item" && !bound) ||
				(root == "arg" && (len(segments) == 0 || !declared(fn.list("params"), segments[0]))) {
				add("SCOPE_INVALID", "", "%s: %s is not in scope", where, node.str("var"))
			}
			if root == "params" && (len(segments) == 0 || !params.has(segments[0])) {
				add("PARAM_UNDECLARED", "", "%s: unknown param in %s", where, node.str("var"))
			}
		})
	}
	var reaches func(start, target string, seen map[string]bool) bool
	reaches = func(start, target string, seen map[string]bool) bool {
		callees, _ := usedNames([]any{functions.obj(start).get("body")}, nil)
		for callee := range callees {
			if callee == target {
				return true
			}
			if functions.has(callee) && !seen[callee] {
				seen[callee] = true // a function already explored cannot lead anywhere new
				if reaches(callee, target, seen) {
					return true
				}
			}
		}
		return false
	}
	for _, name := range functions.names() {
		if reaches(name, name, map[string]bool{name: true}) {
			add("FUNCTION_RECURSIVE", "", "function %s calls itself, directly or indirectly", name)
		}
	}

	defaultMessages := rs.obj("messages").obj(rs.str("defaultLocale"))
	codes := map[string]string{}
	for _, x := range rs.list("rules") {
		r := asObj(x)
		id, target := r.str("id"), r.obj("target")
		entity, hasEntity := target.Get("entity")
		entityName, _ := entity.(string)
		typeName, typed := target.Get("type")
		if t, _ := typeName.(string); typed && !types.has(t) {
			add("TYPE_UNKNOWN", id, "undeclared type %s", t)
		}
		if hasEntity && !entities.has(entityName) {
			add("ENTITY_UNKNOWN", id, "unknown entity %s", entityName)
			continue
		}
		es := entitySchemas[entityName]
		forEach, hasForEach := r.get("forEach").(string)
		var itemSchema any
		if hasForEach && es.schema != nil {
			itemSchema = schemaAt(es.doc, es.schema, append(pointerSegments(forEach), "0"))
			if itemSchema == nil {
				add("PATH_UNKNOWN", id, "forEach %s is not in the %s schema", forEach, entityName)
			}
		}
		checkPointer := func(pointer any, base any, where string) {
			if p, _ := pointer.(string); base != nil && schemaAt(es.doc, base, pointerSegments(p)) == nil {
				add("PATH_UNKNOWN", id, "%s %s is not in the %s schema", where, p, entityName)
			}
		}
		targetBase := es.schema
		if hasForEach {
			targetBase = itemSchema
		}
		if field, ok := target.Get("field"); ok {
			checkPointer(field, targetBase, "target")
		} else {
			for _, field := range target.list("fields") {
				checkPointer(field, targetBase, "target")
			}
		}
		for _, e := range r.list("effects") {
			checkPointer(asObj(e).get("field"), es.schema, "effect field")
		}
		for _, a := range r.list("assign") {
			checkPointer(asObj(a).get("field"), es.schema, "assign field")
		}

		onCreate := declared(r.list("operations"), "create")
		for _, expr := range ruleExprs(r) {
			if expressionTooDeep(expr, maxExpressionDepth) {
				add("EXPRESSION_TOO_DEEP", id, "an expression is nested more than %d deep", maxExpressionDepth)
				continue
			}
			walkExprs(expr, false, func(node *Object, bound bool) {
				checkCalls(node, "rule", id)
				root, segments, ok := varPath(node)
				if !ok {
					return
				}
				path := node.str("var")
				if root == "params" && (len(segments) == 0 || !params.has(segments[0])) {
					add("PARAM_UNDECLARED", id, "unknown param in %s", path)
				}
				if (root == "data" || root == "original") && es.schema != nil && schemaAt(es.doc, es.schema, segments) == nil {
					add("PATH_UNKNOWN", id, "path %s is not in the %s schema", path, entityName)
				}
				if root == "item" && !bound { // the element of forEach, not of a collection operator
					if !hasForEach {
						add("SCOPE_INVALID", id, "item is not in scope here")
					} else if itemSchema != nil && schemaAt(es.doc, itemSchema, segments) == nil {
						add("PATH_UNKNOWN", id, "path %s is not in the item schema", path)
					}
				}
				if root == "original" && onCreate {
					add("ORIGINAL_ON_CREATE", id, "original.* is used but the rule applies to create")
				}
				if root == "arg" || ((root == "value" || root == "field") && !typed) {
					add("SCOPE_INVALID", id, "%s is not in scope here", path)
				}
			})
		}

		if r.str("kind") == "validation" {
			finding := r.obj("finding")
			code, message := finding.str("code"), finding.str("message")
			if other, ok := codes[code]; ok {
				add("FINDING_CODE_DUPLICATE", id, "code %s is also used by %s", code, other)
			}
			codes[code] = id
			if template, ok := defaultMessages.get(message).(string); !ok {
				add("MESSAGE_MISSING", id, "message %s missing for %s", message, rs.str("defaultLocale"))
			} else {
				for _, m := range placeholder.FindAllStringSubmatch(template, -1) {
					if !finding.obj("args").has(m[1]) {
						add("MESSAGE_ARG_MISSING", id, "placeholders without args in %s", message)
						break
					}
				}
			}
		}
	}
	return out, nil
}

// ------------------------------------------------------------------ manifest

// manifest builds what an evaluator consumes. The client manifest carries nothing a browser must
// not see.
func manifest(rs *Object, channel string) *Object {
	rules := rs.list("rules")
	params, messages, functions := NewObject(), rs.obj("messages"), NewObject()
	rs.obj("params").each(func(name string, p any) { params.Set(name, asObj(p).get("value")) })
	rs.obj("functions").each(func(name string, v any) {
		fn := NewObject()
		fn.Set("params", asObj(v).get("params"))
		fn.Set("body", asObj(v).get("body"))
		functions.Set(name, fn)
	})
	client := channel == "client"
	usedMessages := map[string]bool{}
	if client {
		kept := []any{}
		for _, x := range rules {
			r := asObj(x)
			if enforcement := orDefault(r, "enforcement", "both"); (enforcement == "client" || enforcement == "both") && r.str("kind") != "action" {
				kept = append(kept, r)
				if r.has("finding") {
					usedMessages[r.obj("finding").str("message")] = true
				}
			}
		}
		rules = kept
	}
	var exprs []any
	for _, r := range rules {
		exprs = append(exprs, ruleExprs(asObj(r))...)
	}
	usedFunctions, usedOperators := usedNames(exprs, rs.obj("functions"))
	if client {
		functions = filterKeys(functions, usedFunctions)
		usedParams := map[string]bool{}
		collect := func(node *Object, _ bool) {
			if path := node.str("var"); strings.HasPrefix(path, "params.") {
				usedParams[strings.Split(path, ".")[1]] = true
			}
		}
		for _, e := range exprs {
			walkExprs(e, false, collect)
		}
		functions.each(func(_ string, fn any) { walkExprs(asObj(fn).get("body"), false, collect) })
		params = filterKeys(params, usedParams)
		filtered := NewObject()
		messages.each(func(locale string, catalog any) { filtered.Set(locale, filterKeys(asObj(catalog), usedMessages)) })
		messages = filtered
	}
	fieldTypes := NewObject()
	rs.obj("entities").each(func(name string, ent any) {
		if bound := asObj(ent).obj("fieldTypes"); bound.Len() > 0 {
			fieldTypes.Set(name, bound)
		}
	})
	names := make([]string, 0, len(usedOperators))
	for name := range usedOperators {
		names = append(names, name)
	}
	sort.Strings(names)
	sorted := make([]any, len(names))
	for i, name := range names {
		sorted[i] = name
	}

	mf := NewObject()
	for _, key := range []string{"ruleCascade", "id", "version", "checksum"} {
		mf.Set(key, rs.get(key))
	}
	mf.Set("channel", channel)
	mf.Set("conflictPolicy", rs.get("conflictPolicy"))
	mf.Set("defaultLocale", rs.get("defaultLocale"))
	mf.Set("params", params)
	mf.Set("fieldTypes", fieldTypes)
	mf.Set("functions", functions)
	mf.Set("operators", sorted)
	mf.Set("rules", rules)
	mf.Set("messages", messages)
	return mf
}

func filterKeys(o *Object, keep map[string]bool) *Object {
	out := NewObject()
	o.each(func(k string, v any) {
		if keep[k] {
			out.Set(k, v)
		}
	})
	return out
}

// ------------------------------------------------------------------ RuleSet

// RuleSet is a loaded, checked ruleset: compiled from source documents by Load, read from a bundle
// by FromBundle, or read from one manifest by FromManifest. It is immutable and safe for
// concurrent use.
type RuleSet struct {
	id, version, checksum string
	resolved              *Object
	manifests             *Object // channel to manifest
}

// ID returns the identifier of the ruleset.
func (rs *RuleSet) ID() string { return rs.id }

// Version returns the ruleset's own version.
func (rs *RuleSet) Version() string { return rs.version }

// Checksum returns the checksum of the resolved ruleset (specification section 6).
func (rs *RuleSet) Checksum() string { return rs.checksum }

// Resolved returns the resolved ruleset the checksum was computed over, or nil when the ruleset
// was read from a bundle or a manifest.
func (rs *RuleSet) Resolved() *Object { return rs.resolved }

// Channels returns the channels the ruleset can be evaluated on, sorted: "client" and "server",
// unless it was read from one manifest.
func (rs *RuleSet) Channels() []string {
	channels := rs.manifests.Keys()
	sort.Strings(channels)
	return channels
}

func newRuleSet(resolved, manifests *Object) *RuleSet {
	first := manifests.obj(manifests.names()[0])
	return &RuleSet{first.str("id"), first.str("version"), first.str("checksum"), resolved, manifests}
}

// Load compiles a ruleset document: it validates it against the schema, resolves `extends` and
// `overrides` with the parents found in registry (ruleset id to document), runs the load-time
// checks and computes the checksum. Documents are JSON values: *Object as ParseJSON returns
// them, or map[string]any, whose members are taken in sorted order.
//
// loader supplies the entity schemas for the path checks; with a nil loader PATH_UNKNOWN and
// SCHEMA_REF_UNRESOLVED are not checked. A ruleset that fails a check is never returned: the
// error is a *LoadError listing the problems.
func Load(document any, registry map[string]any, loader SchemaLoader) (*RuleSet, error) {
	doc, err := normalize(document)
	if err != nil {
		return nil, err
	}
	l := &loading{registry: registry, loader: loader}
	resolved, err := l.resolve(doc, nil)
	if err != nil {
		return nil, err
	}
	problems, err := l.staticProblems(resolved)
	if err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		return nil, &LoadError{problems}
	}
	manifests := NewObject()
	manifests.Set("server", manifest(resolved, "server"))
	manifests.Set("client", manifest(resolved, "client"))
	return newRuleSet(resolved, manifests), nil
}

// usableManifest reports whether a manifest has the least an evaluator needs before it trusts the
// rest of it: a string id, version and checksum, a list of rules and a channel.
func usableManifest(mf *Object) bool {
	_, usable := mf.get("rules").([]any)
	for _, key := range []string{"id", "version", "checksum"} {
		_, isString := mf.get(key).(string)
		usable = usable && isString
	}
	return usable && (mf.get("channel") == "server" || mf.get("channel") == "client")
}

// FromBundle reads a compiled bundle. No schema validation, inheritance or load checks run here:
// the compiler already did them, which is what lets a runtime be a small evaluator. The error is
// a *LoadError with code BUNDLE_UNSUPPORTED or BUNDLE_INVALID.
func FromBundle(bundle any) (*RuleSet, error) {
	b, err := normalize(bundle)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(asObj(b).str("ruleCascadeBundle"), "1.") {
		return nil, loadError("BUNDLE_UNSUPPORTED", "", "not a Rule Cascade 1.x bundle")
	}
	manifests := NewObject()
	for _, channel := range []string{"server", "client"} {
		mf := asObj(b).obj("manifests").obj(channel)
		if !usableManifest(mf) || mf.str("channel") != channel { // each manifest carries its own name
			return nil, loadError("BUNDLE_INVALID", "", "the bundle has no usable %s manifest", channel)
		}
		manifests.Set(channel, mf)
	}
	return newRuleSet(nil, manifests), nil
}

// FromManifest reads one manifest on its own, as a browser or a mobile app receives it. The
// ruleset then has that one channel: a client manifest cannot be evaluated as the server. The
// error is a *LoadError with code MANIFEST_INVALID.
func FromManifest(manifest any) (*RuleSet, error) {
	m, err := normalize(manifest)
	if err != nil {
		return nil, err
	}
	mf := asObj(m)
	if !usableManifest(mf) {
		return nil, loadError("MANIFEST_INVALID", "", "not a usable Rule Cascade manifest")
	}
	manifests := NewObject()
	manifests.Set(mf.str("channel"), mf)
	return newRuleSet(nil, manifests), nil
}

// Bundle returns the compiled, portable form of the ruleset: everything an evaluator needs, as
// plain JSON. A ruleset read from one manifest gives a bundle with that one manifest, which
// FromBundle does not accept.
func (rs *RuleSet) Bundle() *Object {
	b := NewObject()
	b.Set("ruleCascadeBundle", BundleVersion)
	b.Set("id", rs.id)
	b.Set("version", rs.version)
	b.Set("checksum", rs.checksum)
	b.Set("manifests", rs.manifests)
	return b
}

// Manifest returns the manifest for a channel, "server" or "client". An empty channel is the
// default one: the server's when the ruleset has it, otherwise the channel it has. The error says
// when the ruleset has no manifest for the channel.
func (rs *RuleSet) Manifest(channel string) (*Object, error) {
	if channel == "" {
		channel = rs.manifests.names()[0]
		if rs.manifests.has("server") {
			channel = "server"
		}
	}
	if channel != "server" && channel != "client" {
		return nil, fmt.Errorf("rulecascade: %q is not a channel; a channel is server or client", channel)
	}
	mf := rs.manifests.obj(channel)
	if mf == nil {
		return nil, fmt.Errorf("rulecascade: ruleset %s has no %s manifest", rs.id, channel)
	}
	return mf, nil
}

// MissingOperators returns the custom operators the rules need, on any channel the ruleset has,
// that operators does not supply, sorted. Check it at start-up: a missing operator does not fail
// the load, it fails every rule that uses it, closed.
func (rs *RuleSet) MissingOperators(operators Operators) []string {
	missing := []string{}
	seen := map[string]bool{}
	rs.manifests.each(func(_ string, mf any) {
		for _, x := range asObj(mf).list("operators") {
			if name, ok := x.(string); ok && operators[name] == nil && !seen[name] {
				seen[name] = true
				missing = append(missing, name)
			}
		}
	})
	sort.Strings(missing)
	return missing
}
