package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	rulecascade "rulescascade.com/go"
)

// Reading YAML (specification section 12).
//
// A ruleset is a JSON value and YAML is one way to write it. yaml.v3 is used only to parse the
// structure of the file; what each plain scalar means is decided here by the YAML 1.2 core schema,
// so `no`, `on`, `012` and `2026-10-03` never become what a YAML 1.1 parser would make of them.
// A plain scalar that a YAML 1.1 parser reads differently, and everything else section 12
// forbids, is reported as a YAML_NOT_PORTABLE problem. This mirrors tools/rulecheck.py.

// reading is how a parser reads a plain scalar: the kind of value and the value itself, which is
// nil, bool, *big.Int, float64 or string.
type reading struct {
	kind  string // null, bool, int, float, str, or another name for what a ruleset cannot hold
	value any
}

var (
	// YAML 1.2 core schema
	coreNull  = regexp.MustCompile(`^(?:~|null|Null|NULL|)$`)
	coreBool  = regexp.MustCompile(`^(?:true|True|TRUE|false|False|FALSE)$`)
	coreInt   = regexp.MustCompile(`^(?:[-+]?[0-9]+|0o[0-7]+|0x[0-9a-fA-F]+)$`)
	coreFloat = regexp.MustCompile(`^(?:[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)(?:[eE][-+]?[0-9]+)?|[-+]?\.(?:inf|Inf|INF)|\.(?:nan|NaN|NAN))$`)

	// YAML 1.1 as PyYAML and SnakeYAML resolve it
	oldBool  = regexp.MustCompile(`^(?:yes|Yes|YES|no|No|NO|true|True|TRUE|false|False|FALSE|on|On|ON|off|Off|OFF)$`)
	oldFloat = regexp.MustCompile(`^(?:[-+]?(?:[0-9][0-9_]*)\.[0-9_]*(?:[eE][-+][0-9]+)?|\.[0-9][0-9_]*(?:[eE][-+][0-9]+)?` +
		`|[-+]?[0-9][0-9_]*(?::[0-5]?[0-9])+\.[0-9_]*|[-+]?\.(?:inf|Inf|INF)|\.(?:nan|NaN|NAN))$`)
	oldInt = regexp.MustCompile(`^(?:[-+]?0b[0-1_]+|[-+]?0[0-7_]+|[-+]?(?:0|[1-9][0-9_]*)|[-+]?0x[0-9a-fA-F_]+` +
		`|[-+]?[1-9][0-9_]*(?::[0-5]?[0-9])+)$`)
	oldTimestamp = regexp.MustCompile(`^(?:[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]` +
		`|[0-9][0-9][0-9][0-9]-[0-9][0-9]?-[0-9][0-9]?(?:[Tt]|[ \t]+)[0-9][0-9]?:[0-9][0-9]:[0-9][0-9](?:\.[0-9]*)?` +
		`(?:[ \t]*(?:Z|[-+][0-9][0-9]?(?::[0-9][0-9])?))?)$`)
)

func isTrue(text string) bool {
	return strings.EqualFold(text, "true") || strings.EqualFold(text, "yes") || strings.EqualFold(text, "on")
}

// infinite reports whether a float scalar is one of the .inf and .nan forms.
func infinite(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "inf") || strings.Contains(lower, "nan")
}

// readCore reads a plain scalar by the YAML 1.2 core schema.
func readCore(text string) reading {
	switch {
	case coreNull.MatchString(text):
		return reading{"null", nil}
	case coreBool.MatchString(text):
		return reading{"bool", isTrue(text)}
	case coreInt.MatchString(text):
		n, ok := new(big.Int).SetString(strings.TrimPrefix(text, "+"), 10)
		if strings.HasPrefix(text, "0o") {
			n, ok = new(big.Int).SetString(text[2:], 8)
		} else if strings.HasPrefix(text, "0x") {
			n, ok = new(big.Int).SetString(text[2:], 16)
		}
		if !ok {
			return reading{"error", text}
		}
		return reading{"int", n}
	case coreFloat.MatchString(text):
		if infinite(text) {
			return reading{"error", "infinity and NaN are not JSON numbers"}
		}
		f, _ := strconv.ParseFloat(text, 64) // infinite when it is out of range
		return reading{"float", f}
	}
	return reading{"str", text}
}

