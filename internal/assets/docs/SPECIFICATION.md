# Rule Cascade 1.0 Specification

Status: draft (`1.0.0-alpha.3`). The words MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119.

This document defines the behaviour every runtime must reproduce, in every programming language and
on every operating system. The structure of a ruleset document is defined by
[`rule-cascade.schema.json`](rule-cascade.schema.json); the wire API by
[`rule-evaluation.openapi.yaml`](rule-evaluation.openapi.yaml). Where prose and the
[conformance suite](../../conformance/) disagree, the conformance suite wins and the prose is a bug.

## 1. Model

A **ruleset** is a YAML or JSON document of `kind: RuleSet`. It declares where it sits in the
enterprise hierarchy, what it inherits, the vocabulary its rules may use (entities, data types,
parameters, functions, custom operators), the rules, the messages, its bindings and its golden tests.

Three things happen to a ruleset, and they may happen in three different programs:

1. **Compile**: validate, resolve inheritance, run the load-time checks, compute the checksum, and
   produce a **bundle**: one JSON document holding the manifest for each channel.
2. **Publish**: hand a manifest (`server` or `client`) to whoever evaluates.
3. **Evaluate**: given a manifest and a request, return an *evaluation result*.

Evaluation is a pure function. A runtime MUST NOT read a clock, the network, the file system or a
random source during evaluation. Facts such as the current time are passed in through `ctx`.

Everything a runtime consumes at evaluation time is JSON. YAML is an authoring format only
(section 12).

## 2. Layers and hierarchy

| Layer | Fields | Purpose |
|---|---|---|
| Envelope | `ruleCascade`, `kind`, `metadata` | Spec version, identity, version, owner, lifecycle |
| Hierarchy | `scope`, `extends`, `overrides` | Position in the enterprise and what is inherited |
| Vocabulary | `entities`, `types`, `params`, `functions`, `operators` | What rules may mention |
| Rules | `rules[]` | The logic, addressed down to page, screen, section, component, field or data type |
| Presentation | `messages`, `defaultLocale` | What users see, per locale |
| Bindings | `bindings` | How the ruleset attaches to OpenAPI operations |
| Conformance | `tests` | Golden tests every runtime must pass |

Rules are addressed on two axes.

**Who owns the rule** is the ruleset's `scope`: an ordered list of `{level, id}` from least to most
specific. Level names are open. The recommended levels are `enterprise`, `organization`,
`businessUnit`, `agency`, `project`, `application`, `module` and `feature`. A ruleset inherits from
a less specific one with `extends`.

**What the rule is about** is the rule's `target`: an entity, optionally narrowed to a `page`,
`screen`, `section`, `component` and `field`/`fields`; or a semantic data `type`.

Every object that the schema marks extensible accepts `x-*` fields. Runtimes MUST preserve them in
the resolved ruleset and MUST NOT give them meaning.

### 2.1 Vocabulary

- `entities.<Name>.schema` references the JSON Schema of the payload, usually a component schema of
  an OpenAPI description. `entities.<Name>.fieldTypes` binds fields (JSON Pointers) to type names.
- `types.<Name>` declares a semantic data type (`Money`, `Email`, `CountryCode`). A type has no
  behaviour of its own: rules that target it give it one.
- `params.<name>` declares a typed constant that less general rulesets may override within its
  `overridePolicy`.
- `functions.<name>` declares a reusable expression with named parameters (section 4.5).
- `operators.<x-name>` declares a custom operator the host application supplies (section 4.6).

## 3. Rules

Every rule has `id`, `kind`, `target` and `operations`, and may have `triggers`, `enforcement`,
`when`, `priority`, `enabled`, `overridePolicy` and `tags`.

| Field | Meaning | Default |
|---|---|---|
| `target.entity` | Entity the rule is about | required unless `target.type` is given |
| `target.type` | Semantic type: the rule runs once for every field bound to it (validation rules only) | none |
| `target.page`, `.screen`, `.section`, `.component` | Logical place in the user interface (lower-case kebab-case ids) | none |
| `target.field` / `target.fields` | JSON Pointer(s) into the entity | none |
| `operations` | `create`, `read`, `update`, `delete`, `list` or any custom action name | required |
| `triggers` | Client moments: `load`, `change`, `blur`, `submit` | `[submit]` |
| `enforcement` | `client`, `server` or `both` | `both` |
| `when` | Applicability guard | always applies |
| `priority` | Higher runs first within a kind | `0` |
| `forEach` | JSON Pointer to an array; the rule runs once per element | none |

