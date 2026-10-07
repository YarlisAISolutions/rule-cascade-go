---
name: rules-cascade-python
description: Enforce Rule Cascade rules in Python services and jobs (FastAPI, Django, Flask, Celery, batch scripts) with the rule-cascade package. Use when wiring rules into a Python code path, replacing Pydantic validators or hand-written checks with the engine, or evaluating many records in a batch.
---

# Rule Cascade in Python

Package: `pip install rule-cascade` (Python 3.10+; import `rule_cascade`). Bundles come from
`rcas compile --all` into `{{out}}/`. Never evaluate the YAML at run time and never copy a rule into
code.

## Load once, evaluate per request

```python
import json
from rule_cascade import RuleSet, RequestError

# At start-up (module level, FastAPI lifespan, Django AppConfig.ready). A bad bundle raises: do not serve.
with open("{{out}}/<ruleset-id>.bundle.json", encoding="utf-8") as f:
    rules = RuleSet.from_bundle(json.load(f))
```

**Prefer the decorator** (from 1.0.0-alpha.7): it builds the request, enforces before the function
runs, and raises `RuleViolation`, whose `problem()` is the 422 answer below.

```python
from rule_cascade import enforce_rules, current_evaluation
from rule_cascade.fastapi import install            # or rule_cascade.flask.init_app(app), or the Django middleware

install(app)                                        # RuleViolation -> 422, RequestError -> 400

@app.post("/orders", status_code=201)
@enforce_rules(rules, entity="Order", operation="create", data="order", actor="user")   # below the route decorator
def create_order(order: Order, user: User = Depends(current_user)):
    evaluation = current_evaluation()               # the allowed result: warnings, computed values, commands
```

`rules.enforce(request, "server")` does the same without a decorator. Underneath,
`rules.evaluate(request, "server")` returns a dict: `decision` (`allow` or `deny`), `findings`,
`effects`, `commands`. It raises `RequestError` for a malformed request (answer 400). Every
state-changing endpoint follows four steps:

1. **Evaluate on the server** with `{"entity", "operation", "data", "original", "actor", "resolutions"}`;
   `actor` comes from authentication (`{"id", "roles"}`), never from a field the caller sends.
2. **Refuse on deny** with **422** problem details (`application/problem+json`,
   `type: "urn:rule-cascade:rule-violation"`) that include the result, so clients can map
   `findings[].fields` to inputs.
3. **Persist**, after applying `compute` effects (`effect["type"] == "value"`): the server's value is stored.
4. **Run `result["commands"]`** after the commit, once per `idempotencyKey` (an outbox table, or a
   task queue with deduplication).

Framework placement: FastAPI a dependency (`Depends(get_rules)`) and an exception handler that turns
a `RuleViolation` into the 422; Django a service function called by every view or serializer
`validate()` of the entity, plus middleware or DRF exception handler for the 422; Flask an
extension object created in the app factory. Celery and batch jobs evaluate before every write the
same way.

`RuleSetHolder(load_bundle, interval=300)` (or `cron="0 * * * *"`) reloads bundles in the background
and keeps the last good rules when a reload fails.

## Many records

For files and queues, start `rcas engine` once and stream JSON Lines requests through it, or call
`rules.evaluate` in a loop: load the bundle once either way. See
https://rulescascade.com/examples/batch/.

## Replacing existing validation

`rcas analyze` lists Pydantic validators, marshmallow schemas, Django `clean()` methods and
hand-written checks. Keep type and shape validation (Pydantic models) where it is; move business
decisions to rules. Add a parity test that runs the old check and the engine on the same inputs and
delete the old check only when they agree.

## Tests

Assert `decision` and finding `code`s, not message texts. The rules themselves are tested by their
golden tests (`rcas check`). In CI run `rcas check` and `rcas compile --all` before `pytest`.

Reference: https://rulescascade.com/usage/python/
