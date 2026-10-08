package rulecascade

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// withPatternCache starts a test from a fresh pattern cache: no setter called, the environment
// variable not yet read, and env as the process environment (nil: the variable is not set).
func withPatternCache(t *testing.T, env map[string]string) {
	t.Helper()
	reset := func(lookup func(string) (string, bool)) {
		patterns.Lock()
		defer patterns.Unlock()
		patterns.compiled, patterns.size, patterns.set = map[string]*regexp.Regexp{}, 0, false
		patternEnvOnce, patternEnvSize, patternEnvFailure = sync.Once{}, 0, nil
		lookupEnv = lookup
	}
	reset(func(name string) (string, bool) { v, ok := env[name]; return v, ok })
	t.Cleanup(func() { reset(os.LookupEnv) })
}

func cachedPatterns() int {
	patterns.Lock()
	defer patterns.Unlock()
	return len(patterns.compiled)
}

// compileDistinct compiles n patterns that have not been compiled before in this test.
func compileDistinct(t *testing.T, prefix string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := compilePattern(fmt.Sprintf("^%s%d$", prefix, i)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPatternCacheDefault(t *testing.T) {
	withPatternCache(t, nil)
	compileDistinct(t, "a", DefaultPatternCacheSize)
	if got := cachedPatterns(); got != DefaultPatternCacheSize {
		t.Fatalf("cached %d patterns, want %d", got, DefaultPatternCacheSize)
	}
	compileDistinct(t, "b", 1) // full: emptied, then this one kept
	if got := cachedPatterns(); got != 1 {
		t.Fatalf("cached %d patterns after the cache was full, want 1", got)
	}
}

func TestPatternCacheFromEnvironment(t *testing.T) {
	withPatternCache(t, map[string]string{"RULE_CASCADE_PATTERN_CACHE_SIZE": "5"})
	compileDistinct(t, "a", 5)
	if got := cachedPatterns(); got != 5 {
		t.Fatalf("cached %d patterns, want 5", got)
	}
	compileDistinct(t, "b", 1)
	if got := cachedPatterns(); got != 1 {
		t.Fatalf("cached %d patterns after the cache was full, want 1", got)
	}
}

func TestPatternCacheSetterWinsAndShrinkingEmpties(t *testing.T) {
	withPatternCache(t, map[string]string{"RULE_CASCADE_PATTERN_CACHE_SIZE": "5"})
	if err := SetPatternCacheSize(10); err != nil {
		t.Fatal(err)
	}
	compileDistinct(t, "a", 8)
	if got := cachedPatterns(); got != 8 {
		t.Fatalf("cached %d patterns, want 8: the setter wins over the environment", got)
	}
	if err := SetPatternCacheSize(20); err != nil {
		t.Fatal(err)
	}
	if got := cachedPatterns(); got != 8 {
		t.Fatalf("growing the cache dropped patterns: %d left, want 8", got)
	}
	if err := SetPatternCacheSize(3); err != nil {
		t.Fatal(err)
	}
	if got := cachedPatterns(); got != 0 {
		t.Fatalf("shrinking the cache kept %d patterns, want 0", got)
	}
}

func TestPatternCacheSetterRefusesNegative(t *testing.T) {
	withPatternCache(t, nil)
	err := SetPatternCacheSize(-1)
	want := "the pattern cache size is -1: expected a whole number, 0 or more (0 turns the cache off)"
	if err == nil || err.Error() != want {
		t.Fatalf("SetPatternCacheSize(-1) = %v, want %q", err, want)
	}
	compileDistinct(t, "a", 3) // the refused size changed nothing
	if got := cachedPatterns(); got != 3 {
		t.Fatalf("cached %d patterns, want 3", got)
	}
}

// With the cache off nothing is kept, and the conformance cases give the same results.
func TestPatternCacheOffGivesTheSameResults(t *testing.T) {
	withPatternCache(t, map[string]string{"RULE_CASCADE_PATTERN_CACHE_SIZE": "0"})
	t.Run("expressions", TestConformanceExpressions)
	t.Run("evaluations", TestConformanceEvaluations)
	if got := cachedPatterns(); got != 0 {
		t.Fatalf("cached %d patterns with the cache off", got)
	}
	if err := SetPatternCacheSize(0); err != nil {
		t.Fatal(err)
	}
	t.Run("expressions with the setter", TestConformanceExpressions)
}

// A value that is not a whole number of 0 or more is reported, never replaced by the default: every
// load is refused and a `matches` that is evaluated anyway fails closed.
func TestPatternCacheRefusesBadEnvironment(t *testing.T) {
	for _, value := range []string{"", "-1", "abc", "1.5", "+5", " 5", "5 ", "1e3", "0x10", "99999999999999999999",
		"00000000000000000005", "s3cr3t-t0ken"} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			withPatternCache(t, map[string]string{"RULE_CASCADE_PATTERN_CACHE_SIZE": value})
			want := "RULE_CASCADE_PATTERN_CACHE_SIZE is not valid: expected a whole number, 0 or more (0 turns the cache off)"
			if _, err := compilePattern("^a$"); err == nil || err.Error() != want {
				t.Fatalf("compilePattern: %v, want %q", err, want)
			}
			bundle, err := os.ReadFile("../../conformance/bundles/acme.payments.transfer.bundle.json")
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := ParseJSON(bundle)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := FromBundle(parsed); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("FromBundle: %v, want an error saying %q", err, want)
			}
			if _, err := FromManifest(asObj(parsed).obj("manifests").obj("server")); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("FromManifest: %v, want an error saying %q", err, want)
			}
			if _, err := Load(map[string]any{}, nil, nil); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("Load: %v, want an error saying %q", err, want)
			}
			doc, err := ParseJSON([]byte(`{"ruleCascade": "1.0.0"}`))
			if err != nil {
				t.Fatal(err)
			}
			problems := schemaProblems(doc) // the schema's pattern for ruleCascade cannot be applied
			if len(problems) != 1 || problems[0].Code != "SCHEMA_INVALID" || !strings.Contains(problems[0].Message, want) {
				t.Fatalf("schema validation with the cache refused: %v, want one SCHEMA_INVALID saying %q", problems, want)
			}
			expr := map[string]any{"op": "matches", "args": []any{"a", "^a$"}}
			if _, err := EvaluateExpression(expr, nil, nil, nil); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("matches: %v, want an evaluation error saying %q", err, want)
			}
			if err := SetPatternCacheSize(4); err != nil { // the setter wins over the refused value
				t.Fatal(err)
			}
			if got, err := EvaluateExpression(expr, nil, nil, nil); err != nil || got != true {
				t.Fatalf("matches after SetPatternCacheSize: %v, %v", got, err)
			}
		})
	}
}

