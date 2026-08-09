# Code review: ErrApplicationInterrupted propagation at 3589f64f

## Scope and evidence reviewed

- Goal: restrict `apply` exit 6 to failures after durable `apply.started`, while preserving a recoverable continuation.
- Commit: `3589f64f64f0b80bebde31a558b0332b37a6e10f` in `/private/tmp/partitur-pr214-r6-uwYbbz`.
- Changed files: `cmd/partitur/main.go`, `internal/runstore/application.go`, `cmd/partitur/apply_require_test.go`, and `cmd/partitur/apply_kill_test.go`.
- Verification run: `GOCACHE=/private/tmp/partitur-gocache go test ./cmd/partitur -run '^TestApplyBeforeItsTransactionRefusesRatherThanPromisingRecovery$' -count=1` — PASS.
- Diff checks: `git show --check 3589f64f` and `git diff --check 3589f64f^ 3589f64f` — PASS.

## Skill-perspective check

Ran: yes. I consulted `omo:programming` (including its Go reference) and `omo:remove-ai-slops`.

- `programming`: no untyped escape hatch, needless abstraction, or new boundary parsing/validation was introduced. The error sentinel is a suitable typed branchable error. However the propagation is incomplete (HIGH below).
- `remove-ai-slops`: no deletion-only or tautological test was added, and no needless production data extraction, parsing, or normalization was introduced. The new pre-start CLI test tests an observable outcome rather than a constant, but coverage remains incomplete for the sentinel's post-start recovery paths.

## Findings

### CRITICAL

None.

### HIGH

1. Existing durable `APPLYING` transactions can still lose `ErrApplicationInterrupted` and be rendered as exit 2.

   `recoverApplication` is only entered after a prior durable `apply.started` (`internal/runstore/application.go:178-184`). Its identity-version failure at `:186-189`, and the failed append of `apply.recovery_required` at `:199-201`, return raw errors. The latter leaves the projection `APPLYING` because the recovery-required event did not complete, so `runApply` (`cmd/partitur/main.go:447-457`) categorizes it as a “precondition refused” exit 2. This violates the exit table: an operational interruption after a transaction has started while Application remains `APPLYING` must be exit 6 and advertise `apply --recover` (`docs/DESIGN.md:3539`, `:3747`).

   Fix the recovery path's errors that leave `APPLYING` with the sentinel (and add a narrow fault-injected regression that proves the CLI exits 6 and remains recoverable). Review the error paths after the recovery-required state separately: those cannot truthfully map to exit 6 because the state is no longer APPLYING, but they must not be silently collapsed into a precondition refusal either.

### MEDIUM

1. No regression test exercises the newly required post-start sentinel behavior.

   `cmd/partitur/apply_require_test.go:537-567` usefully proves one pre-start error returns 2. But the changed wrappers at `internal/runstore/application.go:136`, `:153`, `:158`, `:161`, `:166`, and `:175` are untested for `errors.Is(err, ErrApplicationInterrupted)` and CLI exit 6. In particular, there is no injected failure for a post-start durable event append, nor the recovery path described above. This leaves the central error-classification contract vulnerable to accidental unwrap/regression.

### LOW

None.

## Conclusion

- codeQualityStatus: BLOCK
- recommendation: REQUEST_CHANGES
- blockers:
  - Mark recovery failures that leave a durable Application `APPLYING` with `ErrApplicationInterrupted`, so `runApply` returns exit 6 instead of exit 2.
  - Add a narrow, behavior-level regression test for that error-to-exit mapping and recoverability.
