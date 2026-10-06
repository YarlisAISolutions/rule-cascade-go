# Authoring guidelines

The rules for writing a ruleset. They exist so that a rule means the same thing in every runtime,
can be reviewed by someone who did not write it, and can be changed without breaking the rulesets
and applications that depend on it.

- **MUST** and **MUST NOT** are requirements. Where a tool enforces one, the code it reports is
  given in brackets. The others are for the reviewer to enforce.
- **SHOULD** is the expected practice. A pull request that deviates says why.
- The examples are fragments: they show the members a guideline is about and leave the others out.

Run the linter before every commit. It validates the schema, runs every load-time check and the
golden tests, and reports YAML that is not portable:

```bash
rcas check                                  # every ruleset of the project (rcas.yaml)
rcas check path/to/*.ruleset.yaml           # or these files
```

Names are in [naming conventions](naming-conventions.md), worked examples in the
[cookbook](cookbook.md), and the normative text in the
[specification](../spec/v1/SPECIFICATION.md). The
[rule catalog](../examples/catalog/customer-onboarding.ruleset.yaml) shows them applied, with one
rule for every common data type and severity.

## 1. Identity and versioning

**1.1 MUST** give the ruleset a lower-case, dot-separated `metadata.id` that runs from broad to
narrow and never changes. [`SCHEMA_INVALID`]

```yaml
id: acme.payments.transfer      # good
id: Acme.Payments_Transfer      # bad: upper case and underscore
```

**1.2 MUST** set `ruleCascade: 1.0.0`, `kind: RuleSet`, and `metadata.id`, `version` and `title`.
[`SCHEMA_INVALID`]

**1.3 MUST** name the accountable team in `metadata.owner`, and **SHOULD** set `metadata.status`
(`draft`, `active`, `deprecated`, `retired`; the default is `draft`).

**1.4 MUST** change `metadata.version` with every change to a published ruleset. A published version
is immutable: the same id and version always have the same checksum.

**1.5 MUST** choose the version bump by what consumers can observe:

| Change | Bump |
|---|---|
| Add an `info` rule, a message, a locale, a golden test; reword a message | patch |
| Add a `warning` without acknowledgement, a `state` or `compute` rule, a parameter, a function, a type | minor |
| Add an `error` rule or a warning that must be acknowledged; raise a severity; tighten a parameter; remove or rename a rule, parameter, function, type, place id or finding code; change `enforcement` | major |

A child that pins `^1.2.0` on its parent takes patches and minors automatically and has to choose to
take a major.

**1.6 MUST NOT** reuse a rule id or a finding code for a different meaning. Retire the old rule and
give the replacement a new id and a new code: both appear in logs, overrides and audit records.

**1.7 SHOULD** reference a parent with a caret range (`^1.2.0`), and **SHOULD** add the `checksum`
pin when the parent is published by another organisation. [`EXTENDS_VERSION_MISMATCH`,
`EXTENDS_CHECKSUM_MISMATCH`]

## 2. Scope and inheritance

**2.1 MUST** list `scope` from the least to the most specific level, starting with the parent's
whole scope and adding at least one level. [`SCOPE_NOT_NARROWER`]

```yaml
scope:                                              # parent: enterprise, organization
  - { level: enterprise, id: acme-group }
  - { level: organization, id: acme }
  - { level: businessUnit, id: retail-banking }     # good: one more level than the parent
```

**2.2 SHOULD** use the recommended level names (`enterprise`, `organization`, `businessUnit`,
`agency`, `project`, `application`, `module`, `feature`) and the same names across the organisation.

**2.3 MUST** extend at most one parent. Rules shared by unrelated branches belong to a common
ancestor. [`SCHEMA_INVALID`]

**2.4 SHOULD** put a rule at the highest level at which it is true for everything below it, and a
threshold that differs per level in a parameter that the levels override.