// Changing the size while other goroutines evaluate is safe (run with -race).
func TestPatternCacheResizeWhileEvaluating(t *testing.T) {
	withPatternCache(t, nil)
	rules := bundleRuleSet(t, "acme.onboarding.customer")
	request := map[string]any{"entity": "Customer", "operation": "create",
		"data": map[string]any{"email": "jo@example.com", "name": "Jo"}}
	want, err := rules.Evaluate(request, "server", nil)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := want.MarshalJSON()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				if err := SetPatternCacheSize(i % 4); err != nil {
					t.Error(err)
					return
				}
			}
		}
	}()
	var evaluators sync.WaitGroup
	for g := 0; g < 4; g++ {
		evaluators.Add(1)
		go func(g int) {
			defer evaluators.Done()
			for i := 0; i < 200; i++ {
				expr := map[string]any{"op": "matches", "args": []any{"x", fmt.Sprintf("^x|%d-%d$", g, i)}}
				if got, err := EvaluateExpression(expr, nil, nil, nil); err != nil || got != true {
					t.Errorf("matches: %v, %v", got, err)
					return
				}
				result, err := rules.Evaluate(request, "server", nil)
				if err != nil {
					t.Error(err)
					return
				}
				if got, _ := result.MarshalJSON(); string(got) != string(wantJSON) {
					t.Errorf("result changed while the cache was resized:\n%s\nwant\n%s", got, wantJSON)
					return
				}
			}
		}(g)
	}
	evaluators.Wait()
	close(stop)
	wg.Wait()
}
