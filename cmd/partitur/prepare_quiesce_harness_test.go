package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

// TestPrepareQuiesceDriverKillCuts is the production-driver E.2 fixture. It
// deliberately stages the control prepare only after authority acquisition:
// that gives the parent a durable prepared state while the driver has not yet
// had an opportunity to observe it. R cuts use the receipt rendezvous; B cuts
// use the existing neutral probe rendezvous.
func TestPrepareQuiesceDriverKillCuts(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("prepare.prepared_to_observed/prepared", func(t *testing.T) {
		repository, environment := killHarnessRepository(t, bin, vendor)
		child, runID := preparedLiveDriver(t, partitur, repository, environment)
		defer child.stop(t)
		assertPendingPrepare(t, repository, runID)
		assertPrepareBarrier(t, repository, runID)
		child.kill(t)
		assertRecoveryFixedPoint(t, partitur, repository, environment, string(runID), nil)
	})

	t.Run("prepare.prepared_to_observed/observed", func(t *testing.T) {
		repository, environment := killHarnessRepository(t, bin, vendor)
		child, runID := preparedLiveDriver(t, partitur, repository, environment)
		defer child.stop(t)
		child.releaseProbe(t)
		child.waitReceipt(t, "prepare.quiesce_observed")
		assertQuiesceRound(t, repository, runID, 1)
		assertNormalLeasePresent(t, repository, runID)
		child.kill(t)
		assertPrepareBarrier(t, repository, runID)
		assertRecoveryFixedPoint(t, partitur, repository, environment, string(runID), nil)
	})

	t.Run("quiesce.observed_to_swept/observed", func(t *testing.T) {
		repository, environment := killHarnessRepository(t, bin, vendor)
		child, runID := preparedLiveDriver(t, partitur, repository, environment)
		defer child.stop(t)
		child.releaseProbe(t)
		child.waitReceipt(t, "prepare.quiesce_observed")
		assertQuiesceRound(t, repository, runID, 1)
		assertNormalLeasePresent(t, repository, runID)
		child.kill(t)
		assertNoQuiesceSidecar(t, repository, runID)
		before := readHarnessJournal(t, repository, string(runID))
		assertRecoveryFixedPoint(t, partitur, repository, environment, string(runID), nil)
		after := readHarnessJournal(t, repository, string(runID))
		if bytes.Count(after, []byte(`"type":"amendment.quiesce_observed"`)) != bytes.Count(before, []byte(`"type":"amendment.quiesce_observed"`)) {
			t.Fatal("resume reset or appended a quiesce receipt after the silent crash")
		}
	})

	t.Run("quiesce.observed_to_swept/swept", func(t *testing.T) {
		repository, environment := killHarnessRepository(t, bin, vendor)
		child, runID := preparedLiveDriver(t, partitur, repository, environment)
		defer child.stop(t)
		child.releaseProbe(t)
		child.waitProbe(t, faultpoint.PointQuiesceSessionsSwept)
		child.kill(t)
		assertNoQuiesceSidecar(t, repository, runID)
		assertRecoveryFixedPoint(t, partitur, repository, environment, string(runID), nil)
	})

	t.Run("quiesce.swept_to_lease_moved/swept", func(t *testing.T) {
		t.Run("matching_lease", func(t *testing.T) {
			repository, environment := killHarnessRepository(t, bin, vendor)
			child, runID := preparedLiveDriver(t, partitur, repository, environment)
			defer child.stop(t)
			child.releaseProbe(t)
			child.waitProbe(t, faultpoint.PointQuiesceSessionsSwept)
			child.kill(t)
			assertNoQuiesceSidecar(t, repository, runID)
			assertNormalLeasePresent(t, repository, runID)

			// No sidecar is the only durable evidence that step 2 completed. The
			// matching-lease recovery row must therefore sweep again before fencing.
			killAtPoint(t, partitur, repository, environment, faultpoint.PointSupersedeSessionsSwept, "resume", string(runID))
			assertPendingPrepare(t, repository, runID)
			assertNoQuiesceSidecar(t, repository, runID)
			assertNormalLeasePresent(t, repository, runID)
			assertRecoveryFixedPoint(t, partitur, repository, environment, string(runID), nil)
			assertPreparedApprovalFenced(t, repository, runID)
		})

		t.Run("no_lease_control", func(t *testing.T) {
			repository, environment := killHarnessRepository(t, bin, vendor)
			child, runID := preparedLiveDriver(t, partitur, repository, environment)
			defer child.stop(t)
			child.releaseProbe(t)
			child.waitProbe(t, faultpoint.PointQuiesceSessionsSwept)
			child.kill(t)
			assertNoQuiesceSidecar(t, repository, runID)
			removeHarnessLease(t, repository, runID)
			assertNoNormalLease(t, repository, runID)

			// The no-lease, no-sidecar row commits directly. It must not take the
			// matching-lease recovery sweep, whose probe is immediately after it.
			resumeWithoutPoint(t, partitur, repository, environment, string(runID), faultpoint.PointSupersedeSessionsSwept)
			assertPreparedApprovalUnfenced(t, repository, runID)
			assertFixedPointReplay(t, partitur, repository, environment, string(runID), readHarnessJournal(t, repository, string(runID)))
		})
	})

	t.Run("quiesce.swept_to_lease_moved/lease_moved", func(t *testing.T) {
		repository, environment := killHarnessRepository(t, bin, vendor)
		child, runID := preparedLiveDriver(t, partitur, repository, environment)
		defer child.stop(t)
		child.releaseProbe(t)
		child.waitReceipt(t, "prepare.ack.lease")
		child.kill(t)
		assertQuiesceSidecar(t, repository, runID)
		assertRecoveryFixedPoint(t, partitur, repository, environment, string(runID), nil)
		assertPreparedApprovalUnfenced(t, repository, runID)
	})

	t.Run("quiesce.lease_moved_to_commit_lock/lease_moved", func(t *testing.T) {
		repository, environment := killHarnessRepository(t, bin, vendor)
		child, runID := preparedLiveDriver(t, partitur, repository, environment)
		defer child.stop(t)
		child.releaseProbe(t)
		child.waitReceipt(t, "prepare.ack.lease")
		child.kill(t)
		assertQuiesceSidecar(t, repository, runID)
		assertRecoveryFixedPoint(t, partitur, repository, environment, string(runID), nil)
	})

	t.Run("quiesce.lease_moved_to_commit_lock/commit_lock", func(t *testing.T) {
		repository, environment := killHarnessRepository(t, bin, vendor)
		child, runID := preparedLiveDriver(t, partitur, repository, environment)
		defer child.stop(t)
		child.releaseProbe(t)
		child.waitReceipt(t, "prepare.ack.lease")
		child.kill(t)
		killAtPoint(t, partitur, repository, environment, faultpoint.PointQuiesceCommitLockHeld, "resume", string(runID))
		assertPendingPrepare(t, repository, runID)
		assertQuiesceSidecar(t, repository, runID)
		assertRecoveryFixedPoint(t, partitur, repository, environment, string(runID), nil)
	})

	t.Run("quiesce.lease_moved_to_commit_lock/lease_moved/cancellation_wins", func(t *testing.T) {
		repository, environment := killHarnessRepository(t, bin, vendor)
		child, runID := preparedLiveDriver(t, partitur, repository, environment)
		defer child.stop(t)
		child.releaseProbe(t)
		child.waitReceipt(t, "prepare.ack.lease")
		child.kill(t)
		assertQuiesceSidecar(t, repository, runID)
		assertPendingPrepare(t, repository, runID)

		code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "cancel", string(runID))
		if code != 4 || stdout != "" || stderr != "" {
			t.Fatalf("cancel after lease-move cut exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		assertPrepareCancellationWins(t, repository, runID)
		assertFixedPointReplay(t, partitur, repository, environment, string(runID), readHarnessJournal(t, repository, string(runID)))
	})

	t.Run("quiesce.lease_moved_to_commit_lock/commit_lock/hash_mismatch_halts", func(t *testing.T) {
		repository, environment := killHarnessRepository(t, bin, vendor)
		child, runID := preparedLiveDriver(t, partitur, repository, environment)
		defer child.stop(t)
		child.releaseProbe(t)
		child.waitReceipt(t, "prepare.ack.lease")
		child.kill(t)
		killAtPoint(t, partitur, repository, environment, faultpoint.PointQuiesceCommitLockHeld, "resume", string(runID))
		assertPendingPrepare(t, repository, runID)
		assertQuiesceSidecar(t, repository, runID)
		corruptPendingPreparePlan(t, repository, runID)
		assertPreparePlanHashMismatchHalt(t, partitur, repository, environment, runID)
	})
}

type liveDriverNotification struct {
	kind string
	name string
	pid  int
	err  error
}

type preparedLiveRun struct {
	command        *exec.Cmd
	probeRelease   *os.File
	receiptRelease *os.File
	notifications  chan liveDriverNotification
	stdout         bytes.Buffer
	stderr         bytes.Buffer
	ended          bool
}

func preparedLiveDriver(t *testing.T, binary, repository string, environment []string) (*preparedLiveRun, runstate.RunID) {
	t.Helper()
	child := startPreparedLiveRun(t, binary, repository, environment)
	child.waitProbe(t, faultpoint.PointAuthorityLeaseCreated)
	runID, err := soleRunID(repository)
	if err != nil {
		child.stop(t)
		t.Fatal(err)
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		child.stop(t)
		t.Fatal(err)
	}
	if err := appendRecoveryControlPrepare(store, runID, repository); err != nil {
		child.stop(t)
		t.Fatal(err)
	}
	return child, runID
}

func startPreparedLiveRun(t *testing.T, binary, repository string, environment []string) *preparedLiveRun {
	t.Helper()
	receiptRead, receiptWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	receiptReleaseRead, receiptReleaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	probeRead, probeWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	probeReleaseRead, probeReleaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	files := make([]*os.File, 0, 10)
	for range 6 {
		file, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	files = append(files, receiptWrite, receiptReleaseRead, probeWrite, probeReleaseRead)
	child := &preparedLiveRun{probeRelease: probeReleaseWrite, receiptRelease: receiptReleaseWrite, notifications: make(chan liveDriverNotification, 16)}
	child.command = exec.Command(binary, "run")
	child.command.Dir = repository
	child.command.Env = replaceEnvironment(environment, map[string]string{
		"PARTITUR_RECEIPT_NOTIFY_FD":     "9",
		"PARTITUR_RECEIPT_RELEASE_FD":    "10",
		"PARTITUR_FAULTPOINT_HARNESS":    "1",
		"PARTITUR_FAULTPOINT_NOTIFY_FD":  "11",
		"PARTITUR_FAULTPOINT_RELEASE_FD": "12",
	})
	child.command.ExtraFiles = files
	child.command.Stdout = &child.stdout
	child.command.Stderr = &child.stderr
	if err := child.command.Start(); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		_ = file.Close()
	}
	go scanLiveDriverNotifications(receiptRead, "receipt", child.notifications)
	go scanLiveDriverNotifications(probeRead, "probe", child.notifications)
	return child
}

func scanLiveDriverNotifications(file *os.File, kind string, output chan<- liveDriverNotification) {
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			output <- liveDriverNotification{kind: kind, err: fmt.Errorf("malformed %s notification %q", kind, scanner.Text())}
			return
		}
		pid, err := strconv.Atoi(fields[1])
		output <- liveDriverNotification{kind: kind, name: fields[0], pid: pid, err: err}
	}
	if err := scanner.Err(); err != nil {
		output <- liveDriverNotification{kind: kind, err: err}
	}
}

