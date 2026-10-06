package rulecascade

import (
	_ "embed" // the ruleset schema is compiled into the package
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// Schema validation of ruleset documents (loading step 1).
//
// This is a small JSON Schema validator that interprets rule-cascade.schema.json itself, for
// exactly the keywords that schema uses. The file is a byte-identical copy of
// spec/v1/rule-cascade.schema.json; a test fails when the two differ or when the schema starts
// using a keyword this validator does not know.

//go:embed rule-cascade.schema.json
var schemaText []byte

var ruleSetSchema = sync.OnceValue(func() *Object {
	parsed, err := ParseJSON(schemaText)
	if err == nil {
		parsed, err = normalize(parsed)
	}
	if err != nil {
		panic("rulecascade: the embedded schema is not valid JSON: " + err.Error())
	}
	return asObj(parsed)
})

// The keywords the validator implements, and the ones that carry no constraint.
var schemaKeywords = map[string]bool{"$ref": true, "$defs": true, "type": true, "enum": true, "const": true,
	"required": true, "properties": true, "patternProperties": true, "additionalProperties": true,
	"propertyNames": true, "items": true, "minItems": true, "maxItems": true, "uniqueItems": true,
	"minProperties": true, "minLength": true, "minimum": true, "pattern": true, "anyOf": true, "allOf": true,
	"not": true, "if": true, "then": true,
	"$schema": true, "$id": true, "title": true, "description": true, "default": true, "examples": true, "format": true}

type schemaError struct {
	path    []string
	message string
}

type validator struct {
	root   *Object
	errors []schemaError
}

func (v *validator) fail(path []string, format string, args ...any) {
	v.errors = append(v.errors, schemaError{path, fmt.Sprintf(format, args...)})
}

// valid reports whether an instance satisfies a schema, without recording why not.
func (v *validator) valid(schema, instance any) bool {
	probe := &validator{root: v.root}
	probe.check(schema, instance, nil)
	return len(probe.errors) == 0
}

func hasType(name string, instance any) bool {
	switch name {
	case "null":
		return instance == nil
	case "number":
		return isNum(instance)
	case "integer":
		return isNum(instance) && dec(instance).isWhole()
	case "array":
		name = "list"
	}
	return instance != nil && typeOf(instance) == name
}

func hasDuplicates(list []any) bool {
	for i := range list {
		for j := 0; j < i; j++ {
			if equal(list[i], list[j]) {
				return true
			}
		}
	}
	return false
}

// matchesPattern applies a pattern of the schema. Every pattern in the schema is a portable
// pattern (specification section 4.4), so each runtime can check it with the engine it has.
func matchesPattern(pattern, s string) bool {
	re, err := compilePattern(pattern)
	if err != nil {
		panic("rulecascade: the embedded schema has a pattern that is not portable: " + err.Error())
	}
	return re.MatchString(s)
}

func (v *validator) check(schema, instance any, path []string) {
	s, ok := schema.(*Object)
	if !ok {
		if allowed, isBool := schema.(bool); isBool && !allowed {
			v.fail(path, "%s is not allowed here", show(instance))
		}
		return
	}
	child := func(segment string) []string { return append(path[:len(path):len(path)], segment) }

	if ref := s.str("$ref"); ref != "" {
		target, _ := deref(v.root, ref)
		v.check(target, instance, path)
	}
	if t, ok := s.Get("type"); ok {
		names, isList := t.([]any)
		if !isList {
			names = []any{t}
		}
		matched := false
		for _, name := range names {
			if n, _ := name.(string); hasType(n, instance) {
				matched = true
			}
		}
		if !matched {
			v.fail(path, "%s is not of type %s", show(instance), show(t))
		}
	}
	if options, ok := s.get("enum").([]any); ok {
		found := false
		for _, option := range options {
			found = found || equal(option, instance)
		}
		if !found {
			v.fail(path, "%s is not one of %s", show(instance), show(options))
		}
	}
	if want, ok := s.Get("const"); ok && !equal(want, instance) {
		v.fail(path, "%s was expected", show(want))
	}

	switch x := instance.(type) {
	case string:
		if least, ok := s.Get("minLength"); ok && decimalFromInt(int64(utf8.RuneCountInString(x))).cmp(dec(least)) < 0 {
			v.fail(path, "%s is too short", show(x))
		}
		if pattern, ok := s.get("pattern").(string); ok && !matchesPattern(pattern, x) {
			v.fail(path, "%s does not match %s", show(x), show(pattern))
		}
	case []any:
		if items, ok := s.Get("items"); ok {
			for i, item := range x {
				v.check(items, item, child(fmt.Sprint(i)))
			}
		}
		if least, ok := s.Get("minItems"); ok && decimalFromInt(int64(len(x))).cmp(dec(least)) < 0 {
			v.fail(path, "%s is too short", show(x))
		}
		if most, ok := s.Get("maxItems"); ok && decimalFromInt(int64(len(x))).cmp(dec(most)) > 0 {
			v.fail(path, "%s is too long", show(x))
		}
		if s.get("uniqueItems") == true && hasDuplicates(x) {
			v.fail(path, "%s has non-unique elements", show(x))
		}
	case *Object:
		v.checkObject(s, x, path, child)
	case json.Number:
		if least, ok := s.Get("minimum"); ok && dec(x).cmp(dec(least)) < 0 {
			v.fail(path, "%s is less than the minimum of %s", show(x), show(least))
		}
	}

	if options, ok := s.get("anyOf").([]any); ok {
		matched := false
		for _, option := range options {
			matched = matched || v.valid(option, instance)
		}
		if !matched {
			v.fail(path, "%s is not valid under any of the given schemas", show(instance))
		}
	}
	for _, part := range s.list("allOf") {
		v.check(part, instance, path)
	}
	if banned, ok := s.Get("not"); ok && v.valid(banned, instance) {
		v.fail(path, "%s should not be valid under %s", show(instance), show(banned))
	}
	if condition, ok := s.Get("if"); ok && v.valid(condition, instance) {
		if then, ok := s.Get("then"); ok {
			v.check(then, instance, path)
		}
	}
}

func (v *validator) checkObject(s, x *Object, path []string, child func(string) []string) {
	for _, name := range s.list("required") {
		if n, _ := name.(string); !x.has(n) {
			v.fail(path, "%s is a required property", show(name))
		}
	}
	properties, patterns := s.obj("properties"), s.obj("patternProperties")
	var additional []string
	for _, key := range x.keys {
		known := false
		if sub, ok := properties.Get(key); ok {
			known = true
			v.check(sub, x.vals[key], child(key))
		}
		for _, pattern := range patterns.names() {
			if matchesPattern(pattern, key) {
				known = true
				v.check(patterns.vals[pattern], x.vals[key], child(key))
			}
		}
		if !known {
			additional = append(additional, key)
		}
	}
	if rest, ok := s.Get("additionalProperties"); ok {
		if rest == false && len(additional) > 0 {
			v.fail(path, "additional properties are not allowed (%s)", strings.Join(additional, ", "))
		} else {
			for _, key := range additional {
				v.check(rest, x.vals[key], child(key))
			}
		}
	}
	if names, ok := s.Get("propertyNames"); ok {
		for _, key := range x.keys {
			v.check(names, key, path)
		}
	}
	if least, ok := s.Get("minProperties"); ok && decimalFromInt(int64(x.Len())).cmp(dec(least)) < 0 {
		v.fail(path, "%s does not have enough properties", show(x))
	}
}

// schemaProblems validates a document against the ruleset schema.
func schemaProblems(doc any) []Problem {
	v := &validator{root: ruleSetSchema()}
	v.check(v.root, doc, nil)
	sort.SliceStable(v.errors, func(i, j int) bool {
		a, b := v.errors[i].path, v.errors[j].path
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
	problems := make([]Problem, len(v.errors))
	for i, e := range v.errors {
		where := strings.Join(e.path, "/")
		if where == "" {
			where = "<root>"
		}
		problems[i] = Problem{Code: "SCHEMA_INVALID", Message: where + ": " + e.message}
	}
	return problems
}