| Kind | Body | Produces |
|---|---|---|
| `validation` | `assert`, `severity`, `finding`, optional `acknowledgement`, `acceptance` | A finding when `assert` is `false` |
| `state` | `when`, `effects[]` | Field-state effects while `when` is `true` |
| `compute` | `assign[]` | Computed values, applied before validation |
| `action` | `commands[]`, `enforcement: server` | Commands, only after an allowed server evaluation |
| `x-*` | anything | Nothing. Reserved for extensions; ignored by conforming runtimes |

JSON Pointers are limited to segments of letters, digits, `_` and `-`, so a pointer `/a/b` and a
variable path `data.a.b` always name the same location.

## 4. Expressions (core profile 1.0)

An expression is one of:

- a literal: string, number, boolean or `null`;
- a variable reference: `{ "var": "<root>.<path>" }`;
- an operator call: `{ "op": "<operator>", "args": [ ... ] }`;
- a function call: `{ "fn": "<function>", "args": [ ... ] }`.

Each object has exactly the members shown. Anything else (an object with other or additional
members, or a list) is not an expression, and evaluating it is an evaluation error. This is the whole grammar: it is the same
JSON in every language, it needs no parser beyond a JSON parser, and it cannot express anything a
runtime would have to guess at.

| Root | Bound to | In scope |
|---|---|---|
| `data` | The proposed state of the entity | Everywhere |
| `original` | The stored state, `null` on create | Everywhere |
| `actor` | Who is acting; set by the host from its own authentication | Everywhere |
| `ctx` | Facts the host passes in, such as `ctx.now` | Everywhere |
| `params` | Resolved parameter values | Everywhere |
| `item` | The current element | Rules with `forEach`; the second argument of a collection operator |
| `value`, `field` | The value and the JSON Pointer of the current field | Rules that target a `type` |
| `arg` | The arguments of the current function call, by name | Function bodies |

### 4.1 Values

1. There is no implicit coercion. `1` is not `"1"`, and `true` is not `1`.
2. A path that does not resolve evaluates to `null`, and so does a root that is not in the table
   above. In a list, a segment made only of ASCII digits is a decimal index (`01` is `1`); an index
   beyond the end, and any other segment, is `null`.
3. Equality (`eq`, `ne`, `in`) is deep: numbers compare numerically (`1` equals `1.0`), lists
   element by element, objects key by key. Values of different types are not equal.
4. A type error, a wrong argument count or an unknown operator is an **evaluation error**.
5. Strings are sequences of Unicode code points. `len`, `substring` and `.` in a pattern count code
   points, never UTF-16 code units or bytes.

### 4.2 Numbers

1. A JSON number in a ruleset, a bundle or a request denotes the IEEE 754 double nearest to what was
   written, and the engine computes with the shortest decimal that identifies that double. So `0.1`
   is exactly one tenth; `9007199254740993` is `9007199254740992`; a literal with more digits than a
   double can hold is the same number as its nearest double. This is what JSON numbers mean in
   every mainstream parser, made explicit. A number too large for a double is not accepted.
2. Arithmetic inside an expression is decimal, never binary floating point: IEEE 754 decimal128
   semantics, 34 significant digits, round half even.
3. Every number **leaving** an expression (a computed value, a command payload, a message argument,
   an idempotency key part, an argument passed to a custom operator, the result of evaluating an
   expression), whether it was computed or merely passed through, is rounded half even to 15
   significant digits. That is the most a JSON number can carry through an IEEE 754 double
   unchanged, so every runtime and every JSON parser sees the same value. A number whose magnitude
   is then larger than the largest double is an evaluation error; one smaller than the smallest
   normal double leaves as the nearest double. Comparisons and arithmetic inside the expression
   happen before this rounding.
4. Numbers written in a ruleset or sent in a request SHOULD have at most 15 significant digits;
   identifiers SHOULD be strings.

### 4.3 Operators

