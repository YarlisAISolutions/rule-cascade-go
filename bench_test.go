package rulecascade

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"
)

// BenchmarkEvaluateTransfer measures the evaluations per second and the latency of the Go runtime on
// one goroutine:
//
//	go test -run '^$' -bench EvaluateTransfer -benchtime 5s
//
// It reads conformance/bundles/acme.payments.transfer.bundle.json with FromBundle and evaluates the
// requests of tools/bench-requests.json round-robin on the server channel. Each evaluation is also
// timed on its own, for the p50-ns and p99-ns metrics (nearest-rank); those include one clock read.
func BenchmarkEvaluateTransfer(b *testing.B) {
	data, err := os.ReadFile("../../tools/bench-requests.json")
	if err != nil {
		b.Fatal(err)
	}
	var spec struct {
		Bundle   string `json:"bundle"`
		Requests []struct {
			Request json.RawMessage `json:"request"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		b.Fatal(err)
	}
	rules := bundleRuleSet(b, "acme.payments.transfer")
	requests := make([]any, len(spec.Requests))
	for i, r := range spec.Requests {
		if requests[i], err = ParseJSON(r.Request); err != nil {
			b.Fatal(err)
		}
	}
	latencies := make([]time.Duration, 0, b.N)
	findings := 0
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t0 := time.Now()
		result, err := rules.Evaluate(requests[i%len(requests)], "server", nil)
		latencies = append(latencies, time.Since(t0))
		if err != nil {
			b.Fatal(err)
		}
		findings += len(result.Findings)
	}
	b.StopTimer()
	slices.Sort(latencies)
	rank := func(p int) float64 {
		i := (p*len(latencies)+99)/100 - 1
		return float64(latencies[max(0, min(len(latencies)-1, i))].Nanoseconds())
	}
	b.ReportMetric(rank(50), "p50-ns")
	b.ReportMetric(rank(99), "p99-ns")
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "evals/s")
	if findings < 0 {
		b.Log(findings)
	}
}
