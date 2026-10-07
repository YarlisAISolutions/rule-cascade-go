---
name: rules-cascade-migrate
description: Move business rules out of application code into Rule Cascade step by step - inventory the checks, propose rulesets, prove parity, switch the code to the engine, delete the old checks. Use when the user wants to adopt Rule Cascade in an existing code base, centralise validations scattered across services and front ends, or replace a specific check with a rule.
---

# Moving rules out of code

The goal is one source of truth without a big-bang rewrite. Work one entity at a time; behaviour
must not change at any step.

## 1. Inventory

Run `rcas analyze` (MCP `analyze`) on the code base. It lists API schemas and validation code: Zod,
Joi, Yup, class-validator, Pydantic, Bean Validation, Go `validate:` tags, FluentValidation, Rails
`validates` and hand-written `if ... throw`. For each hit write one line: entity, operation, field,
what is allowed, what happens otherwise, `file:line`. Check `rcas` rules that already exist
(`list_rules`) so nothing is proposed twice.

Pick the first entity: the one whose rules are duplicated most (front end and back end, or several
services), or the one changing most often.

## 2. Rules, as proposals

- Schemas first: `rcas derive <api.yaml> --schema <Name> --id <ruleset.id> --propose` produces the
  baseline (required, enum, length, pattern, range).
- Hand-write the rest with the `rules-cascade` skill: cross-field rules, state transitions,
  parameterised limits, warnings, approvals, computed values, commands.
- Golden tests for every rule, then `rcas check`, then a proposal. A person accepts it.

## 3. Parity

In the project's own test framework, write a parity test per entity: the same inputs (the existing
test fixtures, plus boundaries and recorded production requests when available) go to the old code
path and to the engine; the decisions and the failing fields must agree. A disagreement is either a
bug in the new rule or an undocumented behaviour of the old code: ask the user which, never guess.

## 4. Switch, then delete

1. Call the engine in the code path, next to the old check (the stack skill shows how:
   `rules-cascade-typescript`, `-python`, `-java`, `-go`, or `-engine` for other languages). Log any
   disagreement in production for a while if the user wants a shadow period.
2. When parity holds, make the engine's answer the one that counts (422 with the findings) and
   delete the old check, its error strings and its duplicate in the front end.
3. Keep type and shape validation (JSON schema, DTO types) where it is: that is transport, not
   business rules.

## Done for an entity

The ruleset is accepted and passes `rcas check`; the code calls the engine for every operation of
the entity on the server; the front end uses the client manifest; the old checks are deleted; CI
runs `rcas check` and `rcas compile --all`.

Reference: https://rulescascade.com/get-started/existing-project/ and
https://rulescascade.com/playbooks/rules-from-existing-code/
