---
name: rcas-review
description: "Review a ruleset or a proposal against the Rule Cascade authoring guide"
argument-hint: "ruleset id or proposal id"
argument: target|a ruleset id or a proposal id
argument-default: ask which ruleset or proposal
skill-when: Use when the user asks to review a ruleset or a proposal.
---

Review {{args}}.

1. Read it with `list_rules` or `get_proposal`, and the authoring guide with `get_authoring_guide`
   (topics `authoring` and `naming`).
2. Run `check` and `test` on it; report every problem.
3. Check each rule against the MUST and SHOULD rules: one concern, stable ids and finding codes, a
   `when` guard for optional fields, params instead of literals, golden tests with boundaries, and
   nothing secret in a client rule.
4. For a child ruleset, check its `extends`, that its `scope` adds a level to its parent's, and that
   its `overrides` only tighten what the parent allows.
5. Call `analyze` on the code the ruleset replaces: a hit with `drift` or not `coveredBy` it means
   the code and the rule may disagree.

Answer with what to change, most important first, and the exact text to use.
