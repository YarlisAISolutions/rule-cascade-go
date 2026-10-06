# Rule Cascade for Go

The Rule Cascade runtime for Go, in three forms built from the same code:

| Form | What it is | Where |
|---|---|---|
| Library | Package `rulecascade`. Standard library only | this directory |
| Command | `rcas`, one static binary for Linux, macOS and Windows | `cmd/rule-cascade` |
| WebAssembly | The same command as a WASI (preview 1) module that any language can run | `dist/rcas.wasm` |

It implements both conformance levels of the [specification](../../spec/v1/SPECIFICATION.md):
**evaluator** (read a bundle, evaluate) and **compiler** (schema validation, inheritance, load-time
checks, checksum, manifests, bundle). It is a port of the Python reference implementation and passes
the shared [conformance suite](../../conformance/README.md), in process and over the engine protocol,
natively and as WebAssembly.

Requires Go 1.22 or later. The library has no dependencies. The command depends on
`gopkg.in/yaml.v3` to read YAML sources.

## Library

The module path is `rules.sdods.com/go` and the package name is
`rulecascade`.

```go
import rulecascade "rules.sdods.com/go"
```

### Evaluate

A service reads a bundle that was compiled elsewhere, once, and evaluates every state-changing
operation against it.

```go
rules, err := rulecascade.FromBundle(bundle) // bundle: the parsed JSON of a *.bundle.json file
if err != nil {
	log.Fatal(err) // a *rulecascade.LoadError: do not start
}

result, err := rules.Evaluate(map[string]any{
	"entity":    "Transfer",
	"operation": "create",
	"data": map[string]any{
		"type": "international", "amount": 12000, "currency": "USD",
		"beneficiary": map[string]any{"name": "Ana", "country": "ES"},
	},
	"actor": map[string]any{"id": "u-1", "roles": []string{"teller"}},
}, "server", nil)
if err != nil {
	log.Fatal(err)
}

fmt.Println(result.Decision)
for _, f := range result.Findings {
	fmt.Println(f.Code, f.Severity, f.Fields, f.Message)
}
// deny
// ORG-TRF-003 warning [/memo] Adding a memo makes this transfer easier to reconcile.
// PAY-TRF-002 error [/beneficiary/swiftCode] A valid SWIFT/BIC code is required for international transfers.
// PAY-TRF-003 warning [/amount /beneficiary/name] This is a large transfer to Ana. Please confirm the details.
```

`Evaluate` is pure and a `RuleSet` is safe for concurrent use. A rule that cannot be evaluated never
produces an error: it produces a blocking finding with code `RULE-EVALUATION-ERROR`. A request that
does not have the shape of specification section 8 (no `operation`, an `actor` that is not an object,
a role that is not a string ...) is refused before anything is evaluated: the error is a
`*rulecascade.RequestError`. Optional members may be absent or `null`.

`Result`, `Finding`, `Effect` and `Command` are structs. They marshal to the JSON of the evaluation
API with `rulecascade.Marshal` and with `encoding/json`.

### One manifest

A browser or a mobile app receives one manifest, not the bundle. A ruleset read from a manifest has
that one channel: a client manifest cannot be evaluated as the server.

```go
rules, err := rulecascade.FromManifest(manifest)
if err != nil {
	log.Fatal(err) // a *rulecascade.LoadError with code MANIFEST_INVALID
}
fmt.Println(rules.Channels(), rules.MissingOperators(nil)) // check the operators at start-up
// [client] [x-luhn]

request := map[string]any{
	"entity": "Customer", "operation": "create", "trigger": "blur", "locale": "es-MX",
	"data": map[string]any{"email": "maya(at)example.com"},
}
result, err := rules.Evaluate(request, "", nil) // "" is the channel the ruleset has
if err != nil {
	log.Fatal(err)
}
for _, f := range result.Findings {
	fmt.Println(f.Code, f.Message)
}
// ONB-STR-001 Introduce tu nombre completo.
// ONB-TYP-001 Introduce una dirección de correo válida.

_, err = rules.Evaluate(request, "server", nil)
fmt.Println(err)
// rulecascade: ruleset acme.onboarding.customer has no server manifest
```

`Channels` lists the channels a ruleset has. An empty channel in `Evaluate` and `Manifest` is the
server's when the ruleset has it, and otherwise the channel it has. `MissingOperators(operators)`
lists the custom operators the rules need that the host did not supply: a missing operator does not
fail the load, it fails every rule that uses it, closed. Messages fall back from the requested locale
to its prefixes and then to the default locale: `es-MX` reads `es-MX`, `es`, then `en`.

