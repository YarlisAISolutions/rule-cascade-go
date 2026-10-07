package rulecascade

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The HTTP answer to a denied operation is the same in every runtime: conformance/problem-details.json.
func TestProblemDetails(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(suiteDir, "problem-details.json"))
	if err != nil {
		t.Fatal(err)
	}
	var suite struct {
		Cases []struct {
			Name    string          `json:"name"`
			Result  Result          `json:"result"`
			Problem json.RawMessage `json:"problem"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &suite); err != nil {
		t.Fatal(err)
	}
	for _, c := range suite.Cases {
		got, _ := json.Marshal(ProblemDetails(&c.Result))
		var a, b any
		json.Unmarshal(got, &a)
		json.Unmarshal(c.Problem, &b)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s:\n got %s\nwant %s", c.Name, got, c.Problem)
		}
		violation := &RuleViolation{Result: &c.Result}
		if violation.Error() != b.(map[string]any)["detail"] {
			t.Errorf("%s: Error() = %q", c.Name, violation.Error())
		}
	}
}

func TestEnforce(t *testing.T) {
	bundle, err := os.ReadFile(filepath.Join(suiteDir, "bundles", "acme.payments.transfer.bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := ParseJSON(bundle)
	rules, err := FromBundle(doc)
	if err != nil {
		t.Fatal(err)
	}
	allowed, denied := corpusCase(t, "allow"), corpusCase(t, "deny")
	if result, err := rules.Enforce(allowed, "server", nil); err != nil || !result.Allowed() {
		t.Errorf("allowed: %v %v", result, err)
	}
	result, err := rules.Enforce(denied, "server", nil)
	var violation *RuleViolation
	if !errors.As(err, &violation) || violation.Result != result || violation.Problem().Status != 422 {
		t.Errorf("denied: %v", err)
	}
	var malformed *RequestError
	if _, err := rules.Enforce(map[string]any{"entity": "Transfer"}, "server", nil); !errors.As(err, &malformed) {
		t.Errorf("malformed: %v", err)
	}
}

// corpusCase returns a server-channel create request of the payments corpus with that decision.
func corpusCase(t *testing.T, decision string) any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(suiteDir, "evaluations.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := ParseJSON(data)
	cases, _ := doc.(*Object).Get("cases")
	for _, c := range cases.([]any) {
		o := c.(*Object)
		ruleset, _ := o.Get("ruleset")
		channel, _ := o.Get("channel")
		request, _ := o.Get("request")
		result, _ := o.Get("result")
		d, _ := result.(*Object).Get("decision")
		op, _ := request.(*Object).Get("operation")
		if ruleset == "acme.payments.transfer" && channel == "server" && op == "create" && d == decision {
			return request
		}
	}
	t.Fatalf("no %s case", decision)
	return nil
}
