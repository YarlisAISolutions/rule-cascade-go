---
name: rules-cascade-go
description: Enforce Rule Cascade rules in Go services and tools (net/http, chi, gin, echo, gRPC, workers) with rulescascade.com/go. Use when wiring rules into a Go code path, replacing validate tags or hand-written checks with the engine, or mapping rule findings to HTTP or gRPC errors.
---

# Rule Cascade in Go

Module: `go get rulescascade.com/go` (Go 1.22+, no dependencies beyond the standard library and
`gopkg.in/yaml.v3`). Bundles come from `rcas compile --all` into `{{out}}/`. Never evaluate the YAML
at run time and never copy a rule into code.

## Load once, evaluate per request

```go
import rulecascade "rulescascade.com/go"

// At start-up. Any error: do not start.
data, err := os.ReadFile("{{out}}/<ruleset-id>.bundle.json")
doc, err := rulecascade.ParseJSON(data)
rules, err := rulecascade.FromBundle(doc)
```

**Prefer the middleware** (from 1.0.0-alpha.7): `rulescascade.com/go/rulehttp` builds the request
from the JSON body (which stays readable), enforces, and answers a denied request with the 422
problem details below, the same in every Rule Cascade runtime.

```go
mux.Handle("POST /orders", rulehttp.Enforce(rules, rulehttp.Options{Entity: "Order", Operation: "create",
	Actor: func(r *http.Request) map[string]any { return actorFromToken(r) }})(createOrder))
// in the handler: rulehttp.FromContext(r.Context()) is the allowed result
```

Elsewhere, `rules.Enforce(request, "server", nil)` returns a `*rulecascade.RuleViolation` error
(`Problem()` is the body) and `rulehttp.WriteError(w, err)` answers it. Underneath,
`rules.Evaluate(request, "server", nil)` returns a `*rulecascade.Result` (`Decision`, `Findings`,
`Effects`, `Commands`; `result.Allowed()`). A `*RuleSet` is safe for concurrent use. Every
state-changing handler follows four steps:

1. **Evaluate on the server** with `map[string]any{"entity", "operation", "data", "original", "actor", "resolutions"}`.
   The actor comes from authentication middleware, never from a header the caller controls. Decode
   request bodies with `rulecascade.ParseJSON` (or `json.Decoder.UseNumber()`) so numbers stay exact.
2. **Refuse on deny** with **422** `application/problem+json` (`type: urn:rule-cascade:rule-violation`)
   carrying the result; for gRPC, `codes.FailedPrecondition` with the findings in the details.
3. **Persist**, after applying `compute` effects (`Type == "value"`): the server's value is stored.
4. **Run `result.Commands`** after the commit, once per `IdempotencyKey` (an outbox table, or a
   broker with deduplication).

Placement: one function per entity that builds the request and evaluates, called from every handler
(net/http, chi, gin, echo all the same). `rulecascade.NewHolder` reloads bundles on an interval or a
cron schedule and keeps the last good rules.

## Replacing existing validation

`rcas analyze` lists `validate:` struct tags (go-playground/validator) and hand-written checks. Keep
shape checks in decoding; move business decisions to rules. Add a table-driven parity test over the
same inputs for the old check and the engine, then delete the old check.

## Tests

Table-driven tests on `Decision` and finding `Code`s, not message texts. Golden tests in the
ruleset (`rcas check`) test the rules; CI runs `rcas check` and `rcas compile --all` before `go test`.

Reference: https://rulescascade.com/usage/go/
