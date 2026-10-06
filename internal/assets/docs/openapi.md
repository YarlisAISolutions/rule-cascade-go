# OpenAPI and Rule Cascade

Rule Cascade does not replace an OpenAPI description. The description says what an API accepts;
a ruleset says which business rules apply to it. The two are connected in three ways, and the
evaluation API of Rule Cascade is itself described in OpenAPI 3.1
([`spec/v1/rule-evaluation.openapi.yaml`](../spec/v1/rule-evaluation.openapi.yaml)).

1. [Entities reference component schemas](#1-entities-reference-component-schemas): every path a
   rule mentions is checked against the schema when the ruleset is loaded.
2. [`x-rule-cascade` binds operations](#2-x-rule-cascade-binds-operations): the API says which
   ruleset governs it and which rule operation each API operation triggers.
3. [Baseline rules are derived from schema constraints](#3-baseline-rules-are-derived-from-schema-constraints):
   `required`, `enum`, `maxLength` and the like become validation rules with codes, messages and
   field pointers.

## 1. Entities reference component schemas

An entity names the JSON Schema of its payload, usually a component schema of the API:

```yaml
entities:
  Transfer:
    schema: { $ref: "./payments.openapi.yaml#/components/schemas/Transfer" }
```

The file part is resolved relative to the ruleset. When a ruleset is loaded with access to that
file (specification section 5), these are checked against the schema, and the ruleset does not load
if one is missing:

| Checked | Example |
|---|---|
| `data.*` and `original.*` paths in every expression | `{ var: data.beneficiary.swiftCode }` |
| `item.*` paths of a rule with `forEach` | `{ var: item.share }` |
| Target fields, effect fields and assign fields | `target: { field: /beneficiary/country }` |
| `forEach` pointers | `forEach: /splits` |
| `fieldTypes` pointers | `/amount: Money` |

```console
$ rcas check order.ruleset.yaml
order.ruleset.yaml: LOAD FAILED
  PATH_UNKNOWN: order.note.max-length path data.notes is not in the Order schema
```

A reference that does not resolve is `SCHEMA_REF_UNRESOLVED`. The check follows `properties`,
`items` and local `$ref`s. A schema without `properties` is open: any path below it is accepted. A
runtime that is given no way to read schema documents skips both checks and says so in its
documentation; an evaluator that reads a compiled bundle relies on the compiler having run them.

The effect is that a renamed or removed API property breaks the build of the ruleset instead of
leaving a rule that silently never fires.

## 2. `x-rule-cascade` binds operations

The API description names its ruleset once at the root and tags each operation with the entity and
the rule operation it triggers:

```yaml
x-rule-cascade:
  ruleset: acme.payments.transfer
  version: ^1.0.0

paths:
  /transfers:
    post:
      operationId: createTransfer
      x-rule-cascade: { entity: Transfer, operation: create }
  /transfers/{transferId}/approve:
    post:
      operationId: approveTransfer
      x-rule-cascade: { entity: Transfer, operation: approve }
```

The ruleset lists the same mapping from its side:

```yaml
bindings:
  openapi:
    - document: ./payments.openapi.yaml
      entity: Transfer
      operations:
        create: createTransfer
        update: updateTransfer
        approve: approveTransfer
```

`rulecheck check` compares the two (`binding_problems` in `tools/rulecheck.py`) and reports
`BINDING_MISMATCH` when:

- the OpenAPI document is not found;
- the root `x-rule-cascade.ruleset` is not the id of the ruleset, or the ruleset's version does not
  satisfy the root `version` range;
- a bound `operationId` is not in the document, or its `x-rule-cascade` does not name the same
  entity and operation;
- a rule of the entity applies to an operation that has no bound API operation.

The last check is the one that finds a rule written for `approve` when the API has no approve
operation. Rule operations are not limited to create, read, update and delete: any lower-case name is
an operation, so actions such as `approve` or `submit-for-review` bind the same way.

The tag tells a server what to evaluate when the operation is called: the bound ruleset, with that
entity and that operation. It is checked at lint time only. No runtime, and not the rule server,
reads the OpenAPI document when a request arrives: the API's own code passes the entity and the
operation to `evaluate`, and `check` is what makes sure the code, the tag and the ruleset agree. The example descriptions answer 422 with the evaluation result when the
decision is `deny`, and reference the `EvaluationResult` and `Resolution` schemas of
`rule-evaluation.openapi.yaml` for that response and for the resolutions a caller sends back.

## 3. Baseline rules are derived from schema constraints

An OpenAPI schema already states rules: a property is required, a string has a maximum length, a
value comes from an enumeration. An API gateway or a validator enforces them on the server and
answers with its own error format. `rulecheck derive` turns the same constraints into Rule Cascade
validation rules, so that they produce findings in the same form as every other rule (code,
message, field pointer, severity), in the browser as well as on the server, from one source.

```console
$ rcas derive orders.openapi.yaml --schema Order --id acme.generated.order \
    -o order.ruleset.yaml
skipped /components/schemas/Order/required/0: id is readOnly: the server assigns it, so a request need not carry it
skipped /components/schemas/Order/properties/reference/pattern: not a portable pattern (specification 4.4): escape \s is not portable
skipped /components/schemas/Order/properties/website/format: format 'uri' is not derived (derived: email, date, date-time, uuid)
skipped /components/schemas/Order/properties/lines/uniqueItems: Rule Cascade has no operator that compares the entries of a list with each other
wrote order.ruleset.yaml  acme.generated.order  20 rules derived, 4 constraint(s) skipped
```

| Option | Meaning |
|---|---|
| `--schema NAME` | The component schema, `#/components/schemas/NAME` |
| `--id RULESET_ID` | Id of the derived ruleset |
| `--entity NAME` | Entity name. Default: the schema name |
| `--scope LEVEL:ID,...` | Scope, least specific first. Default: `organization:<first segment of the id>` |
| `--version`, `--title` | Metadata of the ruleset |
| `--tests FILE` | A file with a list of golden tests; they are attached as `tests` |
| `--codes-from FILE` | An earlier derivation whose rules keep their finding codes |
| `-o FILE` | Output file. The entity's schema reference is written relative to it. Default: standard output |
| `--check` | With `-o`: write nothing and exit with status 1 when the file is not what would be written |

From Python: `derive(document, "Order", "acme.generated.order")` returns the ruleset document,
`derive_with_report` also returns the skipped constraints, and `to_yaml` writes the document in the
layout of the examples.

Before writing anything the command loads the derived ruleset against the same OpenAPI document, so
a file it writes passes `rulecheck check`, path checks included. The output quotes every scalar that
YAML 1.1 and YAML 1.2 parsers read differently (specification section 12).

### What a derived rule looks like

```yaml
- id: order.lines.quantity.minimum
  kind: validation
  title: Lines quantity is at least 1
  target: { entity: Order, field: /quantity }
  operations: [create, update]
  triggers: [blur, submit]
  forEach: /lines
  when: { op: eq, args: [ { op: typeOf, args: [ { var: item.quantity } ] }, number ] }
  assert: { op: gte, args: [ { var: item.quantity }, 1 ] }
  severity: error
  finding: { code: GEN-ORDER-020, message: generated.minimum, args: { field: Quantity, min: 1 } }
  x-generated-from: /components/schemas/Order/properties/lines/items/properties/quantity/minimum
```

| Part | Value |
|---|---|
| `id` | `<entity>.<property path>.<constraint>`, in kebab-case: `order.lines.quantity.minimum` |
| `finding.code` | `GEN-<ENTITY>-NNN`, numbered in the order of the schema |
| `finding.message` | One message per kind of constraint (`generated.minimum`: "{field} must be at least {min}."), with the field label and the constraint value as arguments. Translate it by adding a locale to `messages` |
| `severity` | `error` |
| `operations`, `triggers` | `[create, update]`, `[blur, submit]` |
| `when` | Every rule except a top-level `required` has a guard, so an absent value produces no finding |
| `forEach` | Rules for the properties of the objects in a list run once per element; findings carry pointers such as `/lines/1/quantity` |
| `x-generated-from` | JSON Pointer of the constraint in the OpenAPI document, after following `$ref` |

### What is derived

`v` is the value of the property: `data.<path>`, or `item.<path>` inside a list.

| Schema keyword | Rule id ends in | Applies when | Asserts |
|---|---|---|---|
| `required` | `.required` | Always; for a nested property, when the parent is an object | `exists(v)` |
| `type` | `.type` | `exists(v)` | `typeOf(v)` is the type. `integer` is a number with `mod(v, 1) = 0`. A list of types is an `or`; `"null"` in the list needs no check |
| `enum` of strings, numbers, booleans | `.enum` | `exists(v)` | `in(v, list(...))` |
| `minLength`, `maxLength` | `.min-length`, `.max-length` | `v` is a string | `len(v)` against the bound, in code points |
| `pattern`, when it is a portable pattern | `.pattern` | `v` is a string | `matches(v, pattern)` |
| `format`: `email`, `date`, `date-time`, `uuid` | `.format` | `v` is a string | `matches` with a portable pattern for the format |
| `minimum`, `maximum` | `.minimum`, `.maximum` | `v` is a number | `gte`, `lte` |
| `exclusiveMinimum`, `exclusiveMaximum` (numbers) | `.exclusive-minimum`, `.exclusive-maximum` | `v` is a number | `gt`, `lt` |
| `multipleOf` | `.multiple-of` | `v` is a number | `mod(v, factor) = 0`, in decimal arithmetic |
| `minItems`, `maxItems` | `.min-items`, `.max-items` | `v` is a list | `len(v)` against the bound |
| `items.type` | `.items-type` | `v` is a list | `all(v, <type check of item>)` |
| `properties` of a nested object | the rules above, with pointers such as `/beneficiary/country` | | |
| `items.properties` of a list of objects | the rules above, with `forEach` | | |
| local `$ref` | followed, at every level | | |

### What is not derived

Each of these is reported on standard error, and in the list `derive_with_report` returns, with the
JSON Pointer of the constraint.

| Constraint | Why |
|---|---|
| `required` of a `readOnly` property | The server assigns it; a request need not carry it |
| `required` of a property that may be `null` | A rule cannot tell a `null` property from an absent one |
| `required` of a property that is not under `properties` | The entity schema does not know the path |
| `pattern` outside the portable subset (`\s`, `\b`, look-around, back-references, `\p{..}`) | It would not mean the same in every runtime (specification 4.4). Rewrite the pattern, or write the rule by hand |
| `format` other than the four above (`uri`, `hostname`, `int32` ...) | No portable pattern is provided |
| `const`; `enum` with lists or objects | Not derived; write a rule with `eq` |
| `uniqueItems`, `contains`, `prefixItems` | No operator compares the entries of a list with each other |
| `additionalProperties`, `patternProperties`, `propertyNames`, `minProperties`, `maxProperties` | A rule names the fields it checks and cannot enumerate the properties of an object |
| `allOf`, `anyOf`, `oneOf`, `not`, `if`/`then`/`else`, `dependentRequired`, `dependentSchemas` | Schema composition is not derived |
| Constraints on the entries of a list of scalars, other than their type | Not derived; write a rule with `all` |
| Lists of objects inside the entries of a list | `forEach` reaches one level |
| `$ref` into another document; a `$ref` back to a schema that contains it | Only local, non-recursive references are followed |
| A constraint written next to a `$ref` | Move it into the referenced schema |
| The boolean form of `exclusiveMinimum`, `exclusiveMaximum` | That is OpenAPI 3.0; OpenAPI 3.1 gives the bound as a number |
| A property name with characters other than letters, digits, `_` and `-` | A ruleset path cannot name it |
| A second property whose name differs from another only in case or punctuation (`fooBar`, `foo_bar`) | Both would get the same rule id |

### Where a derived rule differs from JSON Schema validation

The derived rules follow the schema closely, not exactly:

- **`required` also rejects `null`.** In Rule Cascade a path that does not resolve is `null`, so
  "present with the value null" and "absent" are the same thing.
- **`.` in a pattern matches line breaks.** In ECMAScript, the dialect of JSON Schema, it does not.
  `^` and `$` mean the start and the end of the string in both.
- **Formats check the shape.** `date` accepts months 01 to 12 and days 01 to 31, so `2026-02-30`
  passes. `date-time` requires upper-case `T` and `Z` and seconds up to 59, which is what the date
  operators of Rule Cascade accept. `email` is the pattern of the rule catalog, not RFC 5322.
- **`multipleOf` is exact.** `10.05` is a multiple of `0.01` in decimal arithmetic; many validators
  compute in binary floating point and disagree.
- **A wrong type is reported once.** `maxLength` on a number stays quiet, as in JSON Schema, and the
  `type` rule reports the number. `enum` is checked for any value that is present, so a value of the
  wrong type gets a `type` and an `enum` finding.
- **A list of objects that is not a list fails closed.** `forEach` needs a list (specification
  section 8). When the value is a string or an object, the `type` rule reports it and each
  per-element rule produces a `RULE-EVALUATION-ERROR` finding. The decision is `deny` either way.

### Finding codes stay stable

Codes are numbered in the order of the schema, so a property added in the middle would renumber the
rules after it, and a finding code must never change its meaning
([naming conventions](naming-conventions.md)). Pass the previous output when deriving again:

```sh
rcas derive orders.openapi.yaml --schema Order --id acme.generated.order \
    --codes-from order.ruleset.yaml -o order.ruleset.yaml
```

Rules that exist in the earlier file keep their codes, and new rules continue after the highest
number in use, including the numbers of rules that no longer exist.

### Golden tests for a generated file

A derived ruleset is regenerated, never edited, so its golden tests live in a file of their own and
are attached on every derivation with `--tests`. The file is a YAML or JSON list of tests in the
form of the `tests` section of a ruleset. The two examples in
[`examples/derived`](../examples/derived) are produced this way:

```sh
rcas derive examples/catalog/onboarding.openapi.yaml --schema Customer \
    --id acme.generated.customer --scope organization:acme,project:customer-portal \
    --tests examples/derived/onboarding-customer.tests.yaml \
    -o examples/derived/onboarding-customer.ruleset.yaml

rcas derive examples/contracts/payments.openapi.yaml --schema Transfer \
    --id acme.generated.transfer --scope organization:acme,project:payments-hub \
    --tests examples/derived/payments-transfer.tests.yaml \
    -o examples/derived/payments-transfer.ruleset.yaml

rcas check examples/derived/*.ruleset.yaml
```

Adding `--check` to a `derive` command turns it into a test that the committed file is up to date
with the OpenAPI description; `tools/tests/test_openapi_rules.py` runs it for both examples.

### Using the baseline

A derived ruleset is a starting point. There are two ways to build on it.

**Extend it.** A hand-written ruleset names the derived one as its parent and adds its own rules:

```yaml
scope:
  - { level: organization, id: acme }
  - { level: project, id: customer-portal }
  - { level: module, id: onboarding }
extends:
  - { ruleset: acme.generated.customer, version: ^1.0.0 }
```

The child inherits the entity and every derived rule, and the baseline follows the API whenever it
is derived again. Two properties of inheritance apply (specification section 5): a ruleset has one
parent, so this does not combine with an organisation baseline; and inherited rules are
`tighten-only`, so the child cannot disable a derived rule or lower its severity.

**Copy from it.** Take the derived rules into a hand-written ruleset and maintain them there: give
them the team's finding codes, place them on pages and components, soften some to warnings.
`x-generated-from` keeps the link to the constraint each came from, and deriving again shows what
changed in the API.

The derived ruleset has no `bindings`: an OpenAPI description names one ruleset at its root, and
that is the hand-written one.

### Why rules are still written by hand

Schema constraints describe one value at a time. Most business rules do not fit that form. The
[rule catalog](../examples/catalog/customer-onboarding.ruleset.yaml) and the
[payments example](../examples/contracts/payments-transfer.ruleset.yaml) show what derivation cannot
produce:

| Need | Example |
|---|---|
| Logic across fields | The postal code format depends on the country; a company name is required for business accounts; a SWIFT code is required for international transfers |
| Comparison with stored state, the actor or the time | The country cannot change after registration; an approver is not the creator; a customer is at least 18 on `ctx.now` |
| Parameters and hierarchy | A transfer limit set by the organisation and tightened by a business unit |
| Severities | A hint that a name contains digits is a warning; a hint to add a family name is information shown only in the UI |
| Acknowledgement and acceptance | A high credit limit must be acknowledged; a limit over the ceiling can be accepted by a credit manager with a justification |
| UI state | The company name field is visible and required for business accounts; the credit limit is read-only for agents |
| Computed values and actions | A fee is computed from the amount; a welcome event is emitted after the customer is saved |
| Place in the UI | Page, screen, section and component of a rule, so one screen can be evaluated on its own |
| Messages for people | "Enter your full name." instead of "Full name is required." |

The division of labour that follows: let the OpenAPI description carry the structural constraints
and derive them, and write by hand the rules that need judgement.

## The evaluation API is OpenAPI too

[`spec/v1/rule-evaluation.openapi.yaml`](../spec/v1/rule-evaluation.openapi.yaml) describes the
HTTP interface of a rule server in OpenAPI 3.1: listing rulesets, fetching a manifest or a bundle,
explaining a rule and evaluating a request (`POST /evaluations`). Its component schemas
(`EvaluationRequest`, `EvaluationResult`, `Finding`, `Resolution`) are the wire form of
specification section 8, and domain APIs reference them, as the example descriptions do for their
422 responses. `tools/lint_specs.py` validates this file and the example descriptions as OpenAPI 3.1.
