package rulecascade

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Expression evaluator for core profile 1.0 (specification section 4). It is a port of
// expressions.py of the reference implementation, operator by operator.

const maxCallDepth = 32

// Limits of specification 4.7: the depth of an expression and of a value, and the longest subject
// of matches in code points.
const (
	maxExpressionDepth = 128
	maxValueDepth      = 64
	maxSubjectLength   = 10000
)

// EvalError says that a rule or an expression could not be evaluated. The engine fails closed.
type EvalError struct{ Message string }

func (e *EvalError) Error() string { return e.Message }

// Operators are the custom operators of the host application (specification section 4.6), by name.
//
// A custom operator receives its arguments as plain Go values: nil, bool, string, float64 for
// every number (rounded to 15 significant digits first, like every number that leaves an
// expression), []any and map[string]any. It returns any JSON value; a float64 is read as its
// shortest decimal representation. Returning an error, or panicking, is an evaluation error. A
// custom operator must be a pure function of its arguments.
type Operators map[string]func(args []any) (any, error)

// scope is what an expression can see: the variable roots, the function table and the custom
// operators. Evaluation errors travel as a panic with an *EvalError and are recovered at the
// boundary of a rule or of the package, the way the reference raises and catches EvalError.
type scope struct {
	vars      map[string]any
	functions *Object
	operators Operators
	depth     int
}

// with returns a copy of the scope with one more variable root.
func (s *scope) with(name string, value any) *scope {
	vars := make(map[string]any, len(s.vars)+1)
	for k, v := range s.vars {
		vars[k] = v
	}
	vars[name] = value
	return &scope{vars, s.functions, s.operators, s.depth}
}

func fail(format string, args ...any) { panic(&EvalError{fmt.Sprintf(format, args...)}) }

// try runs fn and returns the evaluation error it raised, if any.
func try(fn func()) (err *EvalError) {
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(*EvalError)
			if !ok {
				panic(r)
			}
			err = e
		}
	}()
	fn()
	return nil
}

// show renders a value for an error message.
func show(v any) string {
	text := string(appendValue(nil, v, "", 0))
	if len(text) > 80 {
		text = text[:80] + "..."
	}
	return text
}

func num(v any) decimal {
	if !isNum(v) {
		fail("number expected, got %s", show(v))
	}
	return dec(v)
}

func boolean(v any) bool {
	b, ok := v.(bool)
	if !ok {
		fail("boolean expected, got %s", show(v))
	}
	return b
}

func text(v any) string {
	s, ok := v.(string)
	if !ok {
		fail("string expected, got %s", show(v))
	}
	return s
}

// index reads a non-negative whole number.
func index(v any, what string) int {
	d := num(v)
	if !d.isWhole() || (d.neg && !d.isZero()) {
		fail("%s must be a non-negative whole number", what)
	}
	return d.toInt()
}

// arithmetic turns a failed decimal operation into an evaluation error.
func arithmetic(op string, d decimal, err error) decimal {
	if err != nil {
		fail("%s overflowed", op)
	}
	return d
}

// ------------------------------------------------------------------ dates