**2.5 MUST** change what is inherited only through `overrides`, each rule override with a `reason`
that a reviewer and an auditor can understand. [`SCHEMA_INVALID` without a reason]

```yaml
overrides:
  params:
    maxTransferAmount: 25000
  rules:
    - rule: org.transfer.memo-recommended
      set: { severity: warning }
      reason: Retail operations needs memos for dispute handling.    # good
      # bad: "reason: changed" says nothing
```

**2.6 MUST NOT** redeclare an inherited parameter or function, rebind a field that a parent bound to
a type, or repeat a rule id. [`PARAM_REDEFINED`, `FUNCTION_REDEFINED`, `FIELD_TYPE_REBOUND`,
`RULE_DUPLICATE`]

**2.7 MUST NOT** work around a parent's policy by copying its rule under a new id with a weaker
condition. Ask the owner of the parent to mark the rule `open`.

**2.8 MUST** state `overridePolicy` on every parameter that children are not meant to change
freely. The default for a parameter is `open`; the default for a rule is `tighten-only`. A
`tighten-only` parameter is a `number` or `integer` and states its `tightenDirection`.
[`SCHEMA_INVALID`] A child that breaks a policy does not load. [`PARAM_LOCKED`, `PARAM_LOOSENED`,
`RULE_LOCKED`, `RULE_LOOSENED`]

## 3. Targets

**3.1 MUST** give every rule a `target` with an `entity`, or with a semantic `type`. A rule that
targets a type is a validation rule and has no `field`, `fields` or `forEach`. [`SCHEMA_INVALID`,
`ENTITY_UNKNOWN`, `TYPE_UNKNOWN`]

**3.2 MUST** point `field` or `fields` at the value the user has to change, as a JSON Pointer with
the property names of the API. A rule about several fields lists them all. [`SCHEMA_INVALID`,
`PATH_UNKNOWN`]

```yaml
target: { entity: Transfer, field: /beneficiary/swiftCode }          # good
target: { entity: Customer, fields: [/creditLimit, /annualIncome] }  # good: a cross-field rule
target: { entity: Transfer, field: beneficiary.swiftCode }           # bad: not a JSON Pointer
```

**3.3 MUST** name places (`page`, `screen`, `section`, `component`) by what the user sees, as
lower-case kebab-case ids that both the ruleset and the user interface use. [`SCHEMA_INVALID` for
anything but lower-case letters, digits and `-`]

```yaml
target: { entity: Customer, page: onboarding, screen: profile, section: identity,
          component: name-input, field: /fullName }                  # good
target: { entity: Customer, component: "#fullName" }                 # bad: a selector
target: { entity: Customer, component: MuiTextField }                # bad: a framework class
target: { entity: Customer, screen: step-2 }                         # bad: a position, no meaning
```

**3.4 MUST** treat a place id as an interface: a user interface passes it in `view`, so renaming one
is a major change (1.5).

**3.5 SHOULD** name every level the user interface filters by. A request `view` skips a rule only
when both name the same level and differ; a rule that leaves a level out is evaluated in every view
of that level.

**3.6 SHOULD** leave the place out of rules that hold wherever the entity is changed, such as
immutability, authorisation and rules on a data type. They then take part in every view.

**3.7 SHOULD** bind fields that share a meaning to a type under `entities.<Name>.fieldTypes` and
write the rule once for the type. The rule reads `value` and `field`, not `data.<path>`.
[`TYPE_UNKNOWN`, `SCOPE_INVALID`]

```yaml
entities:
  Customer:
    fieldTypes: { /email: Email, /backupEmail: Email }
rules:
  - id: type.email.format
    target: { type: Email }
    when: { op: exists, args: [ { var: value } ] }
    assert: { op: matches, args: [ { var: value }, "^[^@ ]+@[^@ ]+\\.[A-Za-z]{2,}$" ] }
```

