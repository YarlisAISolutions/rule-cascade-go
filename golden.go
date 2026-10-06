package rulecascade

import (
	"fmt"
	"sort"
	"strings"
)

// TestOutcome is the outcome of one golden test. Failures is empty when the test passed.
type TestOutcome struct {
	Name     string
	Failures []string
}

// subset reports whether every member of expected is present in actual with an equal value.
func subset(expected, actual *Object) bool {
	for _, k := range expected.names() {
		if v, ok := actual.Get(k); !ok || !equal(v, expected.vals[k]) {
			return false
		}
	}
	return true
}

// RunTests runs golden tests against the ruleset: the `tests` member of a ruleset document, a
// list of objects with name, entity, operation, given, expect and an optional channel. A test
// passes when the decision matches, the findings come from exactly the expected rules, every
// expected finding and effect is matched member by member by an actual one, and the command names
// match in order.
func (rs *RuleSet) RunTests(tests any, operators Operators) ([]TestOutcome, error) {
	normalized, err := normalize(tests)
	if err != nil {
		return nil, err
	}
	list, _ := normalized.([]any)
	outcomes := make([]TestOutcome, 0, len(list))
	for _, x := range list {
		t := asObj(x)
		request := t.obj("given").copy()
		request.Set("entity", t.get("entity"))
		request.Set("operation", t.get("operation"))
		channel, ok := t.get("channel").(string)
		if !ok {
			channel = "server"
		}
		mf, err := rs.Manifest(channel)
		if err != nil {
			return nil, err
		}
		result, err := evaluate(mf, clone(request), operators)
		if err != nil {
			return nil, fmt.Errorf("test %q: %w", t.str("name"), err)
		}
		got, expect := result.jsonValue(), t.obj("expect")
		var why []string
		if !equal(got.get("decision"), expect.get("decision")) {
			why = append(why, fmt.Sprintf("decision %s != %s", show(got.get("decision")), show(expect.get("decision"))))
		}
		rulesOf := func(findings []any) []string {
			rules := make([]string, len(findings))
			for i, f := range findings {
				rules[i] = asObj(f).str("rule")
			}
			sort.Strings(rules)
			return rules
		}
		gotRules, wantRules := rulesOf(got.list("findings")), rulesOf(expect.list("findings"))
		matches := func(expected any, actual []any) bool {
			for _, a := range actual {
				if subset(asObj(expected), asObj(a)) {
					return true
				}
			}
			return false
		}
		if strings.Join(gotRules, "\n") != strings.Join(wantRules, "\n") {
			why = append(why, fmt.Sprintf("findings %v != %v", gotRules, wantRules))
		} else {
			// several findings may share a rule (forEach, type rules): each expectation must match one of them
			for _, e := range expect.list("findings") {
				if !matches(e, got.list("findings")) {
					why = append(why, "no finding matches "+show(e))
				}
			}
		}
		for _, e := range expect.list("effects") {
			if !matches(e, got.list("effects")) {
				why = append(why, "missing effect "+show(e))
			}
		}
		if want, ok := expect.Get("commands"); ok {
			names := []any{}
			for _, c := range result.Commands {
				names = append(names, c.Name)
			}
			if !equal(names, want) {
				why = append(why, fmt.Sprintf("commands %s != %s", show(names), show(want)))
			}
		}
		outcomes = append(outcomes, TestOutcome{t.str("name"), why})
	}
	return outcomes, nil
}