| Operator | Arguments | Result |
|---|---|---|
| `and`, `or` | any number of booleans | Boolean. Short-circuits left to right. `and []` is `true`, `or []` is `false` |
| `not` | boolean | Boolean |
| `eq`, `ne` | any, any | Boolean, by the equality of 4.1 |
| `lt`, `lte`, `gt`, `gte` | number, number | Boolean. Anything but two numbers is an error |
| `between` | number, low, high | `low <= number <= high` |
| `in` | any, list | Boolean |
| `exists` | any | `true` unless the value is `null` |
| `empty` | any | `true` for `null`, `""`, `[]` and `{}` |
| `coalesce` | any number | First non-null argument, else `null`. Lazy |
| `if` | boolean, any, any | Second or third argument. Only the taken branch is evaluated |
| `typeOf` | any | `"null"`, `"boolean"`, `"number"`, `"string"`, `"list"` or `"object"` |
| `add`, `sub`, `mul`, `div` | number, number | Number. Division by zero is an error |
| `mod` | number, number | Remainder of truncated division; the sign follows the first argument. Zero divisor is an error |
| `abs` | number | Number |
| `min`, `max` | one or more numbers | Number |
| `round` | number, optional places (whole, 0 to 15, default 0) | Rounded half even |
| `len` | string or list | Number of code points, or of elements |
| `lower`, `upper` | string | Only the ASCII letters `A`-`Z` / `a`-`z` change |
| `trim` | string | Without leading and trailing space, tab, line feed and carriage return |
| `concat` | any number of strings | Joined string; `""` for no arguments |
| `substring` | string, start, optional length | By code points, zero-based. Start and length are whole and not negative; ranges beyond the end are cut |
| `text` | any | The value rendered as in section 8.1 |
| `matches` | string, pattern | Boolean. Unanchored search with a portable pattern (4.4) |
| `startsWith`, `endsWith`, `contains` | string, string | Boolean |
| `list` | any number | A list of the arguments |
| `all`, `some`, `none` | list, predicate | Boolean. The predicate is evaluated for **every** element with `item` bound; there is no short-circuit, so errors are deterministic. A `null` list counts as empty |
| `sum` | list, expression | Sum of the expression over the elements; `0` for an empty list |
| `map` | list, expression | List of the expression's value for each element |
| `filter` | list, predicate | List of the elements for which the predicate is `true` |
| `daysBetween` | date, date | Whole calendar days from the first to the second, in UTC |
| `yearsBetween` | date, date | Completed calendar years from the first to the second, the way ages are counted; negative when the second is earlier |
| `x-<name>` | as declared | A custom operator (4.6) |

Inside a collection operator `item` is the element of the innermost enclosing operator.

Dates are RFC 3339 `full-date` (`2026-10-03`) or `date-time` with seconds and an offset
(`2026-10-03T09:00:00Z`, `2026-10-03T09:00:00.5-04:00`), written with ASCII digits and upper-case
`T` and `Z`, with nothing before or after. A date-time is converted to UTC before its calendar date
is taken. Anything else is an evaluation error: a date that does not exist, an hour above 23, a
minute or second above 59 (leap seconds included), an offset beyond `23:59`, or a UTC date outside
the years 0001 to 9999.

### 4.4 Portable patterns

Regular-expression engines disagree in ways that are invisible until two runtimes disagree about a
customer. `matches` therefore accepts only the following syntax, and gives it one meaning:

| Syntax | Meaning |
|---|---|
| literal characters | Themselves, by code point |
| `.` | Any one code point, **including** line breaks |
| `^`, `$` | The very start and the very end of the string; never next to a line break |
| `\d` `\D` `\w` `\W` | ASCII digits `[0-9]`, ASCII word characters `[A-Za-z0-9_]`, and their complements |
| `\t` `\n` `\r` | Tab, line feed, carriage return |
| `\` + one of `^ $ \ . * + ? ( ) [ ] { } \| /` | That character |
| `[...]`, `[^...]` | Character class of characters, ranges `a-z` (low to high), `\d \D \w \W \t \n \r`, escaped syntax characters and `\-`. A literal `-` is escaped, first or last. A literal `[` is escaped |
| `(...)`, `(?:...)` | Group |
| `a\|b` | Alternation |
| `*` `+` `?` `{n}` `{n,}` `{n,m}` | Repetition, `n` and `m` ASCII digits and at most 1000; add `?` for lazy |

Counted repetitions multiply when they are nested: `(a{30}){40}` asks for 1200 copies of `a`. Along
any chain of nested groups the product of the `{}` counts (the maximum, or the minimum where there
is no maximum; zero counts as one) MUST NOT exceed 1000. `*`, `+` and `?` do not count. A pattern
has at most 1000 code points.

Repetitions MUST NOT be nested without a bound. A group that can repeat more than once (quantified
by `*`, `+`, `{n,}`, or by `{n}` or `{n,m}` whose upper count is 2 or more; lazy or not) MUST NOT
contain, at any depth, an unbounded quantifier (`*`, `+` or `{n,}`; lazy or not). `(a+)+`, `(a*)*`,
`(?:a{2,})+`, `((ab+)c)*`, `((a+)?)+` and `(a+){2}` are rejected; `(a+)?`, `(a+){1}`, `(\.\d+)?`,
`(?:ab)+` and `([a-z]{1,40}-)+` are accepted. A backtracking engine (ECMAScript, Java, Python) can
need time exponential in the length of the subject to fail such a pattern. The usual rewrite
separates the repeated part by its first character: `^[a-z]+(\.[a-z]+)*$` is
`^[a-z](?:[a-z]|\.[a-z])*$`.

The subject of `matches` MUST NOT be longer than 10000 code points. A longer subject is an
evaluation error, whatever the pattern. (The 1000 code point limit above is on the pattern, which an
author writes; the subject comes from a request.)

These two rules bound the cost of a match but do not make every portable pattern linear. Patterns
are literals written by the author of a ruleset; only the subject comes from a request. What the
rules do not catch remains the author's responsibility, and a reviewer's: alternatives that can
match the same text inside a repetition (`(a|a)*`, `(a|ab)+`), and bounded repetitions of a
variable length inside a repetition (`(a{1,2}){1,500}`, `(a{1,1000})+`), can still take time
exponential in the length of the subject, which the subject limit does not bound; adjacent
repetitions that can match the same text (`\d*\d*\d*$`) take polynomial time, which it does.

Everything else is rejected: `\s`, `\S`, `\b`, `\B`, `\A`, `\z`, `\Z`, `\x..`, `\u....`, `\p{..}`
and every other escape; back-references; lookaround; named, atomic and flag groups; possessive and
stacked quantifiers; an unescaped `{`, `}` or `]` that is not part of the syntax above; POSIX classes
and class intersection. There are no flags: matching is case-sensitive.

The pattern argument MUST be a string literal in a ruleset (`PATTERN_NOT_PORTABLE` at load time). At
evaluation time a pattern outside this subset is an evaluation error in every runtime, whether or
not the local engine could run it.

A runtime translates a portable pattern into the dialect of the engine it uses. For example:
ECMAScript uses flags `s` and `u`; Java uses `DOTALL` and rewrites `$` to `\z`; Python uses
`ASCII | DOTALL` and rewrites `$` to `\Z`; Go/RE2 prefixes `(?s)`.

Every `pattern` in `rule-cascade.schema.json` is itself a portable pattern.

### 4.5 Functions

`functions.<name>` has `params` (a list of names) and a `body` (an expression).
`{ "fn": name, "args": [...] }` evaluates every argument in the caller's scope, left to right, then
evaluates the body with `arg.<param>` bound to the argument values.

- Scope is lexical: a body sees `data`, `original`, `actor`, `ctx`, `params` and its own `arg`. It
  does not see the caller's `item`, `value`, `field` or `arg`.
- The number of arguments MUST equal the number of parameters.
- Functions MUST NOT be recursive, directly or indirectly (`FUNCTION_RECURSIVE` at load time). As a
  second line of defence, a call nested more than 32 function calls deep is an evaluation error.
  The body of a function counts towards the depth of an expression (4.7) from the depth of the
  call.
- A less general ruleset inherits functions and MUST NOT redefine one (`FUNCTION_REDEFINED`): the
  rules it inherits depend on what the function means.

### 4.6 Custom operators

An operator whose name starts with `x-` is supplied by the host application, in every runtime that
evaluates the ruleset. It MUST be declared under `operators` (`OPERATOR_UNDECLARED`).

- Arguments are evaluated first, left to right, and passed as plain JSON values (numbers rounded as
  in 4.2).
- The result is any JSON value. A number returned by the host is read as its shortest decimal
  representation; NaN and the infinities are not JSON values, and returning one is an evaluation
  error.
- An operator that is not registered, or whose host function fails, is an evaluation error: the
  rule fails closed.
- A custom operator MUST be a pure function of its arguments.
- Manifests list the custom operators their rules need under `operators`, so a host can verify at
  start-up that it has registered them all.

Prefer functions to custom operators. A function is part of the contract and behaves identically
everywhere; a custom operator is code that must be written, and kept identical, once per runtime.

### 4.7 Limits

A runtime checks these limits before it recurses into the input, so an input nested too deeply
fails the same way in every runtime instead of exhausting a stack, which some languages cannot
recover from.

- **Depth of an expression.** The expression that a rule, or the `expression` command, evaluates
  has depth 1. The arguments of an operator or function call at depth `d` have depth `d + 1`, and
  so does the body of a function called at depth `d`. Evaluating an expression at a depth greater
  than **128** is an evaluation error. Only what is evaluated counts: an argument that a lazy
  operator skips is never reached. A literal or `var` counts like any other expression, so
  `{"op": "not", "args": [true]}` has depth 2. A ruleset fails to load (`EXPRESSION_TOO_DEEP`)
  when an expression of a rule, or the body of a function, is by itself deeper than 128. A
  compiler MAY run this check on the document as written, before it validates the schema (step 1 of
  section 5), and then report it instead of any other problem.
- **Depth of a value.** A string, number, boolean or `null` has depth 0; a list or an object has
  depth one more than its deepest member, so `[]` and `{}` have depth 1. The `data`, `original`,
  `actor` and `ctx` of a request (section 8), and every root in the `env` of the `expression`
  command (section 13), MUST NOT be deeper than **64**. A request that is deeper is malformed: it is
  refused before anything is evaluated.
- **Subject of `matches`.** At most **10000** code points (4.4).

The engine does not limit the number of members of a list or an object, or the length of a string
other than the subject of `matches`. The host bounds the size of what it accepts (for example the
size of an HTTP request body) before it reaches the engine.

## 5. Loading

Loading a ruleset `R` with a registry of documents MUST perform these steps and MUST fail with the
stated code. A runtime MUST NOT serve a ruleset that failed to load.

1. Validate `R` against the schema: `SCHEMA_INVALID`.
2. If `R` extends a parent `P` (at most one):
   - `P` is not in the registry: `EXTENDS_NOT_FOUND`;
   - `P` is invalid: `SCHEMA_INVALID`;
   - `P`'s version does not satisfy the range: `EXTENDS_VERSION_MISMATCH`. `1.2.0` matches exactly;
     `^1.2.0` matches the same major version at `1.2.0` or later;
   - `P` extends `R`, directly or not: `EXTENDS_CYCLE`;
   - a `checksum` pin is given and differs from `P`'s checksum: `EXTENDS_CHECKSUM_MISMATCH`;
   - `R`'s scope does not start with `P`'s scope followed by at least one more level: `SCOPE_NOT_NARROWER`.
   - `R` starts from `P`'s resolved params, rules, entities, types, functions, operators and messages.
3. Add `R`'s params. Redeclaring an inherited param: `PARAM_REDEFINED`. A default that does not
   match its type: `PARAM_TYPE_MISMATCH`.
4. Merge `R`'s entities over the inherited ones, member by member. `fieldTypes` are merged pointer
   by pointer; binding a pointer that a parent bound to a different type: `FIELD_TYPE_REBOUND`.
5. Merge `R`'s types and operators over the inherited ones. Add `R`'s functions, each recording its
   `origin`; a name that is inherited: `FUNCTION_REDEFINED`. Merge `R`'s messages per locale.
6. Apply `overrides.params`: unknown param `PARAM_UNKNOWN`; `locked` param `PARAM_LOCKED`; wrong
   type `PARAM_TYPE_MISMATCH`; a `tighten-only` param moved against its `tightenDirection`:
   `PARAM_LOOSENED`.
7. Apply `overrides.rules`: unknown rule `RULE_UNKNOWN`; `locked` rule `RULE_LOCKED`. For a
   `tighten-only` rule (the default), disabling it, lowering its severity, adding an allowed
   acceptance or removing a required acknowledgement is `RULE_LOOSENED`.
8. Append `R`'s rules after the inherited ones. A repeated id: `RULE_DUPLICATE`. Every rule records
   its `origin` as `<ruleset id>@<version>`; an overridden rule records `overriddenBy`.
9. Compute the checksum (section 6).
10. Run the static checks. All problems found are reported together:

| Code | Raised when |
|---|---|
| `ENTITY_UNKNOWN` | A rule targets an entity that is not declared |
| `TYPE_UNKNOWN` | A rule targets, or a field is bound to, a type that is not declared |
| `SCHEMA_REF_UNRESOLVED` | An entity's schema reference cannot be resolved |
| `PATH_UNKNOWN` | A `data.*` or `original.*` path, an `item.*` path of a `forEach` rule, a target field, an effect field, an assign field, a `forEach` pointer or a `fieldTypes` pointer is not in the entity schema |
| `PARAM_UNDECLARED` | `params.<name>` is not declared |
| `SCOPE_INVALID` | A root is used where it is not in scope (see the table in section 4), or a function body reads an argument it does not declare |
| `FUNCTION_UNKNOWN` | A call names a function that is not declared |
| `FUNCTION_ARITY` | A call passes the wrong number of arguments |
| `FUNCTION_RECURSIVE` | A function calls itself, directly or indirectly |
| `OPERATOR_UNDECLARED` | An `x-*` operator is used but not declared under `operators` |
| `ORIGINAL_ON_CREATE` | A rule that applies to `create` reads `original.*` |
| `MESSAGE_MISSING` | A finding's message key is absent from the default locale |
| `MESSAGE_ARG_MISSING` | A message placeholder has no matching `finding.args` entry |
| `FINDING_CODE_DUPLICATE` | Two rules use the same finding code |
| `PATTERN_NOT_PORTABLE` | A `matches` pattern is not a literal or is outside the portable subset |
| `EXPRESSION_TOO_DEEP` | An expression of a rule, or a function body, is nested more than 128 deep (4.7) |

When a document has several problems in steps 1 to 8, a runtime reports at least one of them; which
one is not specified.

Path checks need the entity schema. A runtime that is not given a way to load schema documents
skips `PATH_UNKNOWN` and `SCHEMA_REF_UNRESOLVED` and MUST document that it does. Rules that target a
type and name no entity have no schema to check `data.*` paths against.

## 6. Checksum

The **resolved ruleset** is the object with exactly these members: `ruleCascade`, `id`, `version`,
`scope`, `conflictPolicy`, `defaultLocale`, `entities`, `types`, `params` (each declared param plus
`value` and `origin`), `functions` (each as written plus `origin`), `operators`, `rules` (each rule
as written, with overrides applied, plus `origin` and, when overridden, `overriddenBy`) and
`messages`. Defaults are not filled in, except `conflictPolicy` (`fail`) and `defaultLocale` (`en`).

The checksum is `"sha256:"` followed by the lower-case hex SHA-256 of the UTF-8 bytes of the
**canonical JSON** of the resolved ruleset:

- object members sorted by key in UTF-16 code unit order, no whitespace;
- strings escaped as JSON requires and no further (`\"`, `\\`, `\b`, `\f`, `\n`, `\r`, `\t`, and
  `\u00xx` for other characters below U+0020);