**3.8 MUST** use `forEach` for a rule about each element of a list. `target.field` is then relative
to the element and the rule reads `item.<path>`; the finding points at `/lines/1/quantity`.
[`PATH_UNKNOWN`, `SCOPE_INVALID`]

**3.9 MUST** list in `operations` exactly the operations the rule is written for. A rule that reads
`original.*` does not list `create`. [`ORIGINAL_ON_CREATE`] When the ruleset has OpenAPI bindings,
every operation a rule lists is bound. [`BINDING_MISMATCH`]

**3.10 SHOULD** choose `triggers` by the cost of interrupting the user, and include `submit` on
every validation rule, so that a client evaluation at submit shows what the server will refuse. The
default is `[submit]`. Triggers select rules on the client only; the server evaluates every rule of
the operation.

| Rule | Triggers |
|---|---|
| Cheap check of the value being typed (sign, range) | `[change, submit]` |
| Format of a finished value (e-mail, postal code) | `[blur, submit]` |
| Cross-field rule; warning that needs a confirmation | `[submit]` |
| State rule (visible, required, read-only) | `[load, change]` |

## 4. Names

**4.1 MUST** follow [naming conventions](naming-conventions.md) for every name in a ruleset.

**4.2 MUST** name a rule for the constraint, as `<entity>.<field or aspect>.<constraint>`; a rule on
a type as `type.<type>.<constraint>`; an inherited level's rule with the level as prefix.

```yaml
id: transfer.amount.positive           # good
id: type.money.two-decimals            # good
id: transfer.check-17                  # bad: says nothing
id: transfer.amount-max-25000-error    # bad: encodes a value and a severity that will change
```

**4.3 MUST** give every validation rule its own finding code, `<DOMAIN>-<GROUP>-<NNN>`, numbered
upwards and never renumbered. [`SCHEMA_INVALID`, `FINDING_CODE_DUPLICATE`]

**4.4 SHOULD** name a message key `<subject>.<reasonInCamelCase>` (`transfer.amountOverLimit`,
`email.format`) and a parameter in camelCase with the unit when it is not obvious
(`reviewWindowDays`, `internationalFeeRate`).

**4.5 SHOULD** name a function for what it returns (`isBlank`, `ageOn`), a type as a singular noun
for the meaning rather than the representation (`Money`, not `Decimal2`), and a custom operator as
`x-<what-it-checks>` (`x-luhn`).

## 5. Expressions

**5.1 MUST** give `when`, `assert` and every predicate a boolean. There is no truthiness: a string
or `null` where a boolean is expected is an evaluation error, and the rule fails closed.

```yaml
assert: { op: not, args: [ { op: empty, args: [ { var: data.memo } ] } ] }    # good
assert: { var: data.memo }                                                    # bad: not a boolean
```

**5.2 MUST** guard a value that may be absent in `when`, so that the rule does not apply, instead of
letting a comparison meet `null`. Presence is a rule of its own.

```yaml
when: { op: exists, args: [ { var: data.amount } ] }      # good: no amount, the rule does not apply
assert: { op: gt, args: [ { var: data.amount }, 0 ] }
# bad: the same assert without the guard. A missing amount is an evaluation error and the
# user sees "This rule could not be evaluated." instead of a message they can act on.
```

**5.3 SHOULD** guard with `typeOf` instead of `exists` where the value may arrive with another type,
as it can from a form. With `exists`, the string `"100"` in a numeric comparison fails closed.

```yaml
when: { op: eq, args: [ { op: typeOf, args: [ { var: data.amount } ] }, number ] }
```

**5.4 MUST NOT** rely on conversion. `"5"` is not `5` and `true` is not `1`: `eq` between different
types is `false`, without an error, so a rule that compares a number with a string never fires.

**5.5 MUST** decide which kind of "missing" a rule means: `exists` is false only for `null` (an
absent path is `null`); `empty` is true for `null`, `""`, `[]` and `{}`; a string of spaces is
neither, so test it with a function such as `isBlank`.

