package derive

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	rulecascade "rules.sdods.com/go"
)

// The repository the parity tests read: examples/ and the conformance suite.
const repo = "../../../../"

// TestMain runs the tests inside the Rule Cascade repository only: the parity tests read the
// examples from ../../../../. The public Go mirror (rules.sdods.com/go) holds packages/go alone, so
// there `go test ./...` reports that and passes.
func TestMain(m *testing.M) {
	if _, err := os.Stat(repo + "conformance/README.md"); err != nil {
		fmt.Println("rules.sdods.com/go/internal/derive: the tests need the Rule Cascade repository (../../../../conformance); skipped")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// ------------------------------------------------------------------ parity with tools/openapi_rules.py

// The two examples in examples/derived, with the flags docs/openapi.md and
// tools/tests/test_openapi_rules.py derive them with:
//
//	python tools/rulecheck.py derive examples/catalog/onboarding.openapi.yaml --schema Customer \
//	    --id acme.generated.customer --scope organization:acme,project:customer-portal \
//	    --tests examples/derived/onboarding-customer.tests.yaml \
//	    -o examples/derived/onboarding-customer.ruleset.yaml
var examples = []struct {
	input string
	opts  Options
	name  string
}{
	{"examples/catalog/onboarding.openapi.yaml", Options{Schema: "Customer", ID: "acme.generated.customer",
		Scope:     []string{"organization:acme", "project:customer-portal"},
		TestsFile: repo + "examples/derived/onboarding-customer.tests.yaml"}, "onboarding-customer.ruleset.yaml"},
	{"examples/contracts/payments.openapi.yaml", Options{Schema: "Transfer", ID: "acme.generated.transfer",
		Scope:     []string{"organization:acme,project:payments-hub"},
		TestsFile: repo + "examples/derived/payments-transfer.tests.yaml"}, "payments-transfer.ruleset.yaml"},
}

func TestDerivingAgainGivesTheCommittedExamples(t *testing.T) {
	for _, ex := range examples {
		t.Run(ex.name, func(t *testing.T) {
			opts := ex.opts
			opts.Output = repo + "examples/derived/" + ex.name
			got, err := FromFile(repo+ex.input, opts)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(opts.Output)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Ruleset, want) {
				t.Fatalf("differs from %s:\n%s", ex.name, firstDifference(string(want), string(got.Ruleset)))
			}
			if len(got.Skipped) != 0 || got.Tests != nil {
				t.Fatalf("skipped %v, tests %q", got.Skipped, got.Tests)
			}
		})
	}
}

// The examples load with path checks against their OpenAPI document and their golden tests pass,
// as `rulecheck check examples/derived/*.ruleset.yaml` proves for the Python.
func TestTheExamplesLoadAndTheirGoldenTestsPass(t *testing.T) {
	for _, ex := range examples {
		t.Run(ex.name, func(t *testing.T) {
			document, err := readFile(repo + ex.input)
			if err != nil {
				t.Fatal(err)
			}
			opts := ex.opts
			opts.Output = repo + "examples/derived/" + ex.name
			got, err := FromFile(repo+ex.input, opts)
			if err != nil {
				t.Fatal(err)
			}
			ruleset := parse(t, got.Ruleset)
			rs, err := rulecascade.Load(ruleset, nil, func(string) any { return document })
			if err != nil {
				t.Fatal(err)
			}
			tests, _ := ruleset.Get("tests")
			outcomes, err := rs.RunTests(tests, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(outcomes) != 6 {
				t.Fatalf("%d golden tests, want 6", len(outcomes))
			}
			for _, o := range outcomes {
				if len(o.Failures) > 0 {
					t.Errorf("%s: %v", o.Name, o.Failures)
				}
			}
			// The entity reference resolves from the example's directory.
			entities, _ := ruleset.Get("entities")
			ref := getPath(getPath(getPath(entities, entities.(*rulecascade.Object).Keys()[0]), "schema"), "$ref").(string)
			file, fragment, _ := strings.Cut(ref, "#")
			target, err := readFile(filepath.Join(repo, "examples/derived", file))
			if err != nil {
				t.Fatal(err)
			}
			if node := pointerGet(t, target, fragment); getPath(node, "type") != "object" {
				t.Fatalf("%s is not an object schema", ref)
			}
		})
	}
}

// The path checks are real: the same ruleset against another schema is refused.
func TestADerivedRuleThatNamesAnUnknownPathWouldNotLoad(t *testing.T) {
	text, err := os.ReadFile(repo + "examples/derived/payments-transfer.ruleset.yaml")
	if err != nil {
		t.Fatal(err)
	}
	other, err := readFile(repo + "examples/catalog/onboarding.openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	schemas := getPath(getPath(other, "components"), "schemas").(*rulecascade.Object)
	customer, _ := schemas.Get("Customer")
	schemas.Set("Transfer", customer)
	_, err = rulecascade.Load(parse(t, text), nil, func(string) any { return other })
	var problems *rulecascade.LoadError
	if !errors.As(err, &problems) || !contains(problems.Codes(), "PATH_UNKNOWN") {
		t.Fatalf("got %v, want PATH_UNKNOWN", err)
	}
}

func TestDerivationIsDeterministic(t *testing.T) {
	for _, ex := range examples {
		document, err := readFile(repo + ex.input)
		if err != nil {
			t.Fatal(err)
		}
		texts := map[string]bool{}
		for i := 0; i < 3; i++ {
			got, err := FromDocument(document, Options{Schema: ex.opts.Schema, ID: ex.opts.ID})
			if err != nil {
				t.Fatal(err)
			}
			texts[string(got.Ruleset)] = true
		}
		if len(texts) != 1 {
			t.Fatalf("%s: %d different outputs", ex.name, len(texts))
		}
	}
}

// ------------------------------------------------------------------ fixtures

// openapi is an OpenAPI document with one component schema, Thing, and the other schemas in more
// (JSON members, "" for none).
func openapi(t *testing.T, properties string, required []string, more string) *rulecascade.Object {
	t.Helper()
	thing := `{"type": "object", "properties": ` + properties
	if len(required) > 0 {
		thing += `, "required": ["` + strings.Join(required, `", "`) + `"]`
	}
	thing += "}"
	if more != "" {
		more = ", " + more
	}
	return jsonValue(t, `{"openapi": "3.1.0", "info": {"title": "t", "version": "1"}, "paths": {},
		"components": {"schemas": {"Thing": `+thing+more+`}}}`).(*rulecascade.Object)
}

func jsonValue(t *testing.T, text string) any {
	t.Helper()
	v, err := rulecascade.ParseJSON([]byte(text))
	if err != nil {
		t.Fatalf("%v in %s", err, text)
	}
	return v
}

// parse reads emitted YAML back, as a YAML 1.2 core-schema parser does.
func parse(t *testing.T, text []byte) *rulecascade.Object {
	t.Helper()
	v, err := parseYAML(text)
	if err != nil {
		t.Fatalf("%v in\n%s", err, text)
	}
	return v.(*rulecascade.Object)
}

func pointerGet(t *testing.T, doc any, pointer string) any {
	t.Helper()
	node := doc
	for _, part := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		if list, ok := node.([]any); ok {
			var i int
			fmt.Sscan(part, &i)
			node = list[i]
			continue
		}
		var ok bool
		if node, ok = get(node, strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")); !ok {
			t.Fatalf("%s is not in the document", pointer)
		}
	}
	return node
}

type derived struct {
	result  *Result
	ruleset *rulecascade.Object // the emitted YAML, read back
	rules   *rulecascade.RuleSet
	skipped map[string]string // pointer without /components/schemas/ -> reason
}

// derive derives Thing and loads the ruleset with path checks against the same document.
func deriveThing(t *testing.T, document any, opts Options) derived {
	t.Helper()
	if opts.Schema == "" && opts.Pointer == "" {
		opts.Schema = "Thing"
	}
	if opts.ID == "" {
		opts.ID = "acme.generated.thing"
	}
	result, err := FromDocument(document, opts)
	if err != nil {
		t.Fatal(err)
	}
	ruleset := parse(t, result.Ruleset)
	rs, err := rulecascade.Load(ruleset, nil, func(string) any { return document })
	if err != nil {
		t.Fatal(err)
	}
	skipped := map[string]string{}
	for _, line := range result.Skipped {
		pointer, reason, _ := strings.Cut(strings.TrimPrefix(line, "skipped "), ": ")
		skipped[strings.TrimPrefix(pointer, "/components/schemas/")] = reason
	}
	sources := map[string]bool{}
	for _, rule := range rules(ruleset) {
		sources[strings.TrimPrefix(getPath(rule, "x-generated-from").(string), "/components/schemas/")] = true
	}
	for pointer := range skipped {
		if sources[pointer] {
			t.Errorf("%s is both derived and skipped", pointer)
		}
	}
	return derived{result, ruleset, rs, skipped}
}

func rules(ruleset *rulecascade.Object) []*rulecascade.Object {
	list, _ := ruleset.Get("rules")
	items, _ := list.([]any)
	out := make([]*rulecascade.Object, len(items))
	for i, r := range items {
		out[i] = r.(*rulecascade.Object)
	}
	return out
}

func ruleIDs(ruleset *rulecascade.Object) []string {
	var ids []string
	for _, r := range rules(ruleset) {
		ids = append(ids, getPath(r, "id").(string))
	}
	return ids
}

type finding struct {
	rule, code string
	fields     []string
}

func findings(t *testing.T, rs *rulecascade.RuleSet, data string, more string) []finding {
	t.Helper()
	request := `{"entity": "Thing", "operation": "create", "data": ` + data + more + `}`
	result, err := rs.Evaluate(jsonValue(t, request), "server", nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []finding
	for _, f := range result.Findings {
		out = append(out, finding{f.Rule, f.Code, f.Fields})
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func firstDifference(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return fmt.Sprintf("line %d\nwant: %s\n got: %s", i+1, a, b)
		}
	}
	return "(same lines)"
}

// ------------------------------------------------------------------ every constraint kind

// One property per constraint kind (EVERYTHING in tools/tests/test_openapi_rules.py).
const everythingProperties = `{
	"id": {"type": "string", "format": "uuid", "readOnly": true},
	"name": {"type": "string", "minLength": 2, "maxLength": 5},
	"kind": {"type": "string", "enum": ["a", "b", null]},
	"level": {"enum": [1, 2.5, true]},
	"code": {"type": "string", "pattern": "^[A-Z]{3}$"},
	"email": {"type": "string", "format": "email"},
	"born": {"type": "string", "format": "date"},
	"seen": {"type": "string", "format": "date-time"},
	"amount": {"type": "number", "minimum": 0, "maximum": 100, "multipleOf": 0.01},
	"ratio": {"type": "number", "exclusiveMinimum": 0, "exclusiveMaximum": 1},
	"count": {"type": "integer"},
	"active": {"type": "boolean"},
	"nickname": {"type": ["string", "null"]},
	"reference": {"type": ["string", "integer"]},
	"tags": {"type": "array", "minItems": 1, "maxItems": 3, "items": {"type": "string"}},
	"address": {"$ref": "#/components/schemas/Address"},
	"owner": {"type": "object", "required": ["name"],
		"properties": {"name": {"type": "string"},
			"home": {"type": "object", "required": ["zip"], "properties": {"zip": {"type": "string", "maxLength": 5}}}}},
	"lines": {"type": "array",
		"items": {"type": "object", "required": ["sku"],
			"properties": {"sku": {"type": "string", "pattern": "^[a-z]+$"},
				"qty": {"type": "integer", "minimum": 1},
				"size": {"type": "object", "required": ["unit"], "properties": {"unit": {"type": "string", "enum": ["cm", "in"]}}},
				"notes": {"type": "array", "maxItems": 1, "items": {"type": "string"}}}}}
}`

const address = `"Address": {"type": "object", "required": ["city"], "properties": {"city": {"type": "string", "minLength": 2}}}`

func everything(t *testing.T) *rulecascade.Object {
	return openapi(t, everythingProperties, []string{"id", "name", "address"}, address)
}

const base = `{"name": "Maya", "address": {"city": "Apex"}}` // satisfies every required rule

// (rule id, data on which it fires, pointers of its findings, data on which every rule is quiet)
var cases = []struct {
	rule, bad string
	pointers  []string
	good      string
}{
	{"name.required", `{"address": {"city": "Apex"}}`, []string{"/name"}, ""},
	{"name.required", `{"name": null, "address": {"city": "Apex"}}`, []string{"/name"}, ""},
	{"address.required", `{"name": "Maya"}`, []string{"/address"}, ""},
	{"name.type", `{"name": 5}`, []string{"/name"}, `{"name": "Maya"}`},
	{"name.min-length", `{"name": "M"}`, []string{"/name"}, `{"name": "Ma"}`},
	{"name.max-length", `{"name": "Maya O"}`, []string{"/name"}, `{"name": "Mayas"}`},
	{"name.max-length", `{"name": "😀😀😀😀😀😀"}`, []string{"/name"}, `{"name": "😀😀😀😀😀"}`}, // code points
	{"kind.enum", `{"kind": "c"}`, []string{"/kind"}, `{"kind": "b"}`},
	{"level.enum", `{"level": 2}`, []string{"/level"}, `{"level": 1.0}`},
	{"level.enum", `{"level": "1"}`, []string{"/level"}, `{"level": true}`},
	{"code.pattern", `{"code": "usd"}`, []string{"/code"}, `{"code": "USD"}`},
	{"code.pattern", `{"code": "USD\n"}`, []string{"/code"}, ""},
	{"email.format", `{"email": "maya(at)example.com"}`, []string{"/email"}, `{"email": "maya@example.com"}`},
	{"born.format", `{"born": "12/04/1990"}`, []string{"/born"}, `{"born": "1990-04-12"}`},
	{"born.format", `{"born": "1990-13-01"}`, []string{"/born"}, ""},
	{"seen.format", `{"seen": "2026-10-03 09:00"}`, []string{"/seen"}, `{"seen": "2026-10-03T09:00:00Z"}`},
	{"seen.format", `{"seen": "2026-10-03T24:00:00Z"}`, []string{"/seen"}, `{"seen": "2026-10-03T09:00:00.25-04:00"}`},
	{"id.format", `{"id": "not-a-uuid"}`, []string{"/id"}, `{"id": "123e4567-e89b-12d3-a456-426614174000"}`},
	{"amount.minimum", `{"amount": -0.01}`, []string{"/amount"}, `{"amount": 0}`},
	{"amount.maximum", `{"amount": 100.01}`, []string{"/amount"}, `{"amount": 100}`},
	{"amount.multiple-of", `{"amount": 10.005}`, []string{"/amount"}, `{"amount": 10.05}`},
	{"amount.type", `{"amount": "10"}`, []string{"/amount"}, `{"amount": 0.1}`},
	{"ratio.exclusive-minimum", `{"ratio": 0}`, []string{"/ratio"}, `{"ratio": 0.001}`},
	{"ratio.exclusive-maximum", `{"ratio": 1}`, []string{"/ratio"}, `{"ratio": 0.999}`},
	{"count.type", `{"count": 1.5}`, []string{"/count"}, `{"count": 2.0}`},
	{"count.type", `{"count": "2"}`, []string{"/count"}, `{"count": -3}`},
	{"active.type", `{"active": "true"}`, []string{"/active"}, `{"active": false}`},
	{"nickname.type", `{"nickname": 5}`, []string{"/nickname"}, `{"nickname": null}`},
	{"reference.type", `{"reference": 1.5}`, []string{"/reference"}, `{"reference": 7}`},
	{"reference.type", `{"reference": true}`, []string{"/reference"}, `{"reference": "r-7"}`},
	{"tags.type", `{"tags": "a"}`, []string{"/tags"}, `{"tags": ["a"]}`},
	{"tags.min-items", `{"tags": []}`, []string{"/tags"}, `{"tags": ["a", "b", "c"]}`},
	{"tags.max-items", `{"tags": ["a", "b", "c", "d"]}`, []string{"/tags"}, ""},
	{"tags.items-type", `{"tags": ["a", 5]}`, []string{"/tags"}, ""},
	{"address.type", `{"address": "Apex"}`, []string{"/address"}, ""},
	{"address.city.required", `{"address": {}}`, []string{"/address/city"}, `{"address": {"city": "Apex"}}`},
	{"address.city.min-length", `{"address": {"city": "A"}}`, []string{"/address/city"}, ""},
	{"owner.name.required", `{"owner": {}}`, []string{"/owner/name"}, `{"owner": {"name": "Sam"}}`},
	{"owner.home.zip.required", `{"owner": {"name": "Sam", "home": {}}}`, []string{"/owner/home/zip"},
		`{"owner": {"name": "Sam", "home": {"zip": "27502"}}}`},
	{"owner.home.zip.max-length", `{"owner": {"name": "Sam", "home": {"zip": "275021"}}}`, []string{"/owner/home/zip"}, ""},
	{"owner.home.type", `{"owner": {"name": "Sam", "home": 5}}`, []string{"/owner/home"}, ""},
	{"lines.type", `{"lines": {"sku": "a"}}`, []string{"/lines"}, `{"lines": []}`},
	{"lines.items-type", `{"lines": [{"sku": "a"}, 5]}`, []string{"/lines"}, `{"lines": [{"sku": "a"}]}`},
	{"lines.sku.required", `{"lines": [{"sku": "a"}, {}, {"qty": 1}]}`, []string{"/lines/1/sku", "/lines/2/sku"}, ""},
	{"lines.sku.pattern", `{"lines": [{"sku": "a"}, {"sku": "A1"}]}`, []string{"/lines/1/sku"}, ""},
	{"lines.qty.type", `{"lines": [{"sku": "a", "qty": 1.5}]}`, []string{"/lines/0/qty"}, `{"lines": [{"sku": "a", "qty": 3}]}`},
	{"lines.qty.minimum", `{"lines": [{"sku": "a", "qty": 0}]}`, []string{"/lines/0/qty"}, ""},
	{"lines.size.unit.required", `{"lines": [{"sku": "a", "size": {}}]}`, []string{"/lines/0/size/unit"},
		`{"lines": [{"sku": "a", "size": {"unit": "cm"}}]}`},
	{"lines.size.unit.enum", `{"lines": [{"sku": "a", "size": {"unit": "mm"}}]}`, []string{"/lines/0/size/unit"}, ""},
	{"lines.notes.max-items", `{"lines": [{"sku": "a", "notes": ["x", "y"]}]}`, []string{"/lines/0/notes"}, ""},
	{"lines.notes.items-type", `{"lines": [{"sku": "a", "notes": [5]}]}`, []string{"/lines/0/notes"}, ""},
	{"id.type", `{"id": 5}`, []string{"/id"}, ""},
	{"kind.type", `{"kind": 5}`, []string{"/kind"}, ""},
	{"code.type", `{"code": 5}`, []string{"/code"}, ""},
	{"email.type", `{"email": 5}`, []string{"/email"}, ""},
	{"born.type", `{"born": 19900412}`, []string{"/born"}, ""},
	{"seen.type", `{"seen": false}`, []string{"/seen"}, ""},
	{"ratio.type", `{"ratio": "0.5"}`, []string{"/ratio"}, ""},
	{"owner.type", `{"owner": "Sam"}`, []string{"/owner"}, ""},
	{"owner.name.type", `{"owner": {"name": 5}}`, []string{"/owner/name"}, ""},
	{"owner.home.zip.type", `{"owner": {"name": "Sam", "home": {"zip": 27502}}}`, []string{"/owner/home/zip"}, ""},
	{"address.city.type", `{"address": {"city": 5}}`, []string{"/address/city"}, ""},
	{"lines.sku.type", `{"lines": [{"sku": 5}]}`, []string{"/lines/0/sku"}, ""},
	{"lines.size.type", `{"lines": [{"sku": "a", "size": "big"}]}`, []string{"/lines/0/size"}, ""},
	{"lines.size.unit.type", `{"lines": [{"sku": "a", "size": {"unit": 5}}]}`, []string{"/lines/0/size/unit"}, ""},
	{"lines.notes.type", `{"lines": [{"sku": "a", "notes": "x"}]}`, []string{"/lines/0/notes"}, ""},
}

// overlay is {**base, **over} of two JSON objects.
func overlay(t *testing.T, over string) string {
	b := jsonValue(t, base).(*rulecascade.Object)
	o := jsonValue(t, over).(*rulecascade.Object)
	for _, k := range o.Keys() {
		v, _ := o.Get(k)
		b.Set(k, v)
	}
	text, err := rulecascade.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(text)
}

func TestEachConstraintFiresAndStaysQuiet(t *testing.T) {
	d := deriveThing(t, everything(t), Options{})
	covered := map[string]bool{}
	for _, c := range cases {
		rule := "thing." + c.rule
		covered[rule] = true
		t.Run(c.rule, func(t *testing.T) {
			data := c.bad
			if !strings.HasSuffix(rule, "name.required") && !strings.HasSuffix(rule, "address.required") {
				data = overlay(t, c.bad)
			}
			got := findings(t, d.rules, data, "")
			var pointers []string
			for _, f := range got {
				if f.rule == rule {
					pointers = append(pointers, f.fields...)
				}
				if f.code == rulecascade.EngineErrorCode && rule != "thing.lines.type" {
					t.Errorf("%s could not be evaluated", f.rule)
				}
			}
			if !reflect.DeepEqual(pointers, c.pointers) {
				t.Errorf("%s on %s: fields %v, want %v (all findings %v)", rule, data, pointers, c.pointers, got)
			}
			if c.good != "" {
				if quiet := findings(t, d.rules, overlay(t, c.good), ""); len(quiet) > 0 {
					t.Errorf("on %s: %v", c.good, quiet)
				}
			}
		})
	}
	// every derived rule has a case
	for _, id := range ruleIDs(d.ruleset) {
		if !covered[id] {
			t.Errorf("no case for %s", id)
		}
	}
}

func TestAbsentOptionalValuesAndAbsentParents(t *testing.T) {
	d := deriveThing(t, everything(t), Options{})
	if got := findings(t, d.rules, base, ""); len(got) > 0 {
		t.Errorf("findings on the base: %v", got)
	}
	if got := findings(t, d.rules, base, `, "operation": "update", "original": `+base); len(got) > 0 {
		t.Errorf("findings on update: %v", got)
	}
	got := findings(t, d.rules, `{"name": "Maya"}`, "")
	want := []finding{{"thing.address.required", "GEN-THING-033", []string{"/address"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// a value of the wrong type is reported by the type rule alone
	wrong := `{"name": 5, "code": 7, "email": [], "amount": "x", "tags": "a", "owner": "Sam", "address": 3}`
	var names []string
	for _, f := range findings(t, d.rules, wrong, "") {
		names = append(names, strings.TrimPrefix(f.rule, "thing."))
	}
	sort.Strings(names)
	if strings.Join(names, " ") != "address.type amount.type code.type email.type name.type owner.type tags.type" {
		t.Errorf("got %v", names)
	}
	// forEach needs a list: on anything else the per-element rules fail closed
	var each []string
	for _, f := range findings(t, d.rules, overlay(t, `{"lines": "none"}`), "") {
		if f.code == rulecascade.EngineErrorCode {
			each = append(each, f.rule)
		} else if f.rule != "thing.lines.type" {
			t.Errorf("unexpected %v", f)
		}
	}
	var forEach []string
	for _, r := range rules(d.ruleset) {
		if has(r, "forEach") {
			forEach = append(forEach, getPath(r, "id").(string))
		}
	}
	sort.Strings(each)
	sort.Strings(forEach)
	if len(each) == 0 || !reflect.DeepEqual(each, forEach) {
		t.Errorf("failed closed: %v, want every forEach rule %v", each, forEach)
	}
}

func TestMessagesHaveTheirArguments(t *testing.T) {
	d := deriveThing(t, everything(t), Options{})
	data := overlay(t, `{"name": "M", "kind": "c", "code": "usd", "amount": 100.5, "tags": [], "count": 1.5,
		"lines": [{"qty": 0}], "seen": "soon", "reference": []}`)
	result, err := d.rules.Evaluate(jsonValue(t, `{"entity": "Thing", "operation": "create", "data": `+data+`}`), "server", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range result.Findings {
		got[strings.TrimPrefix(f.Rule, "thing.")] = f.Message
		if f.Severity != "error" {
			t.Errorf("%s: severity %s", f.Rule, f.Severity)
		}
	}
	want := map[string]string{
		"name.min-length":    "Name is too short: the minimum length is 2.",
		"kind.enum":          "Kind must be one of: a, b.",
		"code.pattern":       "Code must match the pattern ^[A-Z]{3}$.",
		"seen.format":        "Seen must be a date and time written like 2026-10-03T09:00:00Z.",
		"amount.maximum":     "Amount must be at most 100.",
		"count.type":         "Count must be a whole number.",
		"reference.type":     "Reference must be text or a whole number.",
		"tags.min-items":     "Tags has too few entries: the minimum is 1.",
		"lines.sku.required": "Sku is required.",
		"lines.qty.minimum":  "Qty must be at least 1.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
	if result.Decision != "deny" {
		t.Errorf("decision %s", result.Decision)
	}
}

func TestTheShapeOfEveryRule(t *testing.T) {
	d := deriveThing(t, everything(t), Options{})
	document := everything(t)
	for i, rule := range rules(d.ruleset) {
		id := getPath(rule, "id").(string)
		if code := getPath(getPath(rule, "finding"), "code"); code != fmt.Sprintf("GEN-THING-%03d", i+1) {
			t.Errorf("%s: code %v", id, code)
		}
		if keys := rule.Keys(); strings.Join(keys[:6], " ") != "id kind title target operations triggers" {
			t.Errorf("%s: keys %v", id, keys)
		}
		if getPath(rule, "kind") != "validation" || getPath(rule, "severity") != "error" ||
			getPath(getPath(rule, "target"), "entity") != "Thing" || !strings.HasPrefix(id, "thing.") || id != strings.ToLower(id) {
			t.Errorf("%s: %s", id, pyRepr(rule))
		}
		if !has(getPath(getPath(rule, "finding"), "args"), "field") {
			t.Errorf("%s: no field argument", id)
		}
		pointerGet(t, document, getPath(rule, "x-generated-from").(string)) // names a constraint that exists
		topLevelRequired := strings.HasSuffix(id, ".required") && strings.Count(id, ".") == 2
		if has(rule, "when") != (!topLevelRequired || has(rule, "forEach")) {
			t.Errorf("%s: when", id)
		}
		if each, ok := rule.Get("forEach"); ok && each != "/lines" {
			t.Errorf("%s: forEach %v", id, each)
		}
	}
	byID := map[string]*rulecascade.Object{}
	for _, r := range rules(d.ruleset) {
		byID[getPath(r, "id").(string)] = r
	}
	// a reference is followed to where the constraint is written
	if got := getPath(byID["thing.address.city.min-length"], "x-generated-from"); got != "/components/schemas/Address/properties/city/minLength" {
		t.Errorf("got %v", got)
	}
	if got := getPath(byID["thing.address.required"], "x-generated-from"); got != "/components/schemas/Thing/required/2" {
		t.Errorf("got %v", got)
	}
	// the title names the full path, the finding the place inside the list entry
	sku := byID["thing.lines.sku.required"]
	if getPath(sku, "title") != "Lines sku is required" || getPath(getPath(getPath(sku, "finding"), "args"), "field") != "Sku" ||
		getPath(getPath(sku, "target"), "field") != "/sku" {
		t.Errorf("got %s", pyRepr(sku))
	}
	// read-only properties are not required
	if _, ok := byID["thing.id.required"]; ok {
		t.Error("thing.id.required derived")
	}
	if !reflect.DeepEqual(d.result.Skipped, []string{"skipped /components/schemas/Thing/required/0: " +
		"id is readOnly: the server assigns it, so a request need not carry it"}) {
		t.Errorf("skipped %v", d.result.Skipped)
	}
	// only the messages in use are emitted, and every message is used here
	en := getPath(getPath(d.ruleset, "messages"), "en").(*rulecascade.Object)
	if en.Len() != len(messages) {
		t.Errorf("%d messages, want %d", en.Len(), len(messages))
	}
}

func TestTheFormatPatternsArePortable(t *testing.T) {
	for _, f := range formats {
		if why := patternProblem(f.pattern); why != "" {
			t.Errorf("%s: %s", f.name, why)
		}
	}
	if why := patternProblem(`^\s+$`); why != `escape \s is not portable` {
		t.Errorf("got %q", why)
	}
}

func TestAHandWrittenRulesetCanExtendTheBaseline(t *testing.T) {
	document := everything(t)
	d := deriveThing(t, document, Options{})
	child := jsonValue(t, `{"ruleCascade": "1.0.0", "kind": "RuleSet",
		"metadata": {"id": "acme.things.thing", "version": "1.0.0", "title": "Thing rules"},
		"scope": [{"level": "organization", "id": "acme"}, {"level": "module", "id": "things"}],
		"extends": [{"ruleset": "acme.generated.thing", "version": "^1.0.0"}],
		"rules": [{"id": "thing.amount.needs-code", "kind": "validation",
			"target": {"entity": "Thing", "fields": ["/amount", "/code"]}, "operations": ["create"],
			"when": {"op": "exists", "args": [{"var": "data.amount"}]},
			"assert": {"op": "exists", "args": [{"var": "data.code"}]},
			"severity": "warning", "finding": {"code": "THG-001", "message": "thing.needsCode"}}],
		"messages": {"en": {"thing.needsCode": "Give the currency code of the amount."}}}`)
	rs, err := rulecascade.Load(child, map[string]any{"acme.generated.thing": d.ruleset}, func(string) any { return document })
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range findings(t, rs, overlay(t, `{"amount": -1}`), "") {
		got = append(got, f.rule)
	}
	if strings.Join(got, " ") != "thing.amount.minimum thing.amount.needs-code" {
		t.Errorf("got %v", got)
	}
}

// ------------------------------------------------------------------ what is skipped

func assertSkipped(t *testing.T, d derived, want map[string]string) {
	t.Helper()
	if len(d.skipped) != len(want) {
		t.Errorf("skipped %v", d.result.Skipped)
	}
	for pointer, fragment := range want {
		if reason, ok := d.skipped[pointer]; !ok || !strings.Contains(reason, fragment) {
			t.Errorf("%s: got %q, want %q", pointer, reason, fragment)
		}
	}
}

func TestSkipped(t *testing.T) {
	t.Run("patterns outside the portable subset", func(t *testing.T) {
		d := deriveThing(t, openapi(t, `{"a": {"type": "string", "pattern": "^\\s+$"}, "b": {"type": "string", "pattern": "^(?=x)"},
			"c": {"type": "string", "pattern": "^\\p{L}+$"}, "d": {"type": "string", "pattern": "^[a-z]+$"}}`, nil, ""), Options{})
		assertSkipped(t, d, map[string]string{
			"Thing/properties/a/pattern": "not a portable pattern (specification 4.4): escape \\s is not portable",
			"Thing/properties/b/pattern": "only plain (...) and non-capturing (?:...) groups are portable",
			"Thing/properties/c/pattern": "escape \\p is not portable"})
		if ids := ruleIDs(d.ruleset); !contains(ids, "thing.d.pattern") || contains(ids, "thing.a.pattern") {
			t.Errorf("rules %v", ids)
		}
	})
	t.Run("formats without a pattern", func(t *testing.T) {
		d := deriveThing(t, openapi(t, `{"a": {"type": "string", "format": "uri"}, "b": {"type": "integer", "format": "int32"}}`, nil, ""), Options{})
		assertSkipped(t, d, map[string]string{
			"Thing/properties/a/format": "format 'uri' is not derived (derived: email, date, date-time, uuid)",
			"Thing/properties/b/format": "format 'int32' is not derived"})
	})
	t.Run("constraints that are recognised and not derived", func(t *testing.T) {
		d := deriveThing(t, openapi(t, `{"a": {"const": "x"}, "b": {"type": "array", "uniqueItems": true, "items": {"type": "string"}},
			"c": {"allOf": [{"type": "string"}]}, "d": {"oneOf": [{"type": "string"}, {"type": "number"}]},
			"e": {"type": "object", "additionalProperties": false, "minProperties": 1, "properties": {}},
			"f": {"type": "object", "additionalProperties": true},
			"g": {"type": "number", "exclusiveMinimum": true, "minimum": 0},
			"h": {"enum": ["a", {"b": 1}]}, "i": {"enum": [null]},
			"j": {"type": "string", "minLength": -1, "maxLength": "5"},
			"k": {"type": "number", "multipleOf": 0, "maximum": 0.1234567890123456},
			"l": {"type": "date"}, "m": {"type": "string", "minLength": 2.0}}`, nil, ""), Options{})
		assertSkipped(t, d, map[string]string{
			"Thing/properties/a/const":                "write a rule with eq",
			"Thing/properties/b/uniqueItems":          "no operator that compares the entries of a list",
			"Thing/properties/c/allOf":                "schema composition is not derived",
			"Thing/properties/d/oneOf":                "schema composition is not derived",
			"Thing/properties/e/additionalProperties": "cannot enumerate the properties of an object",
			"Thing/properties/e/minProperties":        "cannot enumerate the properties of an object",
			"Thing/properties/g/exclusiveMinimum":     "the boolean form of OpenAPI 3.0",
			"Thing/properties/h/enum":                 "only an enum of strings, numbers and booleans",
			"Thing/properties/i/enum":                 "only an enum of strings, numbers and booleans",
			"Thing/properties/j/minLength":            "not a non-negative whole number",
			"Thing/properties/j/maxLength":            "not a non-negative whole number",
			"Thing/properties/k/multipleOf":           "not a positive number",
			"Thing/properties/k/maximum":              "more than 15 significant digits",
			"Thing/properties/l/type":                 "'date' is not a JSON Schema type",
			"Thing/properties/m/minLength":            "not a non-negative whole number"})
	})
	t.Run("required that cannot be a rule", func(t *testing.T) {
		d := deriveThing(t, openapi(t, `{"a": {"type": "string", "readOnly": true}, "b": {"type": ["string", "null"]}, "c": {"type": "string"}}`,
			[]string{"a", "b", "c", "ghost"}, ""), Options{})
		assertSkipped(t, d, map[string]string{"Thing/required/0": "a is readOnly", "Thing/required/1": "b may be null",
			"Thing/required/3": "ghost is required but not declared under properties"})
	})
	t.Run("references", func(t *testing.T) {
		d := deriveThing(t, openapi(t, `{"a": {"$ref": "./other.yaml#/components/schemas/X"}, "b": {"$ref": "#/components/schemas/Missing"},
			"c": {"$ref": "#/components/schemas/Node"}, "d": {"$ref": "#/components/schemas/Name", "maxLength": 3},
			"e": {"$ref": "#/components/schemas/Alias"}}`, nil,
			`"Node": {"type": "object", "properties": {"next": {"$ref": "#/components/schemas/Node"}, "value": {"type": "integer"}}},
			"Name": {"type": "string", "minLength": 1}, "Alias": {"$ref": "#/components/schemas/Name"}`), Options{})
		assertSkipped(t, d, map[string]string{
			"Thing/properties/a/$ref":      "is in another document",
			"Thing/properties/b/$ref":      "does not resolve",
			"Node/properties/next/$ref":    "refers back to a schema that contains it",
			"Thing/properties/d/maxLength": "a constraint next to $ref is not derived"})
		if ids := strings.Join(ruleIDs(d.ruleset), " "); ids != "thing.c.type thing.c.value.type thing.d.type thing.d.min-length "+
			"thing.e.type thing.e.min-length" {
			t.Errorf("rules %s", ids)
		}
	})
	t.Run("lists", func(t *testing.T) {
		d := deriveThing(t, openapi(t, `{"codes": {"type": "array", "items": {"type": "string", "pattern": "^[A-Z]+$", "enum": ["A"]}},
			"grid": {"type": "array", "items": {"type": "array", "items": {"type": "number"}}},
			"orders": {"type": "array", "items": {"type": "object", "properties": {
				"lines": {"type": "array", "minItems": 1, "items": {"type": "object", "properties": {"sku": {"type": "string"}}}}}}}}`, nil, ""), Options{})
		assertSkipped(t, d, map[string]string{
			"Thing/properties/codes/items/pattern":                 "constraints on list entries that are not objects",
			"Thing/properties/codes/items/enum":                    "constraints on list entries that are not objects",
			"Thing/properties/grid/items/items":                    "constraints on list entries that are not objects",
			"Thing/properties/orders/items/properties/lines/items": "forEach reaches one level"})
		if ids := strings.Join(ruleIDs(d.ruleset), " "); ids != "thing.codes.type thing.codes.items-type thing.grid.type "+
			"thing.grid.items-type thing.orders.type thing.orders.items-type thing.orders.lines.type thing.orders.lines.min-items "+
			"thing.orders.lines.items-type" {
			t.Errorf("rules %s", ids)
		}
	})
	t.Run("property names", func(t *testing.T) {
		d := deriveThing(t, openapi(t, `{"first name": {"type": "string"}, "a.b": {"type": "string"}, "_": {"type": "string"},
			"fooBar": {"type": "string"}, "foo_bar": {"type": "string"}, "snake_case-and-kebab": {"type": "string"}}`, nil, ""), Options{})
		assertSkipped(t, d, map[string]string{
			"Thing/properties/first name":   "the property name 'first name' cannot be written in a ruleset path",
			"Thing/properties/a.b":          "cannot be written in a ruleset path",
			"Thing/properties/_":            "cannot be written in a ruleset path",
			"Thing/properties/foo_bar/type": "its rule id thing.foo-bar.type is already taken by /components/schemas/Thing/properties/fooBar/type"})
		if ids := strings.Join(ruleIDs(d.ruleset), " "); ids != "thing.foo-bar.type thing.snake-case-and-kebab.type" {
			t.Errorf("rules %s", ids)
		}
	})
}

func TestWordsAndLabels(t *testing.T) {
	for name, want := range map[string]string{"fullName": "full-name", "full_name": "full-name", "full-name": "full-name",
		"HTTPServerURL": "http-server-url", "x2Y": "x2-y", "swiftCode": "swift-code", "_": ""} {
		if got := kebab(name); got != want {
			t.Errorf("kebab(%q) = %q, want %q", name, got, want)
		}
	}
	if got := label([]string{"beneficiary", "swiftCode"}); got != "Beneficiary swift code" {
		t.Errorf("got %q", got)
	}
	if count("1", "entry") != "1 entry" || count("3", "entry") != "3 entries" || count("140", "character") != "140 characters" {
		t.Error("count")
	}
}

// ------------------------------------------------------------------ requests and options

func TestRequestsThatCannotProduceARuleset(t *testing.T) {
	document := openapi(t, `{"a": {"type": "string"}}`, nil, `"Text": {"type": "string"}`)
	for _, c := range []struct {
		doc      any
		opts     Options
		fragment string
	}{
		{document, Options{Schema: "Nothing", ID: "acme.x"}, "#/components/schemas/Nothing is not in the OpenAPI document"},
		{document, Options{Schema: "Text", ID: "acme.x"}, "is not an object schema"},
		{document, Options{Schema: "Thing", ID: "Acme.X"}, "'Acme.X' is not a ruleset id"},
		{document, Options{Schema: "Thing", ID: "acme.x", Entity: "my thing"}, "'my thing' is not an entity name"},
		{document, Options{Schema: "Thing", ID: "acme.x", Scope: []string{"organization:Acme"}}, "is not a scope step"},
		{document, Options{Schema: "Thing", ID: "acme.x", Scope: []string{"organization"}},
			"{'level': 'organization', 'id': ''} is not a scope step"},
		{[]any{}, Options{Schema: "Thing", ID: "acme.x"}, "must be an object"},
	} {
		_, err := FromDocument(c.doc, c.opts)
		var derr *DerivationError
		if !errors.As(err, &derr) || !strings.Contains(err.Error(), c.fragment) {
			t.Errorf("got %v, want %q", err, c.fragment)
		}
	}
	if _, err := FromFile(filepath.Join(t.TempDir(), "none.yaml"), Options{Schema: "Thing", ID: "acme.x"}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("got %v", err)
	}
}

var optionsDocument = `{"a": {"type": "string"}, "b": {"type": "integer", "minimum": 1}}`

func TestDefaultsAndTheLayout(t *testing.T) {
	d := deriveThing(t, openapi(t, optionsDocument, []string{"a"}, ""), Options{})
	if keys := strings.Join(d.ruleset.Keys(), " "); keys != "ruleCascade kind metadata scope entities rules messages" {
		t.Errorf("keys %s", keys)
	}
	text := string(d.result.Ruleset)
	head := "# Baseline rules derived by `rulecheck derive` from\n# ./openapi.yaml#/components/schemas/Thing\n" +
		"# Do not edit this file: change the OpenAPI description and derive it again.\n" + `ruleCascade: 1.0.0
kind: RuleSet

metadata:
  id: acme.generated.thing
  version: 1.0.0
  title: Thing - baseline rules derived from the OpenAPI schema

scope:
  - { level: organization, id: acme }

entities:
  Thing:
    schema: { $ref: "./openapi.yaml#/components/schemas/Thing" }

`
	if before, _, _ := strings.Cut(text, "rules:"); before != head {
		t.Errorf("head:\n%s", firstDifference(head, before))
	}
	rule := `
  - id: thing.b.minimum
    kind: validation
    title: B is at least 1
    target: { entity: Thing, field: /b }
    operations: [create, update]
    triggers: [blur, submit]
    when: { op: eq, args: [ { op: typeOf, args: [ { var: data.b } ] }, number ] }
    assert: { op: gte, args: [ { var: data.b }, 1 ] }
    severity: error
    finding: { code: GEN-THING-004, message: generated.minimum, args: { field: B, min: 1 } }
    x-generated-from: /components/schemas/Thing/properties/b/minimum

messages:
  en:
    generated.required: "{field} is required."
`
	if !strings.Contains(text, rule) {
		t.Errorf("no rule block like\n%s\nin\n%s", rule, text)
	}
	for _, line := range strings.Split(text, "\n") {
		if len([]rune(line)) > width || line != strings.TrimRight(line, " ") {
			t.Errorf("line %q", line)
		}
	}
	if !strings.HasSuffix(text, "\n") || strings.HasSuffix(text, "\n\n") {
		t.Error("the text must end with exactly one line break")
	}
}

func TestOptions(t *testing.T) {
	dir := t.TempDir()
	tests := filepath.Join(dir, "rules", "thing.tests.yaml")
	os.MkdirAll(filepath.Dir(tests), 0o755)
	os.WriteFile(tests, []byte(`- name: t
  entity: Item
  operation: create
  given: { data: {} }
  expect: { decision: deny, findings: [ { rule: item.a.required } ] }
`), 0o644)
	document := openapi(t, optionsDocument, []string{"a"}, "")
	d := deriveThing(t, document, Options{Document: "../api/things.yaml", Entity: "Item",
		Scope: []string{"enterprise:acme", "project:x"}, Version: "2.1.0", Title: "Items", TestsFile: tests,
		Output: filepath.Join(dir, "rules", "item.ruleset.yaml")})
	text := string(d.result.Ruleset)
	for _, want := range []string{
		"# ../api/things.yaml#/components/schemas/Thing\n",
		"# The golden tests are attached from ./thing.tests.yaml.\n",
		"metadata:\n  id: acme.generated.thing\n  version: 2.1.0\n  title: Items\n",
		"scope:\n  - { level: enterprise, id: acme }\n  - { level: project, id: x }\n",
		`  Item:` + "\n" + `    schema: { $ref: "../api/things.yaml#/components/schemas/Thing" }`,
		"tests:\n  - name: t\n    entity: Item\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
	var codes []string
	for _, r := range rules(d.ruleset) {
		codes = append(codes, getPath(getPath(r, "finding"), "code").(string))
	}
	if strings.Join(codes, " ") != "GEN-ITEM-001 GEN-ITEM-002 GEN-ITEM-003 GEN-ITEM-004" {
		t.Errorf("codes %v", codes)
	}
	if outcomes, err := d.rules.RunTests(getPath(d.ruleset, "tests"), nil); err != nil || len(outcomes[0].Failures) > 0 {
		t.Errorf("golden test: %v %v", err, outcomes)
	}
}

func TestFindingCodesSurviveAChangeOfTheSchema(t *testing.T) {
	dir := t.TempDir()
	before := deriveThing(t, openapi(t, optionsDocument, []string{"a"}, ""), Options{})
	earlier := filepath.Join(dir, "earlier.ruleset.yaml")
	os.WriteFile(earlier, before.result.Ruleset, 0o644)
	grown := openapi(t, `{"first": {"type": "boolean"}, "a": {"type": "string", "maxLength": 9},
		"b": {"type": "integer", "minimum": 1}}`, []string{"a"}, "")
	code := func(d derived) map[string]string {
		out := map[string]string{}
		for _, r := range rules(d.ruleset) {
			out[getPath(r, "id").(string)] = getPath(getPath(r, "finding"), "code").(string)
		}
		return out
	}
	old, renumbered, kept := code(before), code(deriveThing(t, grown, Options{})), code(deriveThing(t, grown, Options{CodesFrom: earlier}))
	changed := false
	for id, c := range old {
		changed = changed || renumbered[id] != c
		if kept[id] != c {
			t.Errorf("%s: %s, want %s", id, kept[id], c)
		}
	}
	if !changed || kept["thing.first.type"] != "GEN-THING-005" || kept["thing.a.max-length"] != "GEN-THING-006" {
		t.Errorf("renumbered %v, kept %v", renumbered, kept)
	}
	// codes of another entity, or not in the GEN form, are not kept
	os.WriteFile(earlier, []byte("rules:\n  - { id: thing.a.required, finding: { code: GEN-THING-007 } }\n"+
		"  - { id: thing.b.type, finding: { code: GEN-OTHER-009 } }\n  - { id: thing.a.type }\n"), 0o644)
	got := code(deriveThing(t, openapi(t, optionsDocument, []string{"a"}, ""), Options{CodesFrom: earlier}))
	if got["thing.a.required"] != "GEN-THING-007" || got["thing.a.type"] != "GEN-THING-008" || got["thing.b.type"] != "GEN-THING-009" {
		t.Errorf("got %v", got)
	}
}

func TestFromFileWithPathsRelativeToTheOutput(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "api"), 0o755)
	os.MkdirAll(filepath.Join(dir, "rules"), 0o755)
	api := filepath.Join(dir, "api", "things.openapi.json")
	os.WriteFile(api, []byte(`{"openapi": "3.1.0", "info": {"title": "t", "version": "1"}, "paths": {}, "components": {"schemas":
		{"Thing": {"type": "object", "required": ["a"], "properties": {"a": {"type": "string", "pattern": "^\\s*$", "format": "uri"},
		"b": {"type": "integer"}}}}}}`), 0o644)
	got, err := FromFile(api, Options{Schema: "Thing", ID: "acme.generated.thing", Output: filepath.Join(dir, "rules", "thing.ruleset.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got.Ruleset), "# Baseline rules derived by `rulecheck derive` from\n"+
		"# ../api/things.openapi.json#/components/schemas/Thing\n") {
		t.Errorf("got\n%s", got.Ruleset)
	}
	want := []string{
		"skipped /components/schemas/Thing/properties/a/pattern: not a portable pattern (specification 4.4): escape \\s is not portable",
		"skipped /components/schemas/Thing/properties/a/format: format 'uri' is not derived (derived: email, date, date-time, uuid)"}
	if !reflect.DeepEqual(got.Skipped, want) {
		t.Errorf("skipped %q", got.Skipped)
	}
	if n := len(rules(parse(t, got.Ruleset))); n != 3 {
		t.Errorf("%d rules, want 3", n)
	}
}

func TestGoldenTestsThatDoNotFitTheSchemaStopTheDerivation(t *testing.T) {
	tests := filepath.Join(t.TempDir(), "bad.tests.yaml")
	os.WriteFile(tests, []byte("- { name: no expectation, entity: Thing, operation: create, given: { data: {} } }\n"), 0o644)
	_, err := FromDocument(openapi(t, `{"a": {"type": "string"}}`, nil, ""), Options{Schema: "Thing", ID: "acme.x", TestsFile: tests})
	var problems *rulecascade.LoadError
	if !errors.As(err, &problems) || !contains(problems.Codes(), "SCHEMA_INVALID") ||
		!strings.Contains(err.Error(), "the derived ruleset does not load") {
		t.Errorf("got %v", err)
	}
}

// ------------------------------------------------------------------ YAML

var awkward = []string{"yes", "No", "on", "off", "y", "~", "null", "true", "1", "1.5", "1e3", "0x1F", "0o17", "1_000", "12:30",
	"2026-10-03", ".inf", "-", "- a", "a: b", "a #b", "#b", " lead", "trail ", "", "a,b", "[a]", "{a}", "a\nb",
	`say "hi"`, `back\slash`, "tab\t", "ünï", "😀", " ", "\x07", "*star", "&and", "!bang",
	"%pct", "@at", "`tick", "'single'", "plain words and/or $ref", "1.0.0", "/a/b", "a.b-c_d"}

// Every scalar reads back as the same value, and those YAML 1.1 parsers read differently are quoted.
func TestEveryScalarMeansTheSameToEveryParser(t *testing.T) {
	strs := make([]any, len(awkward))
	keys := rulecascade.NewObject()
	for i, s := range awkward {
		strs[i] = s
		keys.Set(s, num(strconv.Itoa(i)))
	}
	value := obj("strings", strs, "keys", keys,
		"numbers", []any{num("0"), num("-5"), num("2.5"), num("0.000001"), num("1e21"), num("123456789012345"), num("-0.1")},
		"others", []any{true, false, nil}, "empty", obj("list", []any{}, "map", rulecascade.NewObject()),
		"nested", []any{[]any{num("1"), []any{num("2"), num("3")}}, obj("a", []any{obj("b", obj("c", strs[:8]))})})
	text := toYAML(value, nil)
	back := parse(t, []byte(text))
	if !rulecascade.Equal(back, value) {
		t.Errorf("read back differently:\n%s", text)
	}
	for _, want := range []string{"  - \"yes\"\n", "  - 1.0.0\n", "  - 0.000001\n", "  - 1000000000000000000000\n", `  "0x1F": 11`} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
	for _, s := range []string{"yes", "1_000", "12:30", "2026-10-03", "0o17", "1e3"} {
		if isPlain(s) {
			t.Errorf("%q must be quoted", s)
		}
	}
}

func num(text string) any { return json.Number(text) }

func TestAwkwardValuesFromASchemaSurvive(t *testing.T) {
	enum := make([]string, len(awkward))
	for i, s := range awkward {
		q, _ := rulecascade.Marshal(s)
		enum[i] = string(q)
	}
	document := openapi(t, `{"answer": {"type": "string", "enum": [`+strings.Join(enum, ", ")+`], "pattern": "^[^\"'\\\\]*: #\\d+$"},
		"on": {"type": "number", "minimum": 0.000001}}`, nil, "")
	d := deriveThing(t, document, Options{})
	if len(d.result.Skipped) > 0 {
		t.Errorf("skipped %v", d.result.Skipped)
	}
	got := findings(t, d.rules, `{"answer": "No", "on": 1}`, "")
	want := []finding{{"thing.answer.pattern", "GEN-THING-003", []string{"/answer"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestNumbers(t *testing.T) {
	for text, want := range map[string]string{"1": "1", "1.0": "1", "2.50": "2.5", "1e21": "1000000000000000000000",
		"1.00000000000000001": "1", "-0.0": "0", "0.1": "0.1", "123456789012345678": "123456789012345680", "1.5e-7": "0.00000015"} {
		if got := canonNum(json.Number(text)); got != want {
			t.Errorf("canonNum(%s) = %s, want %s", text, got, want)
		}
	}
	// YAML numbers by the 1.2 core schema: 012 is twelve, 0o17 fifteen, `no` a string; 2.0 is not a count
	v, err := parseYAML([]byte("a: 012\nb: 0o17\nc: no\nd: 2.0\ne: 0x1F\nf: 1e3\n"))
	if err != nil {
		t.Fatal(err)
	}
	if pyRepr(v) != "{'a': 12, 'b': 15, 'c': 'no', 'd': 2.0, 'e': 31, 'f': 1000.0}" {
		t.Errorf("got %s", pyRepr(v))
	}
	if isCount(getPath(v, "d")) || !isCount(getPath(v, "a")) {
		t.Error("isCount")
	}
	if _, err := parseYAML([]byte("a: 1\na: 2\n")); err == nil {
		t.Error("a repeated key is an error")
	}
	if _, err := parseYAML([]byte("a: .inf\n")); err == nil {
		t.Error("infinity is an error")
	}
}

// ------------------------------------------------------------------ JSON Schema input

const jsonSchema = `{
	"$schema": "https://json-schema.org/draft/2020-12/schema",
	"title": "Order",
	"type": "object",
	"required": ["id", "lines", "customer"],
	"properties": {
		"id": {"type": "string", "format": "uuid"},
		"status": {"enum": ["open", "paid", "void"]},
		"note": {"type": "string", "maxLength": 140},
		"total": {"type": "number", "minimum": 0, "multipleOf": 0.01},
		"customer": {"$ref": "#/$defs/Customer"},
		"lines": {"type": "array", "minItems": 1, "items": {"$ref": "#/definitions/Line"}},
		"parent": {"$ref": "#"}
	},
	"$defs": {
		"Customer": {"type": "object", "required": ["email"], "properties": {
			"email": {"type": "string", "format": "email"},
			"code": {"type": "string", "pattern": "^[A-Z]{3}$", "minLength": 3}}},
		"Order": {"$ref": "#"}
	},
	"definitions": {
		"Line": {"type": "object", "required": ["sku", "qty"], "properties": {
			"sku": {"type": "string", "pattern": "^[a-z0-9-]+$"},
			"qty": {"type": "integer", "minimum": 1, "maximum": 999}}}
	}
}`

func TestJSONSchemaRoot(t *testing.T) {
	document := jsonValue(t, jsonSchema)
	result, err := FromDocument(document, Options{ID: "acme.generated.order", Document: "./order.schema.json"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(result.Ruleset)
	ruleset := parse(t, result.Ruleset)
	for _, want := range []string{
		"# ./order.schema.json#\n",
		"title: Order - baseline rules derived from the JSON Schema\n",
		`  Order:` + "\n" + `    schema: { $ref: "./order.schema.json#" }`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
	want := "order.id.required order.id.type order.id.format order.status.enum order.note.type order.note.max-length " +
		"order.total.type order.total.minimum order.total.multiple-of order.customer.required order.customer.type " +
		"order.customer.email.required order.customer.email.type order.customer.email.format order.customer.code.type " +
		"order.customer.code.min-length order.customer.code.pattern order.lines.required order.lines.type " +
		"order.lines.min-items order.lines.items-type order.lines.sku.required order.lines.sku.type " +
		"order.lines.sku.pattern order.lines.qty.required order.lines.qty.type order.lines.qty.minimum order.lines.qty.maximum"
	if got := strings.Join(ruleIDs(ruleset), " "); got != want {
		t.Errorf("rules\n got %s\nwant %s", got, want)
	}
	if !reflect.DeepEqual(result.Skipped, []string{"skipped /properties/parent/$ref: # refers back to a schema that contains it; recursion is not derived"}) {
		t.Errorf("skipped %q", result.Skipped)
	}
	sources := map[string]string{}
	for _, r := range rules(ruleset) {
		sources[getPath(r, "id").(string)] = getPath(r, "x-generated-from").(string)
	}
	if sources["order.customer.email.format"] != "/$defs/Customer/properties/email/format" ||
		sources["order.lines.qty.maximum"] != "/definitions/Line/properties/qty/maximum" ||
		sources["order.lines.required"] != "/required/1" {
		t.Errorf("sources %v", sources)
	}
}

func TestJSONSchemaDefinitionLoadsAndEvaluates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "order.schema.json")
	os.WriteFile(path, []byte(jsonSchema), 0o644)
	result, err := FromFile(path, Options{Pointer: "/$defs/Customer", ID: "acme.generated.customer",
		Output: filepath.Join(dir, "customer.ruleset.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Ruleset), `schema: { $ref: "./order.schema.json#/$defs/Customer" }`) {
		t.Errorf("got\n%s", result.Ruleset)
	}
	document, _ := readFile(path)
	rs, err := rulecascade.Load(parse(t, result.Ruleset), nil, func(string) any { return document })
	if err != nil {
		t.Fatal(err)
	}
	request := func(data string) []string {
		r, err := rs.Evaluate(jsonValue(t, `{"entity": "Customer", "operation": "create", "data": `+data+`}`), "server", nil)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range r.Findings {
			out = append(out, f.Rule+" "+f.Code)
		}
		return out
	}
	if got := request(`{"email": "maya@example.com", "code": "USD"}`); len(got) > 0 {
		t.Errorf("findings %v", got)
	}
	if got := strings.Join(request(`{"code": "us"}`), ", "); got !=
		"customer.email.required GEN-CUSTOMER-001, customer.code.min-length GEN-CUSTOMER-005, customer.code.pattern GEN-CUSTOMER-006" {
		t.Errorf("findings %s", got)
	}
	// a YAML JSON Schema, a reference to the root, and an unknown pointer
	yamlPath := filepath.Join(dir, "line.schema.yaml")
	os.WriteFile(yamlPath, []byte("definitions:\n  Line:\n    type: object\n    properties:\n      qty: { type: integer, maximum: 10 }\n"), 0o644)
	got, err := FromFile(yamlPath, Options{Pointer: "/definitions/Line", ID: "acme.generated.line", Output: filepath.Join(dir, "x.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	if ids := strings.Join(ruleIDs(parse(t, got.Ruleset)), " "); ids != "line.qty.type line.qty.maximum" {
		t.Errorf("rules %s", ids)
	}
	if _, err := FromDocument(jsonValue(t, jsonSchema), Options{Pointer: "/$defs/Order", ID: "acme.x"}); err != nil {
		t.Errorf("a $ref to the root: %v", err)
	}
	if _, err := FromDocument(jsonValue(t, jsonSchema), Options{Pointer: "/$defs/Nothing", ID: "acme.x"}); err == nil ||
		!strings.Contains(err.Error(), "#/$defs/Nothing is not in the JSON Schema document") {
		t.Errorf("got %v", err)
	}
	if _, err := FromDocument(jsonValue(t, `{"type": "object", "properties": {"a": {"type": "string"}}}`), Options{ID: "acme.x"}); err == nil {
		t.Error("a root without a title needs an entity name")
	}
}

// A document built by a Go program: map[string]any members are taken in sorted order.
func TestPlainGoValues(t *testing.T) {
	document := map[string]any{"openapi": "3.1.0", "components": map[string]any{"schemas": map[string]any{
		"Thing": map[string]any{"type": "object", "required": []any{"b"}, "properties": map[string]any{
			"b": map[string]any{"type": "integer", "minimum": 1}, "a": map[string]any{"type": "string", "maxLength": 3.0}}}}}}
	result, err := FromDocument(document, Options{Schema: "Thing", ID: "acme.generated.thing"})
	if err != nil {
		t.Fatal(err)
	}
	if ids := strings.Join(ruleIDs(parse(t, result.Ruleset)), " "); ids != "thing.a.type thing.b.required thing.b.type thing.b.minimum" {
		t.Errorf("rules %s", ids)
	}
	if !reflect.DeepEqual(result.Skipped, []string{"skipped /components/schemas/Thing/properties/a/maxLength: the value is not a non-negative whole number"}) {
		t.Errorf("skipped %q", result.Skipped)
	}
}
