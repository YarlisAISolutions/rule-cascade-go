# Naming conventions

One convention per kind of name. The column "Enforced by" says what refuses a name that breaks the
convention: **schema** is the ruleset schema (`SCHEMA_INVALID`), **load** is a load-time check, and
**review** is the reviewer. How to write the rules themselves is in the
[authoring guidelines](authoring-guidelines.md); this page is only about names.

## In a ruleset

| Thing | Convention | Example | Enforced by |
|---|---|---|---|
| Ruleset id | lower-case, dot-separated, broad to narrow: `<org>.<domain>.<capability>` | `acme.payments.transfer` | schema |
| Ruleset version | Semantic Versioning | `1.4.0` | schema |
| Scope level | camelCase noun; prefer `enterprise`, `organization`, `businessUnit`, `agency`, `project`, `application`, `module`, `feature` | `businessUnit` | schema (letters, digits, `_`); review |
| Scope id | kebab-case | `retail-banking` | schema |
| Entity | PascalCase singular noun, same as the OpenAPI schema name | `Transfer` | schema (letters, digits, `_`); review |
| Type | PascalCase singular noun for the meaning, not the representation | `Email`, `Money`, `CountryCode` | schema; load (`TYPE_UNKNOWN`) |
| Parameter | camelCase, unit in the name when it is not obvious | `maxTransferAmount`, `reviewWindowDays` | schema; load (`PARAM_UNDECLARED`) |
| Function | camelCase, named for what it returns; a predicate starts with `is` or `has` | `isBlank`, `ageOn` | schema; load (`FUNCTION_UNKNOWN`) |
| Function parameter | camelCase noun | `text`, `birthDate` | schema |
| Custom operator | `x-` and a kebab-case name for what it checks or computes | `x-luhn` | schema; load (`OPERATOR_UNDECLARED`) |
| Rule id | `<entity>.<field or aspect>.<constraint>`, lower-case, kebab-case inside segments | `transfer.amount.positive`, `transfer.approve.four-eyes` | schema |
| Rule id, rule on a type | `type.<type>.<constraint>` | `type.email.format`, `type.money.two-decimals` | review |
| Rule id, inherited level | Prefix with the level that owns it | `org.transfer.amount-limit` | review |
| Rule kind extension | `x-<name>` | `x-audit` | schema |
| Page, screen, section, component | kebab-case logical id: what the user sees, not a framework class, selector or position | `onboarding`, `profile`, `identity`, `name-input` | schema (lower-case letters, digits, `-`); review |
| Field pointer | JSON Pointer with the API's property names | `/beneficiary/swiftCode` | schema; load (`PATH_UNKNOWN`) |
| Operation | lower-case verb, kebab-case for phrases | `create`, `submit-for-review` | schema |
| Finding code | `<DOMAIN>-<GROUP>-<NNN>`, upper-case, never reused | `PAY-TRF-002`, `ONB-TYP-001` | schema; load (`FINDING_CODE_DUPLICATE`) |
| Message key | `<subject>.<reasonInCamelCase>`; the subject is the entity, field or type | `transfer.amountOverLimit`, `email.format` | load (`MESSAGE_MISSING`) |
| Message placeholder | camelCase, ASCII letters and digits | `{limit}` | load (`MESSAGE_ARG_MISSING`) |
| Locale | Language tag, with a region where needed | `en`, `es`, `fr-CA` | schema |
| Command name | `<domain>.<past-tense-fact>` | `risk.large-transfer-created` | schema |
| Event type (`ref`) | Reverse-DNS with a version | `com.acme.payments.transfer.large.v1` | review |
| Actor role | kebab-case | `risk-officer`, `credit-manager` | review |
| Tag | lower-case single word | `compliance` | review |
| Golden test name | A sentence that states the behaviour | `blocked country is denied on the server` | review |
| Extension field | `x-<owner>-<name>` | `x-acme-jira` | schema (`x-` prefix) |

Derived rulesets (`rulecheck derive`, see [OpenAPI](openapi.md)) name things mechanically: rule ids
are `<entity>.<property path>.<constraint>`, finding codes `GEN-<ENTITY>-<NNN>`, message keys
`generated.<constraint>`, and each rule carries `x-generated-from`.

### Writing a good rule id

A rule id is permanent. It appears in findings, logs, overrides and audit records.

