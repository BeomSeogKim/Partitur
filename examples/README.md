# Example scores

> The instructions in these examples name real packages of this repository so that the scores are concrete, but they describe illustrative tasks, not open issues: run one as-is and the writer may correctly report that there is nothing to change.

Three score shapes, each a complete `partitur.yaml` plus the `cast.yaml` that binds it.
Copy a directory's `partitur.yaml` to your repository root and its `cast.yaml` to
`.partitur/cast.yaml`, then edit the instruction and the acceptance commands.

`examples_test.go` compiles every score here and resolves every cast against it on each
`go test ./...`, so an example that stops validating fails the build rather than rotting.

## Which shape when

The notes below are measurements taken on this repository on 2026-09-21, not claims about
tasks in general.

### `solo/` — one writer, apply gate waived

One writer part, `apply_gate.waived: true`, `allowed_paths: ["**"]`, and acceptance equal to
the repository's own CI commands. Four "confirm-and-remove" and "name-the-condition" code
fixes each converged on the first attempt, took roughly 11–22 minutes, and produced changes
of 100–110 lines. Use it when the task is well specified and you will read the diff yourself
before keeping it.

### `solo-gated/` — the same, plus a movement gate

`solo/` with `human_gate: always` on the writer's movement. A movement gate is independent of
the waived apply gate: the apply gate governs whether a candidate may be applied, the movement
gate stops the run so a human can read the diff first. Use it when you want to see the change
before it reaches the working tree but do not want a second agent to judge it.

### `gated/` — writer, read-only verifier, and both gates

A writer, a `read_only` verifier, `human_gate: always` on the verifier's movement,
`apply_gate.require: [verified, approved]`, and `final_movement` naming the verifier. On a
prose task and on a spec-reading change, the verifier caught one real defect each that the
machine criteria had passed. On small code removals it found nothing. Use it where a passing
test suite does not settle whether the change is right.

## Five rules these examples encode

These came from runs that failed.

1. **Acceptance scope equals CI scope.** Write `./...`, never a narrower package list. A
   criterion that checks less than CI does hands you a verified candidate that CI rejects.
2. **A verifier's criteria must hold in the verifier's own tree.** The writer's change is
   already the base there, so `git diff HEAD` is empty. Assert state — files, test results,
   greps over the tree — not a diff.
3. **Judge text changes with structured output.** `git diff --numstat`, exit codes, and test
   names are stable; a regex over diff lines is not, and passes or fails for reasons that have
   nothing to do with the change.
4. **Give every binding `fallbacks`.** A vendor "model at capacity" error otherwise ends the
   run. The test in this directory enforces it.
5. **Match the enforcement dimensions to the adapter.** With `allowed_paths: ["**"]` the
   `codex` adapter needs no advisory flag; the `claude` adapter still reports no
   `network_grants`, so its performer needs `allow_advisory_enforcement: true`.

## Running one

```bash
partitur run
partitur apply <run-id>
```

`run` prints the run id; pass it to `apply`. For `solo-gated/` and `gated/`, the run stops at
the movement gate, so read it, resolve the decision, and resume before applying:

```bash
partitur status
partitur approve <decision-id> --approve
partitur resume
partitur apply <run-id>
```

Bare `status` refuses once a run is terminal — pass the run id then: `partitur status <run-id>`.