// readOld reads a plain scalar the way a YAML 1.1 parser does: yes and no are booleans, 012 is
// octal, 1_000 and 12:30 are numbers, 2026-10-03 is a date and 1e3 is a string.
func readOld(text string) reading {
	sign := func(digits string) (unsigned string, negative bool) {
		if strings.HasPrefix(digits, "-") || strings.HasPrefix(digits, "+") {
			return digits[1:], digits[0] == '-'
		}
		return digits, false
	}
	switch {
	case oldBool.MatchString(text):
		return reading{"bool", isTrue(text)}
	case oldFloat.MatchString(text):
		digits, negative := sign(strings.ToLower(strings.ReplaceAll(text, "_", "")))
		f, base := 0.0, 1.0
		switch {
		case digits == ".inf":
			f = math.Inf(1)
		case digits == ".nan":
			return reading{"float", math.NaN()}
		default: // sexagesimal when there are colons
			parts := strings.Split(digits, ":")
			for i := len(parts) - 1; i >= 0; i-- {
				part, err := strconv.ParseFloat(parts[i], 64)
				if err != nil && !math.IsInf(part, 0) {
					return reading{"error", text}
				}
				f, base = f+part*base, base*60
			}
		}
		if negative {
			f = -f
		}
		return reading{"float", f}
	case oldInt.MatchString(text):
		digits, negative := sign(strings.ReplaceAll(text, "_", ""))
		n, ok := new(big.Int), true
		switch {
		case digits == "0":
		case strings.HasPrefix(digits, "0b"):
			_, ok = n.SetString(digits[2:], 2)
		case strings.HasPrefix(digits, "0x"):
			_, ok = n.SetString(digits[2:], 16)
		case strings.HasPrefix(digits, "0"):
			_, ok = n.SetString(digits, 8)
		default: // sexagesimal when there are colons
			for _, part := range strings.Split(digits, ":") {
				p, valid := new(big.Int).SetString(part, 10)
				ok = ok && valid
				if valid {
					n.Add(n.Mul(n, big.NewInt(60)), p)
				}
			}
		}
		if !ok {
			return reading{"error", text}
		}
		if negative {
			n.Neg(n)
		}
		return reading{"int", n}
	case text == "<<":
		return reading{"merge", text}
	case coreNull.MatchString(text):
		return reading{"null", nil}
	case oldTimestamp.MatchString(text):
		return reading{"timestamp", text}
	case text == "=":
		return reading{"value", text}
	}
	return reading{"str", text}
}

// sameReading reports whether two parsers read the same value from a scalar.
func sameReading(a, b reading) bool {
	if a.kind != b.kind {
		return false
	}
	if x, ok := a.value.(*big.Int); ok {
		return x.Cmp(b.value.(*big.Int)) == 0
	}
	return a.value == b.value
}

type yamlReader struct {
	problems []rulecascade.Problem
	// budget is how many more nodes may be read: aliases repeat what they point to, and a short
	// document of nested aliases would otherwise expand without end.
	budget int
}

// aliasBudget is how many nodes a document may expand to: ten times the nodes it is written with.
func aliasBudget(document *yaml.Node) int {
	return 10*countNodes(document) + 1000
}

// countNodes counts the nodes of a document as written, without following aliases.
func countNodes(node *yaml.Node) int {
	n := 1
	for _, child := range node.Content {
		n += countNodes(child)
	}
	return n
}

func (r *yamlReader) report(format string, args ...any) {
	r.problems = append(r.problems, rulecascade.Problem{Code: "YAML_NOT_PORTABLE", Message: fmt.Sprintf(format, args...)})
}

// parseYAML reads one YAML document as the JSON value a YAML 1.2 core-schema parser produces, and
// lists what makes the file not portable. The value is still returned when there are problems, so
// that a linter can go on and report everything else.
func parseYAML(data []byte) (any, []rulecascade.Problem, error) {
	r := &yamlReader{}
	if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		r.report("the file starts with a byte order mark")
		data = data[3:]
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document, second yaml.Node
	if err := decoder.Decode(&document); err == io.EOF {
		return nil, r.problems, nil // an empty file is null
	} else if err != nil {
		return nil, nil, err
	}
	if err := decoder.Decode(&second); err == nil {
		r.report("line %d: one document per file", second.Line)
	} else if err != io.EOF {
		return nil, nil, err
	}
	r.budget = aliasBudget(&document)
	value, err := r.value(&document)
	return value, r.problems, err
}

