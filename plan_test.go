package rulecascade

import (
	"os"
	"testing"
)

// benchRequest returns the request of tools/bench-requests.json with the given name.
func benchRequest(t *testing.T, name string) *Object {
	t.Helper()
	data, err := os.ReadFile("../../tools/bench-requests.json")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := ParseJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range asObj(spec).list("requests") {
		if asObj(r).str("name") == name {
			return asObj(r).obj("request")
		}
	}
	t.Fatalf("no request %q", name)
	return nil
}

func resultJSON(t *testing.T, rs *RuleSet, request any) string {
	t.Helper()
	result, err := rs.Evaluate(request, "server", nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := result.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A manifest changed after load is evaluated as changed, exactly as a ruleset freshly read from it
// would be: the plan made at load is never used stale.
func TestPlanFollowsManifestChanges(t *testing.T) {
	several := benchRequest(t, "international without SWIFT code, large: several findings and an effect")
	findings := func(rs *RuleSet) []Finding {
		result, err := rs.Evaluate(several, "server", nil)
		if err != nil {
			t.Fatal(err)
		}
		return result.Findings
	}
	ruleOf := func(rs *RuleSet, id string) (*Object, []any, *Object) {
		mf, err := rs.Manifest("server")
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range mf.list("rules") {
			if asObj(x).str("id") == id {
				return asObj(x), mf.list("rules"), mf
			}
		}
		t.Fatalf("no rule %s", id)
		return nil, nil, nil
	}
	base := findings(bundleRuleSet(t, "acme.payments.transfer"))
	if len(base) < 2 {
		t.Fatalf("the request should give several findings, got %d", len(base))
	}
	first, last := base[0].Rule, base[len(base)-1].Rule

	for _, c := range []struct {
		name   string
		change func(rs *RuleSet)
		check  func(t *testing.T, got []Finding)
	}{
		{"a rule added", func(rs *RuleSet) {
			rule, rules, mf := ruleOf(rs, first)
			added := rule.copy()
			added.Set("id", "added.rule")
			mf.Set("rules", append(append([]any{}, rules...), added))
		}, func(t *testing.T, got []Finding) {
			if len(got) != len(base)+1 {
				t.Fatalf("%d findings, want %d", len(got), len(base)+1)
			}
		}},
		{"a rule removed", func(rs *RuleSet) {
			_, rules, mf := ruleOf(rs, first)
			var kept []any
			for _, x := range rules {
				if asObj(x).str("id") != first {
					kept = append(kept, x)
				}
			}
			mf.Set("rules", kept)
		}, func(t *testing.T, got []Finding) {
			for _, f := range got {
				if f.Rule == first {
					t.Fatalf("the removed rule %s still runs", first)
				}
			}
		}},
		{"a rule replaced in place", func(rs *RuleSet) {
			rule, rules, _ := ruleOf(rs, first)
			for i, x := range rules {
				if asObj(x) == rule {
					replaced := rule.copy()
					replaced.Set("kind", "state")
					rules[i] = replaced
				}
			}
		}, func(t *testing.T, got []Finding) {
			if len(got) != len(base)-1 {
				t.Fatalf("%d findings, want %d", len(got), len(base)-1)
			}
		}},
		{"a kind changed", func(rs *RuleSet) {
			rule, _, _ := ruleOf(rs, first)
			rule.Set("kind", "state")
		}, func(t *testing.T, got []Finding) {
			if len(got) != len(base)-1 {
				t.Fatalf("%d findings, want %d", len(got), len(base)-1)
			}
		}},
		{"a priority changed", func(rs *RuleSet) {
			rule, _, _ := ruleOf(rs, last)
			rule.Set("priority", json1000)
		}, func(t *testing.T, got []Finding) {
			if got[0].Rule != last {
				t.Fatalf("the first finding is from %s, want %s, now the highest priority", got[0].Rule, last)
			}
		}},
		{"the manifest replaced", func(rs *RuleSet) {
			mf, _ := rs.Manifest("server")
			replaced := mf.copy()
			replaced.Set("rules", []any{})
			rs.Bundle().obj("manifests").Set("server", replaced)
		}, func(t *testing.T, got []Finding) {
			if len(got) != 0 {
				t.Fatalf("%d findings from a manifest without rules", len(got))
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rs := bundleRuleSet(t, "acme.payments.transfer")
			findings(rs) // the plan made at load is in use
			c.change(rs)
			c.check(t, findings(rs))
			fresh, err := FromBundle(rs.Bundle()) // a copy, read and planned from scratch
			if err != nil {
				t.Fatal(err)
			}
			if got, want := resultJSON(t, rs, several), resultJSON(t, fresh, several); got != want {
				t.Fatalf("after the change:\n%s\nread afresh:\n%s", got, want)
			}
		})
	}
}

var json1000 = intNumber(1000)