func (child *preparedLiveRun) waitProbe(t *testing.T, target faultpoint.PointID) {
	t.Helper()
	child.wait(t, "probe", string(target))
}

func (child *preparedLiveRun) waitReceipt(t *testing.T, target faultpoint.ReceiptAddress) {
	t.Helper()
	child.wait(t, "receipt", string(target))
}

func (child *preparedLiveRun) wait(t *testing.T, kind, target string) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case notification := <-child.notifications:
			if notification.err != nil || notification.pid != child.command.Process.Pid {
				t.Fatalf("%s notification = %+v, want child pid=%d", kind, notification, child.command.Process.Pid)
			}
			if notification.kind == kind && notification.name == target {
				return
			}
			if notification.kind == "probe" {
				child.releaseProbe(t)
			} else {
				child.releaseReceipt(t)
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s %q\nstdout:\n%s\nstderr:\n%s", kind, target, &child.stdout, &child.stderr)
		}
	}
}

func (child *preparedLiveRun) releaseProbe(t *testing.T) {
	t.Helper()
	if _, err := child.probeRelease.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
}

func (child *preparedLiveRun) releaseReceipt(t *testing.T) {
	t.Helper()
	if _, err := child.receiptRelease.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
}

func (child *preparedLiveRun) kill(t *testing.T) {
	t.Helper()
	if child.ended {
		return
	}
	if err := child.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.probeRelease.Close()
	_ = child.receiptRelease.Close()
	if err := child.command.Wait(); err == nil {
		t.Fatalf("prepared driver exited successfully\nstdout:\n%s\nstderr:\n%s", &child.stdout, &child.stderr)
	}
	child.ended = true
}

