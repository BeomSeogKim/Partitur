# SPIKE-4 Question 3 — acceptance subprocess identity

Date: 2026-07-25

Normative input: `docs/DESIGN.md` at DESIGN v0.2. This report does not amend it.

## Verdict

**The current payload and recovery rule must change.**

PID plus process-start identity applies to a short-lived criterion, but PID identity alone is not
enough. Each external criterion needs the same gated session-leader handoff and the same verified
session sweep as an adapter.

## Measurements

| Measurement | macOS 26.5.2 arm64 | Linux 6.12.76 arm64 |
|---|---:|---:|
| Gated fast criterion identity captures | 100/100 | 100/100 |
| Immediate identity reads after spawning 500 `true` children | 500/500 | 500/500 |
| Same-PID reuse in that bounded churn | 0 | 0 |
| Duplicate start identity across distinct PIDs | 0 | 490 |
| Orphan heartbeat stopped before synthesized completion | yes | yes |
| Descendant that called `setsid()` survived outer session sweep | yes | yes |

The ungated fast children remained inspectable because an unreaped child retains a PID and zombie
record. That is not a safe ordering: the criterion may mutate before the event is fsynced, and a
different supervisor may reap it. The gate makes event-before-execution an invariant instead of a
timing bet.

Linux start ticks have coarse granularity: 490 of 500 children shared a start identity with a
different PID. PID and start identity are a tuple; neither is sufficient alone. Actual same-PID
reuse was not forced in this environment.

The orphaned heartbeat test demonstrated the required ordering directly:

```text
criterion.started durable
→ criterion mutates
→ driver disappears
→ recovery verifies and sweeps recorded session
→ mutation stops
→ only then synthesize criterion.completed(ERROR)
```

## Exact `criterion.started` addition

```text
criterion_process: {
  pid,
  session_id,
  start_identity:
      {platform: "linux", boot_id, start_ticks}
    | {platform: "darwin", start_tvsec, start_tvusec}
}
```

The core spawns a trusted session-leader trampoline, records this object while it is blocked,
fsyncs `criterion.started`, and releases it to `exec` the criterion command in place.

## Deterministic synthesized completion

Recovery did not observe an exit or a duration. It must not encode invented measurements.

```text
criterion.completed {
  criterion_id,
  criterion_spec_hash,
  subject_tree,
  outcome: ERROR,
  # exit_code absent
  # duration_ms absent
  # output_ref absent
  error_detail: "recovered_without_observed_completion",
  identity_versions
}
```

`duration_ms` therefore becomes required only for an observed completion. Absence means unobserved;
zero remains an actual measured zero-millisecond completion. The fixed `error_detail` makes replay
deterministic and avoids platform error text in identity-bearing state.

## Recovery rule

For `criterion.started` without `criterion.completed`:

1. Verify the recorded process tuple when the leader still exists.
2. Enumerate and sweep the recorded session to verified empty. A dead leader does not skip the
   session sweep because descendants may remain.
3. Halt `sweep_unverifiable` if enumeration or identity checking is denied.
4. Re-verify the full worktree invariant only after the sweep.
5. Append the deterministic synthesized completion above, then append `acceptance.failed` using the
   already-defined deterministic disposition.

The deliberate escape test preserves §4's existing ceiling: a criterion descendant that calls
`setsid()` is no longer selectable by the outer session on either OS. Therefore the design must
either make “criterion commands and descendants MUST NOT create another session” a conformance rule,
or admit that absolute macOS worktree quiescence is unavailable without a privileged containment
backend. Linux cgroup v2 should be used when available.

## Required replacement sentences

Add to §7:

> Every external criterion command is launched through the gated session-leader trampoline.
> `criterion.started` records the trampoline's PID, session id, and process-start identity before the
> gate is released; the trampoline then `exec`s the criterion in place. Criterion commands and their
> descendants MUST NOT create another POSIX session.

Replace Appendix C.3's provisional synthesis row with:

> `criterion.started` without `criterion.completed` first requires the recorded criterion session to
> be swept to verified empty. Recovery then re-verifies the worktree and appends
> `criterion.completed {outcome: ERROR, error_detail:
> "recovered_without_observed_completion"}` with `exit_code`, `duration_ms`, and `output_ref` absent,
> followed by `acceptance.failed`. Unverifiable process state halts recovery.

Change B.3's duration sentence to:

> `duration_ms` is present for an observed criterion completion and absent only for a completion
> synthesized because recovery did not observe process exit.

## Reproduction

```sh
cd spikes
GOCACHE=/private/tmp/pt-spike4-go-cache \
  go test -v ./process-identity -run \
  'TestFastCriterion|TestCriterionRecovery|TestCriterionSessionEscape'
```

The same compiled test binary passed in Alpine 3.22.5 on Linux 6.12.76-linuxkit/arm64.

## Not determined

- Real same-PID reuse. Forcing it needs a dedicated low-`pid_max` PID namespace or sustained PID
  wrap; the bounded 500-process churn observed none.
- Absolute containment of a deliberate session escape on macOS. The experiment confirmed it is not
  provided by portable session enumeration.
- Whether arbitrary existing criterion commands already satisfy the new no-`setsid()` conformance
  requirement. A compatibility survey is needed before making it normative.
