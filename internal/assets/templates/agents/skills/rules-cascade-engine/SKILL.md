---
name: rules-cascade-engine
description: Enforce Rule Cascade rules from any language without a native library - C#/.NET, Rust, PHP, Ruby, Swift, Kotlin Native, C++, shell - through the rcas engine process (JSON Lines) or the rcas.wasm WebAssembly module. Use when the project's language has no Rule Cascade package, or rules must run in a sandbox or a database job.
---

# Rule Cascade from any language

There are two portable ways, with exactly the same answers as the native libraries (the same
conformance suite certifies them):

| Way | When | How |
|---|---|---|
| `rcas engine` | a long-running service or job | start once as a child process; one JSON request per line on stdin, one response per line on stdout |
| `rcas.wasm` | a sandbox, a plug-in host, an edge runtime | the WASI module on any WebAssembly runtime (wasmtime, wasmer, wazero, Node) |
| `rcas evaluate` | a script or a one-off | `rcas evaluate --bundle {{out}}/<id>.bundle.json request.json` |

Bundles come from `rcas compile --all` into `{{out}}/`. Install `rcas` with
`curl -fsSL https://rulescascade.com/install.sh | sh` (Windows: `irm https://rulescascade.com/install.ps1 | iex`)
or `npx -y @rules-cascade/cli`.

## The engine process

1. Start `rcas engine` once at start-up; send `{"command":"load","bundle":<the bundle JSON>}` and
   check the answer (`ok`, and an empty `missingOperators`) before serving. Restart the process if
   it exits, and fail closed while it is down.
2. Per request send
   `{"command":"evaluate","ruleset":"<id>","channel":"server","request":{entity, operation, data, original, actor, resolutions}}`
   and read one line back: `{"id":…,"ok":true,"result":{"decision","findings","effects","commands",…}}`,
   or `"ok":false` with an `error` (a malformed request: answer 400). Add an `id` to match answers. Requests on one process are answered in
   order; use a pool of processes for parallelism.
3. Then the same four steps as every runtime: refuse on `deny` with 422 problem details carrying the
   result; persist after applying `compute` effects; run `commands` after the commit, once per
   `idempotencyKey`.

The protocol (specification section 13) and working clients in C#, Rust, PHP, Ruby, PowerShell and
`jq`: https://rulescascade.com/usage/command-and-wasm/ and
https://rulescascade.com/examples/other-languages/. Copy the client of your language rather than
writing one from scratch.

## Replacing existing validation

`rcas analyze` lists FluentValidation, Rails `validates`, Laravel rules and hand-written checks.
Move business decisions to rules, add a parity test over the same inputs, then delete the old check.

## Tests

Assert `decision` and finding codes, not message texts. Golden tests in the ruleset (`rcas check`)
test the rules; CI runs `rcas check` and `rcas compile --all` before the application's tests.