**5.6 MUST** default a list that may be absent before `in`, and remember that `all` over an empty
or absent list is `true`.

```yaml
op: in
args:
  - risk-officer
  - { op: coalesce, args: [ { var: actor.roles }, { op: list, args: [] } ] }
```

**5.7 MUST** keep every number in a ruleset, a golden test and a request within 15 significant
digits. Arithmetic is decimal (`0.1 + 0.2` is `0.3`), and every number that leaves the engine is
rounded to 15 significant digits. [`NUMBER_NOT_PORTABLE` for a literal with more]

**5.8 MUST** carry identifiers as strings: account numbers, card numbers, order ids. As a number,
`9007199254740993` is read as `9007199254740992` and leaves the engine as `9007199254740990`.

**5.9 MUST** round explicitly where the number of decimals matters. `round` rounds half even:
`round(2.675, 2)` is `2.68` and `round(2.5)` is `2`.

**5.10 MUST** read the current time from `ctx.now` and nowhere else, and write dates as RFC 3339:
`2026-10-03`, or a date-time with seconds and an offset such as `2026-10-03T09:00:00Z`. A date-time
is converted to UTC before its calendar date is taken. Anything else is an evaluation error, so
guard a date that comes from the user (`isIsoDate` is a function of the rule catalog):

```yaml
when: { fn: isIsoDate, args: [ { var: data.dateOfBirth } ] }
assert:
  op: gte
  args:
    - { op: yearsBetween, args: [ { var: data.dateOfBirth }, { var: ctx.now } ] }
    - { var: params.minimumAge }
```

**5.11 SHOULD** put every threshold, rate and list that a level may tune, or that a message shows,
in `params`, and read it as `params.<name>`. [`PARAM_UNDECLARED`]

```yaml
assert: { op: lte, args: [ { var: data.amount }, { var: params.maxTransferAmount } ] }   # good
assert: { op: lte, args: [ { var: data.amount }, 25000 ] }                               # bad
```

**5.12 SHOULD** move logic that two rules share into `functions`. A body reads `arg.<name>` and the
roots `data`, `original`, `actor`, `ctx` and `params`; it does not see the caller's `item`, `value`
or `field`, so pass them as arguments. Functions do not recurse. [`FUNCTION_UNKNOWN`,
`FUNCTION_ARITY`, `FUNCTION_RECURSIVE`, `SCOPE_INVALID`]

**5.13 MUST** use a custom operator (`x-...`) only for logic that the core operators and functions
cannot express, declare it under `operators`, and keep it a pure function. It has to be implemented
identically in every runtime that evaluates the ruleset, the browser included, and it is not
available in the `rcas` command, the WebAssembly module or the stock rule server.
[`OPERATOR_UNDECLARED`]

**5.14 SHOULD** remember what the string operators do: `len` and `substring` count code points;
`lower` and `upper` change only the ASCII letters; `trim` removes spaces, tabs, line feeds and
carriage returns.

**5.15 MUST** write compute and state rules so that two rules never give one field different values
in the same request. With `conflictPolicy: fail` (the default) that is an evaluation error; with
`priority` the rule with the higher `priority` wins, and equal priorities are still an error.

**5.16 SHOULD** leave `priority` at `0` unless the order of compute rules matters, and use
`mode: default` for a value the user may overwrite and `mode: always` for one the rules own.

## 6. Portable patterns

**6.1 MUST** write the pattern of `matches` as a string literal in the portable subset of
specification section 4.4. [`PATTERN_NOT_PORTABLE`]

**6.2 MUST** anchor a pattern that describes the whole value. `matches` searches: without anchors,
`[0-9]{5}` accepts `abc12345xyz`.

```yaml
{ op: matches, args: [ { var: data.postalCode }, "^[0-9]{5}(-[0-9]{4})?$" ] }    # good
{ op: matches, args: [ { var: data.postalCode }, "[0-9]{5}" ] }                  # bad
```