func digitsAt(s string, at, count int) (int, bool) {
	n := 0
	for _, c := range []byte(s[at : at+count]) {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// toDate reads an RFC 3339 full-date or date-time and returns midnight of its UTC calendar date.
// ASCII digits, upper-case T and Z, seconds and an offset in a date-time, nothing before or after.
// A date that does not exist, an offset beyond 23:59 and a UTC date outside the years 0001 to 9999
// are errors.
func toDate(v any) time.Time {
	s := text(v)
	bad := func() { fail("RFC 3339 date or date-time expected, got %s", show(v)) }
	if len(s) < 10 || s[4] != '-' || s[7] != '-' {
		bad()
	}
	y, ok1 := digitsAt(s, 0, 4)
	mo, ok2 := digitsAt(s, 5, 2)
	d, ok3 := digitsAt(s, 8, 2)
	if !ok1 || !ok2 || !ok3 {
		bad()
	}
	var h, mi, sec, offset int
	if rest := s[10:]; rest != "" {
		// Thh:mm:ss, optional fraction, then Z or a numeric offset
		if len(rest) < 10 || rest[0] != 'T' || rest[3] != ':' || rest[6] != ':' {
			bad()
		}
		h, ok1 = digitsAt(rest, 1, 2)
		mi, ok2 = digitsAt(rest, 4, 2)
		sec, ok3 = digitsAt(rest, 7, 2)
		if !ok1 || !ok2 || !ok3 {
			bad()
		}
		zone := rest[9:]
		if zone[0] == '.' {
			end := 1
			for end < len(zone) && zone[end] >= '0' && zone[end] <= '9' {
				end++
			}
			if end == 1 {
				bad()
			}
			zone = zone[end:]
		}
		if zone != "Z" {
			if len(zone) != 6 || (zone[0] != '+' && zone[0] != '-') || zone[3] != ':' {
				bad()
			}
			hours, ok1 := digitsAt(zone, 1, 2)
			minutes, ok2 := digitsAt(zone, 4, 2)
			if !ok1 || !ok2 {
				bad()
			}
			if hours > 23 || minutes > 59 {
				fail("invalid date %s", show(v))
			}
			if offset = hours*3600 + minutes*60; zone[0] == '-' {
				offset = -offset
			}
		}
	}
	if y < 1 || mo < 1 || mo > 12 || d < 1 || h > 23 || mi > 59 || sec > 59 ||
		d > time.Date(y, time.Month(mo)+1, 0, 0, 0, 0, 0, time.UTC).Day() {
		fail("invalid date %s", show(v))
	}
	utc := time.Date(y, time.Month(mo), d, h, mi, sec, 0, time.UTC).Add(-time.Duration(offset) * time.Second)
	year, month, day := utc.Date()
	if year < 1 || year > 9999 { // outside the years 0001 to 9999
		fail("invalid date %s", show(v))
	}
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func dayNumber(t time.Time) int { return int(t.Unix() / 86400) }

// wholeYears counts completed calendar years from a to b, the way ages are counted. It is negative
// when b is before a.
func wholeYears(a, b time.Time) int {
	if b.Before(a) {
		return -wholeYears(b, a)
	}
	years := b.Year() - a.Year()
	if b.Month() < a.Month() || (b.Month() == a.Month() && b.Day() < a.Day()) {
		years--
	}
	return years
}

// ------------------------------------------------------------------ helpers

// roots are the variable roots of the specification. Any other root is null.
var roots = map[string]bool{"data": true, "original": true, "actor": true, "ctx": true, "params": true,
	"item": true, "value": true, "field": true, "arg": true}

// lookup follows a dot path from a root. Missing paths, and roots that do not exist, are null. In
// a list a segment of ASCII digits is a decimal index.
func lookup(path string, vars map[string]any) any {
	segments := strings.Split(path, ".")
	if !roots[segments[0]] {
		return nil
	}
	node := vars[segments[0]]
	for _, seg := range segments[1:] {
		switch x := node.(type) {
		case *Object:
			node = x.vals[seg]
		case []any:
			i, err := strconv.Atoi(seg)
			if !isDigits(seg) || err != nil || i >= len(x) {
				return nil
			}
			node = x[i]
		default:
			return nil
		}
	}
	return node
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

func asciiLower(s string) string {
	return strings.Map(func(c rune) rune {
		if c >= 'A' && c <= 'Z' {
			return c + 32
		}
		return c
	}, s)
}

func asciiUpper(s string) string {
	return strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' {
			return c - 32
		}
		return c
	}, s)
}

func typeOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case json.Number, decimal:
		return "number"
	case string:
		return "string"
	case []any:
		return "list"
	}
	return "object"
}

// Exact argument counts. Operators not listed take any number of arguments or are checked below.
var arity = map[string]int{"not": 1, "exists": 1, "empty": 1, "len": 1, "lower": 1, "upper": 1, "trim": 1, "text": 1,
	"abs": 1, "typeOf": 1, "if": 3, "between": 3,
	"eq": 2, "ne": 2, "lt": 2, "lte": 2, "gt": 2, "gte": 2, "in": 2,
	"add": 2, "sub": 2, "mul": 2, "div": 2, "mod": 2, "matches": 2, "startsWith": 2, "endsWith": 2,
	"contains": 2, "all": 2, "some": 2, "none": 2, "sum": 2, "map": 2, "filter": 2, "daysBetween": 2,
	"yearsBetween": 2}

