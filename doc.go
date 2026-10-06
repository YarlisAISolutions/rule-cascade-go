// Package rulecascade is the Go runtime of Rule Cascade, a language-agnostic business-rules
// engine: rules are written once in a YAML or JSON ruleset, compiled to a JSON bundle, and
// evaluated identically by the runtimes in every language.
//
// The package implements both conformance levels of the specification (spec/v1/SPECIFICATION.md):
//
//   - Evaluator: FromBundle reads a compiled bundle and RuleSet.Evaluate evaluates a request
//     against it. This is all a service needs at run time. FromManifest reads one manifest on
//     its own, which is what a client application receives.
//   - Compiler: Load validates a ruleset document against the schema, resolves inheritance, runs
//     the load-time checks and computes the checksum; RuleSet.Bundle returns the bundle.
//
// It is a port of the Python reference implementation and passes the shared conformance suite.
// It imports only the standard library and reads no clock, network, file or random source.
//
// # JSON values
//
// Documents, bundles, manifests and requests are JSON values. This package represents them as
// nil, bool, string, json.Number, []any and *Object. An *Object keeps its members in the order
// they were written, so that manifests and bundles come out as they went in; ParseJSON produces
// these values and Marshal writes them. Wherever a JSON value is an argument, the usual Go forms
// are accepted as well: map[string]any (its members are taken in sorted order, which changes no
// result), float64, int, and anything encoding/json can marshal.
//
// Numbers enter as doubles and are computed with as decimals. A number in a document, a bundle or
// a request is the IEEE 754 double nearest to what was written, and the engine works with the
// shortest decimal that identifies that double: 0.1 is exactly one tenth and 9007199254740993 is
// 9007199254740992. A number too large for a double is refused. Arithmetic inside an expression
// has 34 significant digits and nothing is rounded there. Every number that leaves the engine,
// computed or merely passed through, is rounded half even to 15 significant digits; one beyond
// the range of a double is an evaluation error.
//
// # The engine protocol
//
// Engine speaks the JSON Lines protocol of specification section 13, so that a program in any
// language can drive this runtime as a child process or as a WebAssembly module. The
// rule-cascade command in cmd/rule-cascade serves it with `rcas engine`.
package rulecascade