- numbers in plain decimal notation without an exponent and without trailing zeros; zero is `0`.

Two runtimes that load the same documents MUST produce the same checksum.

## 7. Manifests and bundles

A **manifest** is what an evaluator consumes. It has `ruleCascade`, `id`, `version`, `checksum`,
`channel`, `conflictPolicy`, `defaultLocale`, `params` (name to value), `fieldTypes` (entity to
pointer to type name, for entities that bind any), `functions` (name to `{params, body}`),
`operators` (sorted names of the custom operators the manifest's rules can reach, directly or
through functions), `rules` and `messages`.

The **server** manifest contains everything. The **client** manifest contains only:

- rules whose `enforcement` is `client` or `both` and whose `kind` is not `action`;
- the functions those rules can reach;
- the params those rules and functions read;
- the messages those rules use.

Both carry the checksum of the full resolved ruleset.

A **bundle** is the compiled, portable form of a ruleset:

```json
{ "ruleCascadeBundle": "1.0.0", "id": "...", "version": "...", "checksum": "sha256:...",
  "manifests": { "server": { }, "client": { } } }
```

A bundle is plain JSON. A runtime that reads a bundle MUST reject a `ruleCascadeBundle` whose major
version it does not implement (`BUNDLE_UNSUPPORTED`), MUST reject one without a `server` and a
`client` manifest that each have `id`, `version`, `checksum`, `rules` and their own name as
`channel` (`BUNDLE_INVALID`), and
otherwise trusts it: the compiler has already run every check of section 5. In a manifest that
passes these checks, an absent `params`, `fieldTypes`, `functions` or `messages` is empty, an
absent `conflictPolicy` is `fail` and an absent `defaultLocale` is `en`.