var arityRange = map[string][2]int{"round": {1, 2}, "substring": {2, 3}} // inclusive

var arityMin = map[string]int{"min": 1, "max": 1}

func callFunction(name string, args []any, s *scope, depth int) any {
	fn := s.functions.obj(name)
	if fn == nil {
		fail("unknown function %s", name)
	}
	params, ok := fn.get("params").([]any)
	if !ok || !fn.has("body") { // not a function: an object with a list of params and a body
		fail("unknown function %s", name)
	}
	if len(args) != len(params) {
		fail("function %s takes %d argument(s), got %d", name, len(params), len(args))
	}
	calls := s.depth + 1
	if calls > maxCallDepth {
		fail("function calls nested too deeply")
	}
	bound := NewObject()
	for i, a := range args { // eager, in the caller's scope
		param, _ := params[i].(string)
		bound.Set(param, evAt(a, s, depth+1))
	}
	// lexical scope: a body sees its own arguments, never the caller's item, value or field
	vars := make(map[string]any, len(s.vars)+1)
	for k, v := range s.vars {
		if k != "item" && k != "value" && k != "field" {
			vars[k] = v
		}
	}
	vars["arg"] = bound
	return evAt(fn.get("body"), &scope{vars, s.functions, s.operators, calls}, depth+1) // the body counts from the depth of the call
}

// tooDeep reports whether lists and objects nest in value more than limit deep (specification
// 4.7). It stops descending at the limit, so it terminates on any input.
func tooDeep(value any, limit int) bool {
	var members []any
	switch x := value.(type) {
	case []any:
		members = x
	case *Object:
		if x == nil {
			return false
		}
		x.each(func(_ string, v any) { members = append(members, v) })
	case map[string]any:
		for _, v := range x {
			members = append(members, v)
		}
	default:
		return false
	}
	if limit < 1 {
		return true
	}
	for _, m := range members {
		if tooDeep(m, limit-1) {
			return true
		}
	}
	return false
}

// expressionTooDeep reports whether an expression, by itself, is nested more than limit deep
// (specification 4.7).
func expressionTooDeep(e any, limit int) bool {
	if limit < 1 {
		return true
	}
	o, ok := e.(*Object)
	if !ok || o == nil || !(o.has("op") || o.has("fn")) {
		return false
	}
	args, _ := o.get("args").([]any)
	for _, a := range args {
		if expressionTooDeep(a, limit-1) {
			return true
		}
	}
	return false
}

// hostValue is a plain value in the form a custom operator receives.
func hostValue(v any) any {
	switch x := v.(type) {
	case json.Number:
		f, _ := strconv.ParseFloat(string(x), 64)
		return f
	case []any:
		list := make([]any, len(x))
		for i, item := range x {
			list[i] = hostValue(item)
		}
		return list
	case *Object:
		m := make(map[string]any, x.Len())
		x.each(func(k string, v any) { m[k] = hostValue(v) })
		return m
	}
	return v
}

func callCustom(op string, values []any, s *scope) any {
	fn := s.operators[op]
	if fn == nil {
		fail("custom operator %s is not registered", op)
	}
	args := make([]any, len(values))
	for i, v := range values {
		args[i] = hostValue(plain(v))
	}
	result, err := func() (result any, err error) {
		defer func() { // a host function failed: fail closed
			if r := recover(); r != nil {
				err = fmt.Errorf("%v", r)
			}
		}()
		return fn(args)
	}()
	if err == nil {
		result, err = normalize(result)
	}
	if err != nil {
		fail("custom operator %s failed: %v", op, err)
	}
	return result
}

// ev evaluates an expression. It returns nil, bool, string, a number (a decimal for computed
// values), []any or *Object.
func ev(e any, s *scope) any { return evAt(e, s, 1) }