### Compile

```go
rules, err := rulecascade.Load(document, registry, loader)
var failure *rulecascade.LoadError
if errors.As(err, &failure) {
	for _, p := range failure.Problems {
		fmt.Println(p.Code, p.Rule, p.Message)
	}
	return
} else if err != nil {
	log.Fatal(err)
}
fmt.Println(rules.ID(), rules.Version(), rules.Checksum())
// acme.payments.transfer 1.0.0 sha256:c192dd53b5b1d307d52ccbc27fc1674114e8714d53b699b24088a648ae242c7e

bundle, err := rulecascade.Marshal(rules.Bundle()) // what an evaluator reads with FromBundle
if err != nil {
	log.Fatal(err)
}

client, err := rules.Manifest("client") // what a browser may see
if err != nil {
	log.Fatal(err)
}
```

`registry` maps ruleset ids to documents, for `extends`. `loader` returns the schema document an
entity's `$ref` names; with a nil loader the checks that need the entity schemas (`PATH_UNKNOWN`,
`SCHEMA_REF_UNRESOLVED`) are skipped. The ruleset schema is compiled into the package:
`rule-cascade.schema.json` in this directory is a byte-identical copy of
`spec/v1/rule-cascade.schema.json`, and a test fails when the two differ.

The snippets in this file are taken from the runnable examples in [`example_test.go`](example_test.go).

### JSON values

Documents, bundles, manifests and requests are JSON values. `rulecascade.ParseJSON` reads them as
`nil`, `bool`, `string`, `json.Number`, `[]any` and `*rulecascade.Object`, an object that keeps its
members in the order they were written. Every function also accepts the usual Go forms:
`map[string]any`, `float64`, `int` and anything `encoding/json` can marshal.

No result depends on the order of the members of an object. An `*Object` keeps it so that a manifest
or a bundle comes out in the order it was written; the members of a `map[string]any` are taken in
sorted order.

### Numbers

A number in a ruleset, a bundle or a request is the IEEE 754 double nearest to what was written, and
the engine computes with the shortest decimal that identifies that double: `0.1` is exactly one
tenth, `9007199254740993` is `9007199254740992`, and a literal below the smallest double is zero. The
same holds for a `float64`, an `int64` or a `json.Number` handed to the library. A number too large
for a double, `NaN` and the infinities are refused.

Arithmetic is decimal: 34 significant digits inside an expression, where nothing is rounded. Every
number that leaves the engine (a computed value, a payload, a message argument, an argument of a
custom operator, the result of an expression), computed or merely passed through, is rounded half
even to 15 significant digits; one larger than the largest double is an evaluation error. Results
carry numbers as `json.Number`.

### Custom operators

```go
operators := rulecascade.Operators{
	// true for a string of digits whose Luhn check digit is valid
	"x-luhn": func(args []any) (any, error) {
		digits, ok := args[0].(string)
		if !ok || len(digits) < 2 {
			return false, nil
		}
		sum := 0
		for i := 0; i < len(digits); i++ {
			d := int(digits[len(digits)-1-i]) - '0'
			if d < 0 || d > 9 {
				return false, nil
			}
			if i%2 == 1 {
				if d *= 2; d > 9 {
					d -= 9
				}
			}
			sum += d
		}
		return sum%10 == 0, nil
	},
}

result, err := rules.Evaluate(request, "server", operators)
if err != nil {
	log.Fatal(err)
}
for _, f := range result.Findings {
	fmt.Println(f.Code, f.Message)
}
// ONB-CUS-001 This loyalty number is not valid. Check the digits.

result, _ = rules.Evaluate(request, "server", nil) // not registered: the rule fails closed
for _, f := range result.Findings {
	fmt.Println(f.Code, f.Blocking, f.Detail)
}
// RULE-EVALUATION-ERROR true custom operator x-luhn is not registered
```

A custom operator receives plain Go values: `nil`, `bool`, `string`, `float64` for every number
(after the 15-digit rounding), `[]any` and `map[string]any`. It returns any JSON value; a `float64`
is read as its shortest decimal representation and, like any other number, is not rounded until it
leaves the expression. Returning an error, `NaN` or an infinity (also inside a list or a map), or
panicking, is an evaluation error: the rule fails closed.

### Refresh on a schedule

`Holder` loads the rules again on an interval or a cron schedule and swaps them in atomically. When a
load fails it keeps the last good rules; a ruleset with the checksum already held is not swapped in.

