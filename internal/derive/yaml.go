package derive

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
	rulecascade "rules.sdods.com/go"
)

// ------------------------------------------------------------------ values
//
// Documents are held as JSON values: *rulecascade.Object (members in the order they were written),
// []any, string, bool, nil and json.Number. A json.Number whose text has no '.', 'e' or 'E' is a
// whole number (a Python int); any other is a double (a Python float). The distinction matters:
// minLength: 2.0 is not a count.

var maxSafe = new(big.Int).Lsh(big.NewInt(1), 53)

// isWhole reports whether a number is a whole number, as a Python int is.
func isWhole(n json.Number) bool { return !strings.ContainsAny(string(n), ".eE") }

// canonNum is canon_num of the reference: the number in plain decimal notation, with no exponent
// and no trailing zeros. A double is first reduced to the shortest text that identifies it
// (Python's repr), and so is a whole number beyond 2^53.
func canonNum(n json.Number) string {
	text := string(n)
	if isWhole(n) {
		whole, ok := new(big.Int).SetString(strings.TrimPrefix(text, "+"), 10)
		if ok && new(big.Int).Abs(whole).Cmp(maxSafe) <= 0 {
			return whole.String()
		}
		if ok {
			f, _ := new(big.Float).SetInt(whole).Float64()
			text = strconv.FormatFloat(f, 'e', -1, 64)
		}
	} else if f, err := strconv.ParseFloat(text, 64); err == nil {
		text = strconv.FormatFloat(f, 'e', -1, 64)
	}
	canonical, err := rulecascade.Canonical(json.Number(text))
	if err != nil {
		return text
	}
	return canonical
}

// wholeText is how Python's str() writes a whole number.
func wholeText(n json.Number) string {
	if whole, ok := new(big.Int).SetString(strings.TrimPrefix(string(n), "+"), 10); ok {
		return whole.String()
	}
	return string(n)
}

// floatValue is the double a number stands for.
func floatValue(n json.Number) float64 {
	f, _ := strconv.ParseFloat(string(n), 64)
	return f
}

// normalize turns a value built by a Go program (map[string]any, float64, int ...) into the model
// above. A map[string]any has no order; its keys are taken in sorted order.
func normalize(v any) (any, error) {
	switch x := v.(type) {
	case nil, bool, string:
		return x, nil
	case json.Number:
		if _, err := strconv.ParseFloat(string(x), 64); err != nil {
			return nil, fmt.Errorf("%q is not a number", string(x))
		}
		return x, nil
	case float64:
		return floatNumber(x)
	case float32:
		return floatNumber(float64(x))
	case int:
		return json.Number(strconv.FormatInt(int64(x), 10)), nil
	case int8:
		return json.Number(strconv.FormatInt(int64(x), 10)), nil
	case int16:
		return json.Number(strconv.FormatInt(int64(x), 10)), nil
	case int32:
		return json.Number(strconv.FormatInt(int64(x), 10)), nil
	case int64:
		return json.Number(strconv.FormatInt(x, 10)), nil
	case uint:
		return json.Number(strconv.FormatUint(uint64(x), 10)), nil
	case uint8:
		return json.Number(strconv.FormatUint(uint64(x), 10)), nil
	case uint16:
		return json.Number(strconv.FormatUint(uint64(x), 10)), nil
	case uint32:
		return json.Number(strconv.FormatUint(uint64(x), 10)), nil
	case uint64:
		return json.Number(strconv.FormatUint(x, 10)), nil
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			n, err := normalize(item)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	case *rulecascade.Object:
		out := rulecascade.NewObject()
		for _, k := range x.Keys() {
			item, _ := x.Get(k)
			n, err := normalize(item)
			if err != nil {
				return nil, err
			}
			out.Set(k, n)
		}
		return out, nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := rulecascade.NewObject()
		for _, k := range keys {
			n, err := normalize(x[k])
			if err != nil {
				return nil, err
			}
			out.Set(k, n)
		}
		return out, nil
	}
	return nil, fmt.Errorf("a value of type %T is not a JSON value", v)
}

func floatNumber(f float64) (any, error) {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, errors.New("infinity and NaN are not JSON numbers")
	}
	return json.Number(strconv.FormatFloat(f, 'e', -1, 64)), nil
}

