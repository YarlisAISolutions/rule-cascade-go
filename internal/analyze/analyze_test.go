package analyze

import (
	"path/filepath"
	"testing"
)

func TestKey(t *testing.T) {
	for name, want := range map[string]string{
		"MAX_ORDER_QUANTITY": "order-quantity", "maxOrderQuantity": "order-quantity", "order.quantity": "quantity",
		"orderQty": "order-quantity", "getTotal()": "total", "customerAge": "customer-age", "HTTPTimeout": "http-timeout",
		"i": "i", "MAX": "",
	} {
		if got := Key(name); got != want {
			t.Errorf("Key(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestReadFacts(t *testing.T) {
	for _, c := range []struct {
		lines []string
		want  facts
	}{
		{[]string{"@Max(10)", "private int quantity;"}, facts{"quantity", "<=", "10"}},
		{[]string{"@Size(min = 2, max = 40) String name;"}, facts{"name", "<=", "40"}},
		{[]string{"@DecimalMin(\"0.01\")", "@NotNull", "BigDecimal amount;"}, facts{"amount", ">=", "0.01"}},
		{[]string{"static final int MIN_AGE = 18;"}, facts{"MIN_AGE", ">=", "18"}},
		{[]string{"const schema = z.object({ qty: z.number().int().max(10) })"}, facts{"qty", "<=", "10"}},
		{[]string{`<input {...register("email", { maxLength: { value: 80, message: "too long" } })} />`}, facts{"email", "<=", "80"}},
		{[]string{"if (order.getTotal() > 10000) {"}, facts{"order.getTotal", ">", "10000"}},
		{[]string{"return x;"}, facts{}},
	} {
		if got := readFacts(c.lines, 0); got != c.want {
			t.Errorf("readFacts(%q) = %+v, want %+v", c.lines, got, c.want)
		}
	}
}

func TestScanJavaAndReact(t *testing.T) {
	base, _ := filepath.Abs("testdata/shop")
	r := Scan(base, nil, Options{Features: map[string][]string{"checkout": {"**/checkout/**"}}, Known: map[string]string{"discount-percent": "shop.orders"}})
	if len(r.Modules) != 2 || r.Modules[0].Path != "order-service" || r.Modules[0].Build != "maven" || r.Modules[1].Build != "npm" {
		t.Errorf("modules: %+v", r.Modules)
	}
	libs := map[string]bool{}
	for _, h := range r.Hits {
		libs[h.Library] = true
		if h.Snippet == "for (int i = 0; i < 3; i++) {" {
			t.Error("a loop counter is not a business rule")
		}
		if h.Fingerprint == "" || h.Module == "" {
			t.Errorf("hit without fingerprint or module: %+v", h)
		}
	}
	for _, want := range []string{"bean-validation", "spring-security", "hand-written", "threshold", "branch", "named-limit", "react", "react-hook-form"} {
		if !libs[want] {
			t.Errorf("no %s hit", want)
		}
	}
	drift := map[string]bool{}
	for _, c := range r.Candidates {
		drift[c.Key] = c.Drift
		if c.Key == "discount-percent" && c.CoveredBy != "shop.orders" {
			t.Errorf("discount-percent is named by shop.orders: %+v", c)
		}
	}
	if d, ok := drift["quantity"]; !ok || !d {
		t.Errorf("quantity is 10 in Java and 20 in React: %+v", r.Candidates)
	}
	if d, ok := drift["total"]; !ok || d {
		t.Errorf("total is 10000 on both sides: %+v", r.Candidates)
	}
	if r.ByFeature["checkout"] == 0 || r.ByFeature["checkout"] == len(r.Hits) {
		t.Errorf("only the React checkout folder is the feature: %v of %d", r.ByFeature, len(r.Hits))
	}

	again := Scan(base, nil, Options{Baseline: map[string]bool{r.Hits[0].Fingerprint: true}, MinConfidence: 0.5})
	if again.Baselined == 0 || again.BelowConfidence == 0 {
		t.Errorf("baseline and min confidence leave hits out: %d baselined, %d below", again.Baselined, again.BelowConfidence)
	}
}
