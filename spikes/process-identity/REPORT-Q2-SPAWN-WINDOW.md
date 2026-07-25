# SPIKE-4 Question 2 — adapter spawn window

Date: 2026-07-25

Normative input: `docs/DESIGN.md` at DESIGN v0.2. This report does not amend it.

## Verdict

**Raw spawn-first must change. The event taxonomy does not need another event.**

The recoverable ordering is to spawn a trusted Partitur launch trampoline, hold it behind an
inherited gate, fsync `attempt.started` with the trampoline's identity, and only then release it to
`exec` the adapter in place.

## Measured ordering

```text
performer.selected already durable
→ create attempt-local gate and marker
→ spawn trusted trampoline with setsid()
→ trampoline publishes PID + SID + start identity and blocks on gate
→ core verifies identity
→ append + fsync attempt.started
→ core writes GO
→ trampoline closes gate and execs adapter in place
```

`exec` preserves the process identity being recorded. The trampoline contains no adapter code and
has no worktree mutation path before `GO`.

| Case | macOS 26.5.2 arm64 | Linux 6.12.76 arm64 | Result |
|---|---:|---:|---|
| Parent dies before `attempt.started` | Target ran: no; trampoline exited on EOF | Target ran: no; trampoline exited on EOF | Safe |
| Parent dies after `attempt.started`, before `GO` | Recorded identity recoverable; target ran: no | Recorded identity recoverable; target ran: no | Safe |
| Identity before/after trampoline `exec` | PID, SID, start identity identical | PID, SID, start identity identical | Safe |
| Ungated child after parent exit | Reparented to PPID 1 | Reparented to PPID 1 | Parent attribution is unusable |
| Inherited lock marker after parent exit | Lock remained held by child | Lock remained held by child | Detects a holder, but does not identify its PID/SID |

An environment nonce was also observable through same-user `/proc/<pid>/environ` on Linux and
through `ps eww` on macOS outside the sandbox. macOS sandbox policy denied the same inspection inside
the sandbox. It is therefore a useful diagnostic locator, not portable recovery authority. A
pre-created session does not help: a process cannot join an unrelated POSIX session; the child must
create its own session with `setsid()`, so the SID does not exist before spawn.

## Exact `attempt.started` addition

```text
adapter_process: {
  pid,
  session_id,
  start_identity:
      {platform: "linux", boot_id, start_ticks}
    | {platform: "darwin", start_tvsec, start_tvusec}
}
```

`pid == session_id` is validated at handoff because the trampoline is the session leader. PGID is
not persisted: §4 enumerates the recorded session and discovers its current process groups at sweep
time.

The attempt-local handoff and lock paths are derived from `attempt_id`; they are coordination files,
not journal identity. The handoff repeats a random nonce created with the attempt so a stale file
cannot be accepted.

> **Evidence boundary (added 2026-07-26).** The nonce is **specified, not measured.** The harness
> writes it but never compares it, and no stale handoff was injected. Stale-file rejection is a
> design requirement this spike did not exercise.

## Recovery rule for the crash window

1. `performer.selected` without `attempt.started` means the adapter body was never released.
2. If a handoff identity exists, verify and sweep that trampoline session. If the inherited marker is
   held but no identity can be read, halt `spawn_handoff_unverifiable`; the holder cannot run adapter
   code, but recovery cannot claim it cleaned a process it cannot name.
3. If the marker is free, no launch process survives. Append the existing STARTING-state failure
   (`attempt_never_started`) and schedule from its recorded disposition.

   > **Post-fold correction (2026-07-26).** Item 3 overstates the marker result. The pre-marker
   > window permits an unreleased trampoline to survive while no marker is held. DESIGN §4's
   > normative property is **"marker free ⇒ no *released mutator* survives"**, which is what
   > recovery needs and what the gate earns.

4. `attempt.started` without `performer.completed` always sweeps the recorded session to verified
   empty before failure/retry, whether the recorded leader is live, dead, or already a zombie.
   Inspection failure remains `sweep_unverifiable`.

The gate closes the dangerous ambiguity: an unrecorded trampoline may briefly survive, but an
unrecorded **mutator** cannot.

## Required replacement sentences

Replace §4/§6's implied direct spawn with:

> The core starts a trusted launch trampoline as a new POSIX session leader. While the trampoline is
> blocked on an inherited gate, the core records and fsyncs its PID, session id, and process-start
> identity in `attempt.started`; only then may it release the gate and let the trampoline `exec` the
> adapter in place. EOF before release makes the trampoline exit without executing adapter code.

Replace Appendix C.2's first two process rows with:

> `performer.selected` without `attempt.started` means no adapter body was released; recovery cleans
> a published trampoline identity, halts on a held but unattributable marker, and otherwise records
> `attempt_never_started`. `attempt.started` without `performer.completed` means recovery must sweep
> the recorded adapter session to verified empty before recording
> `attempt_terminated_incomplete`.

## Reproduction

```sh
cd spikes
GOCACHE=/private/tmp/pt-spike4-go-cache \
  go test -v ./process-identity -run \
  'TestGate|TestParentIdentity|TestInheritedLock'

GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  GOCACHE=/private/tmp/pt-spike4-go-cache \
  go test -c -o /private/tmp/process-identity-linux-arm64.test ./process-identity
docker run --rm \
  -v /private/tmp/process-identity-linux-arm64.test:/process-identity.test:ro \
  alpine:3.22 /process-identity.test -test.v
```

## Not determined

- A portable way to map a held inherited file lock back to its holder on macOS. Native
  `proc_pidinfo` FD enumeration could be spiked, but the gate makes it unnecessary for execution
  safety; lack of a published identity already fails closed.
- Power-loss behavior before the handoff file's directory fsync.
- Intel macOS. The Darwin start-identity path was executed only on Apple Silicon.
