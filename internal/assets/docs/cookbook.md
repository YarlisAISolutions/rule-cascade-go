# Rule cookbook: by data type, by place, by severity

A reference to copy from. Every recipe is a rule of one of the two example rulesets, shown as YAML
and followed by a request and the result the engine returns for it.

- [Conventions](#conventions)
- [1. How to read a rule](#1-how-to-read-a-rule)
- [2. Where a rule applies](#2-where-a-rule-applies)
- [3. Severity](#3-severity)
- [4. Recipes by data type](#4-recipes-by-data-type)
- [5. Rules by data type](#5-rules-by-data-type)
- [6. Reusable logic: functions and parameters](#6-reusable-logic-functions-and-parameters)
- [7. Custom rules](#7-custom-rules)
- [8. Field state, computed values, stored state and actions](#8-field-state-computed-values-stored-state-and-actions)
- [9. Golden tests](#9-golden-tests)
- [10. Operator reference](#10-operator-reference)

How to wire rules into a UI and an API is the subject of the
[enforcement guide](enforcement-guide.md); what a well-written ruleset must and should do is in the
[authoring guidelines](authoring-guidelines.md). The normative text is the
[specification](../spec/v1/SPECIFICATION.md).

## Conventions

**Sources.** Rules on the entity `Customer` come from the rule catalog,
[`customer-onboarding.ruleset.yaml`](../examples/catalog/customer-onboarding.ruleset.yaml). Rules on
`Transfer` come from the payments example,
[`payments-transfer.ruleset.yaml`](../examples/contracts/payments-transfer.ruleset.yaml), and its
parent [`acme-org-base.ruleset.yaml`](../examples/contracts/acme-org-base.ruleset.yaml).

**Recipes.** Section 1 shows one rule in full. After that a recipe shows the `id` and `title` of the
rule and the members that carry its logic: `when`, `assert`, `severity` and whatever is special
about it. The members every rule has (`kind`, `target`, `operations`, `triggers`, `finding`) are in
the example file under the same `id`; a target, where one is shown, leaves out the place. A rule
without a `kind` line is a validation rule. Three recipes marked **Variant** are not in the example
files and are shown in full; they were checked in a ruleset that extends the catalog.

**Examples.** An example is a request line and, after `=>`, the result of the reference
implementation for it:

```text
Customer create | data: {"fullName": "   "}
=> deny  ONB-STR-001 error blocking /fullName "Enter your full name."
Customer create | data: {"email": "maya(at)example.com", "backupEmail": "Maya@Example.com"}
=> deny
   ONB-TYP-001 error blocking /email "Enter a valid e-mail address."
   ONB-TYP-002 info not blocking /backupEmail "We will store this address as maya@example.com."
```

| Part of the request line | Meaning |
|---|---|
| `Customer create` | The entity and the operation |
| `data: {...}` | The base record below with these members replaced (a JSON Merge Patch: `null` removes a member) |
| `actor`, `resolutions`, `view`, `trigger`, `locale`, `original` | The request members of the same name |
| `channel: client` | Evaluated against the client manifest. Otherwise the server manifest |
| `ruleset: variants`, `ruleset: org` | Evaluated against the variants ruleset or against `acme.org.base`, not the ruleset of the entity |
| `show: effects`, `show: commands` | The result also lists the effects or the commands |

The result is the decision and one line per finding: code, severity, whether it blocks, the status
when it is not `open`, the fields it points at, the message, and in parentheses what the user may
still do. Every request has `ctx.now` = `2026-10-03T12:00:00Z`. For an operation other than
`create`, `original` is the base record as stored. The custom operator `x-luhn` is registered.

The base records are valid: on their own they produce no finding. The stored transfer also has
`"status": "draft"` and `"createdBy": "u-1"`.

```json
{ "id": "c-1", "fullName": "Maya Okafor", "email": "maya@example.com", "phone": "+14155550100",
  "accountType": "personal", "country": "US", "postalCode": "27502", "dateOfBirth": "1990-04-12",
  "annualIncome": 80000, "creditLimit": 10000, "dependents": 2, "termsAccepted": true,
  "interests": ["travel", "cooking"],
  "addresses": [{ "kind": "home", "line1": "1 Main St", "city": "Apex", "primary": true }] }
```

```json
{ "id": "t-1", "type": "domestic", "amount": 120.5, "currency": "USD", "memo": "rent",
  "beneficiary": { "name": "Jo Lee", "country": "US" } }
```

To run a request of your own, write it to a file and give it to the command;
`--conformance-operators` registers `x-luhn`:

```console
$ rcas evaluate --bundle conformance/bundles/acme.onboarding.customer.bundle.json \
    --conformance-operators request.json
```

## 1. How to read a rule

```yaml
- id: customer.full-name.family-name-hint   # permanent: it appears in findings, logs and overrides
  kind: validation                          # validation, state, compute or action
  title: Suggest adding a family name       # for the people who read the ruleset
  target:                                   # what the rule is about, and where the UI shows it
    entity: Customer
    page: onboarding
    screen: profile
    section: identity
    component: name-input
    field: /fullName                        # JSON Pointer; the finding points at it
  operations: [create, update]              # the operations the rule takes part in
  triggers: [blur, submit]                  # client moments; the default is [submit]
  enforcement: client                       # client, server or both (the default)
  when:                                     # guard: the rule applies only when this is true
    op: not
    args: [ { fn: isBlank, args: [ { var: data.fullName } ] } ]
  assert:                                   # must be true; when it is false the finding is produced
    op: contains
    args: [ { op: trim, args: [ { var: data.fullName } ] }, " " ]
  severity: info                            # info, warning or error
  finding:
    code: ONB-STR-004                       # stable code for support, tests and analytics
    message: fullName.familyNameHint        # key into messages.<locale>
```

```text
Customer create | data: {"fullName": "Maya"} | channel: client | trigger: blur
=> allow
   ONB-STR-004 info not blocking /fullName "Add your family name so we can address you properly."
Customer create | data: {"fullName": "Maya"}
=> allow
```

The second request is a server evaluation. The rule has `enforcement: client`, so the API does not
repeat the hint.

| Member | Meaning | Default |
|---|---|---|
| `id` | Lower-case, dot-separated, never reused for another meaning | required |
| `kind` | See the next table | required |
| `target` | An `entity`, optionally a place and `field` or `fields`; or a data `type` (section 2) | required |
| `operations` | `create`, `read`, `update`, `delete`, `list` or any action name such as `approve` | required |
| `triggers` | `load`, `change`, `blur`, `submit`. Only client evaluations use them | `[submit]` |
| `enforcement` | Which manifest holds the rule: `client`, `server` or `both` | `both` |
| `when` | A boolean expression. When it is `false` the rule does nothing | applies always |
| `forEach` | Pointer to a list; the rule runs once per element, bound to `item` | |
| `priority` | Higher runs first within a kind | `0` |
| `overridePolicy` | What a child ruleset may change: `locked`, `tighten-only`, `open` | `tighten-only` |
| `enabled`, `tags`, `title`, `description` | Switch, labels and documentation | enabled |

| Kind | Body | Produces | Section |
|---|---|---|---|
| `validation` | `assert`, `severity`, `finding`; optional `acknowledgement`, `acceptance` | A finding when `assert` is `false` | 3, 4 |
| `state` | `when`, `effects` | `visible`, `enabled`, `required`, `readOnly` of a field while `when` is `true` | 8 |
| `compute` | `assign` | A value written into the data before validation | 8 |
| `action` | `commands`, `enforcement: server` | Commands, after an allowed server evaluation | 8 |
| `x-<name>` | anything | Nothing. Reserved for extensions | 7 |

An **expression** is a literal, a variable `{ var: <root>.<path> }`, an operator call
`{ op: <name>, args: [...] }` or a function call `{ fn: <name>, args: [...] }`. Nothing is
converted: `1` is not `"1"`. A path that does not exist is `null`. An expression that cannot be
evaluated (a string where a number is required, an invalid date) makes the rule **fail closed**: it
produces a blocking finding with the code `RULE-EVALUATION-ERROR`.

| Root | Bound to | Available in |
|---|---|---|
| `data` | The proposed state of the entity | Every rule |
| `original` | The stored state; `null` on `create` | Every rule that does not apply to `create` |
| `actor` | Who is acting: `actor.id`, `actor.roles` | Every rule |
| `ctx` | Facts the host passes in, such as `ctx.now` | Every rule |
| `params` | The parameters of the ruleset | Every rule |
| `item` | The current element | A rule with `forEach`; the second argument of `all`, `some`, `none`, `sum`, `map`, `filter` |
| `value`, `field` | The value and the pointer of the current field | A rule that targets a `type` |
| `arg` | The arguments of the call | A function body |

Every message a finding names must exist under `messages` in the default locale, and every
`{placeholder}` of a message needs an entry in `finding.args`.

```yaml
messages:
  en:
    fullName.required: "Enter your full name."
    fullName.length: "Your name must have 2 to 80 characters; it has {length}."
  es:
    fullName.required: "Introduce tu nombre completo."
```

A request names its language in `locale`. The catalogs are read from the most to the least
specific: the requested tag, the tag without its last part, and so on, then the default locale.
`es-MX` therefore gets the `es` text here, and a key that `es` does not translate gets the `en`
text. Tags are compared exactly, including case.

```text
Customer create | data: {"fullName": "   "} | locale: es-MX
=> deny  ONB-STR-001 error blocking /fullName "Introduce tu nombre completo."
Customer create | data: {"fullName": " M "} | locale: es-MX
=> deny  ONB-STR-002 error blocking /fullName "Your name must have 2 to 80 characters; it has 1."
```

## 2. Where a rule applies

A rule is addressed on two axes: who owns it, and what it is about.

### Who owns it: `scope`

The scope belongs to the ruleset, not to a rule. It lists levels from the least to the most
specific. The level names are open; the recommended ones are `enterprise`, `organization`,
`businessUnit`, `agency`, `project`, `application`, `module` and `feature`.

One ruleset sits at one point of the hierarchy. A ruleset at a more specific point inherits from one
less specific ruleset with `extends`, and adjusts what it inherits with `overrides`:

```yaml
scope:
  - { level: enterprise, id: acme-group }
  - { level: organization, id: acme }
  - { level: businessUnit, id: retail-banking }
  - { level: application, id: payments-hub }
  - { level: feature, id: transfers }

extends:
  - { ruleset: acme.org.base, version: ^1.2.0 }

overrides:
  params:
    maxTransferAmount: 25000        # retail tightens the org limit of 50000
  rules:
    - rule: org.transfer.memo-recommended
      set: { severity: warning }
      reason: Retail operations needs memos for dispute handling.
```

The same request against the organisation's ruleset and against the feature's ruleset:

```text
Transfer create | data: {"amount": 30000, "memo": ""} | ruleset: org
=> allow
   ORG-TRF-003 info not blocking /memo "Adding a memo makes this transfer easier to reconcile."
Transfer create | data: {"amount": 30000, "memo": ""}
=> deny
   ORG-TRF-002 error blocking /amount (may accept-risk by risk-officer)
      "Amount exceeds the single-transfer limit of 25000."
   ORG-TRF-003 warning not blocking /memo "Adding a memo makes this transfer easier to reconcile."
   PAY-TRF-003 warning blocking /amount,/beneficiary/name (may acknowledge)
      "This is a large transfer to Jo Lee. Please confirm the details."
```

In the feature's ruleset the limit is 25000 and the memo rule is a warning; the third finding comes
from a rule the feature adds. The child inherits params, rules, entities, types, functions,
operators and messages. Its scope must start with the parent's scope and add at least one level.
What it may change is decided by the parent, per parameter and per rule:

| `overridePolicy` | On a parameter (default `open`) | On a rule (default `tighten-only`) |
|---|---|---|
| `locked` | No override: `PARAM_LOCKED` | No override: `RULE_LOCKED` |
| `tighten-only` | Only in `tightenDirection` (`lower` or `higher`), numbers only; otherwise `PARAM_LOOSENED` | The severity may rise. Disabling the rule, lowering the severity, allowing an acceptance or removing a required acknowledgement is `RULE_LOOSENED` |
| `open` | Any value of the parameter's type | `severity`, `enabled`, `acknowledgement` and `acceptance` may change freely |

A rule override sets only those four members and must give a `reason`. A child adds rules of its
own, but cannot reuse an inherited rule id (`RULE_DUPLICATE`), redeclare a parameter
(`PARAM_REDEFINED`), redefine a function (`FUNCTION_REDEFINED`) or bind an inherited field to
another type (`FIELD_TYPE_REBOUND`). An override that is not permitted stops the load, for example
with `PARAM_LOOSENED: acme.payments.transfer: param maxTransferAmount may only move lower`.

Every finding names the ruleset that defined its rule in `source`, for example
`acme.org.base@1.2.0`.

### What it is about: `target`

| Target member | Narrows the rule to | Example |
|---|---|---|
| `entity` | A business object. Required unless `type` is given | `Customer` |
| `page`, `screen`, `section`, `component` | A logical place in the UI, in lower-case kebab-case. Not a framework class name | `onboarding`, `finance`, `credit`, `credit-limit-input` |
| `field` | One field, as a JSON Pointer. The finding points at it | `/creditLimit`, `/beneficiary/swiftCode` |
| `fields` | Several fields: a cross-field rule. The finding points at all of them | `[/creditLimit, /annualIncome]` |
| `type` | Every field bound to a data type (section 5). Without an `entity` it covers every entity that binds the type | `Money` |

The place is where the rule's findings belong: a finding carries it as `location`. A request can
ask for one place with `view`. A rule takes part when, for each of `page`, `screen`, `section` and
`component` that both the view and the rule name, the two are equal. A rule that names no place
takes part in every view; a request without a `view` evaluates every place.

```text
Customer create | channel: client | view: {"page": "onboarding", "screen": "finance"}
  | data: {"fullName": "", "annualIncome": 500000, "creditLimit": 70000, "discountPercent": 40}
=> deny
   ONB-NUM-002 error blocking /creditLimit (may accept-risk by credit-manager)
      "The credit limit cannot exceed 50000."
   ONB-NUM-004 info not blocking /discountPercent "Discounts above 30% are reviewed monthly."
Customer create | channel: client
  | data: {"fullName": "", "annualIncome": 500000, "creditLimit": 70000, "discountPercent": 40}
=> deny
   ONB-STR-001 error blocking /fullName "Enter your full name."
   ONB-NUM-002 error blocking /creditLimit (may accept-risk by credit-manager)
      "The credit limit cannot exceed 50000."
   ONB-NUM-004 info not blocking /discountPercent "Discounts above 30% are reviewed monthly."
```

### From the phrase to the YAML

| A rule "by ..." | Is written as | Where |
|---|---|---|
| enterprise, organization, business unit, agency | `- { level: organization, id: acme }` | `scope` of the ruleset |
| project | `- { level: project, id: customer-portal }` | `scope` |
| application | `- { level: application, id: payments-hub }` | `scope` |
| module | `- { level: module, id: onboarding }` | `scope` |
| feature | `- { level: feature, id: transfers }` | `scope` |
| type | `target: { type: Money }` with `fieldTypes: { /amount: Money }` on the entity | rule and entity |
| page | `target: { entity: Customer, page: onboarding }` | rule |
| screen | `target: { entity: Customer, page: onboarding, screen: finance }` | rule |
| section | `target: { entity: Customer, screen: finance, section: credit }` | rule |
| component | `target: { entity: Transfer, component: amount-panel }` | rule |
| field | `target: { entity: Customer, field: /fullName }` | rule |
| several fields | `target: { entity: Customer, fields: [/creditLimit, /annualIncome] }` | rule |
| element of a list | `forEach: /addresses` with `target: { entity: Customer, field: /city }` | rule |
| operation | `operations: [update]`, `operations: [approve]` | rule |
| channel | `enforcement: server` | rule |

Rules for another project, module or feature go into another ruleset. Rules for another page,
screen, section, component or field go into the same ruleset with another `target`.

## 3. Severity

| The rule | Result | Blocks the operation | What the user can do | Typical use |
|---|---|---|---|---|
| does not apply (`when` is `false`) or holds (`assert` is `true`) | No finding: the data is permissible | No | | |
| `severity: info` | An `info` finding | Never | Read it | A hint, a consequence of the input, a suggestion |
| `severity: warning` | A `warning` finding | No | Proceed, or change the data | An unusual value that is probably a mistake |
| `severity: warning` with `acknowledgement: required` | A `warning` finding with `resolution: acknowledge` | Until it is acknowledged | Confirm it, or change the data | A legitimate but risky value that needs a conscious confirmation |
| `severity: error` | An `error` finding | Yes | Change the data | Invalid data; a rule with no exception |
| `severity: error` with `acceptance.allowed` | An `error` finding with `resolution: accept-risk` | Until it is accepted | A user with one of `acceptance.roles` accepts the risk with a justification; anyone else changes the data | A limit with an exception process |
| cannot be evaluated | A finding with the code `RULE-EVALUATION-ERROR` | Always | Nothing. The data or the rule has to be corrected | Never intended: the engine fails closed |

The decision is `deny` while at least one finding is blocking. `acknowledgement` is allowed only on
a warning and `acceptance` only on an error.

**Info**, with a parameter in the message, and **warning**. Neither blocks.

```yaml
- id: customer.discount.high
  title: High discounts are reviewed
  when: { op: exists, args: [ { var: data.discountPercent } ] }
  assert: { op: lte, args: [ { var: data.discountPercent }, { var: params.highDiscountPercent } ] }
  severity: info
  finding:
    code: ONB-NUM-004
    message: discount.high                  # "Discounts above {threshold}% are reviewed monthly."
    args: { threshold: { var: params.highDiscountPercent } }

- id: customer.full-name.no-digits
  title: Names rarely contain digits
  when: { op: not, args: [ { fn: isBlank, args: [ { var: data.fullName } ] } ] }
  assert: { op: not, args: [ { op: matches, args: [ { var: data.fullName }, "[0-9]" ] } ] }
  severity: warning
```

```text
Customer create | data: {"discountPercent": 40}
=> allow  ONB-NUM-004 info not blocking /discountPercent "Discounts above 30% are reviewed monthly."
Customer create | data: {"fullName": "R2 Dee2"}
=> allow
   ONB-STR-003 warning not blocking /fullName
      "Your name contains digits. Check that it is spelled correctly."
```

**Warning that must be acknowledged.** It blocks until the request carries an `acknowledge`
resolution for the rule.

```yaml
- id: customer.credit-limit.income-ratio
  title: Credit limit compared with income (cross-field, must be acknowledged)
  target: { entity: Customer, fields: [/creditLimit, /annualIncome] }
  when:
    op: and
    args:
      - { op: exists, args: [ { var: data.creditLimit } ] }
      - { op: exists, args: [ { var: data.annualIncome } ] }
      - { op: gte, args: [ { var: data.annualIncome }, 0 ] }
  assert:
    op: lte
    args:
      - { var: data.creditLimit }
      - { op: mul, args: [ { var: data.annualIncome }, { var: params.maxCreditToIncomeRatio } ] }
  severity: warning
  acknowledgement: required
```

```text
Customer create | data: {"annualIncome": 30000, "creditLimit": 18000}
=> deny
   ONB-NUM-003 warning blocking /creditLimit,/annualIncome (may acknowledge)
      "This credit limit is high for the stated income. A limit up to 15000 needs no confirmation."
Customer create | data: {"annualIncome": 30000, "creditLimit": 18000}
  | resolutions: [{"rule": "customer.credit-limit.income-ratio", "type": "acknowledge"}]
=> allow
   ONB-NUM-003 warning not blocking acknowledged /creditLimit,/annualIncome
      "This credit limit is high for the stated income. A limit up to 15000 needs no confirmation."
```

**Error**, and **error that an authorised role may accept.** The second stops blocking when the
request carries an `accept-risk` resolution, the actor holds one of the roles, and a justification
is given (`justification: none` waives it). The accepted finding stays in the result, for the audit
log. `expiresAfter` is metadata for the host: the engine is stateless and does not track time, so a
host that stores acceptances applies the expiry.

```yaml
- id: customer.terms.accepted
  title: Terms must be accepted
  assert: { op: eq, args: [ { var: data.termsAccepted }, true ] }
  severity: error

- id: customer.credit-limit.ceiling
  title: Credit limit ceiling, with accept-with-risk
  when: { op: exists, args: [ { var: data.creditLimit } ] }
  assert: { op: lte, args: [ { var: data.creditLimit }, { var: params.maxCreditLimit } ] }
  severity: error
  acceptance:
    allowed: true
    roles: [credit-manager]
    justification: required
    expiresAfter: P30D
```

```text
Customer create | data: {"termsAccepted": false}
=> deny  ONB-BOO-001 error blocking /termsAccepted "Accept the terms to continue."
Customer create | data: {"annualIncome": 200000, "creditLimit": 60000}
=> deny
   ONB-NUM-002 error blocking /creditLimit (may accept-risk by credit-manager)
      "The credit limit cannot exceed 50000."
Customer create | data: {"annualIncome": 200000, "creditLimit": 60000}
  | actor: {"id": "u-7", "roles": ["credit-manager"]}
  | resolutions: [{"rule": "customer.credit-limit.ceiling", "type": "accept-risk",
      "justification": "long-standing client"}]
=> allow
   ONB-NUM-002 error not blocking accepted /creditLimit "The credit limit cannot exceed 50000."
Customer create | data: {"annualIncome": 200000, "creditLimit": 60000}
  | actor: {"id": "u-2", "roles": ["agent"]}
  | resolutions: [{"rule": "customer.credit-limit.ceiling", "type": "accept-risk",
      "justification": "please"}]
=> deny
   ONB-NUM-002 error blocking /creditLimit (may accept-risk by credit-manager)
      "The credit limit cannot exceed 50000."
```

## 4. Recipes by data type

Most rules guard themselves with `when`, so that an absent value produces no finding and only the
"required" rule speaks.

### String

Required and length. Forbidden characters are the warning of section 3
(`customer.full-name.no-digits`); a hint is the rule of section 1. `len` counts code points, so a
name with accents has the length a person would count.

```yaml
- id: customer.full-name.required
  title: Full name is required
  assert: { op: not, args: [ { fn: isBlank, args: [ { var: data.fullName } ] } ] }
  severity: error

- id: customer.full-name.length
  title: Full name has 2 to 80 characters
  when: { op: not, args: [ { fn: isBlank, args: [ { var: data.fullName } ] } ] }
  assert:
    op: between
    args: [ { op: len, args: [ { op: trim, args: [ { var: data.fullName } ] } ] }, 2, 80 ]
  severity: error
  finding:
    code: ONB-STR-002
    message: fullName.length                # "... it has {length}."
    args: { length: { op: len, args: [ { op: trim, args: [ { var: data.fullName } ] } ] } }
```

```text
Customer create | data: {"fullName": "   "}
=> deny  ONB-STR-001 error blocking /fullName "Enter your full name."
Customer create | data: {"fullName": " M "}
=> deny  ONB-STR-002 error blocking /fullName "Your name must have 2 to 80 characters; it has 1."
```

### E-mail and phone

The rules target a data type, so each runs for every field bound to it (section 5): `/email` and
`/backupEmail` are of type `Email`. Inside such a rule the field's value is `value`.

```yaml
- id: type.email.format
  title: E-mail addresses are well formed
  target: { type: Email }
  when: { op: exists, args: [ { var: value } ] }
  assert: { op: matches, args: [ { var: value }, "^[^@ ]+@[^@ ]+\\.[A-Za-z]{2,}$" ] }
  severity: error

- id: type.email.lower-case
  title: E-mail addresses are stored in lower case
  target: { type: Email }
  when: { op: eq, args: [ { op: typeOf, args: [ { var: value } ] }, string ] }
  assert: { op: eq, args: [ { var: value }, { op: lower, args: [ { var: value } ] } ] }
  severity: info

- id: type.phone.e164
  title: Phone numbers use the international format
  target: { type: PhoneNumber }
  when: { op: exists, args: [ { var: value } ] }
  assert: { op: matches, args: [ { var: value }, "^\\+[1-9][0-9]{7,14}$" ] }
  severity: error
```

```text
Customer create | data: {"email": "maya(at)example.com", "backupEmail": "Maya@Example.com"}
=> deny
   ONB-TYP-001 error blocking /email "Enter a valid e-mail address."
   ONB-TYP-002 info not blocking /backupEmail "We will store this address as maya@example.com."
Customer create | data: {"phone": "4155550100"}
=> deny
   ONB-TYP-003 error blocking /phone
      "Enter the phone number with its country code, for example +14155550100."
```

These rules are guarded by `exists`, and `exists` is `true` for `""`: only `null` and an absent
field do not exist. A form that sends an empty string for an optional field therefore gets the
format error. Send `null` for an empty input, or guard the rule with `isBlank` (section 6), as the
postal-code rule below does.

```text
Customer create | data: {"backupEmail": ""}
=> deny  ONB-TYP-001 error blocking /backupEmail "Enter a valid e-mail address."
Customer create | data: {"backupEmail": null}
=> allow
```

### Enumeration and conditional fields

A field that is required for one value of an enumeration, and a format that depends on another
field. The innermost `if` ends in `true`: a country without a rule passes. Membership of a list of
values is tested with `in` (see "List of scalars").

```yaml
- id: customer.company-name.required-for-business
  title: Business accounts name their company
  when: { op: eq, args: [ { var: data.accountType }, business ] }
  assert: { op: not, args: [ { fn: isBlank, args: [ { var: data.companyName } ] } ] }
  severity: error

- id: customer.postal-code.format
  title: Postal code format depends on the country
  target: { entity: Customer, fields: [/postalCode, /country] }
  when: { op: not, args: [ { fn: isBlank, args: [ { var: data.postalCode } ] } ] }
  assert:
    op: if
    args:
      - { op: eq, args: [ { var: data.country }, US ] }
      - { op: matches, args: [ { var: data.postalCode }, "^[0-9]{5}(-[0-9]{4})?$" ] }
      - op: if
        args:
          - { op: eq, args: [ { var: data.country }, CA ] }
          - op: matches
            args: [ { var: data.postalCode }, "^[A-Z][0-9][A-Z] ?[0-9][A-Z][0-9]$" ]
          - op: if
            args:
              - { op: eq, args: [ { var: data.country }, IN ] }
              - { op: matches, args: [ { var: data.postalCode }, "^[1-9][0-9]{5}$" ] }
              - true
  severity: error
```

```text
Customer create | data: {"accountType": "business"}
=> deny
   ONB-ENM-001 error blocking /companyName "Enter the company name for a business account."
   ONB-OBJ-003 info not blocking /addresses "Business accounts usually add a work address."
Customer create | data: {"country": "IN", "postalCode": "27502-1"}
=> deny  ONB-ENM-002 error blocking /postalCode,/country "This is not a valid postal code for IN."
Customer create | data: {"country": "GB", "postalCode": "SW1A 1AA"}
=> allow
```

### Number and integer

A whole number in a range: `mod(x, 1) = 0` is the test for a whole number. A value of the wrong JSON
type is not converted, so the rule fails closed. A ratio between two numbers is the acknowledged
warning of section 3 (`customer.credit-limit.income-ratio`).

```yaml
- id: customer.dependents.whole-number
  title: Dependents is a whole number from 0 to 20
  when: { op: exists, args: [ { var: data.dependents } ] }
  assert:
    op: and
    args:
      - { op: eq, args: [ { op: mod, args: [ { var: data.dependents }, 1 ] }, 0 ] }
      - { op: between, args: [ { var: data.dependents }, 0, 20 ] }
  severity: error
```

```text
Customer create | data: {"dependents": 1.5}
=> deny  ONB-NUM-001 error blocking /dependents "Dependents must be a whole number from 0 to 20."
Customer create | data: {"dependents": 21}
=> deny  ONB-NUM-001 error blocking /dependents "Dependents must be a whole number from 0 to 20."
Customer create | data: {"dependents": "2"}
=> deny
   RULE-EVALUATION-ERROR error blocking (rule customer.dependents.whole-number)
      "This rule could not be evaluated."
```

### Money, decimals and percentage

Arithmetic is decimal: `10.005` is exactly that, and `round` rounds half to even. A limit that comes
from a parameter is the accepted error of section 3 (`customer.credit-limit.ceiling`); an
informational threshold on a percentage is `customer.discount.high`.

```yaml
- id: type.money.not-negative
  title: Amounts are never negative
  target: { type: Money }
  when: { op: exists, args: [ { var: value } ] }
  assert: { op: gte, args: [ { var: value }, 0 ] }
  severity: error

- id: type.money.two-decimals
  title: Amounts have at most two decimals
  target: { type: Money }
  when: { op: eq, args: [ { op: typeOf, args: [ { var: value } ] }, number ] }
  assert: { op: eq, args: [ { op: round, args: [ { var: value }, 2 ] }, { var: value } ] }
  severity: error

- id: type.percentage.range
  title: Percentages lie between 0 and 100
  target: { type: Percentage }
  when: { op: exists, args: [ { var: value } ] }
  assert: { op: between, args: [ { var: value }, 0, 100 ] }
  severity: error
```

```text
Customer create | data: {"creditLimit": -50}
=> deny  ONB-TYP-004 error blocking /creditLimit "Amounts cannot be negative."
Customer create | data: {"creditLimit": 10.005}
=> deny  ONB-TYP-005 error blocking /creditLimit "Amounts can have at most two decimals."
Customer create | data: {"discountPercent": 101}
=> deny
   ONB-TYP-006 error blocking /discountPercent "Enter a percentage from 0 to 100."
   ONB-NUM-004 info not blocking /discountPercent "Discounts above 30% are reviewed monthly."
```

### Boolean

"Must be true" is the error of section 3 (`customer.terms.accepted`): compare with `true`, so that
`false` and an absent value both fail. A boolean that makes another field necessary:

```yaml
- id: customer.marketing.needs-phone
  title: Marketing messages go to the phone number
  target: { entity: Customer, fields: [/marketingOptIn, /phone] }
  when: { op: eq, args: [ { var: data.marketingOptIn }, true ] }
  assert: { op: not, args: [ { fn: isBlank, args: [ { var: data.phone } ] } ] }
  severity: warning
```

```text
Customer create | data: {"marketingOptIn": true, "phone": null}
=> allow
   ONB-BOO-002 warning not blocking /marketingOptIn,/phone
      "Add a phone number to receive offers, or switch offers off."
```

### Date

Check the format first and let the other rules apply only to well-formed dates. Ages are counted
with `yearsBetween` (inside the function `ageOn`, section 6) against `ctx.now`, which the host
supplies: the engine never reads a clock. `daysBetween` gives a distance in days.

```yaml
- id: customer.date-of-birth.format
  title: Date of birth is a calendar date
  when: { op: exists, args: [ { var: data.dateOfBirth } ] }
  assert: { fn: isIsoDate, args: [ { var: data.dateOfBirth } ] }
  severity: error

- id: customer.date-of-birth.minimum-age
  title: Customers are adults
  when: { fn: isIsoDate, args: [ { var: data.dateOfBirth } ] }
  assert:
    op: gte
    args:
      - { fn: ageOn, args: [ { var: data.dateOfBirth }, { var: ctx.now } ] }
      - { var: params.minimumAge }
  severity: error

- id: customer.date-of-birth.plausible
  title: Very old birth dates are usually typing mistakes
  when: { fn: isIsoDate, args: [ { var: data.dateOfBirth } ] }
  assert:
    op: lte
    args:
      - { fn: ageOn, args: [ { var: data.dateOfBirth }, { var: ctx.now } ] }
      - { var: params.maximumAge }
  severity: warning
```

```text
Customer create | data: {"dateOfBirth": "12/04/1990"}
=> deny  ONB-DAT-001 error blocking /dateOfBirth "Enter the date of birth as YYYY-MM-DD."
Customer create | data: {"dateOfBirth": "2008-10-04"}
=> deny  ONB-DAT-002 error blocking /dateOfBirth "You must be at least 18 years old to register."
Customer create | data: {"dateOfBirth": "2008-10-03"}
=> allow
Customer create | data: {"dateOfBirth": "1900-01-01"}
=> allow  ONB-DAT-003 warning not blocking /dateOfBirth "Check the year of birth."
Customer create | data: {"dateOfBirth": "1990-02-30"}
=> deny
   RULE-EVALUATION-ERROR error blocking (rule customer.date-of-birth.minimum-age)
      "This rule could not be evaluated."
   RULE-EVALUATION-ERROR error blocking (rule customer.date-of-birth.plausible)
      "This rule could not be evaluated."
```

The last request has the shape of a date but names a day that does not exist. The date operators
refuse it, so the two age rules fail closed. A request without `ctx.now` fails the same way.

### List of scalars

Size and "no blank entry". `coalesce` turns an absent list into an empty one for `len`; `none`
treats an absent list as empty by itself.

```yaml
- id: customer.interests.limit
  title: A limited number of interests
  assert:
    op: lte
    args:
      - op: len
        args: [ { op: coalesce, args: [ { var: data.interests }, { op: list, args: [] } ] } ]
      - { var: params.maxInterests }
  severity: error

- id: customer.interests.no-blanks
  title: Interests are not blank
  assert: { op: none, args: [ { var: data.interests }, { fn: isBlank, args: [ { var: item } ] } ] }
  severity: error
```

```text
Customer create | data: {"interests": ["a", "b", "c", " ", "e", "f"]}
=> deny
   ONB-LST-001 error blocking /interests "Choose at most 5 interests."
   ONB-LST-002 error blocking /interests "Remove the empty interest."
Customer create | data: {"interests": null}
=> allow
```

**Variant**: every entry comes from a list of allowed values held in a parameter
(`knownInterests`, a `stringList` with `[travel, cooking, music, sport, reading]`). `filter` puts
the offending entries into the message "We do not know these interests yet: {unknown}."

```yaml
- id: customer.interests.known
  kind: validation
  title: Interests come from the known list
  target: { entity: Customer, page: onboarding, screen: profile, section: interests,
            component: interest-chips, field: /interests }
  operations: [create, update]
  triggers: [change, submit]
  assert:
    op: all
    args:
      - { var: data.interests }
      - { op: in, args: [ { var: item }, { var: params.knownInterests } ] }
  severity: warning
  finding:
    code: ONB-VAR-001
    message: interests.known
    args:
      unknown:
        op: filter
        args:
          - { var: data.interests }
          - op: not
            args: [ { op: in, args: [ { var: item }, { var: params.knownInterests } ] } ]
```

```text
Customer create | data: {"interests": ["travel", "chess", "golf"]} | ruleset: variants
=> allow
   ONB-VAR-001 warning not blocking /interests "We do not know these interests yet: chess, golf."
```

### List of objects

Three shapes: a condition on the list as a whole ("exactly one"), a rule that runs once per element
with `forEach`, and "at least one" with `some`. With `forEach` the element is `item`, the target
field is relative to the element, and the finding points at the element by its index. `forEach`
reaches one level of a list. A total over the elements is written with `sum`.

```yaml
- id: customer.addresses.one-primary
  title: Exactly one primary address
  when: { op: not, args: [ { op: empty, args: [ { var: data.addresses } ] } ] }
  assert:
    op: eq
    args:
      - op: len
        args:
          - op: filter
            args: [ { var: data.addresses }, { op: eq, args: [ { var: item.primary }, true ] } ]
      - 1
  severity: error

- id: customer.address.city-required
  title: Every address names a city (evaluated per element)
  target: { entity: Customer, field: /city }
  forEach: /addresses
  assert: { op: not, args: [ { fn: isBlank, args: [ { var: item.city } ] } ] }
  severity: error

- id: customer.addresses.work-address-hint
  title: Business accounts usually have a work address
  when: { op: eq, args: [ { var: data.accountType }, business ] }
  assert:
    op: some
    args: [ { var: data.addresses }, { op: eq, args: [ { var: item.kind }, work ] } ]
  severity: info
```

```text
Customer create
  | data: {"addresses": [{"city": "Leeds", "primary": true}, {"city": "", "primary": true}]}
=> deny
   ONB-OBJ-001 error blocking /addresses "Mark exactly one address as primary."
   ONB-OBJ-002 error blocking /addresses/1/city "Enter the city."
Customer create | data: {"accountType": "business", "companyName": "Okafor Ltd"}
=> allow  ONB-OBJ-003 info not blocking /addresses "Business accounts usually add a work address."
```

### Object and nested fields

A nested field is a longer pointer and a longer path: `/beneficiary/swiftCode` and
`data.beneficiary.swiftCode`. A path through an absent object is `null`, not an error. The second
rule is on the server only: its parameter, the blocked list, never reaches a browser.

```yaml
- id: transfer.swift.required-international
  title: SWIFT/BIC is required for international transfers
  target: { entity: Transfer, field: /beneficiary/swiftCode }
  when: { op: eq, args: [ { var: data.type }, international ] }
  assert:
    op: and
    args:
      - { op: not, args: [ { op: empty, args: [ { var: data.beneficiary.swiftCode } ] } ] }
      - op: matches
        args: [ { var: data.beneficiary.swiftCode }, "^[A-Z]{6}[A-Z0-9]{2}([A-Z0-9]{3})?$" ]
  severity: error

- id: org.transfer.blocked-country
  title: No transfers to blocked countries
  target: { entity: Transfer, field: /beneficiary/country }
  enforcement: server
  overridePolicy: locked
  assert:
    op: not
    args:
      - { op: in, args: [ { var: data.beneficiary.country }, { var: params.blockedCountries } ] }
  severity: error
```

```text
Transfer create | data: {"type": "international", "beneficiary": {"country": "ES"}}
=> deny
   PAY-TRF-002 error blocking /beneficiary/swiftCode
      "A valid SWIFT/BIC code is required for international transfers."
Transfer create | data: {"beneficiary": {"country": "KP"}}
=> deny  ORG-TRF-001 error blocking /beneficiary/country "Transfers to KP are not permitted."
Transfer create | data: {"beneficiary": {"country": "KP"}} | channel: client
=> allow
```

### Identifiers

Send identifiers as strings, and check their shape with `matches` or a custom operator (section 7).
A JSON number is the double nearest to what was written, so two long numeric ids can be the same
number, and a number that leaves an expression has 15 significant digits. A rule that expects text
fails closed when it is given a number:

```text
Customer create | data: {"loyaltyNumber": 79927398713}
=> deny
   RULE-EVALUATION-ERROR error blocking (rule customer.loyalty-number.check-digit)
      "This rule could not be evaluated."
```

To report the wrong type as a finding of its own, test the type first:
`{ op: eq, args: [ { op: typeOf, args: [ { var: data.loyaltyNumber } ] }, string ] }`.

## 5. Rules by data type

A semantic type gives a rule to many fields at once. Declare the type, bind fields to it on the
entity, and target the type:

```yaml
entities:
  Customer:
    schema: { $ref: "./onboarding.openapi.yaml#/components/schemas/Customer" }
    fieldTypes:
      /email: Email
      /backupEmail: Email
      /phone: PhoneNumber
      /annualIncome: Money
      /creditLimit: Money
      /discountPercent: Percentage

types:
  Email: { base: string, description: An internet e-mail address. }
  PhoneNumber: { base: string, description: A phone number in E.164 format. }
  Money: { base: number, description: An amount in the account currency with at most two decimals. }
  Percentage: { base: number, description: A number from 0 to 100. }
```

A rule with `target: { type: Money }` (section 4) runs once for every field of the request's entity
that is bound to `Money`, in the order of the pointers. Inside the rule `value` is the field's value
(`null` when it is absent) and `field` is its pointer. The finding points at that field.

```text
Customer create | data: {"annualIncome": 80000.005, "creditLimit": 10.005}
=> deny
   ONB-TYP-005 error blocking /annualIncome "Amounts can have at most two decimals."
   ONB-TYP-005 error blocking /creditLimit "Amounts can have at most two decimals."
```

- A type has no behaviour of its own; `base` is documentation. The rules that target it give it one.
- Only validation rules can target a type, and they cannot use `forEach`.
- A new field gets every rule of the type by adding one line to `fieldTypes`.
- A child ruleset may bind more fields, but cannot bind an inherited pointer to another type
  (`FIELD_TYPE_REBOUND`). It cannot detach a field from the rules of its parent.
- A type rule without `entity` applies to every entity that binds the type.

## 6. Reusable logic: functions and parameters

### Functions

A function is a named expression with parameters. It is part of the ruleset, so every runtime
evaluates it identically.

```yaml
functions:
  isBlank:
    description: True for null and for strings that are empty or only spaces.
    params: [text]
    body:
      op: or
      args:
        - { op: not, args: [ { op: exists, args: [ { var: arg.text } ] } ] }
        - { op: eq, args: [ { op: trim, args: [ { var: arg.text } ] }, "" ] }
  isIsoDate:
    description: True for strings shaped like 2026-10-03.
    params: [text]
    body:
      op: and
      args:
        - { op: eq, args: [ { op: typeOf, args: [ { var: arg.text } ] }, string ] }
        - { op: matches, args: [ { var: arg.text }, "^[0-9]{4}-[0-9]{2}-[0-9]{2}$" ] }
  ageOn:
    description: Completed years between a birth date and a moment.
    params: [birthDate, moment]
    body: { op: yearsBetween, args: [ { var: arg.birthDate }, { var: arg.moment } ] }
```

A rule calls a function with `{ fn: isBlank, args: [ { var: data.fullName } ] }`. The functions
called on their own, with these arguments:

```text
fn isBlank ["   "]
=> true
fn isBlank [null]
=> true
fn isIsoDate ["12/04/1990"]
=> false
fn ageOn ["2008-10-04", "2026-10-03T12:00:00Z"]
=> 17
fn ageOn ["2008-10-03", "2026-10-03T12:00:00Z"]
=> 18
```

- The arguments are evaluated first, in the scope of the caller. The body reads them as
  `arg.<name>`.
- A body sees `data`, `original`, `actor`, `ctx` and `params`, but not the caller's `item`, `value`
  or `field`. Pass those as arguments, as `{ fn: isBlank, args: [ { var: item } ] }` does.
- The number of arguments must equal the number of parameters (`FUNCTION_ARITY`).
- A function cannot call itself, directly or through another function (`FUNCTION_RECURSIVE`).
- A child ruleset inherits the functions and cannot redefine one (`FUNCTION_REDEFINED`).

### Parameters

A parameter is a named, typed constant. Rules read it as `params.<name>`; a message shows it through
`finding.args`. Put every number or list that the business may want to change into a parameter.

```yaml
params:
  minimumAge:
    type: integer
    default: 18
    description: Youngest age at which a customer may register.
    overridePolicy: tighten-only
    tightenDirection: higher
  maxInterests:
    type: integer
    default: 5
```

| Member | Values |
|---|---|
| `type` | `string`, `number`, `integer`, `boolean`, `stringList`, `numberList` |
| `default` | The value, of that type (`PARAM_TYPE_MISMATCH` otherwise) |
| `overridePolicy` | `open` (the default), `tighten-only`, `locked` |
| `tightenDirection` | `lower` or `higher`. Required with `tighten-only`, which is for `number` and `integer` only |

A child ruleset changes an inherited parameter under `overrides.params`, never by declaring it
again. The organisation declares the transfer limit (first block), and the retail feature tightens
it (second block):

```yaml
params:
  maxTransferAmount:
    type: number
    default: 50000
    description: Largest single transfer without a risk acceptance.
    overridePolicy: tighten-only
    tightenDirection: lower
```

```yaml
overrides:
  params:
    maxTransferAmount: 25000        # retail tightens the org limit of 50000
```

The example of section 2 shows the effect: a transfer of 30000 passes the organisation's ruleset
and is stopped by the feature's. The rule `org.transfer.amount-limit` is the same in both; only the
value of the parameter differs, and the message follows it. An override of `75000` does not load:
`PARAM_LOOSENED: acme.payments.transfer: param maxTransferAmount may only move lower`.

The client manifest contains only the parameters its rules read. `blockedCountries`, read by a
server-only rule, never reaches a browser.

## 7. Custom rules

### Custom operators

When the core operators cannot express a check, the ruleset declares a custom operator and the host
application supplies it, in every runtime that evaluates the ruleset.

```yaml
operators:
  x-luhn:
    description: True when a string of digits ends in a valid Luhn check digit.
    args: 1
```

```yaml
- id: customer.loyalty-number.check-digit
  title: Loyalty number check digit
  when: { op: not, args: [ { fn: isBlank, args: [ { var: data.loyaltyNumber } ] } ] }
  assert: { op: x-luhn, args: [ { var: data.loyaltyNumber } ] }
  severity: error
```

```text
Customer create | data: {"loyaltyNumber": "79927398713"}
=> allow
Customer create | data: {"loyaltyNumber": "79927398710"}
=> deny
   ONB-CUS-001 error blocking /loyaltyNumber "This loyalty number is not valid. Check the digits."
Customer create | data: {"loyaltyNumber": "79927398710"} | operators: none | show: detail
=> deny
   RULE-EVALUATION-ERROR error blocking (rule customer.loyalty-number.check-digit)
      "This rule could not be evaluated."
      detail: custom operator x-luhn is not registered
```

The name starts with `x-` and must be declared under `operators` (`OPERATOR_UNDECLARED`). The
function must be pure and synchronous. It **fails closed**: a rule whose operator is not registered
(the third request), throws, or returns something JSON cannot carry produces a blocking finding.

| Runtime | The host function | Numbers arrive as | Registered by | Needed and not registered |
|---|---|---|---|---|
| TypeScript | `(...args: Json[]) => unknown` | `number` | The last argument of `evaluate`, `RuleSet.evaluate` and `useRuleEvaluation` | `rules.missingOperators(operators)` |
| Java | `CustomOperator`: `Object apply(List<Object> args)` | `BigDecimal` | `rules.withOperators(map)`, or the last argument of `Evaluator.evaluate` | `rules.missingOperators()` |
| Go | `func(args []any) (any, error)` | `float64` | The last argument of `Evaluate` | `rules.MissingOperators(operators)` |
| Python | `def op(*args)` | `int` or `float` | The last argument of `evaluate` | `rules.missing_operators(operators)` |

```ts
import { evaluate, type Operators } from '@rules-cascade/core';

const operators: Operators = {
  'x-luhn': (text) => {
    if (typeof text !== 'string' || !/^[0-9]{2,}$/.test(text)) return false;
    const digits = [...text].reverse().map(Number);
    const sum = digits.reduce(
      (total, d, i) => total + (i % 2 === 0 ? d : d * 2 > 9 ? d * 2 - 9 : d * 2), 0);
    return sum % 10 === 0;
  },
};

rules.missingOperators(operators);                        // []: check this when the host starts
rules.evaluate(request, 'server', operators);             // on the server
evaluate(rules.manifest('client'), request, operators);   // in a browser, and in the React hook
```

```java
Map<String, CustomOperator> operators = Map.of(
        "x-luhn", args -> args.get(0) instanceof String digits && luhn(digits));
RuleSet rules = RuleSet.fromBundle(bundle).withOperators(operators);
List<String> missing = rules.missingOperators();   // empty: check this when the host starts
EvaluationResult result = rules.evaluate(request);
```

```go
operators := rulecascade.Operators{
	"x-luhn": func(args []any) (any, error) {
		digits, ok := args[0].(string)
		return ok && luhn(digits), nil // func luhn(digits string) bool
	},
}
missing := rules.MissingOperators(operators) // empty: check this when the host starts
result, err := rules.Evaluate(request, "server", operators)
```

```python
def luhn(text):
    if not isinstance(text, str) or len(text) < 2 or not all("0" <= c <= "9" for c in text):
        return False
    total = 0
    for i, c in enumerate(reversed(text)):
        d = int(c) * (2 if i % 2 else 1)
        total += d - 9 if d > 9 else d
    return total % 10 == 0


operators = {"x-luhn": luhn}                          # the arguments arrive as positional arguments
rules.missing_operators(operators)                    # []: check this when the host starts
result = rules.evaluate(request, "server", operators)
```

In the Java and Go listings `luhn` is a function of the host with the logic of the TypeScript and
Python ones; complete implementations are in
[`CustomOperators.java`](../examples/backend-spring-boot/src/main/java/com/example/payments/CustomOperators.java)
and [`example_test.go`](../packages/go/example_test.go). Check at start-up that nothing is missing
([enforcement guide, step 4](enforcement-guide.md#step-4-enforce-on-the-backend)). The command
`rcas` and the rule server have no operators of yours: custom operators cannot cross a
process boundary. The engine protocol says so when a ruleset is loaded: the answer to `load` lists
them as `missingOperators`.

**Prefer a function.** A function is written once, in the ruleset, and behaves identically
everywhere. A custom operator is code that must be written, and kept identical, once per runtime:
the four implementations above have to agree on every input, including a number, a string with a
letter and digits outside ASCII. Use a custom operator only for what expressions cannot do; here, a
loop over the digits of a string.

### Extension points

The ruleset and most of its objects (metadata, entities, types, params, functions, operators, rules,
targets, findings, acceptances, commands and bindings) accept members whose names start with `x-`.
Runtimes preserve them and give them no meaning; the `x-` members of a rule travel with the rule in
the manifest. `rulecheck derive`, for example, records where a rule came from in
`x-generated-from`. A rule whose `kind` starts with `x-` is carried and ignored by every conforming
runtime: it produces nothing, and your own tooling reads it from the manifest. **Variant**:

```yaml
- id: customer.retention.seven-years
  kind: x-acme-retention
  title: Customer records are kept for seven years after closure
  target: { entity: Customer }
  operations: [delete]
  enforcement: server
  x-acme-keep: P7Y
  x-acme-jira: RISK-1432
```

Without `enforcement: server` the rule would have the default, `both`, and be part of the client
manifest.

## 8. Field state, computed values, stored state and actions

### Field state

A state rule sets `visible`, `enabled`, `required` or `readOnly` on a field while its `when` is
`true`. When `when` is `false` there is no effect, and the field has the default the application
gives it.

```yaml
- id: customer.company-name.show-for-business
  kind: state
  triggers: [load, change]
  when: { op: eq, args: [ { var: data.accountType }, business ] }
  effects:
    - field: /companyName
      set: { visible: true, required: true }

- id: customer.credit-limit.read-only-for-agents
  kind: state
  triggers: [load]
  when:
    op: not
    args:
      - op: in
        args:
          - credit-manager
          - { op: coalesce, args: [ { var: actor.roles }, { op: list, args: [] } ] }
  effects:
    - field: /creditLimit
      set: { readOnly: true }
```

```text
Customer create | data: {"accountType": "business"} | channel: client | trigger: change
  | show: effects
=> allow
   effect state /companyName {"visible": true, "required": true} (customer.company-name.show-for-business)
Customer update | actor: {"id": "u-2", "roles": ["agent"]} | channel: client | trigger: load
  | show: effects
=> allow  effect state /creditLimit {"readOnly": true} (customer.credit-limit.read-only-for-agents)
Customer update | actor: {"id": "u-7", "roles": ["credit-manager"]} | channel: client
  | trigger: load | show: effects
=> allow
```

**Variant**: `enabled`.

```yaml
- id: customer.credit-limit.needs-income
  kind: state
  title: The credit limit input is disabled until the income is entered
  target: { entity: Customer, page: onboarding, screen: finance, section: credit,
            component: credit-limit-input, field: /creditLimit }
  operations: [create, update]
  triggers: [load, change]
  when: { op: not, args: [ { op: exists, args: [ { var: data.annualIncome } ] } ] }
  effects:
    - field: /creditLimit
      set: { enabled: false }
```

```text
Customer create | data: {"annualIncome": null, "creditLimit": null} | ruleset: variants
  | channel: client | trigger: load | show: effects
=> allow  effect state /creditLimit {"enabled": false} (customer.credit-limit.needs-income)
```

**A state rule guides the UI. It never protects data.** A caller that ignores the form is stopped
only by a validation rule, so the catalog pairs the read-only state above with a rule on the server:

```yaml
- id: customer.credit-limit.change-needs-manager
  title: Only credit managers change the credit limit
  operations: [update]
  enforcement: server
  when: { op: ne, args: [ { var: data.creditLimit }, { var: original.creditLimit } ] }
  assert:
    op: in
    args:
      - credit-manager
      - { op: coalesce, args: [ { var: actor.roles }, { op: list, args: [] } ] }
  severity: error
```

An agent sends a changed credit limit. The client evaluation shows the read-only state and no
finding, because the rule is not in the client manifest; the server evaluation denies.

```text
Customer update | data: {"creditLimit": 12000} | actor: {"id": "u-2", "roles": ["agent"]}
  | channel: client | show: effects
=> allow
   effect value /displayName = "Maya Okafor" (customer.display-name.default)
   effect state /creditLimit {"readOnly": true} (customer.credit-limit.read-only-for-agents)
Customer update | data: {"creditLimit": 12000} | actor: {"id": "u-2", "roles": ["agent"]}
  | show: effects
=> deny
   ONB-UPD-003 error blocking /creditLimit "Only a credit manager can change the credit limit."
   effect value /displayName = "Maya Okafor" (customer.display-name.default)
   effect value /riskTier = "standard" (customer.risk-tier.derive)
   effect state /creditLimit {"readOnly": true} (customer.credit-limit.read-only-for-agents)
```

Two state rules that set the same property of the same field to different values are a conflict: an
evaluation error, unless the ruleset has `conflictPolicy: priority` and the rules have different
priorities.

### Computed values

A compute rule writes values into the data before the validation rules run, and reports each as a
`value` effect. `mode: default` fills the field only when it is absent or `null`; `mode: always`
overwrites it.
The host stores the values of the server evaluation; the engine returns effects, not the changed
record.

```yaml
- id: customer.display-name.default
  kind: compute
  when: { op: eq, args: [ { op: typeOf, args: [ { var: data.fullName } ] }, string ] }
  assign:
    - field: /displayName
      mode: default
      value: { op: trim, args: [ { var: data.fullName } ] }

- id: transfer.fee.international
  kind: compute
  triggers: [change, submit]
  when:
    op: and
    args:
      - { op: eq, args: [ { var: data.type }, international ] }
      - { op: exists, args: [ { var: data.amount } ] }
  assign:
    - field: /fee
      mode: always
      value: { op: mul, args: [ { var: data.amount }, { var: params.internationalFeeRate } ] }
```

```text
Customer create | data: {"fullName": "  Maya Okafor "} | channel: client | show: effects
=> allow  effect value /displayName = "Maya Okafor" (customer.display-name.default)
Transfer create | show: effects
  | data: {"type": "international", "amount": 500, "fee": 0,
      "beneficiary": {"swiftCode": "BSCHESMM"}}
=> allow  effect value /fee = 7.5 (transfer.fee.international)
```

### Update and delete rules read `original`

`original` is the stored record. It is `null` on `create`, and a rule that applies to `create` may
not read it (`ORIGINAL_ON_CREATE`). The host loads it from its store.

```yaml
- id: customer.country.immutable
  title: Country cannot change after registration
  operations: [update]
  enforcement: server
  assert: { op: eq, args: [ { var: data.country }, { var: original.country } ] }
  severity: error

- id: customer.email.change-notice
  title: Changing the e-mail address triggers verification
  operations: [update]
  assert: { op: eq, args: [ { var: data.email }, { var: original.email } ] }
  severity: info

- id: transfer.delete.only-draft
  title: Only draft transfers can be deleted
  operations: [delete]
  enforcement: server
  assert: { op: eq, args: [ { var: original.status }, draft ] }
  severity: error
```

```text
Customer update | data: {"country": "CA", "postalCode": "K1A 0B1", "email": "new@example.com"}
=> deny
   ONB-UPD-001 error blocking /country "The country cannot be changed after registration."
   ONB-UPD-002 info not blocking /email "We will send a verification link to the new address."
Transfer delete | original: {"id": "t-1", "status": "submitted", "createdBy": "u-1"}
=> deny  PAY-TRF-006 error blocking "Only draft transfers can be deleted."
Transfer delete
=> allow
```

### Custom actions and read rules

An operation is any lower-case name. A rule listens to `approve` exactly as it listens to `update`,
and the API binds an endpoint to it. A state rule on `read` returns field state that the API
applies to its response.

```yaml
- id: transfer.approve.four-eyes
  title: The creator cannot approve their own transfer
  operations: [approve]
  enforcement: server
  assert: { op: ne, args: [ { var: actor.id }, { var: original.createdBy } ] }
  severity: error

- id: transfer.read.mask-for-non-owners
  kind: state
  title: Hide the internal risk score from non-risk staff
  operations: [read]
  when:
    op: not
    args:
      - op: in
        args:
          - risk-officer
          - { op: coalesce, args: [ { var: actor.roles }, { op: list, args: [] } ] }
  effects:
    - field: /riskScore
      set: { visible: false }
```

```text
Transfer approve | actor: {"id": "u-1", "roles": ["approver"]}
=> deny  PAY-TRF-007 error blocking "You cannot approve a transfer you created."
Transfer approve | actor: {"id": "u-2", "roles": ["approver"]}
=> allow
Transfer read | data: {"riskScore": 71} | actor: {"id": "u-1", "roles": ["teller"]} | show: effects
=> allow  effect state /riskScore {"visible": false} (transfer.read.mask-for-non-owners)
```

The engine does not know which operations exist. An operation that no rule lists, or an entity the
ruleset does not have, selects no rule and evaluates to `allow` with no findings, so a misspelt
name passes. Take the entity and the operation from the `x-rule-cascade` binding of the endpoint,
never from the caller.

```text
Transfer aprove | actor: {"id": "u-1", "roles": ["approver"]}
=> allow
```

### Actions

An action rule returns commands. It is always `enforcement: server`, and its commands are returned
only when the decision is `allow`. The engine executes nothing: the host runs each command after it
has stored the change, at most once per `idempotencyKey`.

```yaml
- id: transfer.created.notify-risk
  kind: action
  enforcement: server
  when:
    op: and
    args:
      - { op: exists, args: [ { var: data.amount } ] }
      - { op: gte, args: [ { var: data.amount }, { var: params.largeTransferThreshold } ] }
  commands:
    - name: risk.large-transfer-created
      type: event
      ref: com.acme.payments.transfer.large.v1
      payload:
        transferId: { var: data.id }
        amount: { var: data.amount }
      idempotencyKey: [ large-transfer, { var: data.id } ]
```

```text
Transfer create | data: {"amount": 12000} | show: commands
=> deny
   PAY-TRF-003 warning blocking /amount,/beneficiary/name (may acknowledge)
      "This is a large transfer to Jo Lee. Please confirm the details."
   no commands
Transfer create | data: {"amount": 12000} | show: commands
  | resolutions: [{"rule": "transfer.large.review-warning", "type": "acknowledge"}]
=> allow
   PAY-TRF-003 warning not blocking acknowledged /amount,/beneficiary/name
      "This is a large transfer to Jo Lee. Please confirm the details."
   command risk.large-transfer-created key=large-transfer:t-1 payload={"transferId": "t-1", "amount": 12000}
```

`type` is `event` or `operation`, and `ref` names the event type or the operation to call. The parts
of `idempotencyKey` are rendered and joined with `:`. Build the key from identifiers that are
strings, and include everything that makes the command distinct.

## 9. Golden tests

A golden test is a request with the result it must produce. Tests live in the ruleset under `tests`;
`check` runs them, and every runtime must pass them from the compiled bundle.

```yaml
- name: "number: a credit manager accepts a limit over the ceiling with a justification"
  entity: Customer
  operation: update
  given:
    data:
      fullName: Maya Okafor
      email: a@b.co
      country: GB
      annualIncome: 200000
      creditLimit: 60000
    original: { email: a@b.co, country: GB }
    actor: { id: u-7, roles: [credit-manager] }
    resolutions:
      - rule: customer.credit-limit.ceiling
        type: accept-risk
        justification: long-standing client
  expect:
    decision: allow
    findings:
      - { rule: customer.credit-limit.ceiling, severity: error, status: accepted, blocking: false }
    effects:
      - { type: value, field: /riskTier, value: review }
```

| Member | Meaning | Compared how |
|---|---|---|
| `entity`, `operation`, `given` | The request. `given` holds `data` (required), `original`, `actor`, `ctx`, `resolutions`, `locale`, `trigger`, `view` | |
| `channel` | `server` (the default) or `client` | |
| `expect.decision` | `allow` or `deny` | Exactly |
| `expect.findings` | The findings, each with at least `rule` | The rules of the findings must be exactly the rules listed, in any order. Each listed member (`code`, `severity`, `status`, `blocking`, `fields`, `message`, `location`) must equal that of a finding of the rule |
| `expect.effects` | Effects that must be present | Each listed effect must match one effect on the members it lists. Other effects are allowed |
| `expect.commands` | The names of the commands | Exactly, in order. Not compared when the member is absent |

Write at least these tests: one request that is allowed with no finding; for every blocking rule one
request that triggers it; both sides of every boundary (the day before an 18th birthday and the day
itself); each resolution, given and not given; and for a client-only or server-only rule, one test
per `channel`. Give `ctx.now` a fixed value. Quote every scalar that YAML could read as something
else, such as `"2008-10-04"` and `"27502"`.

## 10. Operator reference

All operators of the core profile. In the list group, `data.shares` is `[60, 40]` and `data.kinds`
is `["home", "work"]`. Every result was returned by the Python, TypeScript, Java and Go runtimes.

| Operator | Example | Result | Notes |
|---|---|---|---|
| **Logic** | | | |
| `and` | `{ op: and, args: [true, { op: gt, args: [2, 1] }] }` | `true` | Booleans only. Stops at the first `false`. With no arguments: `true` |
| `or` | `{ op: or, args: [false, { op: eq, args: [1, 2] }] }` | `false` | Booleans only. Stops at the first `true`. With no arguments: `false` |
| `not` | `{ op: not, args: [false] }` | `true` | A boolean; anything else is an evaluation error |
| `if` | `{ op: if, args: [{ op: gt, args: [5, 3] }, high, low] }` | `"high"` | Only the branch taken is evaluated |
| `coalesce` | `{ op: coalesce, args: [null, null, fallback] }` | `"fallback"` | The first argument that is not `null` |
| `exists` | `{ op: exists, args: [""] }` | `true` | `false` only for `null`; an absent path is `null` |
| `empty` | `{ op: empty, args: [""] }` | `true` | `true` for `null`, `""`, an empty list and an empty object |
| **Comparison** | | | |
| `eq` | `{ op: eq, args: [1, 1.0] }` | `true` | Deep equality. Values of different types are never equal |
| `ne` | `{ op: ne, args: ["1", 1] }` | `true` | The opposite of `eq` |
| `lt` | `{ op: lt, args: [2, 10] }` | `true` | Two numbers; anything else is an evaluation error |
| `lte` | `{ op: lte, args: [10, 10] }` | `true` |  |
| `gt` | `{ op: gt, args: [0.3, { op: add, args: [0.1, 0.2] }] }` | `false` | Arithmetic is decimal: 0.1 + 0.2 is exactly 0.3 |
| `gte` | `{ op: gte, args: [18, 18] }` | `true` |  |
| `between` | `{ op: between, args: [20, 0, 20] }` | `true` | Number, low, high; both bounds are included |
| `in` | `{ op: in, args: [CA, { op: list, args: [US, CA] }] }` | `true` | Value, list; compared as `eq` compares |
| **Numbers** | | | |
| `add` | `{ op: add, args: [0.1, 0.2] }` | `0.3` |  |
| `sub` | `{ op: sub, args: [10, 0.01] }` | `9.99` |  |
| `mul` | `{ op: mul, args: [19.99, 3] }` | `59.97` |  |
| `div` | `{ op: div, args: [1, 3] }` | `0.333333333333333` | 15 significant digits leave an expression. Division by zero is an error |
| `mod` | `{ op: mod, args: [-7, 2] }` | `-1` | The sign follows the first argument. `mod(x, 1) = 0` tests for a whole number |
| `abs` | `{ op: abs, args: [-4.5] }` | `4.5` |  |
| `min` | `{ op: min, args: [3, 1, 2] }` | `1` | One or more numbers |
| `max` | `{ op: max, args: [3, 1, 2] }` | `3` | One or more numbers |
| `round` | `{ op: round, args: [2.675, 2] }` | `2.68` | Half to even. Places 0 to 15, default 0 |
| **Strings** | | | |
| `len` | `{ op: len, args: ["héllo"] }` | `5` | Code points of a string, or elements of a list |
| `lower` | `{ op: lower, args: ["Maya@Example.COM"] }` | `"maya@example.com"` | Only the ASCII letters change |
| `upper` | `{ op: upper, args: ["k1a 0b1"] }` | `"K1A 0B1"` | Only the ASCII letters change |
| `trim` | `{ op: trim, args: ["  Maya  "] }` | `"Maya"` | Removes space, tab, line feed and carriage return at both ends |
| `concat` | `{ op: concat, args: [AB, "-", "12"] }` | `"AB-12"` | Strings only; convert other values with `text` |
| `substring` | `{ op: substring, args: [ABCDUS33, 4, 2] }` | `"US"` | String, start from zero, optional length; in code points |
| `text` | `{ op: text, args: [{ op: list, args: [1, true, x] }] }` | `"1, true, x"` | Any value, rendered as in a message |
| `startsWith` | `{ op: startsWith, args: ["+14155550100", "+1"] }` | `true` |  |
| `endsWith` | `{ op: endsWith, args: ["report.pdf", ".pdf"] }` | `true` |  |
| `contains` | `{ op: contains, args: ["Maya Okafor", " "] }` | `true` | String, substring |
| **Patterns** | | | |
| `matches` | `{ op: matches, args: ["27502-1234", "^[0-9]{5}(-[0-9]{4})?$"] }` | `true` | Unanchored search; the pattern is a portable literal |
| **Lists** | | | |
| `list` | `{ op: list, args: [a, b] }` | `["a", "b"]` | Builds a list from its arguments |
| `all` | `{ op: all, args: [{ var: data.shares }, { op: gt, args: [{ var: item }, 0] }] }` | `true` | `true` for an empty or `null` list |
| `some` | `{ op: some, args: [{ var: data.kinds }, { op: eq, args: [{ var: item }, work] }] }` | `true` | `false` for an empty or `null` list |
| `none` | `{ op: none, args: [{ var: data.kinds }, { op: eq, args: [{ var: item }, ""] }] }` | `true` | `true` for an empty or `null` list |
| `sum` | `{ op: sum, args: [{ var: data.shares }, { var: item }] }` | `100` | `0` for an empty list |
| `map` | `{ op: map, args: [{ var: data.kinds }, { op: upper, args: [{ var: item }] }] }` | `["HOME", "WORK"]` |  |
| `filter` | `{ op: filter, args: [{ var: data.shares }, { op: gt, args: [{ var: item }, 50] }] }` | `[60]` | Count matches with `len` of the result |
| **Dates** | | | |
| `daysBetween` | `{ op: daysBetween, args: ["2026-10-03", "2026-12-25"] }` | `83` | Whole calendar days from the first to the second, in UTC |
| `yearsBetween` | `{ op: yearsBetween, args: ["2008-10-04", "2026-10-03T12:00:00Z"] }` | `17` | Completed years, the way ages are counted |
| **Types** | | | |
| `typeOf` | `{ op: typeOf, args: [{ op: list, args: [] }] }` | `"list"` | `null`, `boolean`, `number`, `string`, `list` or `object` |

A type error, a wrong number of arguments or an unknown operator is an evaluation error. The
predicate of a list operator is evaluated for every element, with `item` bound to the element of the
innermost list operator. Dates are RFC 3339: `2026-10-03`, or a date-time with seconds and an offset
such as `2026-10-03T09:00:00Z`; a date-time is converted to UTC before its calendar date is taken.

### Portable patterns

`matches` accepts one small pattern syntax that means the same in every runtime. The pattern must be
a string literal in the ruleset; a pattern outside the syntax does not load
(`PATTERN_NOT_PORTABLE`). The search is unanchored: use `^` and `$` to match the whole string.

| Allowed | Meaning |
|---|---|
| Literal characters | Themselves, by code point. Matching is case-sensitive; there are no flags |
| `.` | Any one code point, line breaks included |
| `^`, `$` | The very start and the very end of the string |
| `\d`, `\w` and `\D`, `\W` | ASCII digits `[0-9]`, ASCII word characters `[A-Za-z0-9_]`, and their complements |
| `\t`, `\n`, `\r` | Tab, line feed, carriage return |
| `\.` `\+` `\(` `\[` `\{` `\\` `\/` and the other syntax characters | That character |
| `[abc]`, `[a-z]`, `[^0-9]`, `[\d\-]` | A character class; a literal `-` is escaped or stands first or last |
| `(...)`, `(?:...)`, `a\|b` | Groups and alternation |
| `*`, `+`, `?`, `{n}`, `{n,}`, `{n,m}`, and a `?` after any of them for lazy | Repetition; counts up to 1000 |

| Not portable | Write instead |
|---|---|
| `\s`, `\S` | `[ \t\r\n]`, `[^ \t\r\n]`; or `trim` the value first |
| `\b` | Spell out the boundary: `(^\|[^A-Za-z0-9_])cat([^A-Za-z0-9_]\|$)` |
| Flags and `(?i)` | `[Aa][Bb][Cc]`, or match the result of `lower` |
| Lookahead and lookbehind, `(?=...)`, `(?!...)` | Several `matches` joined with `and`; `not` for a negative condition |
| Back-references `\1` | No equivalent. Compare parts with `substring` and `eq` |
| Named groups `(?<year>...)` | `(...)` |
| `\A`, `\z`, `\Z` | `^`, `$` |
| `\p{L}`, `[[:alpha:]]` | An explicit class such as `[A-Za-z]`, or a negated class such as `[^0-9]` |
| `\x41`, `é` | The character itself: `A`, `é` |
| `\-` outside a class | `-` |
| Possessive and stacked quantifiers `a++`, `a{2}{3}` | One quantifier: `a+`, `a{6}` |
| A bare `{`, `}` or `]` | `\{`, `\}`, `\]` |