// ------------------------------------------------------------------ reading files

// readFile parses a YAML or JSON file as tools/rulecheck.py reads it: JSON when the name ends in
// .json, otherwise YAML by the YAML 1.2 core schema (`no` and `on` are strings, `012` is twelve).
func readFile(path string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(path, ".json") {
		v, err := rulecascade.ParseJSON(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", path, err)
		}
		return v, nil
	}
	v, err := parseYAML(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	return v, nil
}

var (
	coreNull  = regexp.MustCompile(`^(?:~|null|Null|NULL|)$`)
	coreBool  = regexp.MustCompile(`^(?:true|True|TRUE|false|False|FALSE)$`)
	coreInt   = regexp.MustCompile(`^(?:[-+]?[0-9]+|0o[0-7]+|0x[0-9a-fA-F]+)$`)
	coreFloat = regexp.MustCompile(`^(?:[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)(?:[eE][-+]?[0-9]+)?|[-+]?\.(?:inf|Inf|INF)|\.(?:nan|NaN|NAN))$`)
)

// parseYAML reads one YAML document as the JSON value a YAML 1.2 core-schema parser produces.
func parseYAML(data []byte) (any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document, second yaml.Node
	if err := decoder.Decode(&document); err == io.EOF {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if err := decoder.Decode(&second); err == nil {
		return nil, fmt.Errorf("line %d: expected a single document", second.Line)
	} else if err != io.EOF {
		return nil, err
	}
	return yamlValue(&document, 0)
}

func yamlValue(node *yaml.Node, depth int) (any, error) {
	if depth > 1000 {
		return nil, errors.New("the document is nested too deeply")
	}
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) == 0 {
			return nil, nil
		}
		return yamlValue(node.Content[0], depth+1)
	case yaml.AliasNode:
		return yamlValue(node.Alias, depth+1)
	case yaml.SequenceNode:
		list := make([]any, 0, len(node.Content))
		for _, item := range node.Content {
			v, err := yamlValue(item, depth+1)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		return list, nil
	case yaml.MappingNode:
		object := rulecascade.NewObject()
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, err := yamlValue(node.Content[i], depth+1)
			if err != nil {
				return nil, err
			}
			name, err := keyText(key)
			if err != nil {
				return nil, fmt.Errorf("line %d: %v", node.Content[i].Line, err)
			}
			if _, repeated := object.Get(name); repeated {
				return nil, fmt.Errorf("line %d: duplicate key %s", node.Content[i].Line, pyRepr(name))
			}
			v, err := yamlValue(node.Content[i+1], depth+1)
			if err != nil {
				return nil, err
			}
			object.Set(name, v)
		}
		return object, nil
	case yaml.ScalarNode:
		return yamlScalar(node)
	}
	return nil, fmt.Errorf("line %d: unsupported YAML node", node.Line)
}

func yamlScalar(node *yaml.Node) (any, error) {
	text := node.Value
	if node.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return text, nil
	}
	if node.Style&yaml.TaggedStyle != 0 && node.Tag == "!!str" {
		return text, nil
	}
	switch {
	case coreNull.MatchString(text):
		return nil, nil
	case coreBool.MatchString(text):
		return text[0] == 't' || text[0] == 'T', nil
	case coreInt.MatchString(text):
		var n *big.Int
		var ok bool
		switch {
		case strings.HasPrefix(text, "0o"):
			n, ok = new(big.Int).SetString(text[2:], 8)
		case strings.HasPrefix(text, "0x"):
			n, ok = new(big.Int).SetString(text[2:], 16)
		default:
			n, ok = new(big.Int).SetString(strings.TrimPrefix(text, "+"), 10)
		}
		if !ok {
			return nil, fmt.Errorf("line %d: cannot read the number %s", node.Line, text)
		}
		if new(big.Int).Abs(n).Cmp(maxSafe) > 0 {
			if f, _ := new(big.Float).SetInt(n).Float64(); math.IsInf(f, 0) {
				return nil, fmt.Errorf("line %d: %s is outside the range of a double", node.Line, text)
			}
		}
		return json.Number(n.String()), nil
	case coreFloat.MatchString(text):
		lower := strings.ToLower(text)
		f, _ := strconv.ParseFloat(text, 64)
		if strings.Contains(lower, "inf") || strings.Contains(lower, "nan") || math.IsInf(f, 0) {
			return nil, fmt.Errorf("line %d: %s is not a number JSON can carry", node.Line, lower)
		}
		return json.Number(strconv.FormatFloat(f, 'e', -1, 64)), nil
	}
	return text, nil
}