func (r *yamlReader) value(node *yaml.Node) (any, error) {
	if r.budget--; r.budget < 0 {
		return nil, fmt.Errorf("line %d: the aliases of the document expand too much", node.Line)
	}
	if node.Anchor != "" {
		r.report("line %d: anchors are not allowed in a ruleset", node.Line)
	}
	if node.Kind != yaml.ScalarNode && node.Kind != yaml.AliasNode && node.Style&yaml.TaggedStyle != 0 {
		r.report("line %d: tags are not allowed in a ruleset", node.Line)
	}
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) != 1 {
			return nil, nil
		}
		return r.value(node.Content[0])
	case yaml.AliasNode:
		r.report("line %d: aliases are not allowed in a ruleset", node.Line)
		return r.value(node.Alias)
	case yaml.SequenceNode:
		list := make([]any, 0, len(node.Content))
		for _, item := range node.Content {
			v, err := r.value(item)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		return list, nil
	case yaml.MappingNode:
		object := rulecascade.NewObject()
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, item := node.Content[i], node.Content[i+1]
			if key.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("line %d: a key must be a scalar", key.Line)
			}
			if key.Anchor != "" {
				r.report("line %d: anchors are not allowed in a ruleset", key.Line)
			}
			name, err := r.scalar(key) // also reports what is not portable about the key
			if err != nil {
				return nil, err
			}
			if key.Value == "<<" && plain(key) {
				r.report("line %d: merge keys (<<) are not allowed in a ruleset", key.Line)
			}
			if _, repeated := object.Get(keyText(name)); repeated {
				r.report("line %d: the key %q is repeated", key.Line, key.Value)
			}
			v, err := r.value(item)
			if err != nil {
				return nil, err
			}
			object.Set(keyText(name), v)
		}
		return object, nil
	case yaml.ScalarNode:
		return r.scalar(node)
	}
	return nil, fmt.Errorf("line %d: unsupported YAML node", node.Line)
}

// plain reports whether a scalar is written without quotes and is not a block scalar.
func plain(node *yaml.Node) bool {
	return node.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) == 0
}

// scalar resolves a scalar by the YAML 1.2 core schema. A plain scalar is portable when YAML 1.1
// parsers and YAML 1.2 parsers read the same value from it.
func (r *yamlReader) scalar(node *yaml.Node) (any, error) {
	text := node.Value
	if node.Style&yaml.TaggedStyle != 0 {
		r.report("line %d: tags are not allowed in a ruleset", node.Line)
	}
	if !plain(node) {
		return text, nil
	}
	core := readCore(text)
	if !sameReading(readOld(text), core) {
		r.report("line %d: quote '%s'; YAML parsers disagree about unquoted %s", node.Line, text, text)
	}
	if node.Style&yaml.TaggedStyle != 0 && node.Tag == "!!str" {
		return text, nil
	}
	switch value := core.value.(type) {
	case *big.Int:
		return jsonNumber(value.String())
	case float64:
		if math.IsInf(value, 0) {
			return nil, fmt.Errorf("line %d: the number %s is out of range", node.Line, text)
		}
	}
	if core.kind == "error" {
		return nil, fmt.Errorf("line %d: %s: %v", node.Line, text, core.value)
	}
	return core.value, nil
}

// keyText is the member name a mapping key gives. JSON has only string keys, so a key that is not
// a string is written the way JSON writes its value: 0x1F is "31" and True is "true".
func keyText(value any) string {
	switch x := value.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return floatText(x)
	}
	return fmt.Sprint(value)
}

// floatText writes a double the way the reference tool shows it: the shortest digits that identify
// it, in plain notation with at least one decimal between 1e-4 and 1e16 and with an exponent
// elsewhere.
func floatText(f float64) string {
	text := strconv.FormatFloat(f, 'e', -1, 64)
	if exponent, _ := strconv.Atoi(text[strings.IndexByte(text, 'e')+1:]); exponent >= -4 && exponent < 16 {
		if text = strconv.FormatFloat(f, 'f', -1, 64); !strings.Contains(text, ".") {
			text += ".0"
		}
	}
	return text
}

func jsonNumber(text string) (any, error) {
	v, err := rulecascade.ParseJSON([]byte(text))
	if err != nil {
		return nil, errors.New("cannot read the number " + text)
	}
	return v, nil
}