func (child *preparedLiveRun) stop(t *testing.T) {
	t.Helper()
	if !child.ended {
		child.kill(t)
	}
}

func assertPendingPrepare(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.PendingPrepare == nil {
		t.Fatal("crash stepped past the pending prepare")
	}
}

func assertQuiesceRound(t *testing.T, repository string, runID runstate.RunID, want uint64) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.PendingPrepare == nil || input.Projection.State.PendingPrepare.LatestQuiesceRound != want {
		t.Fatalf("quiesce round = %+v, want %d", input.Projection.State.PendingPrepare, want)
	}
}

func assertNoQuiesceSidecar(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	if _, err := os.Stat(runstorePath(repository, runID, "driver.quiesced.prepare-control")); !os.IsNotExist(err) {
		t.Fatalf("quiesce sidecar before its lease-move receipt = %v", err)
	}
}

func assertQuiesceSidecar(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	if _, err := os.Stat(runstorePath(repository, runID, "driver.quiesced.prepare-control")); err != nil {
		t.Fatalf("quiesce sidecar after lease move: %v", err)
	}
}

func assertNormalLeasePresent(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	if _, err := os.Stat(runstorePath(repository, runID, "driver.lease")); err != nil {
		t.Fatalf("matching normal lease before lease move: %v", err)
	}
}