**6.3 MUST NOT** use `\s`, `\b`, look-around, back-references, named groups, flags or Unicode
escapes: they are not in the subset. Write a space or `[ \t\n\r]` for white space. `\d` and `\w` are
ASCII only; `.` matches every character, line breaks included; `$` is the very end of the string.

**6.4 MUST** make case explicit, because there are no flags: write `[A-Za-z]`, or compare
`lower(value)` when the text is ASCII.

**6.5 SHOULD NOT** repeat alternatives that overlap (`(a|ab)+`) or put large counts on a wildcard
(`(.*a){12}`). Nested unbounded repetitions (`(a+)+`, `(.*)*`) are no longer a guideline: they fail
to load with `PATTERN_NOT_PORTABLE`. Prefer bounded counts and character classes. The JavaScript, Java and Python engines
backtrack, so such a pattern can take exponential time on a hostile input
([`SECURITY.md`](../SECURITY.md)).

**6.6 MUST** stay within the limits: counts up to 1000, nested counts that multiply to at most 1000,
and at most 1000 code points per pattern. [`PATTERN_NOT_PORTABLE`]

**6.7 SHOULD** write a pattern in double quotes with each backslash doubled, as YAML requires:
`"^\\+[1-9][0-9]{7,14}$"`.

## 7. Severities, acknowledgement and acceptance

**7.1 MUST** choose the severity by what happens to the operation, not by how important the rule
feels.

| Use | When | Blocks |
|---|---|---|
| `info` | A hint. Nothing is wrong | Never |
| `warning` | Probably a mistake, but the operation may proceed | No |
| `warning` with `acknowledgement: required` | The user must consciously confirm, and the confirmation is worth recording | Until the request carries an `acknowledge` resolution |
| `error` | The operation must not proceed | Always |
| `error` with `acceptance` | A limit that a named role may exceed for a stated reason | Until an actor with one of the roles sends an `accept-risk` resolution |

**7.2 SHOULD NOT** require an acknowledgement for a condition that occurs in most operations. Users
learn to confirm without reading, and the confirmations that matter lose their meaning.

**7.3 MUST** list the roles that may accept in `acceptance.roles`. Without roles, any actor may
accept the risk.

```yaml
# good
acceptance: { allowed: true, roles: [risk-officer], justification: required, expiresAfter: P1D }
# bad: anyone may accept
acceptance: { allowed: true }
```

**7.4 SHOULD** keep `justification: required`, which is the default, and state `expiresAfter`. The
engine does not apply `expiresAfter`: it tells the host, which stores acceptances, for how long one
may be sent again.

**7.5 MUST NOT** allow acceptance of a rule that expresses law, sanctions, segregation of duties or
data integrity. Leave `acceptance` out and mark the rule `overridePolicy: locked`.

**7.6 SHOULD** write one rule for one reason. A rule whose `assert` joins unrelated conditions gives
the user one message for several problems and cannot be overridden or accepted separately.

**7.7 SHOULD** mark a rule `open` when it is a default that lower levels may relax, and `locked`
when no level may change it. The default, `tighten-only`, lets a child make the rule stricter and
nothing else (12.2).

## 8. Enforcement

A rule with `enforcement: client` or `both` is part of the **client manifest**, which is public by
design: its expression, the values of the parameters it reads, the functions it calls and its
messages in every locale can be read by any user. A rule with `enforcement: server` never leaves the
server, and the parameters and messages that only server rules use stay there with it.

**8.1 SHOULD** use `both`, the default, for every validation rule a user can fix in the user
interface. The browser gives feedback and the server decides.

**8.2 MUST** use `server`, never `client` or `both`, for a rule whose expression, parameters or
message reveal something users must not learn:

- sanction and embargo lists, and blocked persons or accounts;
- fraud and risk thresholds, scores and the conditions that trigger a review;
- internal limits, margins and prices that are not shown to the customer;
- anything computed from data of other customers or tenants.

```yaml
- id: org.transfer.blocked-country
  enforcement: server            # good: params.blockedCountries is never shipped to a browser
```

**8.3 MUST** use `server` for a rule that reads what a browser does not have or cannot be trusted
with: stored state the client was not sent, and facts the host adds to `ctx`. Action rules are
always `server`. [`SCHEMA_INVALID` for an action rule that is not]

**8.4 MUST NOT** use `client` for anything the business relies on. A `client` rule is never
evaluated on the server. Use it for state rules that drive the form and for hints.

**8.5 MUST** back a state effect with a validation rule when it matters. `required: true` and
`readOnly: true` tell the user interface how to present a field; they do not make the server refuse
anything.

```yaml
- id: customer.company-name.show-for-business      # presentation
  kind: state
  effects: [ { field: /companyName, set: { visible: true, required: true } } ]
- id: customer.company-name.required-for-business  # enforcement
  kind: validation
  assert: { op: not, args: [ { fn: isBlank, args: [ { var: data.companyName } ] } ] }
```

**8.6 MUST** remember that a parameter is as public as the most public rule that reads it. Before a
`both` rule reads a parameter, check that its value may be published.

**8.7 MUST** write the message of a `server` rule so that it can be shown: the finding is returned
to the caller. Say what is refused, not how it was detected.

**8.8 MUST NOT** write a rule that trusts the caller for who is acting. `actor` is set by the host
from its own authentication; a rule reads `actor.id` and `actor.roles` and nothing in `data` that
claims a role.

## 9. Messages and localisation

**9.1 MUST** give every finding a message in the default locale and an entry in `finding.args` for
every `{placeholder}`. [`MESSAGE_MISSING`, `MESSAGE_ARG_MISSING`]

**9.2 MUST** write messages for the person who has to act: what to do, in their words, as a whole
sentence. No field paths, rule ids, codes or pattern syntax.

```yaml
phone.e164: "Enter the phone number with its country code, for example +14155550100."   # good
phone.e164: "phone does not match ^\\+[1-9][0-9]{7,14}$"                                # bad
```

**9.3 MUST** take every value that can change from an argument, never from the text.

```yaml
transfer.amountOverLimit: "Amount exceeds the single-transfer limit of {limit}."   # good
transfer.amountOverLimit: "Amount exceeds the single-transfer limit of 25000."     # bad
```

**9.4 SHOULD** keep formatted numbers out of messages or format them in the user interface. A number
argument is rendered in plain decimal notation, without separators or a currency: `50000`.

**9.5 MUST** provide a catalog for every locale tag the host sends. A request locale selects the
catalog with exactly that tag and otherwise falls back to the default locale: `fr-CA` does not fall
back to `fr`. A key that is missing in a locale falls back to the default locale without an error.

**9.6 SHOULD** translate every key of the default locale in every other locale, and add a golden
test with `locale` for at least one message with arguments per locale.

**9.7 MUST NOT** put personal data of anyone but the user, or anything secret, into a message
argument. Arguments are echoed to the caller.

## 10. Golden tests

**10.1 MUST** add, for every validation rule, a test in which it fires, and **SHOULD** add one at
the boundary in which it does not (the limit itself and the first value beyond it).

**10.2 MUST** test every state a rule with a resolution can be in: open, resolved, and for an
acceptance also an actor without the role and a request without a justification.

**10.3 MUST** test every `server` rule on both channels: denied on the server, and absent with
`channel: client`.

**10.4 MUST** test every override in the ruleset that makes it: the tightened limit, the raised
severity.

**10.5 MUST** list in `expect.findings` every finding the request produces. The set of rules is
compared exactly; the members listed for a finding are compared with a finding of that rule;
`effects` is a subset match; `commands` is the exact list of names.