// evAt evaluates e at the given depth (specification 4.7), checked before anything nested in it is
// evaluated.
func evAt(e any, s *scope, depth int) any {
	if depth > maxExpressionDepth {
		fail("expression nested more than %d deep", maxExpressionDepth)
	}
	switch e.(type) {
	case nil, bool, string, json.Number, decimal:
		return e // a literal
	}
	// exactly {var}, {fn, args} or {op, args}, with members of the right type
	o := asObj(e)
	args, hasArgs := o.get("args").([]any)
	if path, ok := o.get("var").(string); ok && o.Len() == 1 {
		return lookup(path, s.vars)
	}
	if name, ok := o.get("fn").(string); ok && hasArgs && o.Len() == 2 {
		return callFunction(name, args, s, depth)
	}
	op, ok := o.get("op").(string)
	if !ok || !hasArgs || o.Len() != 2 {
		fail("not an expression: expected a literal, {var}, {op, args} or {fn, args}")
	}
	n, inner := len(args), depth+1
	if want, ok := arity[op]; ok && n != want {
		fail("%s takes %d argument(s), got %d", op, want, n)
	}
	if want, ok := arityRange[op]; ok && (n < want[0] || n > want[1]) {
		fail("%s takes %d to %d arguments, got %d", op, want[0], want[1], n)
	}
	if want, ok := arityMin[op]; ok && n < want {
		fail("%s takes at least %d argument(s)", op, want)
	}

	// lazy operators
	switch op {
	case "and":
		for _, a := range args {
			if !boolean(evAt(a, s, inner)) {
				return false
			}
		}
		return true
	case "or":
		for _, a := range args {
			if boolean(evAt(a, s, inner)) {
				return true
			}
		}
		return false
	case "if":
		if boolean(evAt(args[0], s, inner)) {
			return evAt(args[1], s, inner)
		}
		return evAt(args[2], s, inner)
	case "coalesce":
		for _, a := range args {
			if v := evAt(a, s, inner); v != nil {
				return v
			}
		}
		return nil
	case "all", "some", "none", "sum", "map", "filter":
		return collection(op, args, s, inner)
	}

	v := make([]any, n)
	for i, a := range args {
		v[i] = evAt(a, s, inner)
	}
	switch op {
	case "list":
		return v
	case "not":
		return !boolean(v[0])
	case "exists":
		return v[0] != nil
	case "empty":
		switch x := v[0].(type) {
		case nil:
			return true
		case string:
			return x == ""
		case []any:
			return len(x) == 0
		case *Object:
			return x.Len() == 0
		}
		return false
	case "eq":
		return equal(v[0], v[1])
	case "ne":
		return !equal(v[0], v[1])
	case "lt", "lte", "gt", "gte":
		switch c := num(v[0]).cmp(num(v[1])); op {
		case "lt":
			return c < 0
		case "lte":
			return c <= 0
		case "gt":
			return c > 0
		default:
			return c >= 0
		}
	case "between":
		x, low, high := num(v[0]), num(v[1]), num(v[2])
		return low.cmp(x) <= 0 && x.cmp(high) <= 0
	case "in":
		list, ok := v[1].([]any)
		if !ok {
			fail("in expects a list as its second argument")
		}
		for _, x := range list {
			if equal(v[0], x) {
				return true
			}
		}
		return false
	case "add", "sub", "mul", "div", "mod":
		a, b := num(v[0]), num(v[1])
		if (op == "div" || op == "mod") && b.isZero() {
			fail("division by zero")
		}
		operation := map[string]func(decimal) (decimal, error){
			"add": a.add, "sub": a.sub, "mul": a.mul, "div": a.div, "mod": a.rem}[op]
		result, err := operation(b)
		return arithmetic(op, result, err)
	case "abs":
		d := num(v[0])
		d.neg = false
		return d
	case "min", "max":
		values := make([]decimal, n)
		for i, x := range v {
			values[i] = num(x)
		}
		best := values[0]
		for _, d := range values[1:] {
			if c := d.cmp(best); (op == "min" && c < 0) || (op == "max" && c > 0) {
				best = d
			}
		}
		return best
	case "round":
		places := 0
		if n == 2 {
			places = index(v[1], "round places")
		}
		if places > 15 {
			fail("round supports at most 15 decimal places")
		}
		result, err := num(v[0]).quantize(places)
		return arithmetic(op, result, err)
	case "len":
		switch x := v[0].(type) {
		case string:
			return intNumber(utf8.RuneCountInString(x)) // Unicode code points
		case []any:
			return intNumber(len(x))
		}
		fail("len expects a string or a list")
	case "lower":
		return asciiLower(text(v[0]))
	case "upper":
		return asciiUpper(text(v[0]))
	case "trim":
		return strings.Trim(text(v[0]), " \t\n\r")
	case "concat":
		var b strings.Builder
		for _, x := range v {
			b.WriteString(text(x))
		}
		return b.String()
	case "text":
		return render(plain(v[0]))
	case "substring":
		runes := []rune(text(v[0])) // by code points
		start := min(index(v[1], "substring start"), len(runes))
		end := len(runes)
		if n == 3 {
			if length := index(v[2], "substring length"); length < end-start {
				end = start + length
			}
		}
		return string(runes[start:end])
	case "matches":
		re, err := compilePattern(text(v[1]))
		if err != nil {
			fail("%v", err)
		}
		subject := text(v[0])
		if utf8.RuneCountInString(subject) > maxSubjectLength {
			fail("matches: the subject is longer than %d code points", maxSubjectLength)
		}
		return re.MatchString(subject)
	case "startsWith":
		return strings.HasPrefix(text(v[0]), text(v[1]))
	case "endsWith":
		return strings.HasSuffix(text(v[0]), text(v[1]))
	case "contains":
		return strings.Contains(text(v[0]), text(v[1]))
	case "typeOf":
		return typeOf(v[0])
	case "daysBetween":
		to := toDate(v[1])
		return intNumber(dayNumber(to) - dayNumber(toDate(v[0])))
	case "yearsBetween":
		return intNumber(wholeYears(toDate(v[0]), toDate(v[1])))
	}
	if strings.HasPrefix(op, "x-") {
		return callCustom(op, v, s)
	}
	fail("unknown operator %s", op)
	return nil
}

