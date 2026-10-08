package rulecascade

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Object is a JSON object that keeps its members in the order they were written.
//
// No result depends on the order of the members of an object, but a manifest or a bundle that comes
// out in the order it was written is easier to read and to compare, so every JSON object this
// package reads or returns is an *Object. The zero value is an empty object.
//
// A manifest is an *Object that a RuleSet keeps: Manifest and Bundle return it, not a copy. A
// change to it applies to the evaluations that follow; changing it while it is being evaluated is
// a data race in the caller. Documents and requests passed in are copied first, so changing them
// afterwards has no effect.
type Object struct {
	keys []string
	vals map[string]any
}

// NewObject returns an empty object.
func NewObject() *Object { return &Object{vals: map[string]any{}} }

// Len returns the number of members.
func (o *Object) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Keys returns the member names in order.
func (o *Object) Keys() []string {
	if o == nil {
		return nil
	}
	return append([]string(nil), o.keys...)
}

// Get returns the value of a member and whether it is present.
func (o *Object) Get(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.vals[key]
	return v, ok
}

// Set adds a member, or replaces its value and keeps its position.
func (o *Object) Set(key string, value any) {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = value
}

// Delete removes a member.
func (o *Object) Delete(key string) {
	if o == nil {
		return
	}
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// MarshalJSON writes the object with its members in order.
func (o *Object) MarshalJSON() ([]byte, error) { return Marshal(o) }

// UnmarshalJSON reads an object and keeps the order of its members.
func (o *Object) UnmarshalJSON(data []byte) error {
	v, err := ParseJSON(data)
	if err != nil {
		return err
	}
	parsed, ok := v.(*Object)
	if !ok {
		return errors.New("rulecascade: not a JSON object")
	}
	*o = *parsed
	return nil
}

// Nil-safe accessors used throughout the package. A missing member and a member of another type
// both give the zero value, which is what dict.get gives the reference implementation.

func (o *Object) get(key string) any {
	if o == nil {
		return nil
	}
	return o.vals[key]
}

func (o *Object) has(key string) bool {
	_, ok := o.Get(key)
	return ok
}

func (o *Object) names() []string {
	if o == nil {
		return nil
	}
	return o.keys
}

func (o *Object) str(key string) string  { s, _ := o.get(key).(string); return s }
func (o *Object) obj(key string) *Object { return asObj(o.get(key)) }
func (o *Object) list(key string) []any  { l, _ := o.get(key).([]any); return l }
func (o *Object) copy() *Object          { c := NewObject(); c.update(o); return c }
func (o *Object) update(other *Object)   { other.each(o.Set) }
func (o *Object) each(fn func(string, any)) {
	for _, k := range o.names() {
		fn(k, o.vals[k])
	}
}

func asObj(v any) *Object { o, _ := v.(*Object); return o }

// ParseJSON reads one JSON value. Objects become *Object, arrays []any and numbers json.Number, so
// neither the order of members nor the text of a number is lost. The functions of this package
// that take such a value read each number as the IEEE 754 double nearest to its text.
func ParseJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("unexpected data after the JSON value")
	}
	return v, nil
}

func parseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err == io.EOF {
		return nil, io.ErrUnexpectedEOF
	}
	if err != nil {
		return nil, err
	}
	switch tok {
	case json.Delim('{'):
		o := NewObject()
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return nil, err
			}
			value, err := parseValue(dec)
			if err != nil {
				return nil, err
			}
			o.Set(key.(string), value)
		}
		_, err := dec.Token()
		return o, err
	case json.Delim('['):
		list := []any{}
		for dec.More() {
			value, err := parseValue(dec)
			if err != nil {
				return nil, err
			}
			list = append(list, value)
		}
		_, err := dec.Token()
		return list, err
	}
	return tok, nil
}

// Marshal writes a JSON value compactly. Unlike encoding/json it keeps the member order of an
// *Object and escapes nothing in a string beyond what JSON requires.
func Marshal(v any) ([]byte, error) { return MarshalIndent(v, "") }

// MarshalIndent is Marshal with one member or element per line, each level indented by indent.
func MarshalIndent(v any, indent string) ([]byte, error) {
	n, err := normalize(v)
	if err != nil {
		return nil, err
	}
	return appendValue(nil, n, indent, 0), nil
}

func appendValue(b []byte, v any, indent string, depth int) []byte {
	newline := func(depth int) {
		if indent != "" {
			b = append(b, '\n')
			b = append(b, strings.Repeat(indent, depth)...)
		}
	}
	switch x := v.(type) {
	case nil:
		return append(b, "null"...)
	case bool:
		return strconv.AppendBool(b, x)
	case string:
		return appendString(b, x)
	case json.Number:
		return append(b, x...)
	case decimal:
		return append(b, x.String()...)
	case []any:
		if len(x) == 0 {
			return append(b, "[]"...)
		}
		b = append(b, '[')
		for i, item := range x {
			if i > 0 {
				b = append(b, ',')
			}
			newline(depth + 1)
			b = appendValue(b, item, indent, depth+1)
		}
		newline(depth)
		return append(b, ']')
	case *Object:
		if x.Len() == 0 {
			return append(b, "{}"...)
		}
		b = append(b, '{')
		for i, k := range x.keys {
			if i > 0 {
				b = append(b, ',')
			}
			newline(depth + 1)
			b = append(appendString(b, k), ':')
			if indent != "" {
				b = append(b, ' ')
			}
			b = appendValue(b, x.vals[k], indent, depth+1)
		}
		newline(depth)
		return append(b, '}')
	}
	panic(fmt.Sprintf("rulecascade: %T is not a JSON value", v))
}