**10.6 MUST** pass `ctx.now` in every test of a rule that reads it, and **SHOULD** assert the
rendered `message` of every rule that has arguments.

**10.7 SHOULD** name a test as a sentence that states the behaviour:
`large transfer blocks until the warning is acknowledged`.

**10.8 MUST** test rules that call your own custom operators in the test suite of each host, with
the operators registered. `rulecheck check` and `rcas check` register only the three
operators of the conformance suite, so under them such a rule fails closed.

**10.9 MUST** keep the tests of a derived ruleset in a file of their own and attach them with
`rulecheck derive --tests` ([OpenAPI](openapi.md)): the ruleset file is regenerated.

## 11. YAML portability

**11.1 MUST** quote every scalar that YAML 1.1 and YAML 1.2 parsers read differently.
[`YAML_NOT_PORTABLE`]

```yaml
default: [KP, IR, "NO"]         # good: unquoted NO is false to a YAML 1.1 parser
now: "2026-10-03"               # good: unquoted, a YAML 1.1 parser makes it a date
labels: { tier: "1" }           # good: a string, as the schema requires
code: 012                       # bad: ten or twelve, depending on the parser
enabled: yes                    # bad: write true
```

The scalars to quote include `yes`, `no`, `on`, `off` in any case, numbers with a leading zero, with
`_` or with an exponent (`012`, `1_000`, `1e3`), times (`12:30`) and dates.

**11.2 MUST NOT** use anchors, aliases, merge keys (`<<`), tags, several documents in one file, or
the same key twice in a mapping. [`YAML_NOT_PORTABLE`; a file with a repeated key is not read]

**11.3 MUST** save files as UTF-8 without a byte order mark. [`YAML_NOT_PORTABLE`]

**11.4 SHOULD** quote message texts and patterns always, and write short expressions in flow style
on one line and long ones in block style, as the examples do.

**11.5 SHOULD** group rules under comment headings and keep one rule per block, in the order
validation, state, compute, action.

## 12. Reviewing and governance

**12.1 MUST** change rulesets through pull requests. CI runs the linter and the golden tests on
every one, and `.github/CODEOWNERS` routes a ruleset to the team that owns the risk it controls.

**12.2 MUST** choose the override policy of a rule or parameter deliberately:

| Policy | Children may | Use for |
|---|---|---|
| `locked` | Change nothing | Law, sanctions, segregation of duties, integrity |
| `tighten-only` | Rules: raise the severity, require an acknowledgement, withdraw an acceptance. Parameters: move the value in `tightenDirection` | Limits and controls that lower levels may make stricter. The default for rules |
| `open` | Rules: change severity, acknowledgement, acceptance and `enabled`. Parameters: any value of the type | Defaults and recommendations. The default for parameters |

**12.3 MUST** review a ruleset change as a change to what the business permits. The reviewer checks:

- the version bump (1.5) and that no id or code is reused (1.6);
- `enforcement` of every new or changed rule and of the parameters it reads (section 8);
- every override: its `reason`, and that the parent's owner agrees when a policy was opened;
- acceptance roles (7.3) and that no rule of 7.5 can be accepted;
- golden tests for the new behaviour (section 10), including the client channel;
- messages (section 9) in every locale.

**12.4 MUST** build bundles in CI from the reviewed commit, name them `<ruleset id>.bundle.json`,
store them immutably and never edit one. A bundle contains the server manifest: it is deployed to
backends and never served to a browser.

**12.5 MUST NOT** edit a generated file: a derived ruleset (`rulecheck derive`), a fixture or a
bundle under `conformance/`, or a client manifest. Change the source and regenerate.

**12.6 SHOULD** retire a rule in the ruleset that defines it, by removing it in a major version, or
by `enabled: false` in an override where the parent marked it `open`. Set `metadata.status` to
`deprecated` before a ruleset is withdrawn.
