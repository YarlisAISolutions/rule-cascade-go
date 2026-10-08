## Business rules: Rule Cascade (`rcas`)

This project keeps its business rules (validations, limits, required approvals, computed values,
follow-up commands) as **data**: YAML rulesets under `{{rules}}/`, compiled into JSON bundles that
every language evaluates identically. Rules are not re-implemented in application code.

### Where things live

| What | Where |
|---|---|
| Project configuration | `rcas.yaml` |
| Rulesets (source of truth) | `{{rules}}/**/*.ruleset.yaml` |
| Compiled bundles and client manifests (generated, never edited) | `{{out}}/` |
| Proposals waiting for a person | `.rcas/proposals/` |
| Specification, guidelines, cookbook | MCP tools `get_spec_section`, `get_authoring_guide`, `get_operator_reference` (also on the hosted server, https://mcp.rulescascade.com/mcp); https://rulescascade.com |

### How to work

1. **Never edit rulesets directly when acting as an agent.** Draft them, check them, and submit them
   with the MCP tool `propose_ruleset` (or `rcas derive --propose`). A person reviews with
   `rcas proposals show <id>` and accepts with `rcas proposals accept <id>`.
2. **Find the decisions in the code first.** Run the MCP tool `analyze` (or `rcas analyze`): it lists
   the API schemas and the validation code (Zod, Joi, class-validator, Pydantic, Bean Validation, Go
   `validate:` tags, FluentValidation, Rails `validates`, hand-written `if ... throw`). Each hit is a
   candidate rule; read the surrounding code to learn what it really decides and why.
3. **Start from the schemas.** For an OpenAPI or JSON Schema, `derive` produces the baseline rules
   (required, enum, length, pattern, range) deterministically. Write by hand only what a schema
   cannot say: rules across fields, rules on state transitions, limits that depend on parameters,
   warnings, acknowledgements, risk acceptance, computed values and commands.
4. **Check before you claim done.** `rcas check` (MCP `check`) must report `0 failed` and no problems
   for every ruleset you touched. `rcas compile --all` must succeed.

### Engineering rules for rulesets

- **One rule, one concern.** A rule asserts one condition about one field or one relation. Split
  "amount is positive and below the limit" into two rules with two codes.
- **Stable identities.** Rule ids are `<entity>.<field or aspect>.<constraint>` (`transfer.amount.positive`), finding codes
  `<DOMAIN>-<GROUP>-<NNN>` (`PAY-TRF-002`). Never reuse or repurpose an id or a code; retire it and add a new one. Bump
  `metadata.version` by what consumers observe: a new `error` rule, a raised severity, a tightened parameter or
  anything removed or renamed is major; a warning without acknowledgement or a `compute` rule is minor.
- **Guard optional data.** Every rule that reads an optional field has a `when` that tests it exists;
  an unguarded missing value is an evaluation error, not a finding.
- **Parameters, not literals.** Limits, lists and thresholds are `params` with an override policy
  (`locked` for law and integrity, `tighten-only` for limits, `open` for defaults).
- **Severity is a decision.** `error` blocks, `warning` warns and can require an acknowledgement, `info` informs.
  Risk acceptance names the roles that may accept; integrity and legal rules are never acceptable.
- **The server decides.** Every rule is enforced on the server; a rule also runs in the browser only
  when it reads nothing secret (`enforcement: client|both`). Never put limits a user must not see in
  a client rule.
- **No side effects, portable expressions.** Expressions are data from the specification's operator
  list. Patterns use the portable subset: anchored, character classes, bounded counts, no nested
  unbounded repetition.
- **Messages are keys.** Findings reference message keys with arguments; texts live in `messages`
  per locale, never in code.
- **Golden tests for every rule.** At least one passing and one failing case per rule, plus boundary
  values (exactly at the limit) and the client channel when the rule runs in the browser. Tests are
  the specification of the rule: a reviewer reads them first.
- **Levels instead of copies.** Shared rules live in a parent ruleset (`extends`); a child may only
  tighten what the parent allows.
- **Replace code checks gradually.** When a rule takes over a check in code, keep the code check until
  a parity test shows both agree, then delete it and call the engine instead.

### Commands

| Command | Does |
|---|---|
| `rcas check` | lint, load and run the golden tests of every ruleset |
| `rcas compile --all` | write bundles and manifests to `{{out}}/` |
| `rcas evaluate --bundle <b> request.json` | evaluate one request |
| `rcas analyze [path]` | inventory schemas and validation code |
| `rcas derive <api.yaml> --schema <Name> --id <ruleset.id> --propose` | baseline rules from a schema, as a proposal |
| `rcas proposals list` / `show <id>` | what is waiting for review |

### Skills

Step-by-step instructions for each task are in skills (`.agents/skills/`; Claude Code, Kiro and
Cline get copies in their own folders). Load the one that matches the task:

{{skills}}

Skills marked `rcas-managed` are refreshed by `rcas agent install`; delete that line in a skill to
keep your own edits. Add team skills next to them under any other name.