const hexDigits = "0123456789abcdef"

// appendString writes a JSON string with the escapes of specification section 6 and no others:
// encoding/json would also escape <, >, & and U+2028/U+2029, which changes a checksum.
func appendString(b []byte, s string) []byte {
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' {
			continue
		}
		b = append(b, s[start:i]...)
		start = i + 1
		switch c {
		case '"', '\\':
			b = append(b, '\\', c)
		case '\b':
			b = append(b, '\\', 'b')
		case '\f':
			b = append(b, '\\', 'f')
		case '\n':
			b = append(b, '\\', 'n')
		case '\r':
			b = append(b, '\\', 'r')
		case '\t':
			b = append(b, '\\', 't')
		default:
			b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
		}
	}
	return append(append(b, s[start:]...), '"')
}

// jsonValuer is implemented by the result types, which know their own JSON form.
type jsonValuer interface{ jsonValue() *Object }

// normalize turns whatever a caller passes into the values this package works on: nil, bool,
// string, json.Number, []any and *Object. The result shares nothing with the argument.
//
// A map[string]any becomes an *Object with its keys sorted, and every number is read as the
// double nearest to it (see number).
func normalize(v any) (any, error) {
	switch x := v.(type) {
	case nil, bool, string:
		return x, nil
	case json.Number:
		return number(string(x))
	case float64:
		return floatNumber(x)
	case float32:
		return floatNumber(float64(x))
	case int:
		return number(strconv.Itoa(x))
	case int64:
		return number(strconv.FormatInt(x, 10))
	case int32:
		return number(strconv.FormatInt(int64(x), 10))
	case uint64:
		return number(strconv.FormatUint(x, 10))
	case []any:
		list := make([]any, len(x))
		for i, item := range x {
			n, err := normalize(item)
			if err != nil {
				return nil, err
			}
			list[i] = n
		}
		return list, nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		o := NewObject()
		for _, k := range keys {
			n, err := normalize(x[k])
			if err != nil {
				return nil, err
			}
			o.Set(k, n)
		}
		return o, nil
	case *Object:
		if x == nil {
			return nil, nil
		}
		o := NewObject()
		for _, k := range x.keys {
			n, err := normalize(x.vals[k])
			if err != nil {
				return nil, err
			}
			o.Set(k, n)
		}
		return o, nil
	case jsonValuer:
		return normalize(x.jsonValue())
	}
	// anything else encoding/json can write: structs, typed maps and slices, other integer types
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("rulecascade: %T is not a JSON value: %w", v, err)
	}
	parsed, err := ParseJSON(data)
	if err != nil {
		return nil, err
	}
	return normalize(parsed)
}

// number reads a JSON number the way every mainstream parser does (specification section 4.2):
// it is the IEEE 754 double nearest to what was written, and the engine computes with the shortest
// decimal that identifies that double. So 9007199254740993 is 9007199254740992, a literal below
// the smallest double is zero, and a number too large for a double is not accepted.
//
// This is the one place where numbers enter the package: JSON text, YAML numbers read by the
// command and Go numbers handed to the library all pass through it.
func number(text string) (any, error) {
	if !validNumber(text) {
		return nil, fmt.Errorf("rulecascade: %q is not a JSON number", text)
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(f, 0) {
		return nil, fmt.Errorf("rulecascade: the number %.40s is outside the range of a double", text)
	}
	return json.Number(formatFloat(f)), nil
}

func floatNumber(f float64) (any, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, errors.New("rulecascade: NaN and infinity are not JSON numbers")
	}
	return json.Number(formatFloat(f)), nil
}

// formatFloat gives the shortest text that reads back as the same double.
func formatFloat(f float64) string {
	if f == 0 {
		return "0"
	}
	format := byte('f')
	if abs := math.Abs(f); abs < 1e-6 || abs >= 1e21 {
		format = 'e'
	}
	return strconv.FormatFloat(f, format, -1, 64)
}

// validNumber reports whether s follows the JSON number grammar.
func validNumber(s string) bool {
	digits := func() bool {
		n := 0
		for len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
			s, n = s[1:], n+1
		}
		return n > 0
	}
	s = strings.TrimPrefix(s, "-")
	if strings.HasPrefix(s, "0") {
		s = s[1:]
	} else if !digits() {
		return false
	}
	if strings.HasPrefix(s, ".") {
		if s = s[1:]; !digits() {
			return false
		}
	}
	if len(s) > 0 && (s[0] == 'e' || s[0] == 'E') {
		s = s[1:]
		if len(s) > 0 && (s[0] == '+' || s[0] == '-') {
			s = s[1:]
		}
		if !digits() {
			return false
		}
	}
	return s == ""
}

// clone copies a value of the internal model.
func clone(v any) any {
	switch x := v.(type) {
	case []any:
		list := make([]any, len(x))
		for i, item := range x {
			list[i] = clone(item)
		}
		return list
	case *Object:
		o := NewObject()
		x.each(func(k string, v any) { o.Set(k, clone(v)) })
		return o
	}
	return v
}