// keyText is the member name a mapping key gives: a key that is not a string is written the way
// JSON writes its value, so `1:` is "1" and `true:` is "true".
func keyText(key any) (string, error) {
	switch x := key.(type) {
	case string:
		return x, nil
	case nil:
		return "null", nil
	case bool:
		return strconv.FormatBool(x), nil
	case json.Number:
		if isWhole(x) {
			return wholeText(x), nil
		}
		return floatText(floatValue(x)), nil
	}
	return "", errors.New("a key must be a scalar")
}

// floatText is Python's repr of a double: the shortest digits that identify it, in plain notation
// with at least one decimal between 1e-4 and 1e16 and with an exponent elsewhere.
func floatText(f float64) string {
	text := strconv.FormatFloat(f, 'e', -1, 64)
	if exponent, _ := strconv.Atoi(text[strings.IndexByte(text, 'e')+1:]); exponent >= -4 && exponent < 16 {
		if text = strconv.FormatFloat(f, 'f', -1, 64); !strings.Contains(text, ".") {
			text += ".0"
		}
	}
	return text
}

// ------------------------------------------------------------------ writing YAML
//
// A small emitter, the port of the one in tools/openapi_rules.py: the output is laid out like the
// hand-written examples (one rule per block, expressions on one line while they fit), is the same
// on every run, and quotes every scalar that YAML 1.1 and YAML 1.2 parsers read differently
// (specification 12).

const width = 120

// how many levels below a top-level key are always block style (default 1)
var blockLevels = map[string]int{"entities": 2, "messages": 2}

var (
	plainText         = regexp.MustCompile(`^[A-Za-z0-9_/$](?: ?[A-Za-z0-9_/$.+()@'-])*$`)
	yaml12NotAString  = regexp.MustCompile(`^(?:~|null|Null|NULL|true|True|TRUE|false|False|FALSE|[-+]?[0-9]+|0o[0-7]+|0x[0-9a-fA-F]+|[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)(?:[eE][-+]?[0-9]+)?|[-+]?\.(?:inf|Inf|INF)|\.(?:nan|NaN|NAN))$`)
	yaml11Resolutions = []*regexp.Regexp{ // the implicit resolvers of PyYAML's SafeLoader
		regexp.MustCompile(`^(?:yes|Yes|YES|no|No|NO|true|True|TRUE|false|False|FALSE|on|On|ON|off|Off|OFF)$`),
		regexp.MustCompile(`^(?:~|null|Null|NULL|)$`),
		regexp.MustCompile(`^(?:[-+]?(?:[0-9][0-9_]*)\.[0-9_]*(?:[eE][-+][0-9]+)?|\.[0-9][0-9_]*(?:[eE][-+][0-9]+)?` +
			`|[-+]?[0-9][0-9_]*(?::[0-5]?[0-9])+\.[0-9_]*|[-+]?\.(?:inf|Inf|INF)|\.(?:nan|NaN|NAN))$`),
		regexp.MustCompile(`^(?:[-+]?0b[0-1_]+|[-+]?0[0-7_]+|[-+]?(?:0|[1-9][0-9_]*)|[-+]?0x[0-9a-fA-F_]+` +
			`|[-+]?[1-9][0-9_]*(?::[0-5]?[0-9])+)$`),
		regexp.MustCompile(`^(?:[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]` +
			`|[0-9][0-9][0-9][0-9]-[0-9][0-9]?-[0-9][0-9]?(?:[Tt]|[ \t]+)[0-9][0-9]?:[0-9][0-9]:[0-9][0-9](?:\.[0-9]*)?` +
			`(?:[ \t]*(?:Z|[-+][0-9][0-9]?(?::[0-9][0-9])?))?)$`),
		regexp.MustCompile(`^(?:<<)$`),
		regexp.MustCompile(`^(?:=)$`),
		regexp.MustCompile(`^(?:!|&|\*)$`),
	}
)

