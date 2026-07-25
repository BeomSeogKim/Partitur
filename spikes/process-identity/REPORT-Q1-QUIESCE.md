# SPIKE-4 Question 1 — quiesce handshake

Date: 2026-07-25

Normative input: `docs/DESIGN.md` at DESIGN v0.2. This report does not amend it.

## Verdict

**The intended sequence must change.**

`amendment.approved` can remain the only event that changes the score head, revision, and
supersession projection. It cannot remain the only durable event on the approval path. Safe,
deterministic recovery requires one additional preparatory journal event. It does **not** require a
post-approval fencing event.

> **Evidence boundary (added 2026-07-26).** What the model measured is the *shape* of the handshake:
> phase and `prepare_id` transitions under a real lock, and the races tabulated below. Two things
> this report specifies are **designed, not proved.** The exact-incarnation CAS on
> `{epoch, token, pid, start_identity}` in step 3 was not exercised — the model's lease carries no
> PID or start identity, and its acknowledgement compares phase and prepare id only. Full
> crash-restart recovery was likewise reasoned about rather than replayed. DESIGN's readiness table
> records the same limit: the protocol is **not proved**, and the fault-injection harness is the
> instrument that would change that.

## What failed

| Candidate | Measurement | Verdict |
|---|---|---|
| Plain acknowledgement on a separate channel | The acknowledged driver retained an active matching `driver.lease` and its next mutation was accepted | Unsafe |
| Hold the repository state lock while awaiting acknowledgement | The driver cannot take the same lock to close its interval or CAS its lease | Deadlock |
| Downgrade exclusive lock to shared | A competing exclusive writer remained blocked in 100/100 Darwin trials and 100/100 Linux trials | Does not solve the handshake |
| Approval first, fence later | Leaves an interval in which the approved old incarnation still passes the four-part mutation CAS; requires a second authoritative fence state and blocks safe restart | Rejected |
| Durable prepare + prepare-bound lease-CAS acknowledgement | Survived acknowledgement-then-wedge, acknowledgement-then-death, no acknowledgement, and two concurrent approvers | Safe within the measured model |

A plain ACK is not a disposition. The shortest counterexample is:

```text
driver sends ACK
→ driver wedges
→ approver appends amendment.approved without fenced_epoch
→ driver resumes
→ matching ACTIVE driver.lease still lets its mutation CAS pass
```

## Safe sequence

1. Under the repository state lock, re-run the approval checks, verify the base head, reserve the
   run against a second prepare, and append and fsync:

   ```text
   amendment.approval_prepared {
     prepare_id,
     proposal_id,
     mode, decision_id?,
     base_revision, base_hash,
     new_snapshot_hash,
     observed_authority_epoch,
     superseded_attempt_ids
   }
   ```

   This event records approval intent and the exact operation recovery may finish. It changes no
   score head, revision, attempt state, decision state, or candidate binding.

2. The matching driver observes the prepare, stops/drains the adapter, verifies that its session is
   empty, closes any open execution interval with its own monotonic measurement, and barriers all
   driver-authorized writers.

3. Under the state lock, the driver CAS-checks the still-pending `prepare_id` and its exact
   `{epoch, token, pid, start_identity}` lease incarnation. It atomically renames
   `driver.lease` to `driver.quiesced.<prepare_id>` and fsyncs the directory. The rename is the ACK:
   it both publishes the prepare-bound acknowledgement and removes the path every ordinary driver
   mutation requires.

4. The approver or recovery process takes the state lock, rechecks the prepared operation and base
   head, verifies the exact quiesced lease, appends and fsyncs `amendment.approved` once without
   `fenced_epoch`, then removes the quiesced sidecar idempotently.

5. If the ACK deadline expires, terminate and sweep the recorded adapter session outside the lock,
   then reacquire the lock and CAS the exact lease again. If the ACK won the race, take step 4. If the
   same active incarnation remains, close the interval with clamped charging and append
   `amendment.approved` carrying the incremented `fenced_epoch`; that append advances the journaled
   epoch before the stale lease is removed. No later fence event is needed.

6. Only one prepare may be pending per run. The same `prepare_id` is idempotent; another approver is
   busy until the first prepare resolves, then must revalidate its base head and normally loses as
   stale.

## Race results

| Race | Result |
|---|---|
| Driver ACKs then wedges | Safe: the active lease path was gone before ACK became observable; later mutation failed |
| Driver ACKs then dies | Safe: recovery consumed the durable prepare and matching quiesced lease |
| Driver never ACKs | Safe: verified termination/sweep followed by a fence carried by the single approval |
| Two approvers | Exactly one prepare won; the other was rejected before it could append an approval |
| Crash after prepare | Recoverable from `amendment.approval_prepared`; no new driver may acquire while it is pending |
| Crash after ACK | Recoverable from the prepare plus `driver.quiesced.<prepare_id>` |
| Crash after approval fsync | Approval is authoritative; remaining lease-sidecar cleanup is idempotent |

## Required replacement sentences

Replace §6's provisional pre-append disposition rule with:

> Before an approval may change the score head, the core appends and fsyncs
> `amendment.approval_prepared`, bound to the proposal, base head, target attempts, and observed
> authority epoch. A quiesce ACK is accepted only when the driver has verified the adapter session
> empty, closed its interval, stopped every driver-authorized writer, and atomically moved its exact
> matching `driver.lease` to the prepare-bound quiesced path. The approving command then revalidates
> the prepare and base head under the state lock and appends `amendment.approved` once; on timeout it
> fences the still-matching incarnation and carries that epoch in the same approval event.

Replace §9's “only authoritative transition” sentence with:

> `amendment.approval_prepared` is the durable control request and changes no score or lifecycle
> projection. `amendment.approved` remains the only event that changes the snapshot head and revision
> and atomically projects supersession, decision obsoletion, and candidate rebinding.

## Reproduction

```sh
cd spikes
GOCACHE=/private/tmp/pt-spike4-go-cache \
  go test -v ./process-identity -run \
  'TestQuiesce|TestPlainAcknowledgement|TestPrepareMust|TestDowngraded'
```

The same compiled test binary passed in Alpine 3.22.5 on Linux 6.12.76-linuxkit/arm64.

## Not determined

- Power-loss durability of rename plus directory fsync on each filesystem. This needs a
  crash/fault-injection harness, not process-kill tests.
- The acknowledgement deadline and adapter drain grace. These need measurements against real
  adapters.
- Cancellation precedence when `amendment.approval_prepared` is pending. The safe default is that a
  durable cancellation wins, consumes/abandons the prepare, and never launches a replacement driver,
  but the recovery table must state it.
