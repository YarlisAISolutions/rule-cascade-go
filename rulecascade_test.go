package rulecascade

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, text string) any {
	t.Helper()
	v, err := ParseJSON([]byte(text))
	if err == nil {
		v, err = normalize(v)
	}
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return v
}

// The schema compiled into the package must be the one in the specification, byte for byte.
func TestEmbeddedSchemaIsTheSpecification(t *testing.T) {
	spec, err := os.ReadFile("../../spec/v1/rule-cascade.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(spec, schemaText) {
		t.Error("packages/go/rule-cascade.schema.json differs from spec/v1/rule-cascade.schema.json; copy it again")
	}
}

// The validator interprets the schema itself, so the schema may only use keywords it implements,
// and every pattern in it must be a portable pattern.
func TestSchemaUsesOnlySupportedKeywords(t *testing.T) {
	var walk func(schema any, where string)
	walk = func(schema any, where string) {
		s := asObj(schema)
		if s == nil {
			return
		}
		s.each(func(keyword string, v any) {
			if !schemaKeywords[keyword] {
				t.Errorf("%s: keyword %q is not implemented by schema.go", where, keyword)
			}
			switch keyword {
			case "properties", "patternProperties", "$defs":
				asObj(v).each(func(name string, sub any) {
					if keyword == "patternProperties" && patternProblem(name) != "" {
						t.Errorf("%s: pattern %q is not portable", where, name)
					}
					walk(sub, where+"/"+keyword+"/"+name)
				})
			case "items", "additionalProperties", "propertyNames", "not", "if", "then":
				walk(v, where+"/"+keyword)
			case "anyOf", "allOf":
				for _, sub := range v.([]any) {
					walk(sub, where+"/"+keyword)
				}
			case "pattern":
				if why := patternProblem(v.(string)); why != "" {
					t.Errorf("%s: pattern %q is not portable: %s", where, v, why)
				}
			case "$ref":
				if _, ok := deref(ruleSetSchema(), v.(string)); !ok {
					t.Errorf("%s: %v cannot be resolved", where, v)
				}
			}
		})
	}
	walk(ruleSetSchema(), "#")
}

