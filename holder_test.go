package rulecascade

import (
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

func bundleRuleSet(t testing.TB, id string) *RuleSet {
	t.Helper()
	data, err := os.ReadFile("../../conformance/bundles/" + id + ".bundle.json")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := ParseJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := FromBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

// ruleSource is a load function whose answer the test changes.
type ruleSource struct {
	mu  sync.Mutex
	rs  *RuleSet
	err error
}

func (s *ruleSource) set(rs *RuleSet, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rs, s.err = rs, err
}

func (s *ruleSource) load() (*RuleSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rs, s.err
}

func TestHolderKeepsTheLastGoodRules(t *testing.T) {
	transfer, base := bundleRuleSet(t, "acme.payments.transfer"), bundleRuleSet(t, "acme.org.base")
	src := &ruleSource{rs: transfer}
	var heard []Reload
	h, err := NewHolder(src.load, HolderOptions{OnReload: func(r Reload) { heard = append(heard, r) }})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if h.Get() != transfer {
		t.Fatal("not the first rules")
	}
	if _, ok := h.NextRun(); ok {
		t.Fatal("a schedule without Interval or Cron")
	}

	src.set(nil, errors.New("artefact store unreachable"))
	if r := h.Refresh(); r.Err == nil || r.Changed || r.RuleSet != transfer || h.Get() != transfer {
		t.Fatalf("a failed load replaced the rules: %+v", r)
	}
	src.set(bundleRuleSet(t, "acme.payments.transfer"), nil) // same checksum, another value
	if r := h.Refresh(); r.Err != nil || r.Changed || h.Get() != transfer {
		t.Fatalf("the same checksum was swapped in: %+v", r)
	}
	src.set(base, nil)
	if r := h.Refresh(); r.Err != nil || !r.Changed || h.Get() != base {
		t.Fatalf("new rules were not swapped in: %+v", r)
	}
	if len(heard) != 3 {
		t.Fatalf("OnReload heard %d refreshes, want 3", len(heard))
	}
}

func TestHolderRefusesToStartWithoutRules(t *testing.T) {
	if _, err := NewHolder(func() (*RuleSet, error) { return nil, errors.New("no bundle") }, HolderOptions{}); err == nil || err.Error() != "no bundle" {
		t.Fatalf("got %v", err)
	}
	if _, err := NewHolder(func() (*RuleSet, error) { return nil, nil }, HolderOptions{}); err == nil {
		t.Fatal("nil rules accepted")
	}
	if _, err := NewHolder(func() (*RuleSet, error) { return nil, nil }, HolderOptions{Cron: "61 * * * *"}); err == nil {
		t.Fatal("bad cron accepted")
	}
}

func TestHolderRefreshesOnAnIntervalAndStopsOnClose(t *testing.T) {
	transfer, base := bundleRuleSet(t, "acme.payments.transfer"), bundleRuleSet(t, "acme.org.base")
	src := &ruleSource{rs: transfer}
	var mu sync.Mutex
	count := 0
	h, err := NewHolder(src.load, HolderOptions{Interval: 10 * time.Millisecond, OnReload: func(Reload) {
		mu.Lock()
		count++
		mu.Unlock()
	}})
	if err != nil {
		t.Fatal(err)
	}
	src.set(base, nil)
	deadline := time.Now().Add(5 * time.Second)
	for h.Get() != base && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.Get() != base {
		t.Fatal("the interval never swapped in the new rules")
	}
	h.Close()
	h.Close() // twice is fine
	mu.Lock()
	after := count
	mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if count != after {
		t.Fatalf("refreshed %d times after Close", count-after)
	}
	if _, ok := h.NextRun(); ok {
		t.Fatal("NextRun after Close")
	}
}

func TestHolderSchedulesTheNextCronFireTime(t *testing.T) {
	transfer := bundleRuleSet(t, "acme.payments.transfer")
	h, err := NewHolder(func() (*RuleSet, error) { return transfer, nil }, HolderOptions{Cron: "*/5 * * * *"})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	var next time.Time
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		var ok bool
		if next, ok = h.NextRun(); ok {
			break
		}
	}
	if next.IsZero() || next.UTC().Minute()%5 != 0 || next.Second() != 0 || !next.After(time.Now()) || next.After(time.Now().Add(5*time.Minute+time.Second)) {
		t.Fatalf("next run %v", next)
	}
}
