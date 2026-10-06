package rulecascade

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Value semantics shared by loading and evaluation: numbers, equality, canonical JSON, rendering.
//
// Inside the package a number is either a json.Number, which is a number that came in from a
// document or a request (the shortest decimal of the double nearest to what was written, see
// number in json.go), or a decimal, which is a number an expression computed. Nothing is rounded
// inside an expression; every number is rounded when it leaves the engine.

// maxDouble is the largest IEEE 754 double. A number beyond it cannot leave the engine.
var maxDouble, _ = parseDecimal("1.7976931348623157e308")

func isNum(v any) bool {
	switch v.(type) {
	case json.Number, decimal:
		return true
	}
	return false
}

// dec gives the exact decimal value of a number.
func dec(v any) decimal {
	switch x := v.(type) {
	case decimal:
		return x
	case json.Number:
		if d, ok := parseDecimal(string(x)); ok {
			return d
		}
	}
	return decimalZero
}

func intNumber(n int) json.Number { return json.Number(strconv.Itoa(n)) }

// canonNum is a number in plain decimal notation without an exponent or trailing zeros.
func canonNum(v any) string { return dec(v).String() }

// Canonical returns the canonical JSON of a value (specification section 6): object members sorted
// by key in UTF-16 code unit order, no whitespace, minimal string escapes and canonical numbers.
// The checksum of a ruleset is the SHA-256 of this text.
func Canonical(v any) (string, error) {
	n, err := normalize(v)
	if err != nil {
		return "", err
	}
	return canonical(n), nil
}

func canonical(v any) string { return string(appendCanonical(nil, v)) }

func appendCanonical(b []byte, v any) []byte {
	switch x := v.(type) {
	case nil:
		return append(b, "null"...)
	case bool:
		return strconv.AppendBool(b, x)
	case json.Number, decimal:
		return append(b, canonNum(x)...)
	case string:
		return appendString(b, x)
	case []any:
		b = append(b, '[')
		for i, item := range x {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendCanonical(b, item)
		}
		return append(b, ']')
	case *Object:
		keys := append([]string(nil), x.names()...)
		sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
		b = append(b, '{')
		for i, k := range keys {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendCanonical(append(appendString(b, k), ':'), x.vals[k])
		}
		return append(b, '}')
	}
	return b
}

// utf16Less orders strings by UTF-16 code units, which differs from byte order for characters
// outside the Basic Multilingual Plane.
func utf16Less(a, b string) bool {
	x, y := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return len(x) < len(y)
}

// render is how a value appears inside a message or an idempotency key (section 8.1).
func render(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case bool:
		return strconv.FormatBool(x)
	case json.Number, decimal:
		return canonNum(x)
	case string:
		return x
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = render(item)
		}
		return strings.Join(parts, ", ")
	}
	return canonical(v)
}

// plain prepares a value for leaving the engine. Every number, computed or merely passed through,
// is rounded half even to 15 significant digits, the most a JSON number can carry through an
// IEEE 754 double unchanged. One too large for a double is an evaluation error. Containers are
// copied.
func plain(v any) any {
	switch x := v.(type) {
	case json.Number, decimal:
		d := dec(x).round(wirePrecision)
		if (decimal{coef: d.coef, exp: d.exp}).cmp(maxDouble) > 0 {
			fail("number out of range")
		}
		if d.isWhole() {
			return json.Number(d.String())
		}
		if d.adjusted() < -400 { // below the range of a double
			return json.Number("0")
		}
		f, _ := strconv.ParseFloat(d.String(), 64)
		return json.Number(formatFloat(f))
	case []any:
		list := make([]any, len(x))
		for i, item := range x {
			list[i] = plain(item)
		}
		return list
	case *Object:
		o := NewObject()
		x.each(func(k string, v any) { o.Set(k, plain(v)) })
		return o
	}
	return v
}

// Equal reports whether two JSON values are equal by specification section 4.1: numbers compare
// numerically (1 equals 1.0), lists element by element, objects member by member whatever their
// order, and values of different types are never equal.
func Equal(a, b any) bool {
	x, err := normalize(a)
	if err != nil {
		return false
	}
	y, err := normalize(b)
	return err == nil && equal(x, y)
}

func equal(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	case json.Number, decimal:
		return isNum(b) && dec(a).cmp(dec(b)) == 0
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case *Object:
		y, ok := b.(*Object)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for _, k := range x.names() {
			other, ok := y.vals[k]
			if !ok || !equal(x.vals[k], other) {
				return false
			}
		}
		return true
	}
	return false
}

// truthy is the test the reference applies with `or`: null, false, zero and empty values are not.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number, decimal:
		return !dec(x).isZero()
	case []any:
		return len(x) > 0
	case *Object:
		return x.Len() > 0
	}
	return true
}