func TestParseKeepsOrderAndNumbers(t *testing.T) {
	const text = `{"b":1,"a":[1.50,{"z":null,"y":true}],"c":1e2,"b2":"x","big":12345678901234567890123}`
	v, err := ParseJSON([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	o := v.(*Object)
	if got := strings.Join(o.Keys(), ","); got != "b,a,c,b2,big" {
		t.Errorf("keys: %s", got)
	}
	if n, _ := o.Get("big"); n != json.Number("12345678901234567890123") {
		t.Errorf("big: %v", n)
	}
	// ParseJSON keeps the text; everything that uses the value reads a number as the nearest double
	out, err := Marshal(v)
	if err != nil || string(out) != `{"b":1,"a":[1.5,{"z":null,"y":true}],"c":100,"b2":"x","big":1.2345678901234568e+22}` {
		t.Errorf("Marshal: %s %v", out, err)
	}
	indented, _ := MarshalIndent(mustParse(t, `{"a":[1,{}],"b":{"c":[]}}`), "  ")
	if want := "{\n  \"a\": [\n    1,\n    {}\n  ],\n  \"b\": {\n    \"c\": []\n  }\n}"; string(indented) != want {
		t.Errorf("MarshalIndent:\n%s", indented)
	}
	for _, bad := range []string{``, `{`, `{"a":1} x`, `[1,]`, `{"a":1e999}`, `[-1e999]`, `1` + strings.Repeat("0", 400), `NaN`, `[Infinity]`, `nul`} {
		if v, err := ParseJSON([]byte(bad)); err == nil {
			if _, err = normalize(v); err == nil {
				t.Errorf("%q was accepted", bad)
			}
		}
	}
	// a repeated key keeps its first position and its last value
	if got := canonical(mustParse(t, `{"a":1,"b":2,"a":3}`)); got != `{"a":3,"b":2}` {
		t.Errorf("repeated key: %s", got)
	}
}

func TestCanonicalJSON(t *testing.T) {
	// keys are sorted by UTF-16 code units: U+1D49C is the surrogate pair D835 DC9C, which sorts
	// before U+FFFF although its code point is larger
	value := mustParse(t, `{"\uffff":1,"\ud835\udc9c":2,"b":3,"a":{"z":[1.0,-0.0,1e3,0.10,2.5E-3],"A":null},"":true}`)
	want := `{"":true,"a":{"A":null,"z":[1,0,1000,0.1,0.0025]},"b":3,"` + "\U0001d49c" + `":2,"` + "\uffff" + `":1}`
	if got := canonical(value); got != want {
		t.Errorf("canonical:\n got %s\nwant %s", got, want)
	}
	// strings are escaped as JSON requires and no further
	s := "<a href=\"x\">&\\ \b\f\n\r\t \x00\x1f \x7f \u2028\u2029 \u00e9 \U0001f600"
	wantString := `"<a href=\"x\">&\\ \b\f\n\r\t \u0000\u001f ` + "\x7f \u2028\u2029 \u00e9 \U0001f600" + `"`
	if got := canonical(s); got != wantString {
		t.Errorf("string:\n got %s\nwant %s", got, wantString)
	}
	tenth, fifth := 0.1, 0.2
	if got, _ := Canonical(map[string]any{"n": tenth + fifth, "i": 7, "list": []any{int64(1), uint64(2), float32(0.5)}}); got != `{"i":7,"list":[1,2,0.5],"n":0.30000000000000004}` {
		t.Errorf("Go values: %s", got)
	}
	if !Equal(map[string]any{"a": 1.0, "b": []any{"x"}}, mustParse(t, `{"b":["x"],"a":1}`)) || Equal(1, "1") || Equal(true, 1) || Equal(nil, false) {
		t.Error("Equal does not follow section 4.1")
	}
}

func TestNormalizeAcceptsGoValues(t *testing.T) {
	type payload struct {
		Name  string            `json:"name"`
		Tags  []string          `json:"tags"`
		Extra map[string]int    `json:"extra"`
		Notes map[string]string `json:"notes,omitempty"`
	}
	got, err := Canonical(payload{Name: "x", Tags: []string{"a"}, Extra: map[string]int{"n": 1}})
	if err != nil || got != `{"extra":{"n":1},"name":"x","tags":["a"]}` {
		t.Errorf("struct: %s %v", got, err)
	}
	if _, err := Canonical(map[string]any{"f": func() {}}); err == nil {
		t.Error("a function was accepted as a JSON value")
	}
	if _, err := Canonical(json.Number("1_000")); err == nil {
		t.Error("an invalid json.Number was accepted")
	}
}

func TestPortablePatterns(t *testing.T) {
	for _, p := range []string{``, `^a.c$`, `\d+\.\d{2}`, `[A-Za-z0-9_-]+`, `[a\-z]`, `[-a]`, `[a-]`, `(?:ab|c)*?`, `a{2,}`, `a{1000}`,
		`\/`, `[\t-\r]`, `[^\n]`, `^/[A-Za-z0-9_-](?:[A-Za-z0-9_-]|/[A-Za-z0-9_-])*$`, `()`, `(|a)`, `[.^$]`, `x{0}`,
		`^(?:(?:ab){1,25}){1,40}$`, `^(?:a{2,1000})*$`, `(a+)?`, `(a+){1}`, `(a{2,}){0,1}`, `^(?:a{0,900}b{0,900}){1}$`, `^(?:a{1,2}){1,500}b{1,1000}$`,
		`(a{600}){0}`, `((a{10}){10}){10}`, `(a{2}|b{500}){2}`, `(?:[a-z]{1,40}-){1,25}`,
		strings.Repeat("a", 1000), strings.Repeat("\U0001f600", 1000), strings.Repeat(".{1000}", 142)} {
		if why := patternProblem(p); why != "" {
			t.Errorf("%q is portable, got: %s", p, why)
		}
		if _, err := compilePattern(p); err != nil {
			t.Errorf("%q does not compile: %v", p, err)
		}
	}
	for _, p := range []string{`\s`, `\b`, `\x41`, `\u0041`, `\p{L}`, `\1`, `(?=a)`, `(?i)a`, `(?P<n>a)`, `a**`, `a*+`, `a{2}{3}`, `a{,3}`,
		`a{3,1}`, `a{1001}`, `{`, `}`, `]`, `[]`, `[^]`, `[a`, `[[]`, `[a&&b]`, `[z-a]`, `[a-c-e]`, `[\w-a]`, `(a`, `a)`, `*a`, `^*`, `|+`,
		`(a{30}){40}`, `((a{10}){10}){11}`, `(a{600,}){2}`, `(a{600}){2}`, `(?:b|(a{501})){2}`, `(?:[a-z]{1,40}-){1,26}`,
		`(a+)+$`, `(a*)*`, `(a+){2,}`, `(?:a{2,})+`, `((ab+)c)*`, `((a+)?)+`, `(a+){2}`, `(a+?)+?`, `(a|b+)*`, `^(?:(?:a+){2,1000})*$`,
		strings.Repeat("a", 1001), strings.Repeat("\U0001f600", 1001),
		`\`, `[\b]`, `[[:alpha:]]`, "a{\u0663}"} {
		if patternProblem(p) == "" {
			t.Errorf("%q is not portable but was accepted", p)
		}
	}
	matches := func(subject, pattern string) bool {
		re, err := compilePattern(pattern)
		if err != nil {
			t.Fatalf("%q: %v", pattern, err)
		}
		return re.MatchString(subject)
	}
	if !matches("a\nb", `^a.b$`) || matches("ab\n", `b$`) || matches("\u0663", `\d`) || !matches("é", `^.$`) ||
		!matches("\U0001d49c", `^.$`) || matches("é", `\w`) || !matches("xay", `a`) {
		t.Error("a portable pattern does not have its one meaning")
	}
}

func TestCustomOperators(t *testing.T) {
	var seen []any
	operators := Operators{
		"x-echo":  func(args []any) (any, error) { seen = args; return args[0], nil },
		"x-fail":  func(args []any) (any, error) { return nil, errors.New("no") },
		"x-panic": func(args []any) (any, error) { panic("boom") },
		"x-third": func(args []any) (any, error) { tenth, fifth := 0.1, 0.2; return tenth + fifth, nil },
		"x-many":  func(args []any) (any, error) { return map[string]any{"n": 3, "list": []string{"a"}}, nil },
		"x-bad":   func(args []any) (any, error) { return make(chan int), nil },
	}
	eval := func(expr string) (any, error) { return EvaluateExpression(mustParse(t, expr), nil, nil, operators) }

	// arguments are plain values; computed numbers arrive rounded to 15 significant digits
	if _, err := eval(`{"op":"x-echo","args":[{"op":"div","args":[1,3]},"s",null,true,{"op":"list","args":[1,2.5]},7]}`); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 6 || seen[0] != 0.333333333333333 || seen[1] != "s" || seen[2] != nil || seen[3] != true || seen[5] != 7.0 {
		t.Errorf("arguments: %#v", seen)
	}
	if list, ok := seen[4].([]any); !ok || len(list) != 2 || list[0] != 1.0 || list[1] != 2.5 {
		t.Errorf("list argument: %#v", seen[4])
	}
	// a float64 result is read as its shortest decimal representation; like every number it is
	// rounded when it leaves the expression, and not before
	if got, err := eval(`{"op":"text","args":[{"op":"x-third","args":[]}]}`); err != nil || got != "0.3" {
		t.Errorf("float result leaving: %v %v", got, err)
	}
	if got, err := eval(`{"op":"eq","args":[{"op":"x-third","args":[]},0.3]}`); err != nil || got != false {
		t.Errorf("float result inside: %v %v", got, err)
	}
	if got, err := eval(`{"op":"x-many","args":[]}`); err != nil || canonical(got) != `{"list":["a"],"n":3}` {
		t.Errorf("object result: %v %v", got, err)
	}
	operators["x-nan"] = func(args []any) (any, error) { return math.NaN(), nil }
	operators["x-inf"] = func(args []any) (any, error) { return map[string]any{"deep": []any{math.Inf(-1)}}, nil }
	for _, expr := range []string{`{"op":"x-fail","args":[]}`, `{"op":"x-panic","args":[]}`, `{"op":"x-bad","args":[]}`, `{"op":"x-missing","args":[]}`,
		`{"op":"typeOf","args":[{"op":"x-nan","args":[]}]}`, `{"op":"exists","args":[{"op":"x-inf","args":[]}]}`} {
		var failure *EvalError
		if _, err := eval(expr); !errors.As(err, &failure) {
			t.Errorf("%s: expected an evaluation error, got %v", expr, err)
		}
	}
}

func TestMalformedExpressionsAreEvaluationErrors(t *testing.T) {
	for _, expr := range []string{`{"foo":1}`, `{}`, `[1,"a"]`, `{"op":"add"}`, `{"op":"list","args":5}`, `{"var":5}`,
		`{"var":"data.a","default":1}`, `{"op":5,"args":[]}`, `{"op":"not","args":[true],"x-note":"n"}`, `{"fn":"f"}`,
		`{"fn":5,"args":[]}`, `{"op":"not","fn":"f","args":[true]}`, `{"op":"nope","args":[]}`, `{"op":"list","args":[[1]]}`,
		`{"fn":"noBody","args":[]}`, `{"fn":"noParams","args":[]}`, `{"fn":"notAFunction","args":[]}`} {
		functions := mustParse(t, `{"noBody":{"params":[]},"noParams":{"body":1},"notAFunction":5}`)
		var failure *EvalError
		if _, err := EvaluateExpression(mustParse(t, expr), nil, functions, nil); !errors.As(err, &failure) {
			t.Errorf("%s: expected an evaluation error, got %v", expr, err)
		}
	}
	// only what is evaluated is checked, and a list is a value when it comes from a variable
	for expr, want := range map[string]string{
		`{"op":"if","args":[true,1,{"foo":1}]}`: `1`,
		`{"var":"data.list"}`:                   `[1,"a"]`,
		`{"var":"session.user"}`:                `null`,
		`{"var":"data.list.01"}`:                `"a"`,
		`{"var":"data.list.2"}`:                 `null`,
		`{"var":"data.list.-1"}`:                `null`,
	} {
		env := map[string]any{"data": map[string]any{"list": []any{1, "a"}}, "session": map[string]any{"user": "u"}}
		if got, err := EvaluateExpression(mustParse(t, expr), env, nil, nil); err != nil || canonical(got) != want {
			t.Errorf("%s: %v %v, want %s", expr, got, err, want)
		}
	}
}

// Every number is rounded half even to 15 significant digits when it leaves the engine, computed
// or not, and never inside an expression. One beyond the range of a double cannot leave.
func TestNumbersLeavingTheEngine(t *testing.T) {
	for expr, want := range map[string]string{
		`0.1234567890123456`:                        `0.123456789012346`,
		`123456789012345678`:                        `123456789012346000`,
		`{"var":"data"}`:                            `{"a":[0.123456789012346],"b":2,"big":12345678901234600000}`,
		`{"op":"eq","args":[{"var":"data.b"},2.0]}`: `true`,
		`{"op":"eq","args":[{"var":"data.a.0"},0.123456789012346]}`:                                                                 `false`,
		`{"op":"text","args":[{"var":"data.big"}]}`:                                                                                 `"12345678901234600000"`,
		`{"op":"gt","args":[{"op":"mul","args":[1e200,1e200]},1e300]}`:                                                              `true`,
		`{"op":"eq","args":[9007199254740993,9007199254740992]}`:                                                                    `true`,
		`920101782750211427020`:                                                                                                     `920101782750212000000`,
		`{"op":"text","args":[{"op":"sub","args":[0.12345678901234567891,0.1234567890123456789]}]}`:                                 `"0"`,
		`{"op":"eq","args":[1e-999,0]}`:                                                                                             `true`,
		`{"op":"gt","args":[1.7976931348623157e308,1e308]}`:                                                                         `true`,
		`{"op":"mul","args":[1.79769313486231e308,1]}`:                                                                              `179769313486231` + strings.Repeat("0", 294),
		`{"op":"eq","args":[{"op":"abs","args":[{"op":"div","args":[-1,3]}]},{"op":"div","args":[1,3]}]}`:                           `true`,
		`{"op":"eq","args":[{"op":"abs","args":[-12345678901234567890123456789012345678]},12345678901234567890123456789012345678]}`: `true`,
	} {
		env := map[string]any{"data": mustParse(t, `{"a":[0.1234567890123456],"b":2,"big":12345678901234567890}`)}
		if got, err := EvaluateExpression(mustParse(t, expr), env, nil, nil); err != nil || canonical(got) != want {
			t.Errorf("%s: %v %v, want %s", expr, got, err, want)
		}
	}
	for _, expr := range []string{`{"op":"mul","args":[1e200,1e200]}`, `{"op":"text","args":[{"op":"mul","args":[1e308,10]}]}`,
		`{"op":"list","args":[1,{"op":"mul","args":[-1e308,10]}]}`} {
		var failure *EvalError
		if got, err := EvaluateExpression(mustParse(t, expr), nil, nil, nil); !errors.As(err, &failure) {
			t.Errorf("%s: expected an evaluation error, got %v %v", expr, got, err)
		}
	}
	// numbers enter as doubles whatever Go type carries them, and one beyond the doubles is refused
	if got, _ := Canonical([]any{int64(9007199254740993), uint64(18446744073709551615), json.Number("0.10000000000000000001"), 1e22}); got != `[9007199254740992,18446744073709552000,0.1,10000000000000000000000]` {
		t.Errorf("Go numbers: %s", got)
	}
	for _, bad := range []any{json.Number("1e999"), json.Number("1" + strings.Repeat("0", 400)), math.Inf(1), math.NaN(), []any{math.Inf(-1)}} {
		if got, err := EvaluateExpression(bad, nil, nil, nil); err == nil {
			t.Errorf("%v was accepted: %v", bad, got)
		}
	}
}

// testBundle is a bundle whose server manifest holds the given rules.
func testBundle(rules ...string) string {
	return `{"ruleCascadeBundle":"1.0.0","id":"t","version":"1.0.0","checksum":"sha256:0","manifests":{
 "client":{"id":"t","version":"1.0.0","checksum":"sha256:0","channel":"client","rules":[]},
 "server":{"id":"t","version":"1.0.0","checksum":"sha256:0","channel":"server","conflictPolicy":"fail","defaultLocale":"en",
  "params":{},"messages":{},"rules":[` + strings.Join(rules, ",") + `]}}}`
}

const (
	firstStateRule = `{"id":"first","kind":"state","origin":"t@1.0.0","target":{"entity":"Thing"},"operations":["create"],"when":true,
    "effects":[{"field":"/b","set":{"required":true}},{"field":"/a","set":{"visible":true,"enabled":true}}]}`
	secondStateRule = `{"id":"second","kind":"state","origin":"t@1.0.0","target":{"entity":"Thing"},"operations":["create"],"when":true,
    "effects":[{"field":"/a","set":{"readOnly":true,"visible":false,"x-late":1}}]}`
	computeRule = `{"id":"total","kind":"compute","origin":"t@1.0.0","target":{"entity":"Thing"},"operations":["create"],
    "assign":[{"field":"/sum/total","value":{"op":"div","args":[{"var":"data.n"},3]}},
              {"field":"/note","value":null,"mode":"always"}]}`
	actionRule = `{"id":"notify","kind":"action","origin":"t@1.0.0","enforcement":"server","target":{"entity":"Thing"},"operations":["create"],
    "commands":[{"name":"thing.created","type":"event","ref":"","idempotencyKey":["thing",{"var":"data.sum.total"}],
                 "payload":{"total":{"var":"data.sum.total"},"html":"<b>&</b>"}}]}`
)

// A state rule applies all of its effects or none: the second rule conflicts with the first on
// `visible`, so its `readOnly` is not applied either, whatever the order of the members of `set`.
// A compute rule is sequential: its earlier assignments stay.
func TestStateRulesAreAtomic(t *testing.T) {
	want := `{"checksum":"sha256:0","commands":[],"decision":"deny","effects":[` +
		`{"field":"/sum/total","rule":"total","type":"value","value":0.333333333333333},` +
		`{"field":"/note","rule":"total","type":"value","value":null},` +
		`{"field":"/b","rule":"first","set":{"required":true},"type":"state"},` +
		`{"field":"/a","rule":"first","set":{"enabled":true,"visible":true},"type":"state"}],` +
		`"findings":[{"blocking":true,"code":"RULE-EVALUATION-ERROR","fields":[],"message":"This rule could not be evaluated.",` +
		`"resolution":"none","rule":"second","severity":"error","source":"t@1.0.0","status":"open"}],"ruleset":"t","version":"1.0.0"}`
	reordered := strings.Replace(secondStateRule, `"readOnly":true,"visible":false,"x-late":1`, `"x-late":1,"visible":false,"readOnly":true`, 1)
	for _, second := range []string{secondStateRule, reordered} {
		rs, err := FromBundle(mustParse(t, testBundle(firstStateRule, second, computeRule, actionRule)))
		if err != nil {
			t.Fatal(err)
		}
		result, err := rs.Evaluate(map[string]any{"entity": "Thing", "operation": "create", "data": map[string]any{"n": 1}}, "server", nil)
		if err != nil {
			t.Fatal(err)
		}
		for i := range result.Findings {
			result.Findings[i].Detail = ""
		}
		if got, _ := Canonical(result); got != want {
			t.Errorf("result:\n got %s\nwant %s", got, want)
		}
		if result.Allowed() || len(result.Effects) != 4 || result.Effects[3].Set.Len() != 2 {
			t.Errorf("typed result: %+v", result)
		}
	}
	// two effects of the same rule that disagree are a conflict too
	const selfConflict = `{"id":"self","kind":"state","origin":"t@1.0.0","target":{"entity":"Thing"},"operations":["create"],"when":true,
    "effects":[{"field":"/a","set":{"visible":true}},{"field":"/b","set":{"visible":true}},{"field":"/a","set":{"visible":false}}]}`
	rs, err := FromBundle(mustParse(t, testBundle(selfConflict)))
	if err != nil {
		t.Fatal(err)
	}
	result, err := rs.Evaluate(map[string]any{"entity": "Thing", "operation": "create"}, "server", nil)
	if err != nil || len(result.Effects) != 0 || len(result.Findings) != 1 || result.Findings[0].Code != EngineErrorCode {
		t.Errorf("a rule that conflicts with itself: %+v %v", result, err)
	}
}

func TestResultMarshalsLikeTheEvaluationAPI(t *testing.T) {
	rs, err := FromBundle(mustParse(t, testBundle(computeRule, actionRule)))
	if err != nil {
		t.Fatal(err)
	}
	result, err := rs.Evaluate(map[string]any{"entity": "Thing", "operation": "create", "data": map[string]any{"n": 2}}, "server", nil)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"ruleset":"t","version":"1.0.0","checksum":"sha256:0","decision":"allow","findings":[],"effects":[` +
		`{"type":"value","field":"/sum/total","value":0.666666666666667,"rule":"total"},` +
		`{"type":"value","field":"/note","value":null,"rule":"total"}],"commands":[` +
		`{"name":"thing.created","type":"event","rule":"notify","idempotencyKey":"thing:0.666666666666667",` +
		`"payload":{"total":0.666666666666667,"html":"<b>&</b>"},"ref":""}]}`
	if got, err := Marshal(result); err != nil || string(got) != want {
		t.Errorf("Marshal:\n got %s\nwant %s (%v)", got, want, err)
	}
	// encoding/json writes the same value (it escapes <, > and &, which changes no value)
	viaStandard, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if a, b := mustParse(t, string(viaStandard)), mustParse(t, want); !equal(a, b) {
		t.Errorf("json.Marshal: %s", viaStandard)
	}
	var decoded Result
	if err := json.Unmarshal(viaStandard, &decoded); err != nil || decoded.Decision != "allow" || len(decoded.Commands) != 1 ||
		decoded.Commands[0].IdempotencyKey != "thing:0.666666666666667" || decoded.Commands[0].Payload.Len() != 2 {
		t.Errorf("json.Unmarshal into Result: %+v %v", decoded, err)
	}
}

// A request that does not have the shape of specification section 8 is refused before anything is
// evaluated.
func TestEvaluateRefusesMalformedRequests(t *testing.T) {
	rs, err := FromBundle(mustParse(t, testBundle(computeRule)))
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []string{
		`[1]`, `{"entity":"Thing"}`, `{"entity":5,"operation":"create"}`,
		`{"entity":"Thing","operation":"create","data":[1]}`, `{"entity":"Thing","operation":"create","data":""}`,
		`{"entity":"Thing","operation":"create","original":"x"}`, `{"entity":"Thing","operation":"create","actor":"me"}`,
		`{"entity":"Thing","operation":"create","actor":{"roles":"admin"}}`, `{"entity":"Thing","operation":"create","actor":{"roles":["a",5]}}`,
		`{"entity":"Thing","operation":"create","ctx":1}`, `{"entity":"Thing","operation":"create","view":["p"]}`,
		`{"entity":"Thing","operation":"create","view":{"page":5}}`, `{"entity":"Thing","operation":"create","resolutions":{}}`,
		`{"entity":"Thing","operation":"create","resolutions":["a"]}`, `{"entity":"Thing","operation":"create","resolutions":[{"rule":"a"}]}`,
		`{"entity":"Thing","operation":"create","resolutions":[{"rule":"a","type":"accept-risk","justification":5}]}`,
		`{"entity":"Thing","operation":"create","trigger":5}`, `{"entity":"Thing","operation":"create","locale":["en"]}`,
	} {
		var failure *RequestError
		if result, err := rs.Evaluate(mustParse(t, request), "server", nil); !errors.As(err, &failure) {
			t.Errorf("%s: expected a RequestError, got %+v %v", request, result, err)
		}
	}
	// optional members may be null, and members that are not listed are ignored
	const lenient = `{"entity":"Thing","operation":"create","data":null,"original":null,"actor":{"id":null,"roles":null},"ctx":null,
		"view":{"page":null},"resolutions":[{"rule":"a","type":"acknowledge","justification":null}],"trigger":null,"locale":null,"extra":[1]}`
	if result, err := rs.Evaluate(mustParse(t, lenient), "server", nil); err != nil || result.Decision != "deny" {
		t.Errorf("a lenient request: %+v %v", result, err)
	}
	if _, err := rs.Evaluate(map[string]any{"entity": "Thing", "operation": "create"}, "browser", nil); err == nil {
		t.Error("an unknown channel was accepted")
	}
	var failure *LoadError
	if _, err := FromBundle(map[string]any{"ruleCascadeBundle": "2.0.0"}); !errors.As(err, &failure) || failure.Codes()[0] != "BUNDLE_UNSUPPORTED" {
		t.Errorf("a version 2 bundle: %v", err)
	}
	if _, err := FromBundle(map[string]any{"ruleCascadeBundle": 1.5, "manifests": map[string]any{}}); !errors.As(err, &failure) || failure.Codes()[0] != "BUNDLE_UNSUPPORTED" {
		t.Errorf("a bundle whose version is a number: %v", err)
	}
	if _, err := FromBundle(map[string]any{"ruleCascadeBundle": "1.3.0", "manifests": map[string]any{}}); !errors.As(err, &failure) || failure.Codes()[0] != "BUNDLE_INVALID" {
		t.Errorf("a bundle without manifests: %v", err)
	}
	// each manifest carries its own name as channel
	for _, broken := range []string{
		strings.Replace(testBundle(), `"channel":"client"`, `"channel":"server"`, 1),
		strings.Replace(testBundle(), `"channel":"server",`, ``, 1),
	} {
		if _, err := FromBundle(mustParse(t, broken)); !errors.As(err, &failure) || failure.Codes()[0] != "BUNDLE_INVALID" {
			t.Errorf("a manifest under the wrong channel: %v", err)
		}
	}
}

func TestEngineServe(t *testing.T) {
	long := `{"id":1,"command":"expression","expr":{"op":"len","args":["` + strings.Repeat("x", 200_000) + `"]}}`
	input := "\n  \n" + long + "\r\n\n" + `{"id":2,"command":"version"}` + "\nnot json\n" + `{"id":3,"command":"expression","expr":{"op":"text","args":["<é&>"]}}`
	var out bytes.Buffer
	if err := NewEngine(nil).Serve(strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 responses, got %d: %s", len(lines), out.String())
	}
	if lines[0] != `{"id":1,"ok":true,"result":200000}` {
		t.Errorf("a long line: %s", lines[0])
	}
	if !strings.HasPrefix(lines[1], `{"id":2,"ok":true,"result":{"engine":"rule-cascade-go","engineVersion":"`+Version+`","ruleCascade":"1.0.0","bundle":"1.0.0","levels":["evaluator","compiler"],"operators":[]}`) {
		t.Errorf("version: %s", lines[1])
	}
	if !strings.HasPrefix(lines[2], `{"ok":false,"error":{"code":"BAD_REQUEST"`) {
		t.Errorf("not JSON: %s", lines[2])
	}
	if lines[3] != `{"id":3,"ok":true,"result":"<é&>"}` {
		t.Errorf("the last line, without a line feed: %s", lines[3])
	}
}

// The members of a request are checked before a ruleset is looked up (specification section 13).
func TestEngineChecksMembersFirst(t *testing.T) {
	engine := NewEngine(nil)
	for _, request := range []string{
		`{"command":"manifest","ruleset":"never.loaded","channel":"browser"}`,
		`{"command":"manifest","ruleset":"never.loaded","channel":""}`,
		`{"command":"expression","expr":1e999}`,
		`{"command":"expression","expr":NaN}`,
		`{"command":"expression","expr":-Infinity}`,
		`{"command":"expression","expr":1` + strings.Repeat("0", 400) + `}`,
		`{"command":"evaluate","ruleset":"never.loaded","channel":5,"request":{"entity":"Thing","operation":"create"}}`,
		`{"command":"manifest","bundle":{"ruleCascadeBundle":"9.0.0"},"channel":"browser"}`,
		`{"command":"manifest","bundle":"x"}`,
		`{"command":"compile","document":[]}`,
		`{"command":"expression","expr":1,"env":[1]}`,
	} {
		if answer := string(engine.HandleLine([]byte(request))); !strings.Contains(answer, `"code":"BAD_REQUEST"`) {
			t.Errorf("%s: %s", request, answer)
		}
	}
}

func TestVersionSatisfies(t *testing.T) {
	for _, c := range []struct {
		version, wanted string
		ok              bool
	}{
		{"1.2.0", "1.2.0", true}, {"1.2.1", "1.2.0", false}, {"1.2.0", "^1.2.0", true}, {"1.10.0", "^1.9.3", true}, {"1.1.9", "^1.2.0", false},
		{"2.0.0", "^1.2.0", false}, {"1.2.0-rc.1", "1.2.0", true}, {"1.02.0", "1.2.0", true}, {"123456789012345678901.0.0", "^123456789012345678901.0.0", true},
	} {
		if got := VersionSatisfies(c.version, c.wanted); got != c.ok {
			t.Errorf("VersionSatisfies(%s, %s) = %v", c.version, c.wanted, got)
		}
	}
}

// A RuleSet is immutable: concurrent evaluations must not interfere (run with -race).
func TestConcurrentEvaluate(t *testing.T) {
	data, err := os.ReadFile("../../conformance/bundles/acme.onboarding.customer.bundle.json")
	if err != nil {
		t.Fatal(err)
	}
	rs, err := FromBundle(mustParse(t, string(data)))
	if err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"entity": "Customer", "operation": "create", "data": map[string]any{
		"fullName": "Maya Okafor", "email": "maya(at)example.com", "postalCode": "27502", "country": "US", "creditLimit": 10.005}}
	first, err := rs.Evaluate(request, "server", ConformanceOperators())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := Canonical(first)
	done := make(chan string)
	for i := 0; i < 8; i++ {
		go func() {
			got := ""
			for k := 0; k < 20; k++ {
				result, err := rs.Evaluate(request, "server", ConformanceOperators())
				if err != nil {
					got = err.Error()
					break
				}
				got, _ = Canonical(result)
			}
			done <- got
		}()
	}
	for i := 0; i < 8; i++ {
		if got := <-done; got != want {
			t.Errorf("a concurrent evaluation returned %s", got)
		}
	}
}

// One manifest on its own is a ruleset with that one channel (specification section 7).
func TestFromManifest(t *testing.T) {
	const manifest = `{"id":"m","version":"2.1.0","checksum":"sha256:1","channel":"client","rules":[],"operators":["x-b","x-a","x-luhn"]}`
	rs, err := FromManifest(mustParse(t, manifest))
	if err != nil {
		t.Fatal(err)
	}
	if rs.ID() != "m" || rs.Version() != "2.1.0" || rs.Checksum() != "sha256:1" || strings.Join(rs.Channels(), ",") != "client" || rs.Resolved() != nil {
		t.Errorf("loaded as %s@%s %s %v", rs.ID(), rs.Version(), rs.Checksum(), rs.Channels())
	}
	if got := strings.Join(rs.MissingOperators(ConformanceOperators()), ","); got != "x-a,x-b" {
		t.Errorf("missing operators: %s", got)
	}
	if got := rs.MissingOperators(nil); len(got) != 3 || got[0] != "x-a" {
		t.Errorf("missing operators without any: %v", got)
	}
	// absent params, functions, messages and policies have defaults
	result, err := rs.Evaluate(map[string]any{"entity": "Thing", "operation": "create", "locale": "fr"}, "", nil)
	if err != nil || result.Decision != "allow" || result.Ruleset != "m" {
		t.Errorf("a minimal manifest: %+v %v", result, err)
	}
	for _, channel := range []string{"server", "browser"} {
		if _, err := rs.Evaluate(map[string]any{"entity": "Thing", "operation": "create"}, channel, nil); err == nil {
			t.Errorf("evaluated on %s", channel)
		}
		if _, err := rs.Manifest(channel); err == nil {
			t.Errorf("a %s manifest was returned", channel)
		}
	}
	if mf, err := rs.Manifest(""); err != nil || mf.str("channel") != "client" {
		t.Errorf("the default manifest: %v", err)
	}
	for _, broken := range []string{`[]`, `{}`, strings.Replace(manifest, `"rules":[],`, ``, 1), strings.Replace(manifest, `"client"`, `"browser"`, 1),
		strings.Replace(manifest, `"id":"m"`, `"id":5`, 1), strings.Replace(manifest, `"channel":"client",`, ``, 1)} {
		var failure *LoadError
		if _, err := FromManifest(mustParse(t, broken)); !errors.As(err, &failure) || failure.Codes()[0] != "MANIFEST_INVALID" {
			t.Errorf("%s: %v", broken, err)
		}
	}
	// a bundle has both channels, the default is the server's, and only its two manifests are kept
	bundle, err := FromBundle(mustParse(t, strings.Replace(testBundle(computeRule), `"manifests":{`, `"manifests":{"extra":{"id":"other"},`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if mf, _ := bundle.Manifest(""); strings.Join(bundle.Channels(), ",") != "client,server" || mf.str("channel") != "server" ||
		bundle.Bundle().obj("manifests").Len() != 2 || len(bundle.MissingOperators(nil)) != 0 {
		t.Errorf("a bundle: channels %v", bundle.Channels())
	}
}

// Message catalogs fall back from the requested tag to its prefixes, then to the default locale;
// tags are compared exactly (specification section 8.1).
func TestLocaleFallback(t *testing.T) {
	const rule = `{"id":"r","kind":"validation","origin":"t@1.0.0","target":{"entity":"Thing"},"operations":["create"],
    "assert":false,"severity":"info","finding":{"code":"T-1","message":"KEY"}}`
	bundle := strings.Replace(testBundle(strings.Replace(rule, "KEY", "a", 1), strings.Replace(strings.Replace(rule, "KEY", "b", 1), `"r"`, `"s"`, 1),
		strings.Replace(strings.Replace(rule, "KEY", "c", 1), `"r"`, `"t"`, 1), strings.Replace(strings.Replace(rule, "KEY", "missing", 1), `"r"`, `"u"`, 1)),
		`"messages":{}`, `"messages":{"en":{"a":"en a","b":"en b","c":"en c"},"fr":{"a":"fr a","b":"fr b"},"fr-CA":{"a":"fr-CA a"},"fr-CA-x-a":{"c":"deep c"}}`, 1)
	rs, err := FromBundle(mustParse(t, bundle))
	if err != nil {
		t.Fatal(err)
	}
	for locale, want := range map[string]string{
		"":          "en a|en b|en c|missing",
		"en":        "en a|en b|en c|missing",
		"fr":        "fr a|fr b|en c|missing",
		"fr-CA":     "fr-CA a|fr b|en c|missing",
		"fr-BE":     "fr a|fr b|en c|missing",
		"fr-CA-x-a": "fr-CA a|fr b|deep c|missing",
		"FR":        "en a|en b|en c|missing",
		"fr-ca":     "fr a|fr b|en c|missing",
		"de":        "en a|en b|en c|missing",
	} {
		request := map[string]any{"entity": "Thing", "operation": "create"}
		if locale != "" {
			request["locale"] = locale
		}
		result, err := rs.Evaluate(request, "server", nil)
		if err != nil {
			t.Fatal(err)
		}
		var messages []string
		for _, f := range result.Findings {
			messages = append(messages, f.Message)
		}
		if got := strings.Join(messages, "|"); got != want {
			t.Errorf("locale %q: %s, want %s", locale, got, want)
		}
	}
	if got := strings.Join(localeChain("en", "fr-CA-x-a", -1), " "); got != "en fr fr-CA fr-CA-x fr-CA-x-a" {
		t.Errorf("localeChain: %s", got)
	}
	if got := strings.Join(localeChain("en", "fr-CA-x-a", 5), " "); got != "en fr fr-CA" {
		t.Errorf("localeChain bounded: %s", got)
	}

	// A 1 MB locale costs no more than a short one: before, the chain held every prefix of it.
	short := map[string]any{"entity": "Thing", "operation": "create", "locale": "fr-CA-y"}
	want, _ := rs.Evaluate(short, "server", nil)
	for _, locale := range []string{"fr-CA-" + strings.Repeat("a-", 500_000), "fr-CA-" + strings.Repeat("a", 1_000_000), strings.Repeat("-", 1_000_000)} {
		long := map[string]any{"entity": "Thing", "operation": "create", "locale": locale}
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		started := time.Now()
		got, err := rs.Evaluate(long, "server", nil)
		elapsed := time.Since(started)
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatal(err)
		}
		if elapsed > time.Second {
			t.Errorf("a %d-byte locale took %v", len(locale), elapsed)
		}
		if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
			t.Errorf("a %d-byte locale allocated %d bytes", len(locale), allocated)
		}
		expected := want.Findings
		if locale[0] == '-' {
			none, _ := rs.Evaluate(map[string]any{"entity": "Thing", "operation": "create"}, "server", nil)
			expected = none.Findings
		}
		for i := range expected {
			if got.Findings[i].Message != expected[i].Message {
				t.Errorf("a %d-byte locale: %q, want %q", len(locale), got.Findings[i].Message, expected[i].Message)
			}
		}
	}
}

// The bounded chain is the full chain without the prefixes longer than the bound, for every tag of
// up to 7 characters from 'a', 'b' and '-' and every bound.
func TestLocaleChainBound(t *testing.T) {
	naive := func(wanted string) []string {
		chain := []string{"en"}
		if wanted != "" {
			parts := strings.Split(wanted, "-")
			for i := 1; i <= len(parts); i++ {
				chain = append(chain, strings.Join(parts[:i], "-"))
			}
		}
		return chain
	}
	tags := []string{""}
	for start := 0; start < len(tags); start++ {
		if len(tags[start]) < 7 {
			tags = append(tags, tags[start]+"a", tags[start]+"b", tags[start]+"-")
		}
	}
	for _, tag := range tags {
		full := naive(tag)
		if got := localeChain("en", tag, -1); strings.Join(got, "|") != strings.Join(full, "|") || len(got) != len(full) {
			t.Fatalf("%q: %q, want %q", tag, got, full)
		}
		for longest := 0; longest <= 8; longest++ {
			want := []string{"en"}
			for _, prefix := range full[1:] {
				if len(prefix) <= longest {
					want = append(want, prefix)
				}
			}
			if got := localeChain("en", tag, longest); strings.Join(got, "|") != strings.Join(want, "|") || len(got) != len(want) {
				t.Fatalf("%q bounded by %d: %q, want %q", tag, longest, got, want)
			}
		}
	}
}
