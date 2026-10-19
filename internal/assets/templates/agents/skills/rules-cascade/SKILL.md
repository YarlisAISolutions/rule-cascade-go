---
name: rules-cascade
description: Find business rules in code and turn them into Rule Cascade rulesets (YAML, golden tests, rcas check). Use when the user asks to extract, write, review, test or change business rules, validations, limits or approvals, or mentions rcas, rulesets or Rule Cascade.
---

# Rule Cascade rulesets

Rules are data in `{{rules}}/*.ruleset.yaml`, compiled by `rcas` and evaluated identically in every
language. Work through the `rules-cascade` MCP server when it is connected; otherwise run `rcas`.
When the server is the hosted one (`mcp.rulescascade.com`), it sees no project: send the rulesets
and schemas in the `files` argument. It cannot propose: to submit, switch to the local server
(`rcas mcp`) and call `propose_ruleset`, or show the user the exact checked ruleset and the path to
save it under.

## Checklist

1. `analyze` the code: the path, `module` or `feature` the user named, or `changed` for a pull
   request. List candidate rules: where, what it decides. A `candidates` entry is one name decided in
   several languages; with `drift` the backend and the user interface disagree, so ask which value
   is right. Skip what `coveredBy` shows a ruleset already decides, unless the code disagrees.
2. For each API schema, `derive` the baseline ruleset. Do not hand-write what derive produces.
3. For each remaining decision, write a rule: one concern, guarded `when`, parameters for limits,
   severity chosen deliberately, message key with arguments, `enforcement: server` unless the rule
   reads nothing secret.
4. Write golden tests: a passing case, a failing case, the boundary, and the client channel.
5. `check` until it reports no problems and `0 failed`.
6. `propose_ruleset` with a rationale and the source locations (`file:line`). Never write the
   rulesets directly. Tell the user to run `rcas proposals show <id>` and `rcas proposals accept <id>`.

## Parents and children

Shared rules and limits live once, in a parent; a child adds what one feature needs and may only
tighten what it inherits. For a project with modules and features:

- One ruleset per module (`{{rules}}/orders.ruleset.yaml`, id `<prefix>.orders`) with the params and
  rules its features share, and one per feature (`{{rules}}/orders.checkout.ruleset.yaml`, id
  `<prefix>.orders.checkout`) that extends it.
- Keep a child **in the same directory** as its parent: `check` looks the parent up there.
- A child has one parent (`extends` takes one entry), and its `scope` is the parent's scope plus at
  least one level, or loading fails with `SCOPE_NOT_NARROWER`.
- A child changes an inherited param only under `overrides.params`, within the param's
  `overridePolicy` (`tighten-only` needs a `tightenDirection`). It changes an inherited rule only
  under `overrides.rules`, and only its `severity`, `enabled`, `acknowledgement` or `acceptance`,
  with a `reason`. Everything else it needs is a new rule with its own id.
- A hand-written ruleset extends either a derived baseline or the module parent, not both. When
  both exist, the module parent extends the derived one.

```yaml
# {{rules}}/orders.ruleset.yaml: the module
metadata: { id: shop.orders, version: 1.0.0, title: Orders }
scope: [{ level: organization, id: shop }, { level: module, id: orders }]
params:
  maxQuantity: { type: integer, default: 20, overridePolicy: tighten-only, tightenDirection: lower }

# {{rules}}/orders.checkout.ruleset.yaml: one feature of it
metadata: { id: shop.orders.checkout, version: 1.0.0, title: Checkout }
scope: [{ level: organization, id: shop }, { level: module, id: orders }, { level: feature, id: checkout }]
extends: [{ ruleset: shop.orders, version: ^1.0.0 }]
overrides:
  params: { maxQuantity: 10 }
```

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
