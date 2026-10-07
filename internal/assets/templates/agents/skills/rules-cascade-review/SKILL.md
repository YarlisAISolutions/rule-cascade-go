---
name: rules-cascade-review
description: Review a Rule Cascade ruleset change or proposal before it is accepted - ids and codes, version bump, enforcement channel, overrides, risk acceptance, golden tests, messages, portable patterns. Use when the user asks to review rules, a ruleset diff, a pull request touching *.ruleset.yaml, or a proposal from rcas proposals.
---

# Reviewing a ruleset change

You review; you change nothing. Read the guide first: MCP `get_authoring_guide` (topic `authoring`,
section 12 is the review checklist) or https://rulescascade.com/reference/authoring-guidelines/.

Get the change: a proposal (`rcas proposals show <id>`, MCP `get_proposal`) or the diff of
`{{rules}}/**/*.ruleset.yaml`. Then check, and report each item as pass or fail with the line:

1. **It checks.** `rcas check` (MCP `check`) is clean and every golden test passes.
2. **Version and identity.** The `metadata.version` bump matches what consumers observe: a new `error`
   rule, a raised severity, a tightened parameter, anything removed or renamed is **major**; a
   warning without acknowledgement or a `compute` rule is minor. No rule id or finding code is
   reused or repurposed.
3. **Enforcement.** Nothing secret (internal limits, fraud signals, other users' data) in a rule with
   `enforcement: client` or `both`. The server enforces everything.
4. **Overrides.** Parameters have the right override policy (`locked` for law and integrity,
   `tighten-only` for limits); every override carries a `reason`; a child level only tightens.
5. **Risk acceptance.** Acceptable warnings name the roles that may accept; integrity and legal rules
   are never acceptable.
6. **Tests.** Each new or changed rule has a passing case, a failing case, the exact boundary, a
   missing optional field, and the client channel when it runs in the browser.
7. **Messages and patterns.** A message key with arguments in every locale the ruleset has; patterns
   in the portable subset (anchored, bounded, no nested unbounded repetition); every optional field
   guarded by `when`.
8. **Evidence.** The rationale names the code or policy the rule comes from (`file:line`, a ticket).

End with **approve**, or the numbered list of changes required. A person accepts with
`rcas proposals accept <id>`; you never accept a proposal yourself.
