# Example scores

> The instructions in these examples name real packages of this repository so that the scores are concrete, but they describe illustrative tasks, not open issues: run one as-is and the writer may correctly report that there is nothing to change.

Three score shapes, each a complete `partitur.yaml` plus the `cast.yaml` that binds it.
Copy a directory's `partitur.yaml` to your repository root and its `cast.yaml` to
`.partitur/cast.yaml`, then edit the instruction and the acceptance commands. In a
repository that has never run Partitur, do what [Before the first run](#before-the-first-run)
lists first.

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

## Six rules these examples encode

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
6. **An acceptance criterion must not write into the workspace.** A file a criterion leaves in
   the tree fails the attempt with `acceptance_mutated_workspace`, even when the command exits
   0. `go build ./...` does exactly that when `./...` matches a single `main` package, as in a
   fresh one-package module: it writes a binary named after the module. The scores here build
   with `go build -o /dev/null ./...`, which compiles the same packages and writes nothing.

## Before the first run

Copying the two files is not enough in a repository that has never run Partitur: `run` refuses
a dirty source repository, and a copied score and cast are untracked files. In the target
repository:

```bash
partitur init
cp <example>/partitur.yaml partitur.yaml
cp <example>/cast.yaml .partitur/cast.yaml
# edit the instruction, the acceptance commands, and the cast
git add partitur.yaml .partitur/.gitignore .partitur/cast.yaml
git commit -m "Add Partitur score and cast"
```

`init` writes `.partitur/.gitignore`, which keeps the `.partitur/runs/` and `.partitur/work/`
that every run writes out of the tree the next run checks. It also writes a draft
`partitur.yaml`; copy the example's over it. `init` never overwrites an existing
`partitur.yaml`, so running it after the copy keeps the example's as well. Commit again after
every later edit to the score or the cast.

`name:` and every movement `id:` are free to rename, and worth renaming: `status` reports each
movement by its `id`, so until you do, an unrelated task shows up as `name-the-condition`. A
renamed movement `id:` must be renamed wherever `needs:` or `final_movement` names it too. Part
names (`writer`, `verifier`) are the keys the cast binds, so rename them in both files or in
neither.

### On a Claude-only host

Every cast here binds a `codex` performer first. On a host with only the Claude CLI, replace
the `sol` performer with a second `claude` one, so the binding still has a fallback (rule 4):

```yaml
cast: "0.1"
performers:
  opus:
    adapter: claude
    model: claude-opus-5-5
    allow_advisory_enforcement: true
  fable:
    adapter: claude
    model: claude-fable-5
    allow_advisory_enforcement: true
bindings:
  writer: { performer: opus, fallbacks: [fable] }
```

For `gated/`, bind `verifier` the same way. Keep `allow_advisory_enforcement: true` on every
`claude` performer: the `claude` adapter reports `network_grants: false`, so without the flag
`validate` refuses the score, and with it each attempt records an `unmet=["network_grants"]`
advisory instead (rule 5).

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