A runtime can also read **one manifest** on its own, which is how a browser or a mobile app
receives rules. The manifest must have `id`, `version`, `checksum`, `rules` and a `channel` of
`server` or `client` (`MANIFEST_INVALID`). A ruleset read this way has that one channel: a client
manifest cannot be evaluated as the server. Bundles are how a ruleset reaches a runtime that has no
compiler, and how one compilation is guaranteed to be evaluated identically everywhere. A host
SHOULD treat bundles like any other deployable artefact: build them in CI, store them immutably and
verify their origin.

## 8. Evaluation

A request has `entity`, `operation`, and optionally `data`, `original`, `actor`, `ctx`,
`resolutions`, `trigger`, `locale` and `view`.

| Member | Type | When absent or `null` |
|---|---|---|
| `entity`, `operation` | string | required |
| `data` | object | `{}` |
| `original` | object | `null` |
| `actor` | object; `actor.roles`, when present, is a list of strings | `{}` |
| `ctx` | object | `{}` |
| `resolutions` | list of `{rule, type, justification?}`, all strings | none |
| `trigger`, `locale` | string | none |
| `view` | object whose `page`, `screen`, `section`, `component` are strings | every place |

A request that does not have this shape, or whose `data`, `original`, `actor` or `ctx` is nested
more than 64 deep (4.7), is refused before anything is evaluated; it is never half-evaluated.
Members not listed here are ignored.

**Selection.** A rule takes part when all of these hold:

- it is enabled;
- it targets the entity, or targets a type and names no entity;
- it lists the operation;
- its `enforcement` fits the manifest's channel (`client`: `client` or `both`; `server`: `server`
  or `both`);
- on a client manifest, when the request has a `trigger`, the rule's `triggers` contain it;
- for each of `page`, `screen`, `section` and `component` that the request's `view` names **and**
  the rule's target names, the two are equal. A rule that does not name a place applies in every
  place; a request without a `view` evaluates every place.

