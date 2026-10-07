---
name: rules-cascade-java
description: Enforce Rule Cascade rules on the JVM - Java 17+, Kotlin and Scala; Spring Boot, Quarkus, Micronaut, Jakarta EE - with com.rulescascade:rules-cascade-core. Use when wiring rules into a JVM service, replacing Bean Validation or hand-written checks with the engine, or mapping rule findings to HTTP errors.
---

# Rule Cascade on the JVM

Dependency (Maven): `com.rulescascade:rules-cascade-core` (Java 17+, no other runtime dependency;
package `com.rulescascade`). Gradle: `implementation("com.rulescascade:rules-cascade-core:<version>")`.
Bundles come from `rcas compile --all` into `{{out}}/`. Never evaluate the YAML at run time and never
copy a rule into code.

## Load once, evaluate per request

```java
import com.rulescascade.*;

// One bean / singleton, created at start-up. An exception here must stop the application.
RuleSet rules = RuleSet.fromBundle((Map<String, Object>) Json.parse(Files.readString(Path.of("{{out}}/<ruleset-id>.bundle.json"))));
List<String> missing = rules.missingOperators();   // custom operators the rules need
if (!missing.isEmpty()) throw new IllegalStateException("unregistered operators: " + missing);
```

When the service parses JSON with Jackson, enable `DeserializationFeature.USE_BIG_DECIMAL_FOR_FLOATS`
so amounts reach the engine exactly.

**Prefer `rules.enforce(request)`** (from 1.0.0-alpha.7): it returns the allowed result or throws
`com.rulescascade.RuleViolationException`, whose `problem()` is the 422 answer, the same in every
Rule Cascade runtime. One handler answers it for every endpoint:

```java
@RestControllerAdvice
class RuleErrors {
    @ExceptionHandler(RuleViolationException.class)
    ResponseEntity<Map<String, Object>> denied(RuleViolationException e) {
        return ResponseEntity.status(e.status()).contentType(MediaType.APPLICATION_PROBLEM_JSON).body(e.problem());
    }
}
```

For an annotation on controller methods, copy `@EnforceRules` and `RuleEnforcementAspect` from the
Spring Boot example (https://rulescascade.com/reference/examples/backend-spring-boot/); it needs
`spring-boot-starter-aop`. Every state-changing endpoint follows four steps:

1. **Evaluate on the server**:
   `rules.evaluate(EvaluationRequest.builder(entity, operation).data(data).original(original).actor(...).build(), Channel.SERVER)`.
   The actor comes from the security context, never from a header the caller controls.
2. **Refuse on deny**: when `!result.allowed()`, throw a `RuleViolationException(result)` and map it
   once, in a `@RestControllerAdvice` / exception mapper, to **422** `ProblemDetail` with
   `type: urn:rule-cascade:rule-violation` and the result (`result.toMap()`) as a property.
3. **Persist**, after applying `compute` effects (`effect.type()` is `value`): the server's value is stored.
4. **Run `result.commands()`** after the commit (`@TransactionalEventListener(phase = AFTER_COMMIT)` or
   an outbox), once per `idempotencyKey`.

Placement: Spring a `@Configuration` with a `RuleSet` `@Bean` and a service per entity that builds the
request; Quarkus/Micronaut a `@Singleton` producer. Kotlin and Scala call the same API.
`RuleSetHolder` reloads bundles on an interval or a cron schedule and keeps the last good rules.

## Replacing existing validation

`rcas analyze` lists Bean Validation annotations and hand-written checks. Keep `@NotNull`/`@Size` type
checks on the DTO if you like; move business decisions to rules. Add a parity test (JUnit
`@ParameterizedTest` over the same inputs for the old check and the engine), then delete the old check.

## Tests

Assert `decision` and finding codes, not message texts. Golden tests in the ruleset (`rcas check`)
test the rules; CI runs `rcas check` and `rcas compile --all` before `mvn verify` / `gradle test`.

Reference: https://rulescascade.com/usage/java/ and https://rulescascade.com/playbooks/backend-api/