// isPlain reports whether the unquoted text is the same string to a YAML 1.1 and to a YAML 1.2
// parser.
func isPlain(text string) bool {
	if !plainText.MatchString(text) || yaml12NotAString.MatchString(text) {
		return false
	}
	for _, re := range yaml11Resolutions {
		if re.MatchString(text) {
			return false
		}
	}
	return true
}

func quoted(text string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range text {
		switch {
		case c == '\\':
			b.WriteString(`\\`)
		case c == '"':
			b.WriteString(`\"`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 0x20 || (c >= 0x7F && c <= 0xA0) || c == 0x2028 || c == 0x2029 || c == 0xFEFF ||
			(c >= 0xD800 && c <= 0xDFFF) || c >= 0xFFFE:
			switch {
			case c <= 0xFF:
				fmt.Fprintf(&b, `\x%02X`, c)
			case c <= 0xFFFF:
				fmt.Fprintf(&b, `\u%04X`, c)
			default:
				fmt.Fprintf(&b, `\U%08X`, c)
			}
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func scalarText(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		if x {
			return "true"
		}
		return "false"
	case json.Number:
		return canonNum(x)
	case string:
		if isPlain(x) {
			return x
		}
		return quoted(x)
	}
	return quoted(fmt.Sprint(v))
}

func isContainer(v any) bool {
	switch v.(type) {
	case *rulecascade.Object, []any:
		return true
	}
	return false
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case *rulecascade.Object:
		return x.Len() == 0
	case []any:
		return len(x) == 0
	}
	return false
}

// flow writes a value on one line.
func flow(v any) string {
	switch x := v.(type) {
	case *rulecascade.Object:
		if x.Len() == 0 {
			return "{}"
		}
		parts := make([]string, 0, x.Len())
		for _, k := range x.Keys() {
			item, _ := x.Get(k)
			parts = append(parts, scalarText(k)+": "+flow(item))
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	case []any:
		parts := make([]string, len(x))
		nested := false
		for i, item := range x {
			parts[i] = flow(item)
			nested = nested || isContainer(item)
		}
		if nested {
			return "[ " + strings.Join(parts, ", ") + " ]"
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return scalarText(v)
}

// lines writes `lead` ("key:" or "-") and its value. The first `block` levels are block style;
// below them a value is written on one line when it fits.
func lines(lead string, v any, indent, block int) []string {
	pad := strings.Repeat(" ", indent)
	oneLine := pad + lead + " " + flow(v)
	if !isContainer(v) || isEmpty(v) || (block <= 0 && utf8.RuneCountInString(oneLine) <= width) {
		return []string{oneLine}
	}
	if object, ok := v.(*rulecascade.Object); ok {
		var out []string
		for _, k := range object.Keys() {
			item, _ := object.Get(k)
			out = append(out, lines(scalarText(k)+":", item, indent+2, block-1)...)
		}
		if lead == "-" { // the first key shares the line of the dash
			return append([]string{pad + "- " + out[0][indent+2:]}, out[1:]...)
		}
		return append([]string{pad + lead}, out...)
	}
	out := []string{pad + lead}
	for _, item := range v.([]any) {
		out = append(out, lines("-", item, indent+2, block-1)...)
	}
	return out
}

// toYAML writes a ruleset. The header lines become comments at the top.
func toYAML(ruleset *rulecascade.Object, header []string) string {
	var out []string
	for _, line := range header {
		out = append(out, strings.TrimRight("# "+line, " \t\n\r\x0b\x0c"))
	}
	for i, key := range ruleset.Keys() {
		value, _ := ruleset.Get(key)
		if i > 0 && key != "kind" {
			out = append(out, "")
		}
		if list, ok := value.([]any); ok && (key == "rules" || key == "tests") && len(list) > 0 {
			out = append(out, key+":")
			for n, item := range list {
				if n > 0 {
					out = append(out, "")
				}
				out = append(out, lines("-", item, 2, 1)...)
			}
			continue
		}
		level, ok := blockLevels[key]
		if !ok {
			level = 1
		}
		out = append(out, lines(scalarText(key)+":", value, 0, level)...)
	}
	return strings.Join(out, "\n") + "\n"
}
