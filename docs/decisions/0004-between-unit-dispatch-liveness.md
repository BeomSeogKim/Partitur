# Decision 0004 — Between-unit dispatch liveness evidence

- Status: accepted (2026-08-17)
- Scope: repository completion evidence; not system behaviour

## Context

`TestPlanBetweenUnitActionKindsHaveLiveDriverDispatch` derives the ActionKinds
that `PlanBetweenUnit` can return and checks that the live driver has matching
switch cases. Its denominator is the recovery selector's code closure, not a
normative enumeration: at this decision it contains seven kinds. `DESIGN.md`
does not name those Go identifiers, so adding a document list would only make a
second, weaker source of truth.

The derivation recognised four return shapes and failed closed on four related
ambiguous shapes, but a whole-field write such as
`decision.Action = &Action{Kind: ...}` was silently outside its view. Six
attempts to recognise more right-hand sides were each evaded one level lower.
The driver check also established case presence, not whether a case did useful
work: a refusal body could pass it.

The recovery-executor sibling shares the first blind spot. It has no live
driver dispatch body, so the second issue belongs only to the driver lock.

When the liveness harness was first connected, four of the seven kinds had
tests that reached `liveRunLoop`: READY, STARTED, initial selection, and
direct run failure. `append_budget_exhaustion` and
`materialize_selected_successor` were exercised only through their direct
handlers. A third apparent witness, `TestLiveCompositionConflictStopsBeforeCreatingTargetAttempt`,
looked plausible for `compose_application_candidate` but reached movement
fan-in composition instead; refusing the application-candidate switch body
left it green. The executed per-kind harness exposed that a plausible test name
is not evidence of the dispatch path it is assigned to witness.

Three new tests therefore create a watcher, probe the live selector for the
intended kind, and assert the durable effect after the loop. The candidate test
also proves that its fixture has no pre-existing `application_candidate.recorded`
event or projected candidate before it enters the loop, then binds the newly
recorded event to the projected candidate and its contributors.

## Decision

The derivation rejects every assignment whose left-hand side is an `Action`
field in any production `internal/recovery` Go file. It does not interpret the
right-hand side or attempt to prove its receiver's static type. This is
deliberately conservative: no current production file performs such a write,
and a future one stops the derivation with its file, line, and expression.
The same guard is applied to the executor sibling. Source-copy mutations that
introduce a whole-field replacement prove both guards and require their named
lock tests to run and fail.

The driver adds a mutation-tagged liveness harness. Its fixed
kind-to-behavioural-test mapping is compared for exact equality with the
source-derived set: a new derived kind without a named witness, or a stale
witness, fails rather than being skipped. For every selected kind, the harness
copies the source, replaces only that kind's live dispatch body with a refusal,
and requires its named behavioural test to fail. A separate source-copy
mutation changes a planner result to a declared but unknown action kind and
requires the unknown-kind-path witness to fail. The child-run protocol verifies
that every named target actually ran; a build failure, skipped target, or mixed
result is not a kill.

The completion row this supports is:

> Every action kind the between-unit planner can return has exactly one live-driver dispatch site, and replacing that site's body with a refusal fails a named behavioural test. The unknown-kind path carries its own witness. This proves per-kind dispatch liveness. It does not prove that a dispatched case does the right work — that obligation stays with each kind's behavioural tests.

## Mechanical limit

The lock sees recovery source only: an unrecognised return shape or an
`Action`-field assignment there fails closed, but code outside that package
cannot itself be a `PlanBetweenUnit` return. The liveness harness proves that
each selected switch branch changes the outcome of its named behavioural path;
it does not prove that the branch implements the semantically correct effect.
That remains the obligation of the behavioural tests and normative review.
