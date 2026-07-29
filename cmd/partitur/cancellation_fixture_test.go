package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

const cancellationFixtureOwnerEnvironment = "PARTITUR_CANCELLATION_FIXTURE_OWNER"

type cancellationFixturePredicates struct {
	preparePending bool
	intervalOpen   bool
	matchingLease  bool
}

func TestCancellationFixturePredicatesAndOracleConsequences(t *testing.T) {
	bin := t.TempDir()
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, predicates := range cancellationFixtureCombinations() {
		predicates := predicates
		t.Run(cancellationFixtureName(predicates), func(t *testing.T) {
			repository, _, runID, store := cancellationFixture(t, bin, vendor, predicates, faultpoint.Nop{})
			assertCancellationFixturePredicates(t, repository, runID, store, predicates)
			if err := store.RequestCancellation(runstate.RunID(runID)); err != nil {
				t.Fatal(err)
			}
			if err := store.ExecuteCancellation(context.Background(), runstate.RunID(runID)); err != nil {
				t.Fatal(err)
			}
			assertCancellationFixtureConsequences(t, store, runID, predicates)
		})
	}
}

func TestCancellationFixtureCanBeCancelledBySubprocess(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, predicates := range cancellationFixtureCombinations() {
		predicates := predicates
		t.Run(cancellationFixtureName(predicates), func(t *testing.T) {
			repository, environment, runID, store := cancellationFixture(t, bin, vendor, predicates, faultpoint.Nop{})
			code, stdout, stderr := runCommandBinaryWithin(t, 15*time.Second, partitur, repository, environment, "cancel", runID)
			if code != 4 || stdout != "" || stderr != "" {
				t.Fatalf("cancel exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			input, err := store.LoadRecoveryInput(runstate.RunID(runID))
			if err != nil {
				t.Fatal(err)
			}
			if input.Projection.State.Run != runstate.RunCancelled {
				t.Fatalf("subprocess state=%q, want CANCELLED", input.Projection.State.Run)
			}
		})
	}
}

func TestCancellationSubprocessEmitsOrderedFaultpoints(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository, environment, runID, _ := cancellationFixture(t, bin, vendor, cancellationFixturePredicates{
		preparePending: true,
		intervalOpen:   true,
		matchingLease:  true,
	}, faultpoint.Nop{})

	want := []faultpoint.PointID{
		faultpoint.PointCancelSessionsSwept,
		faultpoint.PointCancelSnapshotQuarantined,
		faultpoint.PointCancelExecutionStopped,
		faultpoint.PointCancelFenceDecided,
		faultpoint.PointCancelRunCancelled,
		faultpoint.PointCancelLeaseRemoved,
	}
	got, code, stdout, stderr := cancellationSubprocessPoints(t, partitur, repository, environment, runID, want)
	if strings.Join(pointStrings(got), ",") != strings.Join(pointStrings(want), ",") {
		t.Fatalf("cancel faultpoint sequence=%v, want %v", got, want)
	}
	if code != 4 || stdout != "" || stderr != "" {
		t.Fatalf("cancel exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func cancellationSubprocessPoints(
	t *testing.T,
	binary, repository string,
	environment []string,
	runID string,
	want []faultpoint.PointID,
) ([]faultpoint.PointID, int, string, string) {
	t.Helper()
	notifyRead, notifyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer notifyRead.Close()
	defer notifyWrite.Close()
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseRead.Close()
	defer releaseWrite.Close()

	files := make([]*os.File, 0, 8)
	for range 6 {
		file, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
		defer file.Close()
	}
	files = append(files, notifyWrite, releaseRead)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "cancel", runID)
	command.Dir = repository
	command.Env = replaceEnvironment(environment, map[string]string{
		"PARTITUR_FAULTPOINT_NOTIFY_FD":  "9",
		"PARTITUR_FAULTPOINT_RELEASE_FD": "10",
	})
	command.ExtraFiles = files
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = command.Wait()
	}()
	_ = notifyWrite.Close()
	_ = releaseRead.Close()

	scanner := bufio.NewScanner(notifyRead)
	got := make([]faultpoint.PointID, 0, len(want))
	for _, expected := range want {
		point, pid := nextCancellationSubprocessPoint(t, scanner)
		if point != expected {
			t.Fatalf("cancel faultpoint=%q, want %q", point, expected)
		}
		if pid != command.Process.Pid {
			t.Fatalf("cancel faultpoint %q pid=%d, want subprocess pid=%d", point, pid, command.Process.Pid)
		}
		got = append(got, point)
		if _, err := releaseWrite.Write([]byte{1}); err != nil {
			t.Fatalf("release cancellation point %q: %v", point, err)
		}
	}
	if err := command.Wait(); err != nil {
		exitError, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		if exitError.ExitCode() != 4 {
			t.Fatalf("cancel exited %d after faultpoint sequence\nstdout:\n%s\nstderr:\n%s", exitError.ExitCode(), &stdout, &stderr)
		}
	} else {
		t.Fatalf("cancel exited successfully after faultpoint sequence\nstdout:\n%s\nstderr:\n%s", &stdout, &stderr)
	}
	if scanner.Scan() {
		t.Fatalf("cancel emitted unexpected faultpoint notification %q", scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read cancel faultpoint notifications: %v", err)
	}
	return got, 4, stdout.String(), stderr.String()
}

func nextCancellationSubprocessPoint(t *testing.T, scanner *bufio.Scanner) (faultpoint.PointID, int) {
	t.Helper()
	type reached struct {
		point faultpoint.PointID
		pid   int
		err   error
	}
	ready := make(chan reached, 1)
	go func() {
		if !scanner.Scan() {
			ready <- reached{err: scanner.Err()}
			return
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			ready <- reached{err: fmt.Errorf("malformed probe notification %q", scanner.Text())}
			return
		}
		pid, err := strconv.Atoi(fields[1])
		ready <- reached{point: faultpoint.PointID(fields[0]), pid: pid, err: err}
	}()
	select {
	case result := <-ready:
		if result.err != nil || result.point == "" || result.pid <= 0 {
			t.Fatalf("cancel probe ended before a cancellation point: %#v", result)
		}
		return result.point, result.pid
	case <-time.After(15 * time.Second):
		t.Fatal("cancel did not emit a cancellation faultpoint within 15s")
		return "", 0
	}
}

func pointStrings(points []faultpoint.PointID) []string {
	strings := make([]string, len(points))
	for index, point := range points {
		strings[index] = string(point)
	}
	return strings
}

func TestCancellationPointsFollowDurabilityReceipts(t *testing.T) {
	bin := t.TempDir()
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	probe := &cancellationReceiptProbe{t: t}
	repository, _, runID, store := cancellationFixture(t, bin, vendor, cancellationFixturePredicates{
		preparePending: true,
		intervalOpen:   true,
		matchingLease:  true,
	}, probe)
	probe.runRoot = filepath.Join(repository, ".partitur", "runs", runID)
	if err := store.RequestCancellation(runstate.RunID(runID)); err != nil {
		t.Fatal(err)
	}
	if err := store.ExecuteCancellation(context.Background(), runstate.RunID(runID)); err != nil {
		t.Fatal(err)
	}
	for _, point := range []faultpoint.PointID{
		faultpoint.PointCancelSnapshotQuarantined,
		faultpoint.PointCancelExecutionStopped,
		faultpoint.PointCancelFenceDecided,
		faultpoint.PointCancelRunCancelled,
		faultpoint.PointCancelLeaseRemoved,
	} {
		if !probe.seen[point] {
			t.Fatalf("cancellation did not reach %q", point)
		}
	}
}

func TestCancellationOracleRequiresCurrentLeaseEpoch(t *testing.T) {
	bin := t.TempDir()
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, _, runID, store := cancellationFixture(t, bin, vendor, cancellationFixturePredicates{}, faultpoint.Nop{})
	lease, present, err := store.ReadLease(runstate.RunID(runID))
	if err != nil || !present {
		t.Fatalf("fixture lease present=%t err=%v", present, err)
	}
	replacement := lease
	replacement.Epoch += 2
	if err := store.Mutate(runstate.RunID(runID), "", func(transaction *runstore.Txn) error {
		if _, err := transaction.At("fixture.future_lease.remove").CompareRemoveLease(lease.Identity()); err != nil {
			return err
		}
		_, err := transaction.At("fixture.future_lease.create").CreateLease(true, replacement)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RequestCancellation(runstate.RunID(runID)); err != nil {
		t.Fatal(err)
	}
	if err := store.ExecuteCancellation(context.Background(), runstate.RunID(runID)); err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	terminal := journal.Events[len(journal.Events)-1]
	var payload map[string]any
	if err := json.Unmarshal(terminal.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if _, fenced := payload["fenced_epoch"]; fenced {
		t.Fatalf("future lease fenced current epoch: payload=%v", payload)
	}
	remaining, present, err := store.ReadLease(runstate.RunID(runID))
	if err != nil || !present || remaining.Epoch != replacement.Epoch {
		t.Fatalf("future lease present=%t lease=%+v err=%v", present, remaining, err)
	}
}

func cancellationFixtureCombinations() []cancellationFixturePredicates {
	var combinations []cancellationFixturePredicates
	for _, preparePending := range []bool{false, true} {
		for _, intervalOpen := range []bool{false, true} {
			for _, matchingLease := range []bool{false, true} {
				combinations = append(combinations, cancellationFixturePredicates{preparePending, intervalOpen, matchingLease})
			}
		}
	}
	return combinations
}

func cancellationFixtureName(predicates cancellationFixturePredicates) string {
	return fmt.Sprintf("b=%t/c=%t/d=%t", predicates.preparePending, predicates.intervalOpen, predicates.matchingLease)
}

func cancellationFixture(
	t *testing.T,
	bin, vendor string,
	predicates cancellationFixturePredicates,
	probe faultpoint.Probe,
) (string, []string, string, *runstore.Store) {
	t.Helper()
	repository, environment := killHarnessRepository(t, bin, vendor)
	store, err := runstore.New(repository, probe)
	if err != nil {
		t.Fatal(err)
	}

	const runID = "run-1"
	snapshot := resumeScore(1, "cancellation fixture")
	compiled, diagnostics := score.Compile(snapshot)
	if len(diagnostics) != 0 {
		t.Fatalf("score diagnostics=%v", diagnostics)
	}
	scoreHash, err := compiled.Hash()
	if err != nil {
		t.Fatal(err)
	}
	resolvedCast := []byte("cast: \"0.1\"\nperformers: {}\nbindings: {}\n")
	resolved, castDiagnostics := cast.Resolve([]cast.Layer{{Origin: "fixture", Data: resolvedCast}})
	if len(castDiagnostics) != 0 {
		t.Fatalf("cast diagnostics=%v", castDiagnostics)
	}
	castHash, err := resolved.Hash()
	if err != nil {
		t.Fatal(err)
	}
	owner := cancellationFixtureGoneOwner(t)
	const authorityEpoch = uint64(2)
	leaseEpoch := authorityEpoch - 1
	if predicates.matchingLease {
		leaseEpoch = authorityEpoch
	}
	lease := runstore.Lease{Epoch: leaseEpoch, Token: "cancellation-fixture", PID: owner.pid, Start: owner.start}

	if err := store.Mutate(runID, "", func(transaction *runstore.Txn) error {
		if _, err := transaction.At("fixture.score").PublishImmutable("scores/revision-1.yaml", snapshot, runstore.Hash(cancellationFixtureHash(snapshot))); err != nil {
			return err
		}
		if _, err := transaction.At("fixture.cast").PublishImmutable("resolved-cast.yaml", resolvedCast, runstore.Hash(cancellationFixtureHash(resolvedCast))); err != nil {
			return err
		}
		if _, err := transaction.At("fixture.started").Append(runstate.Event{
			RunID: runID, ScoreRevision: 1, Type: runstate.EventRunStarted,
			Payload: cancellationFixturePayload(t, map[string]any{
				"base_commit": "base", "base_tree": "tree", "score_hash": scoreHash,
				"score_file_hash": cancellationFixtureHash(snapshot), "resolved_cast_hash": castHash,
				"identity_versions": resumeIdentityVersions(),
			}),
		}); err != nil {
			return err
		}
		if _, err := transaction.At("fixture.authority").Append(runstate.Event{
			RunID: runID, ScoreRevision: 1, Type: runstate.EventAuthorityGranted,
			Payload: cancellationFixturePayload(t, map[string]any{
				"authority_epoch": authorityEpoch, "owner_pid": owner.pid,
				"owner_start_identity": cancellationFixtureStartIdentity(t, owner.start),
			}),
		}); err != nil {
			return err
		}
		if _, err := transaction.At("fixture.lease").CreateLease(true, lease); err != nil {
			return err
		}
		if predicates.preparePending {
			return appendCancellationFixturePrepare(t, transaction, runID, compiled, authorityEpoch)
		}
		if predicates.intervalOpen {
			return appendCancellationFixtureInterval(t, transaction, runID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if predicates.preparePending && predicates.intervalOpen {
		if err := store.Mutate(runID, "", func(transaction *runstore.Txn) error {
			return appendCancellationFixtureInterval(t, transaction, runID)
		}); err != nil {
			t.Fatal(err)
		}
	}
	return repository, environment, runID, store
}

func appendCancellationFixturePrepare(
	t *testing.T,
	transaction *runstore.Txn,
	runID string,
	baseScore *score.Score,
	observedEpoch uint64,
) error {
	t.Helper()
	nextSnapshot := resumeScore(2, "cancellation fixture pending prepare")
	nextScore, diagnostics := score.Compile(nextSnapshot)
	if len(diagnostics) != 0 {
		t.Fatalf("next score diagnostics=%v", diagnostics)
	}
	nextScoreHash, err := nextScore.Hash()
	if err != nil {
		t.Fatal(err)
	}
	baseScoreHash, err := baseScore.Hash()
	if err != nil {
		t.Fatal(err)
	}
	plan := []byte("cancellation fixture plan\n")
	if _, err := transaction.At("fixture.prepare.snapshot").PublishImmutable("scores/revision-2.yaml", nextSnapshot, runstore.Hash(cancellationFixtureHash(nextSnapshot))); err != nil {
		return err
	}
	if _, err := transaction.At("fixture.prepare.plan").PublishImmutable("prepares/prepare-1.json", plan, runstore.Hash(cancellationFixtureHash(plan))); err != nil {
		return err
	}
	if _, err := transaction.At("fixture.prepare.sidecar").PublishImmutable("driver.quiesced.prepare-1", []byte("quiesced\n"), runstore.Hash(cancellationFixtureHash([]byte("quiesced\n")))); err != nil {
		return err
	}
	_, err = transaction.At("fixture.prepare.recorded").Append(runstate.Event{
		RunID: runstate.RunID(runID), ScoreRevision: 1, Type: runstate.EventAmendmentApprovalPrepared,
		Payload: cancellationFixturePayload(t, map[string]any{
			"prepare_id": "prepare-1", "proposal_id": "proposal-1", "mode": "auto", "envelope_class": "NARROW_PATHS",
			"base_revision": 1, "base_hash": baseScoreHash, "new_revision": 2, "new_snapshot_hash": nextScoreHash,
			"new_snapshot_file_hash": cancellationFixtureHash(nextSnapshot), "plan_record_hash": cancellationFixtureHash(plan),
			"target_attempt_ids": []any{}, "observed_authority_epoch": observedEpoch,
			"quiesce_deadline": "2026-07-29T00:00:00.000Z", "classifier_version": 1,
			"identity_versions": resumeIdentityVersions(),
		}),
	})
	return err
}

func appendCancellationFixtureInterval(t *testing.T, transaction *runstore.Txn, runID string) error {
	t.Helper()
	_, err := transaction.At("fixture.execution.started").Append(runstate.Event{
		RunID: runstate.RunID(runID), ScoreRevision: 1, Type: runstate.EventExecutionStarted,
		Payload: cancellationFixturePayload(t, map[string]any{
			"interval_id": "interval-1", "phase": "fixture", "wall_start": "2026-07-29T00:00:00.000Z", "remaining_at_start": 600000,
		}),
	})
	return err
}

func assertCancellationFixturePredicates(
	t *testing.T,
	repository, runID string,
	store *runstore.Store,
	predicates cancellationFixturePredicates,
) {
	t.Helper()
	input, err := store.LoadRecoveryInput(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	state := input.Projection.State
	if (state.PendingPrepare != nil) != predicates.preparePending {
		t.Fatalf("pending prepare=%+v want %t", state.PendingPrepare, predicates.preparePending)
	}
	runRoot := filepath.Join(repository, ".partitur", "runs", runID)
	for _, path := range []string{"scores/revision-2.yaml", "prepares/prepare-1.json", "driver.quiesced.prepare-1"} {
		_, err := os.Stat(filepath.Join(runRoot, path))
		if predicates.preparePending && err != nil {
			t.Fatalf("pending prepare file %q: %v", path, err)
		}
		if !predicates.preparePending && !os.IsNotExist(err) {
			t.Fatalf("unexpected prepare file %q: %v", path, err)
		}
	}
	if (state.OpenExecution != nil) != predicates.intervalOpen {
		t.Fatalf("open execution=%+v want %t", state.OpenExecution, predicates.intervalOpen)
	}
	lease, present, err := store.ReadLease(runstate.RunID(runID))
	if err != nil || !present {
		t.Fatalf("fixture lease present=%t err=%v", present, err)
	}
	if (lease.Epoch == state.Authority.Epoch) != predicates.matchingLease {
		t.Fatalf("lease epoch=%d authority epoch=%d matching=%t want %t", lease.Epoch, state.Authority.Epoch, lease.Epoch == state.Authority.Epoch, predicates.matchingLease)
	}
	if result := lease.MatchOwner(); result.Status != procid.GoneOrReused {
		t.Fatalf("fixture lease owner=%+v, want verifiably gone", result)
	}
}

func assertCancellationFixtureConsequences(t *testing.T, store *runstore.Store, runID string, predicates cancellationFixturePredicates) {
	t.Helper()
	journal, err := store.ReadJournal(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	var abandoned, stopped int
	var cancelled runstate.Event
	for _, event := range journal.Events {
		switch event.Type {
		case runstate.EventAmendmentApprovalAbandoned:
			abandoned++
		case runstate.EventExecutionStopped:
			stopped++
			var payload map[string]any
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["reason"] != "cancelled" || payload["charging"] != "clamped" {
				t.Fatalf("execution.stopped payload=%v", payload)
			}
		case runstate.EventRunCancelled:
			cancelled = event
		}
	}
	if (abandoned == 1) != predicates.preparePending {
		t.Fatalf("approval_abandoned=%d want iff b=%t", abandoned, predicates.preparePending)
	}
	if (stopped == 1) != predicates.intervalOpen {
		t.Fatalf("execution.stopped=%d want iff c=%t", stopped, predicates.intervalOpen)
	}
	if cancelled.Type == "" {
		t.Fatal("run.cancelled is absent")
	}
	var payload map[string]any
	if err := json.Unmarshal(cancelled.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	_, fenced := payload["fenced_epoch"]
	if fenced != predicates.matchingLease {
		t.Fatalf("run.cancelled payload=%v fenced_epoch=%t want iff d=%t", payload, fenced, predicates.matchingLease)
	}
	_, present, err := store.ReadLease(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	if present == predicates.matchingLease {
		t.Fatalf("lease present=%t want removed iff d=%t", present, predicates.matchingLease)
	}
}

type cancellationReceiptProbe struct {
	t       *testing.T
	runRoot string
	seen    map[faultpoint.PointID]bool
}

func (probe *cancellationReceiptProbe) Reached(point faultpoint.PointID) {
	probe.t.Helper()
	if probe.seen == nil {
		probe.seen = make(map[faultpoint.PointID]bool)
	}
	probe.seen[point] = true
	journal, err := os.ReadFile(filepath.Join(probe.runRoot, "journal.jsonl"))
	if err != nil {
		probe.t.Fatalf("read journal at %q: %v", point, err)
	}
	has := func(eventType string) bool { return stringContainsJournalEvent(journal, eventType) }
	switch point {
	case faultpoint.PointCancelSnapshotQuarantined:
		if _, err := os.Stat(filepath.Join(probe.runRoot, "scores", "revision-2.yaml")); !os.IsNotExist(err) {
			probe.t.Fatalf("snapshot still present at %q: %v", point, err)
		}
		for _, path := range []string{"prepares/prepare-1.json", "driver.quiesced.prepare-1"} {
			if _, err := os.Stat(filepath.Join(probe.runRoot, path)); err != nil {
				probe.t.Fatalf("%q removed before %q: %v", path, point, err)
			}
		}
	case faultpoint.PointCancelExecutionStopped:
		if !has(string(runstate.EventExecutionStopped)) || has(string(runstate.EventRunCancelled)) {
			probe.t.Fatalf("journal at %q does not stop before terminalization: %s", point, journal)
		}
	case faultpoint.PointCancelFenceDecided:
		if has(string(runstate.EventRunCancelled)) {
			probe.t.Fatalf("fence decision at %q has durable terminal output: %s", point, journal)
		}
	case faultpoint.PointCancelRunCancelled:
		if !has(string(runstate.EventRunCancelled)) {
			probe.t.Fatalf("terminalization is not durable at %q: %s", point, journal)
		}
		if _, err := os.Stat(filepath.Join(probe.runRoot, "driver.lease")); err != nil {
			probe.t.Fatalf("lease removed before %q: %v", point, err)
		}
	case faultpoint.PointCancelLeaseRemoved:
		if !has(string(runstate.EventRunCancelled)) {
			probe.t.Fatalf("lease removal lacks durable terminalization at %q: %s", point, journal)
		}
		if _, err := os.Stat(filepath.Join(probe.runRoot, "driver.lease")); !os.IsNotExist(err) {
			probe.t.Fatalf("lease still present at %q: %v", point, err)
		}
	}
}

func stringContainsJournalEvent(journal []byte, eventType string) bool {
	return bytes.Contains(journal, []byte(`"type":"`+eventType+`"`))
}

type cancellationFixtureOwner struct {
	pid   int
	start runstate.StartIdentity
}

func cancellationFixtureGoneOwner(t *testing.T) cancellationFixtureOwner {
	t.Helper()
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readyRead.Close()
	defer readyWrite.Close()
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseRead.Close()
	defer releaseWrite.Close()
	command := exec.Command(os.Args[0], "-test.run=TestCancellationFixtureOwnerHelper")
	command.Env = append(os.Environ(), cancellationFixtureOwnerEnvironment+"=1")
	command.ExtraFiles = []*os.File{readyWrite, releaseRead}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = readyWrite.Close()
	_ = releaseRead.Close()
	var ready [1]byte
	if _, err := io.ReadFull(readyRead, ready[:]); err != nil {
		t.Fatalf("fixture owner readiness: %v", err)
	}
	start, err := procid.Read(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := releaseWrite.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := waitCancellationFixtureOwner(command); err != nil {
		t.Fatal(err)
	}
	return cancellationFixtureOwner{pid: command.Process.Pid, start: start}
}

func TestCancellationFixtureOwnerHelper(t *testing.T) {
	if os.Getenv(cancellationFixtureOwnerEnvironment) != "1" {
		return
	}
	ready := os.NewFile(uintptr(3), "fixture-owner-ready")
	release := os.NewFile(uintptr(4), "fixture-owner-release")
	if ready == nil || release == nil {
		os.Exit(96)
	}
	if _, err := ready.Write([]byte{1}); err != nil {
		os.Exit(97)
	}
	var signal [1]byte
	if _, err := io.ReadFull(release, signal[:]); err != nil {
		os.Exit(98)
	}
}

func waitCancellationFixtureOwner(command *exec.Cmd) error {
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		return fmt.Errorf("fixture owner did not exit after release")
	}
}

func cancellationFixtureStartIdentity(t *testing.T, identity runstate.StartIdentity) map[string]any {
	t.Helper()
	switch value := identity.(type) {
	case runstate.LinuxStartIdentity:
		return map[string]any{"platform": "linux", "boot_id": value.BootID, "start_ticks": value.StartTicks}
	case runstate.DarwinStartIdentity:
		return map[string]any{"platform": "darwin", "start_tvsec": value.StartTVSec, "start_tvusec": value.StartTVUsec}
	default:
		t.Fatalf("unsupported fixture owner identity %T", identity)
		return nil
	}
}

func cancellationFixturePayload(t *testing.T, payload any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func cancellationFixtureHash(value []byte) string {
	digest := sha256.Sum256(value)
	return fmt.Sprintf("sha256:%x", digest)
}
