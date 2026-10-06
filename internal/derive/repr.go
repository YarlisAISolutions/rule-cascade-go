package derive

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	rulecascade "rulescascade.com/go"
)

// pyRepr writes a value the way Python's repr does, for the messages that quote a value from the
// schema: 'date', ['string', 5], {'level': 'organization', 'id': 'acme'}, True, None.
func pyRepr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case json.Number:
		if isWhole(x) {
			return wholeText(x)
		}
		return floatText(floatValue(x))
	case string:
		return reprString(x)
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = pyRepr(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *rulecascade.Object:
		parts := make([]string, 0, x.Len())
		for _, k := range x.Keys() {
			item, _ := x.Get(k)
			parts = append(parts, reprString(k)+": "+pyRepr(item))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}

// pyStr is Python's str: a string as it is, anything else as repr writes it.
func pyStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return pyRepr(v)
}

func reprString(s string) string {
	quote := '\''
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteRune(quote)
	for _, c := range s {
		switch {
		case c == quote || c == '\\':
			b.WriteRune('\\')
			b.WriteRune(c)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c == ' ' || (unicode.IsPrint(c) && c != 0xAD):
			b.WriteRune(c)
		case c <= 0xFF:
			fmt.Fprintf(&b, `\x%02x`, c)
		case c <= 0xFFFF:
			fmt.Fprintf(&b, `\u%04x`, c)
		default:
			fmt.Fprintf(&b, `\U%08x`, c)
		}
	}
	b.WriteRune(quote)
	return b.String()
}