func assertNoNormalLease(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	if _, err := os.Stat(runstorePath(repository, runID, "driver.lease")); !os.IsNotExist(err) {
		t.Fatalf("normal lease after durable removal = %v", err)
	}
}

func removeHarnessLease(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	lease, present, err := store.ReadLease(runID)
	if err != nil || !present {
		t.Fatalf("read matching lease before control removal: present=%t err=%v", present, err)
	}
	if err := store.Mutate(runID, "", func(transaction *runstore.Txn) error {
		_, err := transaction.At("quiesce.swept_to_lease_moved/no_lease_control").CompareRemoveLease(lease.Identity())
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func assertPreparedApprovalFenced(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	assertPreparedApprovalFence(t, repository, runID, true)
}

func assertPreparedApprovalUnfenced(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	assertPreparedApprovalFence(t, repository, runID, false)
}

func assertPreparedApprovalFence(t *testing.T, repository string, runID runstate.RunID, wantFenced bool) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range journal.Events {
		if event.Type != runstate.EventAmendmentApproved {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		_, fenced := payload["fenced_epoch"]
		if fenced != wantFenced {
			t.Fatalf("prepared approval fenced=%t, want %t: %s", fenced, wantFenced, event.Payload)
		}
		return
	}
	t.Fatal("recovery did not append amendment.approved")
}

func assertPrepareCancellationWins(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.Run != runstate.RunCancelled || input.Projection.State.PendingPrepare != nil {
		t.Fatalf("cancellation after lease move state=%+v", input.Projection.State)
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	var abandoned int
	for _, event := range journal.Events {
		if event.Type == runstate.EventAmendmentApproved {
			t.Fatal("matching sidecar committed after cancellation")
		}
		if event.Type != runstate.EventAmendmentApprovalAbandoned {
			continue
		}
		abandoned++
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["reason"] != "cancelled" {
			t.Fatalf("cancellation abandonment payload=%v", payload)
		}
	}
	if abandoned != 1 {
		t.Fatalf("cancellation abandonment events=%d, want one", abandoned)
	}
}

func corruptPendingPreparePlan(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	prepare := input.Projection.State.PendingPrepare
	if prepare == nil {
		t.Fatal("cannot corrupt a completed prepare plan")
	}
	path := runstorePath(repository, runID, filepath.Join("prepares", string(prepare.ID)+".json"))
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(contents, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertPreparePlanHashMismatchHalt(t *testing.T, binary, repository string, environment []string, runID runstate.RunID) {
	t.Helper()
	journal := readHarnessJournal(t, repository, string(runID))
	for attempt := 0; attempt < 2; attempt++ {
		code, stdout, stderr := runCommandBinary(t, binary, repository, environment, "resume", string(runID))
		if code != 5 || stdout != "" || !strings.Contains(stderr, `reason="missing_prepare_plan"`) {
			t.Fatalf("hash-mismatched prepare resume exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if replay := readHarnessJournal(t, repository, string(runID)); !bytes.Equal(replay, journal) {
			t.Fatal("hash-mismatched prepare resume appended durable state")
		}
		assertPendingPrepare(t, repository, runID)
		assertQuiesceSidecar(t, repository, runID)
		assertNoEvent(t, repository, runID, runstate.EventAmendmentApproved)
		assertNoEvent(t, repository, runID, runstate.EventAmendmentApprovalAbandoned)
	}
}

func resumeWithoutPoint(
	t *testing.T,
	binary, repository string,
	environment []string,
	runID string,
	forbidden faultpoint.PointID,
) {
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
	var stdout, stderr bytes.Buffer
	command := exec.Command(binary, "resume", runID)
	command.Dir = repository
	command.Env = replaceEnvironment(environment, map[string]string{
		"PARTITUR_FAULTPOINT_HARNESS":    "1",
		"PARTITUR_FAULTPOINT_NOTIFY_FD":  "9",
		"PARTITUR_FAULTPOINT_RELEASE_FD": "10",
	})
	command.ExtraFiles = files
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = notifyWrite.Close()
	_ = releaseRead.Close()

	type result struct {
		err error
	}
	done := make(chan result, 1)
	go func() { done <- result{err: command.Wait()} }()
	notifications := make(chan faultpoint.PointID, 1)
	go func() {
		scanner := bufio.NewScanner(notifyRead)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) == 2 {
				notifications <- faultpoint.PointID(fields[0])
			}
		}
		close(notifications)
	}()

	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		select {
		case point, ok := <-notifications:
			if !ok {
				notifications = nil
				continue
			}
			if point == forbidden {
				_ = command.Process.Kill()
				<-done
				t.Fatalf("resume reached forbidden recovery point %q", forbidden)
			}
			if _, err := releaseWrite.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
		case result := <-done:
			if result.err != nil || stdout.String() != "" || stderr.String() != "" {
				t.Fatalf("resume avoiding %q: err=%v stdout=%q stderr=%q", forbidden, result.err, stdout.String(), stderr.String())
			}
			return
		case <-timer.C:
			_ = command.Process.Kill()
			<-done
			t.Fatalf("timed out waiting for resume avoiding %q", forbidden)
		}
	}
}

func assertPrepareBarrier(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Mutate(runID, "", func(transaction *runstore.Txn) error {
		event := runstate.Event{
			RunID: runID, ScoreRevision: input.Projection.State.ScoreHead.Revision,
			Type: runstate.EventRunFailed, Payload: []byte(`{"reason":"fixture"}`),
		}
		if _, err := runstate.Apply(input.Projection.State, event); err != nil {
			return err
		}
		_, err := transaction.At("prepare.barrier.ordinary_mutation").Append(event)
		return err
	})
	if !errors.Is(err, runstate.ErrIllegalTransition) || !strings.Contains(err.Error(), "prepare_pending") {
		t.Fatalf("ordinary mutation while prepare is pending = %v, want prepare_pending refusal", err)
	}
}

// This is a harness capability exercise, not an E.2 crash fixture. It proves
// that a parent can inject a control prepare after the child acquired authority
// and before it begins normal driver work. The B3 fixture owns the crash cuts.
func TestParentInjectsPrepareIntoPausedLiveDriver(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository, environment := killHarnessRepository(t, bin, vendor)
	child := pauseRunAtPoint(t, partitur, repository, environment, faultpoint.PointAuthorityLeaseCreated)
	defer child.stop(t)

	runID, err := soleRunID(repository)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.Authority.Owner == nil {
		t.Fatal("paused driver has no authority owner")
	}
	if err := appendRecoveryControlPrepare(store, runID, repository); err != nil {
		t.Fatal(err)
	}

	child.releaseAndExpect(t, []faultpoint.PointID{
		faultpoint.PointPrepareObserved,
		faultpoint.PointQuiesceSessionsSwept,
		faultpoint.PointQuiesceLeaseMoved,
	})
	if child.stdout.String() != string(runID)+"\n" || child.stderr.Len() != 0 {
		t.Fatalf("run stdout=%q stderr=%q", child.stdout.String(), child.stderr.String())
	}
	input, err = store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	prepare := input.Projection.State.PendingPrepare
	if prepare == nil || prepare.ObservedAuthorityEpoch != input.Projection.State.Authority.Epoch {
		t.Fatalf("pending prepare=%+v authority=%+v", prepare, input.Projection.State.Authority)
	}
	if _, err := os.Stat(runstorePath(repository, runID, "driver.quiesced."+string(prepare.ID))); err != nil {
		t.Fatalf("quiesced sidecar: %v", err)
	}
}

type pausedRun struct {
	command      *exec.Cmd
	notify       *os.File
	release      *os.File
	scanner      *bufio.Scanner
	stdout       bytes.Buffer
	stderr       bytes.Buffer
	processPID   int
	receipts     []string
	paused       bool
	commandEnded bool
}

func pauseRunAtReceipt(t *testing.T, binary, repository string, environment []string, target faultpoint.ReceiptAddress) *pausedRun {
	return pauseCommandAtReceipt(t, binary, repository, environment, target, "run")
}

func pauseCommandAtReceipt(t *testing.T, binary, repository string, environment []string, target faultpoint.ReceiptAddress, arguments ...string) *pausedRun {
	t.Helper()
	notifyRead, notifyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	files := make([]*os.File, 0, 8)
	for range 6 {
		file, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	files = append(files, notifyWrite, releaseRead)
	child := &pausedRun{notify: notifyRead, release: releaseWrite, scanner: bufio.NewScanner(notifyRead)}
	child.command = exec.Command(binary, arguments...)
	child.command.Dir = repository
	child.command.Env = replaceEnvironment(environment, map[string]string{
		"PARTITUR_RECEIPT_NOTIFY_FD":  "9",
		"PARTITUR_RECEIPT_RELEASE_FD": "10",
	})
	child.command.ExtraFiles = files
	child.command.Stdout = &child.stdout
	child.command.Stderr = &child.stderr
	if err := child.command.Start(); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		defer file.Close()
	}
	if err := notifyWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := releaseRead.Close(); err != nil {
		t.Fatal(err)
	}
	child.processPID = child.command.Process.Pid
	for child.scanner.Scan() {
		fields := bytes.Fields(child.scanner.Bytes())
		if len(fields) != 2 {
			t.Fatalf("receipt rendezvous = %q", child.scanner.Text())
		}
		pid, err := strconv.Atoi(string(fields[1]))
		if err != nil || pid != child.processPID {
			t.Fatalf("receipt rendezvous pid=%q, want child pid=%d", fields[1], child.processPID)
		}
		if faultpoint.ReceiptAddress(fields[0]) == target {
			child.paused = true
			return child
		}
		child.receipts = append(child.receipts, string(fields[0]))
		if _, err := child.release.Write([]byte{1}); err != nil {
			t.Fatalf("release receipt %q: %v", fields[0], err)
		}
	}
	if err := child.scanner.Err(); err != nil {
		t.Fatal(err)
	}
	err = child.command.Wait()
	journal, _ := os.ReadFile(filepath.Join(repository, ".partitur", "runs", strings.TrimSpace(child.stdout.String()), "journal.jsonl"))
	t.Fatalf("run ended before receipt rendezvous %q; saw=%v: %v\nstdout:\n%s\nstderr:\n%s\njournal:\n%s", target, child.receipts, err, &child.stdout, &child.stderr, journal)
	return nil
}

func pauseRunAtPoint(t *testing.T, binary, repository string, environment []string, target faultpoint.PointID) *pausedRun {
	t.Helper()
	notifyRead, notifyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		_ = notifyRead.Close()
		_ = notifyWrite.Close()
		t.Fatal(err)
	}
	files := make([]*os.File, 0, 8)
	for range 6 {
		file, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	files = append(files, notifyWrite, releaseRead)
	child := &pausedRun{notify: notifyRead, release: releaseWrite, scanner: bufio.NewScanner(notifyRead)}
	child.command = exec.Command(binary, "run")
	child.command.Dir = repository
	child.command.Env = replaceEnvironment(environment, map[string]string{
		"PARTITUR_FAULTPOINT_HARNESS":    "1",
		"PARTITUR_FAULTPOINT_NOTIFY_FD":  "9",
		"PARTITUR_FAULTPOINT_RELEASE_FD": "10",
	})
	child.command.ExtraFiles = files
	child.command.Stdout = &child.stdout
	child.command.Stderr = &child.stderr
	if err := child.command.Start(); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		defer file.Close()
	}
	if err := notifyWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := releaseRead.Close(); err != nil {
		t.Fatal(err)
	}
	child.processPID = child.command.Process.Pid
	for {
		point, pid := nextKillPoint(t, child.scanner)
		if pid != child.processPID {
			t.Fatalf("faultpoint %q pid=%d, want child pid=%d", point, pid, child.processPID)
		}
		if point == target {
			child.paused = true
			return child
		}
		if _, err := child.release.Write([]byte{1}); err != nil {
			t.Fatalf("release %q: %v", point, err)
		}
	}
}

func (child *pausedRun) releaseAndExpect(t *testing.T, want []faultpoint.PointID) {
	t.Helper()
	if !child.paused {
		t.Fatal("child is not paused")
	}
	if _, err := child.release.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range want {
		point, pid := nextKillPoint(t, child.scanner)
		if pid != child.processPID || point != expected {
			t.Fatalf("faultpoint=%q pid=%d, want %q from pid=%d", point, pid, expected, child.processPID)
		}
		if _, err := child.release.Write([]byte{1}); err != nil {
			t.Fatalf("release %q: %v", point, err)
		}
	}
	if err := child.release.Close(); err != nil {
		t.Fatal(err)
	}
	if err := child.command.Wait(); err != nil {
		t.Fatalf("run after prepare injection: %v\nstdout:\n%s\nstderr:\n%s", err, &child.stdout, &child.stderr)
	}
	child.commandEnded = true
	if child.scanner.Scan() {
		t.Fatalf("unexpected faultpoint %q", child.scanner.Text())
	}
	if err := child.scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func (child *pausedRun) stop(t *testing.T) {
	t.Helper()
	if child == nil {
		return
	}
	if !child.commandEnded && child.command.Process != nil {
		_ = child.command.Process.Kill()
	}
	_ = child.release.Close()
	if !child.commandEnded {
		_ = child.command.Wait()
	}
	_ = child.notify.Close()
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(root, "..", ".."))
}

func runstorePath(repository string, runID runstate.RunID, name string) string {
	return filepath.Join(repository, ".partitur", "runs", string(runID), name)
}