**Order.** Within each phase rules run by descending `priority`; equal priorities keep document
order, inherited rules first.

1. **compute.** For each rule whose `when` holds, for each assignment: with `mode: default` skip
   it when the field is already non-null; otherwise evaluate the value, write it into a working copy
   of `data`, and emit a `value` effect. Later phases see the written value.
   Assignments of one rule apply in order; when one fails, the earlier ones stay applied.
2. **state.** For each rule whose `when` holds, collect the field properties it sets. A rule applies
   all of its effects or, when one of them is a conflict, none: the outcome never depends on the
   order of the members of `set`. Emit one `state` effect per field and rule, in the order first set.
3. **validation.** A rule is evaluated once, except:
   - with `forEach`, once per element of the array, in order, with `item` bound. A `null` array
     counts as empty; any other non-list value is an evaluation error;
   - with `target.type`, once per field of the request's entity that is bound to the type, in
     ascending order of the pointers, with `value` bound to the field's value (`null` when absent)
     and `field` to its pointer.

   When `when` holds and `assert` is `false`, emit a finding.
4. **decision.** `deny` when at least one finding is blocking, else `allow`.
5. **action.** Only on a server manifest and only when the decision is `allow`: for each rule whose
   `when` holds, emit its commands.

**Conflicts.** When a second compute or state rule (or a later effect of the same state rule) sets
the same field (or field property) to a different value: with `conflictPolicy: fail`, or when both rules have the same priority, it is an
evaluation error of the second rule; with `conflictPolicy: priority` the earlier, higher-priority
rule wins silently.

**Evaluation errors.** A rule that raises an evaluation error produces a finding with code
`RULE-EVALUATION-ERROR`, severity `error`, message `This rule could not be evaluated.`, no fields,
`blocking: true`, and stops. The engine fails closed. An error in an action rule also sets the
decision to `deny` and discards all commands. A finding MAY carry a runtime-specific `detail`.

### 8.1 Findings

| Field | Value |
|---|---|
| `rule`, `code`, `severity` | From the rule |
| `message` | The template for the request `locale`. Catalogs are consulted from most to least specific: the requested tag, then the tag with its last subtag removed, and so on (`fr-CA-x-a`, `fr-CA`, `fr`), then the default locale, then the key itself. Tags are compared exactly, including case. Each `{name}` is replaced by the rendered argument; unknown placeholders stay as written |
| `fields` | The target pointer(s); with `forEach`, prefixed by `<forEach>/<index>`; for a type rule, the pointer of the bound field |
| `location` | The `page`, `screen`, `section` and `component` the target names; absent when it names none |
| `status` | `open`, `acknowledged` or `accepted` |
| `resolution` | `none`, `acknowledge` or `accept-risk`: what a user may do |
| `blocking` | See below |
| `acceptableBy` | The acceptance roles, when the rule lists any |
| `source` | The rule's `origin` |

Rendering a value: `null` is the empty string, booleans are `true`/`false`, numbers use the
canonical form of section 6, strings are themselves, lists are their rendered elements joined by
`", "`, objects are their canonical JSON.

| Severity | `resolution` | Becomes non-blocking when |
|---|---|---|
| `info` | `none` | Always non-blocking |
| `warning` | `acknowledge` if `acknowledgement: required`, else `none` | No acknowledgement required, or the request carries an `acknowledge` resolution for the rule (`status: acknowledged`) |
| `error` | `accept-risk` if `acceptance.allowed`, else `none` | The request carries an `accept-risk` resolution for the rule, the actor holds one of `acceptance.roles` (or no roles are listed), and a non-empty justification is present unless `acceptance.justification` is `none` (`status: accepted`) |

### 8.2 Commands

A command has `name`, `type`, optional `ref`, `rule`, `payload` and `idempotencyKey`: the rendered
key parts joined by `:`. The engine only returns commands. The host executes them after it has
persisted the change and MUST de-duplicate on `idempotencyKey`.

## 9. Channels and trust

A client evaluation is advice for the user. A server evaluation is the decision. A host MUST
evaluate on the server for every state-changing operation, whatever the client reported, and MUST
take `actor` from its own authentication, never from the caller's payload.

## 10. Versioning

- `ruleCascade` is the version of this specification. Minor versions only add. A runtime MUST
  reject a document whose major version it does not implement.
- `ruleCascadeBundle` is the version of the bundle format, with the same rule.
- `metadata.version` is the ruleset's own semantic version.
- Operators belong to a profile. New operators arrive in a new profile; the meaning of an existing
  operator never changes.

## 11. Conformance

There are two conformance levels.