```go
rules, err := rulecascade.NewHolder(func() (*rulecascade.RuleSet, error) {
	data, err := os.ReadFile("transfer.bundle.json")
	if err != nil {
		return nil, err
	}
	bundle, err := rulecascade.ParseJSON(data)
	if err != nil {
		return nil, err
	}
	return rulecascade.FromBundle(bundle)
}, rulecascade.HolderOptions{
	Interval: 5 * time.Minute, // or Cron: "0 * * * *" with Location
	OnReload: func(r rulecascade.Reload) {
		if r.Err != nil {
			log.Printf("rules not refreshed: %v", r.Err)
		}
	},
})
if err != nil {
	log.Fatal(err) // the first load failed
}
defer rules.Close()
result, err := rules.Get().Evaluate(request, "server", nil)
```

`ParseCron` and `(*Cron).Next` implement the cron syntax of
[docs/caching.md](../../docs/caching.md#cron-syntax). `go test -run '^$' -bench EvaluateTransfer`
measures evaluations per second ([docs/performance.md](../../docs/performance.md)).

## Command

Build it from this directory:

```sh
go build -o dist/rcas ./cmd/rcas
dist/rcas version
```

```text
rcas 1.0.0-alpha.4 (specification 1.0.0, bundle format 1.0.0)
```

| Command | Does |
|---|---|
| `rcas version` | Prints the version |
| `rcas check <file>...` | Lints each ruleset, loads it and runs its golden tests. Exit status 1 on any problem |
| `rcas compile <file> [-o out.bundle.json]` | Compiles a ruleset into a bundle |
| `rcas manifest <ruleset-or-bundle> [--channel client\|server] [-o out.manifest.json]` | Prints or writes a manifest. The default channel is `client`. `--manifest <file>` reads a manifest instead |
| `rcas evaluate --bundle <bundle.json> [--channel server\|client] [--conformance-operators] [request.json\|-]` | Evaluates one request, read from a file or from standard input. The default channel is `server`. `--conformance-operators` registers the three operators of the conformance suite |
| `rcas evaluate --manifest <manifest.json> [--conformance-operators] [request.json\|-]` | The same against one manifest, on the manifest's own channel |
| `rcas engine [--conformance-operators]` | Serves the engine protocol on standard input and output |

Rulesets are YAML or JSON. Parents are looked up among the `*.ruleset.*` files in the directory of the
ruleset, and entity schemas are resolved relative to it.

```sh
dist/rcas check ../../examples/contracts/*.ruleset.yaml ../../examples/catalog/*.ruleset.yaml
```

```text
acme.org.base@1.2.0  sha256:4dff42ddd5da...  3 rules (2 client-safe), 2 params
  3 golden tests, 0 failed
acme.payments.transfer@1.0.0  sha256:c192dd53b5b1...  14 rules (9 client-safe), 4 params
  14 golden tests, 0 failed
acme.onboarding.customer@1.0.0  sha256:5adaab0be3f3...  35 rules (31 client-safe), 7 params
  27 golden tests, 0 failed
```

```sh
dist/rcas compile ../../examples/contracts/payments-transfer.ruleset.yaml -o dist/transfer.bundle.json
dist/rcas manifest dist/transfer.bundle.json
echo '{"entity": "Transfer", "operation": "create", "data": {"type": "domestic", "amount": -5}}' |
  dist/rcas evaluate --bundle dist/transfer.bundle.json
```

`check` also verifies the OpenAPI bindings of a ruleset, reports every number with more than 15
significant digits (`NUMBER_NOT_PORTABLE`), and runs the golden tests with the three conformance
operators registered, as `tools/rulecheck.py check` does. When a ruleset needs a custom operator the
command does not have, `check` prints a `NOTE`: the rules that use it fail closed here, so their
golden tests belong in the test suite of the host that supplies the operator. `evaluate` exits with status 0
whatever the decision; the decision is in the output.

### YAML

A ruleset is a JSON value, and YAML parsers do not agree on what an unquoted scalar means
(specification section 12). The command reads every plain scalar by the YAML 1.2 core schema, so
`no` and `2026-10-03` are strings and `012` is twelve. Like `tools/rulecheck.py`, it reports as
`YAML_NOT_PORTABLE` every plain scalar from which a YAML 1.1 parser reads another value (`no`, `012`,
`0o17`, `1e3`, `1_000`, `12:30`, `2026-10-03`, `-.5`, `<<` ...), and anchors, aliases, tags, merge
keys, repeated keys, a second document and a byte order mark. `check` lists these problems with the
others; `compile` and `manifest` refuse the file. A file with a number too large for a double is
refused by every command.

### All platforms

```sh
sh scripts/build-all.sh
```

builds `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`,
`windows/arm64` and the WebAssembly module into `dist/`, with `CGO_ENABLED=0`, and writes
`dist/SHA256SUMS`. The version comes from `version.go`, or from the `VERSION` environment variable.

## WebAssembly

`dist/rcas.wasm` is the same command built with `GOOS=wasip1 GOARCH=wasm`. Any host with a WASI
preview 1 runtime can run it. [`wasi/run.mjs`](wasi/run.mjs) runs it under Node.js 20 or later, with
standard input, output and error connected and the host file system visible, so every command above
works:

```sh
node wasi/run.mjs dist/rcas.wasm check ../../examples/contracts/*.ruleset.yaml
echo '{"id":1,"command":"expression","expr":{"op":"round","args":[2.675,2]}}' |
  node wasi/run.mjs dist/rcas.wasm engine
```

```text
{"id":1,"ok":true,"result":2.68}
```

From a Node.js program, start the engine once and exchange one line per request:

```js
import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';

const engine = spawn('node', ['wasi/run.mjs', 'dist/rcas.wasm', 'engine'],
  { stdio: ['pipe', 'pipe', 'inherit'] });
const answers = createInterface({ input: engine.stdout })[Symbol.asyncIterator]();
async function call(request) {
  engine.stdin.write(JSON.stringify(request) + '\n');
  const { value } = await answers.next();
  return JSON.parse(value);
}

console.log(await call({ command: 'expression', expr: { op: 'mul', args: [1.1, 3] } }));
// { ok: true, result: 3.3 }
engine.stdin.end();
```

## Engine protocol

`rcas engine` reads one JSON request per line on standard input and writes one JSON response
per line on standard output, in the same order, until its input ends.
A request is `{"command": ...}` with `version`, `load`, `manifest`, `evaluate`, `expression` or
`compile` and the members of that command, plus an optional `id` that the response repeats.
A response is `{"ok": true, "result": ...}` or `{"ok": false, "error": {"code", "message"}}`.

The commands, their members and the error codes are defined in
[section 13 of the specification](../../spec/v1/SPECIFICATION.md#13-engine-protocol).

```sh
printf '%s\n' '{"id":1,"command":"version"}' '{"id":2,"command":"expression","expr":{"op":"add","args":[0.1,0.2]}}' |
  dist/rcas engine
```

```text
{"id":1,"ok":true,"result":{"engine":"rule-cascade-go","engineVersion":"1.0.0-alpha.4","ruleCascade":"1.0.0","bundle":"1.0.0","levels":["evaluator","compiler"],"operators":[]}}
{"id":2,"ok":true,"result":0.3}
```

`load`, `manifest` and `evaluate` take a bundle or one manifest. `load` answers with the channels the
ruleset has and the custom operators it needs that the engine does not have:

```sh
printf '%s\n' '{"command":"load","manifest":{"id":"m","version":"1.0.0","checksum":"sha256:1","channel":"client","rules":[],"operators":["x-vat-id"]}}' \
  '{"command":"evaluate","ruleset":"m","channel":"server","request":{"entity":"Thing","operation":"create"}}' |
  dist/rcas engine
```

```text
{"ok":true,"result":{"ruleset":"m","version":"1.0.0","checksum":"sha256:1","channels":["client"],"missingOperators":["x-vat-id"]}}
{"ok":false,"error":{"code":"CHANNEL_UNAVAILABLE","message":"ruleset m was loaded without a server manifest"}}
```

Custom operators cannot cross a process boundary. `--conformance-operators` builds in the three
operators of the conformance suite; a ruleset that needs its own operators is evaluated with the
library, or with a build of the command that registers them.

## Test

```sh
go vet ./... && go test ./...
```

runs the whole conformance suite in process from `../../conformance`: the expression cases, the
protocol cases, the published bundles with their golden tests, the compiler checks (including that
each compiled bundle equals the published one), the evaluation corpus and the load errors. It also
runs unit tests for the decimal arithmetic, canonical JSON, portable patterns, request checking and
the YAML reader, which must read the example contracts and `conformance/sources` exactly as the
published fixtures.

The independent driver certifies the built engine over the protocol. From the repository root:

```sh
PYTHONPATH=packages/python/src python3 tools/rulecheck.py conformance \
  --engine "packages/go/dist/rcas engine --conformance-operators"
PYTHONPATH=packages/python/src python3 tools/rulecheck.py conformance \
  --engine "node packages/go/wasi/run.mjs packages/go/dist/rcas.wasm engine --conformance-operators"
```

Each ends with `conformance: <n> cases, 0 failure(s)`.
