---
name: rules-cascade-typescript
description: Enforce Rule Cascade rules in TypeScript and JavaScript - Node.js services (Express, Fastify, NestJS, Next.js route handlers, node:http) with @rules-cascade/core, and browsers or React Native forms with the client manifest and @rules-cascade/client. Use when wiring rules into a Node or browser code path, replacing Zod/Joi/class-validator checks with the engine, or showing rule findings in a form.
---

# Rule Cascade in TypeScript and JavaScript

Packages: `@rules-cascade/core` (the engine, Node 20+ and browsers), `@rules-cascade/client`
(fetches and caches manifests; `@rules-cascade/client/react` has `useRuleEvaluation`). Bundles come
from `rcas compile --all` into `{{out}}/`. Never evaluate the YAML at run time and never copy a rule
into code.

## Server: load once, evaluate per request

```ts
import { readFileSync } from 'node:fs';
import { RuleSet, RequestError } from '@rules-cascade/core';

// At start-up. A bad bundle throws here: let it stop the process, never serve without rules.
export const rules = RuleSet.fromBundle(JSON.parse(readFileSync('{{out}}/<ruleset-id>.bundle.json', 'utf8')));
```

**Prefer the one-line helpers** (from 1.0.0-alpha.7): they build the request, enforce, and answer a
denied request with the 422 problem details below, the same in every Rule Cascade runtime.

```ts
import { rulesMiddleware, rulesErrorHandler, rulesPreHandler } from '@rules-cascade/core/http';

app.post('/orders', rulesMiddleware(rules, { entity: 'Order', operation: 'create', actor: (req) => req.user }), handler);
app.use(rulesErrorHandler());   // Express: RuleViolationError -> 422, RequestError -> 400
// Fastify: { preHandler: rulesPreHandler(rules, { entity: 'Order', operation: 'create' }) }
// Anywhere else: rules.enforce(request) returns the result or throws RuleViolationError (error.problem() is the body)
```

The allowed result is on `res.locals.ruleEvaluation` (Fastify `request.ruleEvaluation`). For
updates pass `original: (req) => loadStored(req.params.id)`. Whatever the wiring, every
state-changing handler follows the same four steps:

1. **Evaluate on the server**: `rules.evaluate({ entity, operation, data, original, actor, resolutions }, 'server')`.
   `operation` is `create`, `update`, `delete`, `read` or a custom name from the ruleset; `original`
   is the stored entity for updates; `actor` comes from authentication (`{ id, roles }`), never from a
   header the caller controls. A `RequestError` means a malformed request: answer 400.
2. **Refuse on deny**: when `result.decision !== 'allow'`, answer **422** with RFC 9457 problem
   details (`type: 'urn:rule-cascade:rule-violation'`) and put `result` in the body, so clients can
   map `findings[].fields` to inputs. Do not translate findings into your own error format by hand.
3. **Persist**, after applying `compute` effects: for `effect.type === 'value'`, the server's value
   (a fee, a status) is the one stored.
4. **Run `result.commands`** after the commit, at most once per `command.idempotencyKey` (an outbox
   table or a broker with deduplication).

Framework placement: Express/Fastify middleware or a service method called by every route of the
entity; NestJS an injectable `RulesService` provided once (`useValue: rules`) plus an exception
filter for the 422; Next.js route handlers import the module-level `rules`. Keep one place per
entity that builds the request, so every route evaluates the same way.

For hot reload of bundles without a restart, keep the `RuleSet` behind a holder you swap atomically
after a successful load, and keep the last good one when a load fails.

## Browser and React: the client manifest only

Ship or serve `<ruleset-id>.client.manifest.json` (never the bundle: it may hold server-only rules
and limits). Serve it with an `ETag` of the bundle checksum.

```ts
import { createManifestClient } from '@rules-cascade/client';
import { useRuleEvaluation } from '@rules-cascade/client/react';

const manifests = createManifestClient({ baseUrl: '/api/rules' }); // cached, revalidated with the ETag
const { allowed, findingsFor } = useRuleEvaluation(manifest, request); // per field: findingsFor('/amount')
```

The browser gives early feedback; **the server evaluates again on submit and decides**. Show the
422 findings from the server the same way as the local ones.

## Replacing existing validation

`rcas analyze` lists Zod, Joi, Yup, class-validator and hand-written checks. For each: write or
derive the rule, add a parity test that runs the old check and the engine on the same inputs, and
delete the old check only when they agree. Keep transport validation (JSON shape, types) where it
is; move business decisions to rules.

## Tests

- Unit: build requests and assert `decision` and the finding `code`s, not message texts.
- The rules' own behaviour is tested by the golden tests in the ruleset (`rcas check`), not here.
- CI: `npx -y @rules-cascade/cli check` and `compile --all` before the app's tests, so the app
  tests run against freshly compiled bundles.

Reference: https://rulescascade.com/usage/typescript/ and the example service at
https://rulescascade.com/examples/api-endpoint/.