| Level | The runtime can | It must pass |
|---|---|---|
| **Evaluator** | read a bundle and evaluate | every expression case, every golden test and the whole evaluation corpus, starting from the published bundles |
| **Compiler** | also load source documents and produce bundles | additionally every load-error case, and the checksum, bundle checksum and client manifest of each fixture |

A runtime states the level it implements. An evaluator is a few hundred lines in most languages;
that is what makes a new language cheap to add. Every conformance runner registers the custom
operators described in [`conformance/README.md`](../../conformance/README.md).

## 12. Authoring in YAML

A ruleset is a JSON value. YAML is a convenient way to write one, and YAML parsers do not agree on
what an unquoted scalar means: `no`, `on` and `yes` are booleans to a YAML 1.1 parser and strings to
a YAML 1.2 parser; `012`, `1_000`, `12:30` and `2026-10-03` have the same problem.

- A compiler that reads YAML MUST produce the JSON value a YAML 1.2 core-schema parser produces.
- Rulesets MUST quote every scalar that YAML 1.1 and YAML 1.2 read differently. Tools SHOULD report
  an unquoted one (`YAML_NOT_PORTABLE`).
- Anchors, aliases, merge keys, tags, multiple documents and repeated keys MUST NOT be used.
- Numbers SHOULD have at most 15 significant digits (section 4.2). Tools SHOULD report longer ones
  (`NUMBER_NOT_PORTABLE`).
- Files are UTF-8 without a byte order mark. Line endings are not significant.

A runtime that only evaluates never reads YAML.

## 13. Engine protocol

An engine may be offered as a program instead of a library: a command-line binary, or a WebAssembly
module, that any language on any operating system can run. Such an engine speaks **JSON Lines** on
its standard input and output: one request object per line in, one response object per line out, in
the same order. Lines are UTF-8 and end with a line feed; a carriage return before it is tolerated,
and blank lines are ignored. Members a command does not define are ignored. The engine exits when
its input ends.

A response is `{"ok": true, "result": ...}` or `{"ok": false, "error": {"code", "message", ...}}`,
and repeats the request's `id` when it has one.

| `command` | Request members | Result |
|---|---|---|
| `version` | none | `engine`, `engineVersion`, `ruleCascade`, `bundle`, `levels` (`evaluator`, and `compiler` when implemented), `operators` (registered custom operators) |
| `load` | `bundle` or `manifest` | `{ruleset, version, checksum, channels, missingOperators}`. The engine keeps the ruleset for later requests, replacing any it held under the same id. `channels` is the sorted list of channels the ruleset has; `missingOperators` the sorted custom operators its rules need that the engine does not have |
| `manifest` | a source; optional `channel` | The manifest |
| `evaluate` | a source; optional `channel`; `request` | The evaluation result of section 8 |
| `expression` | `expr`; optional `env`, `functions` | The value of the expression |
| `compile` | `document`; optional `registry` (id to document), `schemaDocuments` (file to document) | The bundle. Compiler level only |

A **source** is `ruleset` (the id of a loaded ruleset), or an inline `bundle`, or an inline `manifest`.
When more than one is given, `bundle` wins over `manifest`, and `manifest` over `ruleset`. An inline
source is used for that request only. `channel` defaults to `server` when the ruleset has a server
manifest, and otherwise to the channel it has.

| Error code | Meaning |
|---|---|
| `BAD_REQUEST` | Not JSON, not an object, an unknown command, or a missing or mistyped member |
| `UNKNOWN_RULESET` | `ruleset` names an id that was not loaded |
| `CHANNEL_UNAVAILABLE` | The ruleset was read from one manifest and the request asks for the other channel |
| `LOAD_FAILED` | The bundle or document was rejected; `problems` lists `{code, message, rule?}` |
| `EVALUATION_ERROR` | `expression` only: the expression raised an evaluation error |
| `UNSUPPORTED` | The engine does not implement the command (for example `compile` on an evaluator) |

The members of a request are checked before a ruleset is looked up or a bundle is read, so a
malformed request is always `BAD_REQUEST`: a `channel` other than `server` or `client`; a `ruleset`
that is not a string; a `bundle`, `manifest`, `document`, `registry`, `schemaDocuments`, `env` or
`functions` that is not an object; a root of `env` nested more than 64 deep (4.7); a `request` that
does not have the shape of section 8. Optional members may be `null`: a `null` channel is the default
channel. A line that is not JSON includes one with `NaN`,
`Infinity` or a number too large for a double.

Entity schemas referenced by `compile` are resolved through `schemaDocuments`, keyed by the file
part of the entity's `$ref`; when it is absent the path checks are skipped (section 5).

Custom operators cannot cross a process boundary. An engine offered as a program evaluates rulesets
that use them only if those operators were built into it; otherwise the rules fail closed.