- Name the **constraint**, not the implementation: `transfer.amount.positive`, not `transfer.check1`.
- Do not encode severity or numbers that may change: `amount-limit`, not `amount-max-25000-error`.
- When a rule is replaced, give the replacement a new id and retire the old one. Never reuse an id
  or a finding code for a different meaning.

### Writing a good place id

A page, screen, section or component id is shared by the ruleset and the user interface: the
interface sends it in `view`, and findings return it in `location`.

- Name what the user sees: `amount-panel`, not `MuiGrid-3` and not `#amount`.
- Keep it stable across redesigns. Renaming one is a major change to the ruleset.
- Use the same id on every platform that shows the same place: web, mobile, desktop.

## Files

| Thing | Convention | Example |
|---|---|---|
| Ruleset | `<domain>-<capability>.ruleset.yaml` (or `.yml`, `.json`), kebab-case. Tools find rulesets by `*.ruleset.*` | `payments-transfer.ruleset.yaml` |
| Bundle | `<ruleset id>.bundle.json`. The rule server loads `*.bundle.json` | `acme.payments.transfer.bundle.json` |
| Golden tests of a derived ruleset | `<name>.tests.yaml`, next to the ruleset | `payments-transfer.tests.yaml` |
| OpenAPI description | `<domain>.openapi.yaml` | `payments.openapi.yaml` |
| Specification files | `<name>.schema.json`, `<name>.openapi.yaml`, under `spec/v<major>/` | `spec/v1/rule-cascade.schema.json` |
| ADR | `docs/adr/NNNN-<decision>.md` | `0005-compile-once-bundles.md` |

## In the repository

| Thing | Convention | Example |
|---|---|---|
| Repository and directories | kebab-case | `packages/typescript` |
| Document keys | `ruleCascade` in a ruleset and a manifest, `ruleCascadeBundle` in a bundle | `ruleCascade: 1.0.0` |
| OpenAPI extension | `x-rule-cascade` | on the root and on each operation |
| npm package | `@rules-cascade/<part>`, in the `rules-cascade` npm scope (not the GitHub organisation's) | `@rules-cascade/core`, `@rules-cascade/server` |
| npm commands | `rule-cascade-<part>` | `rule-cascade-node`, `rule-cascade-server` |
| Maven coordinates | `com.rulescascade:rules-cascade-<part>`, in the `com.rulescascade` namespace, verified on Maven Central by DNS on rulescascade.com | `com.rulescascade:rules-cascade-core` |
| Java package | `com.rulescascade` | `com.rulescascade.RuleSet` |
| Python distribution and package | `rcas`, imported as `rule_cascade` | `from rule_cascade import load` |
| Go module | The vanity path on the documentation domain, served from the public mirror of `packages/go` | `rulescascade.com/go` |
| Go package | `rulecascade`; the command lives in `cmd/rule-cascade` | `rulecascade.FromBundle` |
| Command | `rcas`; release files `rule-cascade-<os>-<arch>`, with `.exe` on Windows | `rcas-linux-arm64` |
| WebAssembly module | `rcas.wasm` | |
| Engine name, as the `version` command of the engine protocol reports it | `rule-cascade-<language>` | `rule-cascade-python`, `rule-cascade-typescript`, `rule-cascade-java`, `rule-cascade-go` |
| Load error code, lint code, protocol error code | UPPER_SNAKE_CASE, identical in every runtime | `PARAM_LOOSENED`, `YAML_NOT_PORTABLE`, `BAD_REQUEST` |
| Finding code of the engine | Upper-case with hyphens, like every finding code | `RULE-EVALUATION-ERROR` |
| Conformance operators | `x-test-<name>` for operators that exist only for the suite | `x-test-reverse`, `x-test-sum` |
| Environment variable | UPPER_SNAKE_CASE | `RULES_DIR`, `RULE_SERVER_TOKEN`, `RULE_CASCADE_ENGINE` |
| Branch | `<type>/<short-description>` | `feat/date-operators` |
| Commit | Conventional Commits | `feat(spec): add daysBetween` |
| Tag and release | `v<semver>` | `v1.0.0` |

## Versions

A ruleset has its own semantic version in `metadata.version`. Which change needs which bump is
guideline 1.5 of the [authoring guidelines](authoring-guidelines.md#1-identity-and-versioning). The
specification (`ruleCascade`) and the bundle format (`ruleCascadeBundle`) are versioned separately;
see specification section 10.
