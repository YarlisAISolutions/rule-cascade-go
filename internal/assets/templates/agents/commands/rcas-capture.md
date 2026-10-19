---
name: rcas-capture
description: "Find the business rules in a folder, module or feature and propose rulesets for them"
argument-hint: "a path, a module or a feature name (default: the whole project)"
argument: scope|a path, a module or a feature
skill-when: Use when the user asks to find, extract or capture the business rules of a folder, a module, a feature or the whole project.
argument-default: the whole project
---

Capture the business rules in {{args}}.

1. Call the `analyze` tool of the rules-cascade MCP server with that scope (`path`, `module` or
   `feature`). Read its `modules`, `candidates` and `hits`.
2. Open the code around each hit and state the decision in one sentence: entity, operation, field,
   what is allowed, what happens otherwise. Drop hits that decide nothing (a loop bound, a retry
   count, a layout choice).
3. A candidate is one name decided in several languages, typically the backend service and the
   user interface. When it shows `drift`, the two disagree: say which value is right and why, or ask.
4. Skip what a ruleset already decides (`coveredBy`, and `list_rules`), unless the code disagrees with it.
5. Start from the API schemas with `derive`; write by hand only what a schema cannot say.
6. Shape the rulesets as the `rules-cascade` skill says: one ruleset
   per module that holds the shared params and rules, and one per feature that `extends` it. A
   child's `scope` is its parent's scope plus one level (`module`, then `feature`), and both files
   sit in the same directory, because a parent is looked up next to its child.
7. Add golden tests for every rule (pass, fail, boundary, client channel), run `check` until it
   reports ok, then submit with `propose_ruleset`, listing the file:line of each source.

End with a table: rule id | decision | source file:line | backend | user interface | drift, and the decisions you
left out and why. Never edit a ruleset directly: a person accepts the proposal with
`rcas proposals accept <id>`.
