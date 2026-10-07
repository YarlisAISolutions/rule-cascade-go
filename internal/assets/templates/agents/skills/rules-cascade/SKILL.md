---
name: rules-cascade
description: Find business rules in code and turn them into Rule Cascade rulesets (YAML, golden tests, rcas check). Use when the user asks to extract, write, review, test or change business rules, validations, limits or approvals, or mentions rcas, rulesets or Rule Cascade.
---

# Rule Cascade rulesets

Rules are data in `{{rules}}/*.ruleset.yaml`, compiled by `rcas` and evaluated identically in every
language. Work through the `rules-cascade` MCP server when it is connected; otherwise run `rcas`.

## Checklist

1. `analyze` the code (or the path the user named). List candidate rules: where, what it decides.
2. For each API schema, `derive` the baseline ruleset. Do not hand-write what derive produces.
3. For each remaining decision, write a rule: one concern, guarded `when`, parameters for limits,
   severity chosen deliberately, message key with arguments, `enforcement: server` unless the rule
   reads nothing secret.
4. Write golden tests: a passing case, a failing case, the boundary, and the client channel.
5. `check` until it reports no problems and `0 failed`.
6. `propose_ruleset` with a rationale and the source locations (`file:line`). Never write the
   rulesets directly. Tell the user to run `rcas proposals show <id>` and `rcas proposals accept <id>`.

## Quick reference

| Need | Tool / command |
|---|---|
| Operators and their arguments | `get_operator_reference` |
| A section of the specification | `get_spec_section` (e.g. "4.4" for patterns) |
| Authoring, naming, enforcement rules | `get_authoring_guide` with topic authoring, naming, enforcement, openapi or cookbook |
| Try a request | `evaluate_rules` (a dry run) |
| What applies to an operation | `list_rules`, `explain_rule` |

Done means: every touched ruleset passes `rcas check`, every rule has golden tests, and a proposal id
was given to the user.

## Related skills

- `rules-cascade-review`: review a ruleset change or a proposal before it is accepted.
- `rules-cascade-migrate`: move existing checks out of the code, with parity tests.
- Enforcing rules in the application: `rules-cascade-typescript` (Node.js, browsers, React),
  `rules-cascade-python`, `rules-cascade-java` (JVM, Spring), `rules-cascade-go`, and
  `rules-cascade-engine` for any other language.