// collection evaluates all, some, none, sum, map and filter. The second argument is evaluated for
// every element with `item` bound: there is no short-circuit, so errors are deterministic.
func collection(op string, args []any, s *scope, depth int) any {
	var list []any
	switch x := evAt(args[0], s, depth).(type) {
	case nil:
	case []any:
		list = x
	default:
		fail("%s expects a list", op)
	}
	results := make([]any, len(list))
	inner := s.with("item", nil)
	for i, x := range list {
		inner.vars["item"] = x
		results[i] = evAt(args[1], inner, depth)
	}
	switch op {
	case "map":
		return results
	case "sum":
		total := decimalZero
		for _, r := range results {
			sum, err := total.add(num(r))
			total = arithmetic(op, sum, err)
		}
		return total
	}
	count := 0
	kept := []any{}
	for i, r := range results {
		if boolean(r) {
			count++
			kept = append(kept, list[i])
		}
	}
	switch op {
	case "filter":
		return kept
	case "all":
		return count == len(list)
	case "some":
		return count > 0
	}
	return count == 0
}

// EvaluateExpression evaluates one expression and returns a plain JSON value: nil, bool, string,
// json.Number, []any or *Object. env binds the variable roots (data, original, actor, ctx, params,
// item, value, field, arg; any other root is null), functions is a function table as a manifest
// carries it (name to {params, body}) and operators are the host's custom operators; each may be
// nil. The error is an *EvalError when the expression could not be evaluated.
func EvaluateExpression(expr any, env map[string]any, functions any, operators Operators) (any, error) {
	e, err := normalize(expr)
	if err != nil {
		return nil, err
	}
	roots, err := normalize(env)
	if err != nil {
		return nil, err
	}
	table, err := normalize(functions)
	if err != nil {
		return nil, err
	}
	return evaluateExpression(e, asObj(roots), asObj(table), operators)
}

func evaluateExpression(expr any, env, functions *Object, operators Operators) (result any, err error) {
	vars := make(map[string]any, env.Len())
	env.each(func(k string, v any) { vars[k] = v })
	if failure := try(func() { result = plain(ev(expr, &scope{vars: vars, functions: functions, operators: operators})) }); failure != nil {
		return nil, failure
	}
	return result, nil
}
