package rulehttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	rulecascade "rulescascade.com/go"
)

func load(t *testing.T) *rulecascade.RuleSet {
	data, err := os.ReadFile("../../../conformance/bundles/acme.payments.transfer.bundle.json")
	if os.IsNotExist(err) {
		t.Skip("the test needs the Rule Cascade repository's conformance bundles") // the public Go mirror has only packages/go
	} else if err != nil {
		t.Fatal(err)
	}
	doc, _ := rulecascade.ParseJSON(data)
	rules, err := rulecascade.FromBundle(doc)
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

func TestEnforceMiddleware(t *testing.T) {
	rules := load(t)
	reached := false
	handler := Enforce(rules, Options{Entity: "Transfer", Operation: "create",
		Actor: func(*http.Request) map[string]any { return map[string]any{"id": "u-1", "roles": []any{"payer"}} },
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("the handler cannot read the body: %v", err)
		}
		if FromContext(r.Context()) == nil {
			t.Error("no evaluation in the context")
		}
		w.WriteHeader(http.StatusCreated)
	}))

	// an amount far above every limit: denied
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("POST", "/transfers", strings.NewReader(`{"amount": 99999999, "currency": "EUR", "beneficiary": {"name": "Sam", "country": "FR"}}`)))
	if w.Code != 422 || w.Header().Get("Content-Type") != "application/problem+json" || reached {
		t.Fatalf("denied: %d %s reached=%v", w.Code, w.Body, reached)
	}
	var problem map[string]any
	json.Unmarshal(w.Body.Bytes(), &problem)
	if problem["type"] != rulecascade.RuleViolationType || problem["evaluation"].(map[string]any)["decision"] != "deny" {
		t.Errorf("problem: %v", problem)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("POST", "/transfers", strings.NewReader(`not json`)))
	if w.Code != 400 {
		t.Errorf("unreadable body: %d", w.Code)
	}
}

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()
	if WriteError(w, &rulecascade.RequestError{Message: "x"}) != true || w.Code != 400 {
		t.Errorf("RequestError: %d", w.Code)
	}
	if WriteError(httptest.NewRecorder(), os.ErrNotExist) {
		t.Error("another error was answered")
	}
}
