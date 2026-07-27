package runstore

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

func TestAppendSyncsBeforeReturningReceipt(t *testing.T) {
	store := newTestStore(t)
	recorder := &recordingFS{delegate: realFS{}}
	store.fs = recorder

	var receipt DurabilityReceipt
	err := store.Mutate("run-1", "", func(transaction *Txn) error {
		var err error
		receipt, err = transaction.At("run.started/journal_append").Append(runStartedEvent())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Address == "" || receipt.Mutation.EventID == "" ||
		receipt.Mutation.Sequence != 1 || receipt.Mutation.Timestamp == "" {
		t.Fatalf("receipt = %+v", receipt)
	}
	if got, want := recorder.journalMutationOperations(), []string{"append", "sync-file"}; !slices.Equal(got, want) {
		t.Fatalf("journal operations = %v, want %v", got, want)
	}
}

func TestAppendAllocatesEnvelopeForMultipleEventsInOneMutation(t *testing.T) {
	store := newTestStore(t)
	var receipts []DurabilityReceipt
	err := store.Mutate("run-1", "", func(transaction *Txn) error {
		for _, event := range []runstate.Event{runStartedEvent(), movementReadyEvent()} {
			receipt, err := transaction.At("test/journal_append").Append(event)
			if err != nil {
				return err
			}
			receipts = append(receipts, receipt)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipts[0].Mutation.Sequence != 1 || receipts[1].Mutation.Sequence != 2 {
		t.Fatalf("receipt sequences = %d, %d", receipts[0].Mutation.Sequence, receipts[1].Mutation.Sequence)
	}
	if receipts[0].Mutation.EventID == "" ||
		receipts[1].Mutation.EventID == "" ||
		receipts[0].Mutation.EventID == receipts[1].Mutation.EventID {
		t.Fatalf("receipt event ids = %q, %q", receipts[0].Mutation.EventID, receipts[1].Mutation.EventID)
	}
	for _, receipt := range receipts {
		if _, err := time.Parse("2006-01-02T15:04:05.000Z", receipt.Mutation.Timestamp); err != nil {
			t.Fatalf("receipt timestamp %q: %v", receipt.Mutation.Timestamp, err)
		}
	}

	journal := filepath.Join(store.root, ".partitur", "runs", "run-1", "journal.jsonl")
	contents, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	lines := journalLines(contents)
	if len(lines) != len(receipts) {
		t.Fatalf("journal line count = %d, want %d", len(lines), len(receipts))
	}
	for index, line := range lines {
		event, syntaxErr, err := decodeEvent(line.bytes)
		if syntaxErr != nil || err != nil {
			t.Fatalf("decode line %d: syntax=%v error=%v", index+1, syntaxErr, err)
		}
		receipt := receipts[index]
		if event.EventID != receipt.Mutation.EventID ||
			event.Seq != receipt.Mutation.Sequence ||
			event.Timestamp != receipt.Mutation.Timestamp {
			t.Fatalf("line %d envelope = %+v, receipt = %+v", index+1, event, receipt)
		}
	}
}

func TestAppendRejectsCallerAllocatedEnvelope(t *testing.T) {
	tests := []struct {
		name   string
		change func(*runstate.Event)
	}{
		{name: "event id", change: func(event *runstate.Event) { event.EventID = "caller-event" }},
		{name: "sequence", change: func(event *runstate.Event) { event.Seq = 1 }},
		{name: "timestamp", change: func(event *runstate.Event) {
			event.Timestamp = "2026-07-26T00:00:00.000Z"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			event := runStartedEvent()
			test.change(&event)
			var receipt DurabilityReceipt
			err := store.Mutate("run-1", "", func(transaction *Txn) error {
				var err error
				receipt, err = transaction.At("test/journal_append").Append(event)
				return err
			})
			if !errors.Is(err, ErrJournalCorrupt) || receipt.Address != "" {
				t.Fatalf("error=%v receipt=%+v", err, receipt)
			}
		})
	}
}

func TestRequiredSyncFailurePreventsReceipt(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Txn) (DurabilityReceipt, error)
		fail func(*recordingFS)
	}{
		{
			name: "append",
			run: func(transaction *Txn) (DurabilityReceipt, error) {
				return transaction.At("run.started/journal_append").Append(runStartedEvent())
			},
			fail: func(recorder *recordingFS) { recorder.failSyncFile = true },
		},
		{
			name: "publication file sync",
			run: func(transaction *Txn) (DurabilityReceipt, error) {
				contents := []byte("snapshot")
				return transaction.At("prepare.snapshot_to_plan/snapshot").PublishImmutable(
					"scores/revision-1.yaml", contents, Hash(digest(contents)),
				)
			},
			fail: func(recorder *recordingFS) { recorder.failSyncFile = true },
		},
		{
			name: "publication directory sync",
			run: func(transaction *Txn) (DurabilityReceipt, error) {
				contents := []byte("snapshot")
				return transaction.At("prepare.snapshot_to_plan/snapshot").PublishImmutable(
					"scores/revision-1.yaml", contents, Hash(digest(contents)),
				)
			},
			fail: func(recorder *recordingFS) { recorder.failSyncDirAt = 1 },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			recorder := &recordingFS{delegate: realFS{}}
			test.fail(recorder)
			store.fs = recorder
			var receipt DurabilityReceipt
			err := store.Mutate("run-1", "", func(transaction *Txn) error {
				var err error
				receipt, err = test.run(transaction)
				return err
			})
			if err == nil {
				t.Fatal("operation succeeded despite injected sync failure")
			}
			if receipt.Address != "" {
				t.Fatalf("receipt escaped sync failure: %+v", receipt)
			}
		})
	}
}

func TestPublicationOrderAndImmutability(t *testing.T) {
	store := newTestStore(t)
	recorder := &recordingFS{delegate: realFS{}}
	store.fs = recorder
	contents := []byte("snapshot")
	path := Path("scores/revision-1.yaml")

	err := store.Mutate("run-1", "", func(transaction *Txn) error {
		_, err := transaction.At("prepare.snapshot_to_plan/snapshot").PublishImmutable(
			path, contents, Hash(digest(contents)),
		)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got := recorder.publicationOperations()
	want := []string{"write-temp", "sync-file", "rename", "sync-dir"}
	if !slices.Equal(got, want) {
		t.Fatalf("publication operations = %v, want %v", got, want)
	}

	err = store.Mutate("run-1", "", func(transaction *Txn) error {
		_, err := transaction.At("prepare.snapshot_to_plan/snapshot").PublishImmutable(
			path, contents, Hash(digest(contents)),
		)
		return err
	})
	if err != nil {
		t.Fatalf("idempotent publication: %v", err)
	}

	different := []byte("different")
	err = store.Mutate("run-1", "", func(transaction *Txn) error {
		_, err := transaction.At("prepare.snapshot_to_plan/snapshot").PublishImmutable(
			path, different, Hash(digest(different)),
		)
		return err
	})
	if !errors.Is(err, ErrImmutablePublicationConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	finalPath := filepath.Join(store.root, ".partitur", "runs", "run-1", string(path))
	final, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(final) != string(contents) {
		t.Fatalf("immutable target overwritten with %q", final)
	}
}

func TestHashMismatchPublishesNothing(t *testing.T) {
	store := newTestStore(t)
	err := store.Mutate("run-1", "", func(transaction *Txn) error {
		_, err := transaction.At("prepare.snapshot_to_plan/snapshot").PublishImmutable(
			"scores/revision-1.yaml", []byte("snapshot"), "sha256:wrong",
		)
		return err
	})
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("error = %v, want ErrHashMismatch", err)
	}
	path := filepath.Join(store.root, ".partitur", "runs", "run-1", "scores", "revision-1.yaml")
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("mismatched publication exists: %v", err)
	}
}

func TestQuarantineSyncsBothDirectories(t *testing.T) {
	store := newTestStore(t)
	source := filepath.Join(store.root, ".partitur", "runs", "run-1", "scores", "revision-2.yaml")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingFS{delegate: realFS{}}
	store.fs = recorder
	var result QuarantineResult
	err := store.Mutate("run-1", "", func(transaction *Txn) error {
		var err error
		result, err = transaction.
			At("prepare.quarantined_to_abandoned/snapshot").
			QuarantineAs("snapshot").
			Quarantine("scores/revision-2.yaml")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.Mutation.Source == "" || result.Receipt.Mutation.Destination == "" {
		t.Fatalf("quarantine receipt = %+v", result.Receipt)
	}
	if recorder.syncDirCount != 2 {
		t.Fatalf("directory sync count = %d, want 2", recorder.syncDirCount)
	}
	destination := filepath.Join(store.root, ".partitur", "runs", "run-1", filepath.FromSlash(string(result.Destination)))
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("quarantine destination: %v", err)
	}
}

func TestQuarantineAndRemovalSyncFailuresPreventReceipts(t *testing.T) {
	t.Run("quarantine source directory", func(t *testing.T) {
		testQuarantineSyncFailure(t, 1)
	})
	t.Run("quarantine destination directory", func(t *testing.T) {
		testQuarantineSyncFailure(t, 2)
	})
	t.Run("removal directory", func(t *testing.T) {
		store := newTestStore(t)
		target := filepath.Join(store.root, ".partitur", "runs", "run-1", "prepares", "plan.json")
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		recorder := &recordingFS{delegate: realFS{}, failSyncDirAt: 1}
		store.fs = recorder
		var receipt DurabilityReceipt
		err := store.Mutate("run-1", "", func(transaction *Txn) error {
			var err error
			receipt, err = transaction.At("prepare.quarantined_to_abandoned/plan_removed").
				RemoveDurable("prepares/plan.json")
			return err
		})
		if err == nil || receipt.Address != "" {
			t.Fatalf("error=%v receipt=%+v", err, receipt)
		}
	})
}

func testQuarantineSyncFailure(t *testing.T, failAt int) {
	t.Helper()
	store := newTestStore(t)
	source := filepath.Join(store.root, ".partitur", "runs", "run-1", "scores", "revision-2.yaml")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingFS{delegate: realFS{}, failSyncDirAt: failAt}
	store.fs = recorder
	var result QuarantineResult
	err := store.Mutate("run-1", "", func(transaction *Txn) error {
		var err error
		result, err = transaction.
			At("prepare.quarantined_to_abandoned/snapshot").
			QuarantineAs("snapshot").
			Quarantine("scores/revision-2.yaml")
		return err
	})
	if err == nil || result.Receipt.Address != "" {
		t.Fatalf("error=%v result=%+v", err, result)
	}
}

func TestReplayRepairsOnlySyntacticallyUnparseableTail(t *testing.T) {
	store := newTestStore(t)
	if err := store.Mutate("run-1", "", func(transaction *Txn) error {
		_, err := transaction.At("run.started/journal_append").Append(runStartedEvent())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(store.root, ".partitur", "runs", "run-1", "journal.jsonl")
	file, err := os.OpenFile(journal, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"event_id":"torn"`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := store.Replay("run-1", nil, "recovery/journal_tail_truncated")
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Run != runstate.RunRunning || result.RepairReceipt == nil {
		t.Fatalf("replay result = %+v", result)
	}
	if result.RepairReceipt.Mutation.EventType != string(runstate.EventJournalTailTruncated) ||
		result.RepairReceipt.Mutation.Sequence != 2 {
		t.Fatalf("repair receipt = %+v", result.RepairReceipt)
	}
	contents, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), `"torn"`) ||
		!strings.Contains(string(contents), `"type":"journal.tail_truncated"`) {
		t.Fatalf("repaired journal = %q", contents)
	}

	again, err := store.Replay("run-1", nil, "recovery/journal_tail_truncated")
	if err != nil {
		t.Fatal(err)
	}
	if again.RepairReceipt != nil {
		t.Fatalf("clean replay produced repair receipt: %+v", again.RepairReceipt)
	}
}

func TestReplayDoesNotTruncateValidJSONWithInvalidEnvelope(t *testing.T) {
	store := newTestStore(t)
	if err := store.Mutate("run-1", "", func(transaction *Txn) error {
		_, err := transaction.At("run.started/journal_append").Append(runStartedEvent())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(store.root, ".partitur", "runs", "run-1", "journal.jsonl")
	file, err := os.OpenFile(journal, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	const invalid = `{"seq":2}` + "\n"
	if _, err := file.WriteString(invalid); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(journal)
	_, err = store.Replay("run-1", nil, "recovery/journal_tail_truncated")
	if !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("error = %v, want ErrJournalCorrupt", err)
	}
	after, _ := os.ReadFile(journal)
	if string(after) != string(before) {
		t.Fatal("valid JSON with invalid envelope was truncated")
	}
}

func TestReplayDoesNotTruncateDuplicateNameJSON(t *testing.T) {
	store := newTestStore(t)
	journal := filepath.Join(store.root, ".partitur", "runs", "run-1", "journal.jsonl")
	if err := os.MkdirAll(filepath.Dir(journal), 0o700); err != nil {
		t.Fatal(err)
	}
	const duplicate = `{"event_id":"one","event_id":"two","seq":1,"ts":"2026-07-26T00:00:00.000Z","run_id":"run-1","score_revision":1,"type":"run.started","payload":{}}` + "\n"
	if err := os.WriteFile(journal, []byte(duplicate), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := store.Replay("run-1", nil, "recovery/journal_tail_truncated")
	if !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("error = %v, want ErrJournalCorrupt", err)
	}
	contents, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != duplicate {
		t.Fatal("syntactically valid duplicate-name JSON was truncated")
	}
}

func TestReplayReturnsUnsupportedEventDistinctFromCorruption(t *testing.T) {
	store := newTestStore(t)
	if err := store.Mutate("run-1", "", func(transaction *Txn) error {
		_, err := transaction.At("run.started/journal_append").Append(runStartedEvent())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	event := runstate.Event{
		EventID:       "event-movement-failed",
		Seq:           2,
		Timestamp:     "2026-07-26T00:00:00.000Z",
		RunID:         "run-1",
		ScoreRevision: 1,
		MovementID:    "m1",
		AttemptID:     "a1",
		Type:          "movement.failed",
		Payload: json.RawMessage(`{
			"reason": "retries_exhausted",
			"run_failed": false
		}`),
	}
	line, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(store.root, ".partitur", "runs", "run-1", "journal.jsonl")
	file, err := os.OpenFile(journal, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = store.Replay("run-1", []runstate.MovementSeed{{ID: "m1", Initial: runstate.MovementPending}}, "recovery/journal_tail_truncated")
	if !errors.Is(err, runstate.ErrUnsupportedEventType) {
		t.Fatalf("error = %v, want ErrUnsupportedEventType", err)
	}
	if errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("unsupported event was classified as corruption: %v", err)
	}
}

func TestJournalIdempotencyUsesCanonicalPayloadOnly(t *testing.T) {
	store := newTestStore(t)
	first := runStartedEvent()
	second := runStartedEvent()
	second.Payload = json.RawMessage(`{"score_hash":"sha256:score-1","base_tree":"git-sha1:tree","base_commit":"git-sha1:commit","identity_versions":{"canonical_encoding":1,"projections":{}},"resolved_cast_hash":"sha256:cast","score_file_hash":"sha256:file-1"}`)
	var firstReceipt DurabilityReceipt
	var secondReceipt DurabilityReceipt
	err := store.Mutate("run-1", "", func(transaction *Txn) error {
		var err error
		firstReceipt, err = transaction.At("run.started/journal_append").Append(first)
		if err != nil {
			return err
		}
		secondReceipt, err = transaction.At("run.started/journal_append").Append(second)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if secondReceipt.Mutation.Sequence != 1 {
		t.Fatalf("idempotent receipt sequence = %d", secondReceipt.Mutation.Sequence)
	}
	if secondReceipt.Mutation.EventID != firstReceipt.Mutation.EventID ||
		secondReceipt.Mutation.Timestamp != firstReceipt.Mutation.Timestamp {
		t.Fatalf("idempotent receipt changed envelope: first=%+v second=%+v", firstReceipt, secondReceipt)
	}

	conflict := runStartedEvent()
	conflictPayload := runStartedPayload()
	conflictPayload["score_hash"] = "sha256:different"
	conflict.Payload, _ = json.Marshal(conflictPayload)
	err = store.Mutate("run-1", "", func(transaction *Txn) error {
		_, err := transaction.At("run.started/journal_append").Append(conflict)
		return err
	})
	if !errors.Is(err, ErrJournalIdempotencyConflict) {
		t.Fatalf("error = %v, want ErrJournalIdempotencyConflict", err)
	}
}

func TestRepositoryLockSerializesRunsAndPersists(t *testing.T) {
	store := newTestStore(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- store.Mutate("run-1", "", func(*Txn) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- store.Mutate("run-2", "", func(*Txn) error {
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
		t.Fatal("second run entered while repository lock was held")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(store.root, ".partitur", "runs", ".state.lock")
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Mutate("run-3", "", func(*Txn) error { return nil }); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("persistent state lock was replaced")
	}
}

func TestCallerTaggedLockBoundaryUsesInjectedProbe(t *testing.T) {
	probe := &recordingProbe{}
	store, err := New(t.TempDir(), probe)
	if err != nil {
		t.Fatal(err)
	}
	point := faultpoint.PointQuiesceCommitLockHeld
	if err := store.Mutate("run-1", point, func(*Txn) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(probe.points, []faultpoint.PointID{point}) {
		t.Fatalf("probe points = %v", probe.points)
	}
}

func TestLeaseCASComparesFullIdentity(t *testing.T) {
	store := newTestStore(t)
	lease := Lease{
		Epoch: 7, Token: "token-a", PID: 42,
		Start: runstate.LinuxStartIdentity{BootID: "boot", StartTicks: "100"},
	}
	err := store.Mutate("run-1", "", func(transaction *Txn) error {
		if _, err := transaction.At("authority.granted_to_lease_created/lease").CreateLease(true, lease); err != nil {
			return err
		}
		wrong := lease.Identity()
		wrong.Start = runstate.LinuxStartIdentity{BootID: "boot", StartTicks: "101"}
		if _, err := transaction.At("quiesce.swept_to_lease_moved/lease").CompareMoveLease(
			wrong, "driver.quiesced.prepare-1",
		); !errors.Is(err, ErrLeaseConflict) {
			t.Fatalf("wrong identity move error = %v", err)
		}
		_, err := transaction.At("quiesce.swept_to_lease_moved/lease").CompareMoveLease(
			lease.Identity(), "driver.quiesced.prepare-1",
		)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(store.root, ".partitur", "runs", "run-1", "driver.lease")
	if _, err := os.Stat(driver); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("driver lease still exists: %v", err)
	}
	sidecar := filepath.Join(store.root, ".partitur", "runs", "run-1", "driver.quiesced.prepare-1")
	if _, err := os.Stat(sidecar); err != nil {
		t.Fatalf("quiesced lease: %v", err)
	}
}

func TestLeaseOwnerMatchPreservesProcessInspectionResult(t *testing.T) {
	identity, err := procid.Read(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	lease := Lease{Epoch: 1, Token: "token", PID: os.Getpid(), Start: identity}
	if result := lease.MatchOwner(); result.Status != procid.MatchingAndLive || result.Err != nil {
		t.Fatalf("owner match = %+v", result)
	}
}

func TestLeaseSyncFailuresPreventReceipts(t *testing.T) {
	lease := Lease{
		Epoch: 7, Token: "token-a", PID: 42,
		Start: runstate.LinuxStartIdentity{BootID: "boot", StartTicks: "100"},
	}
	for _, test := range []struct {
		name string
		fail func(*recordingFS)
	}{
		{name: "file sync", fail: func(recorder *recordingFS) { recorder.failSyncFile = true }},
		{name: "directory sync", fail: func(recorder *recordingFS) { recorder.failSyncDirAt = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			recorder := &recordingFS{delegate: realFS{}}
			test.fail(recorder)
			store.fs = recorder
			var receipt DurabilityReceipt
			err := store.Mutate("run-1", "", func(transaction *Txn) error {
				var err error
				receipt, err = transaction.At("authority.granted_to_lease_created/lease").
					CreateLease(true, lease)
				return err
			})
			if err == nil || receipt.Address != "" {
				t.Fatalf("error=%v receipt=%+v", err, receipt)
			}
		})
	}
	for _, operation := range []string{"move", "remove"} {
		t.Run(operation+" directory sync", func(t *testing.T) {
			store := newTestStore(t)
			if err := store.Mutate("run-1", "", func(transaction *Txn) error {
				_, err := transaction.At("authority.granted_to_lease_created/lease").
					CreateLease(true, lease)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			recorder := &recordingFS{delegate: realFS{}, failSyncDirAt: 1}
			store.fs = recorder
			var receipt DurabilityReceipt
			err := store.Mutate("run-1", "", func(transaction *Txn) error {
				var err error
				if operation == "move" {
					receipt, err = transaction.At("quiesce.swept_to_lease_moved/lease").
						CompareMoveLease(lease.Identity(), "driver.quiesced.prepare-1")
				} else {
					receipt, err = transaction.At("cancel.terminal_to_lease_removed/lease").
						CompareRemoveLease(lease.Identity())
				}
				return err
			})
			if err == nil || receipt.Address != "" {
				t.Fatalf("error=%v receipt=%+v", err, receipt)
			}
		})
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(t.TempDir(), faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func runStartedEvent() runstate.Event {
	payload, err := json.Marshal(runStartedPayload())
	if err != nil {
		panic(err)
	}
	return runstate.Event{
		RunID:         "run-1",
		ScoreRevision: 1,
		Type:          runstate.EventRunStarted,
		Payload:       payload,
	}
}

func movementReadyEvent() runstate.Event {
	return runstate.Event{
		RunID:         "run-1",
		ScoreRevision: 1,
		MovementID:    "m1",
		Type:          runstate.EventMovementReady,
		Payload:       json.RawMessage(`{}`),
	}
}

func runStartedPayload() map[string]any {
	return map[string]any{
		"base_commit":        "git-sha1:commit",
		"base_tree":          "git-sha1:tree",
		"score_hash":         "sha256:score-1",
		"score_file_hash":    "sha256:file-1",
		"resolved_cast_hash": "sha256:cast",
		"identity_versions": map[string]any{
			"canonical_encoding": 1,
			"projections":        map[string]any{},
		},
	}
}

type recordingFS struct {
	delegate      fsOperations
	mu            sync.Mutex
	operations    []string
	failSyncFile  bool
	failSyncDirAt int
	syncDirCount  int
}

type recordingProbe struct {
	points []faultpoint.PointID
}

func (probe *recordingProbe) Reached(point faultpoint.PointID) {
	probe.points = append(probe.points, point)
}

func (filesystem *recordingFS) record(operation string) {
	filesystem.mu.Lock()
	defer filesystem.mu.Unlock()
	filesystem.operations = append(filesystem.operations, operation)
}

func (filesystem *recordingFS) MkdirAll(path string, mode fs.FileMode) error {
	filesystem.record("mkdir:" + filepath.Base(path))
	return filesystem.delegate.MkdirAll(path, mode)
}

func (filesystem *recordingFS) ReadFile(path string) ([]byte, error) {
	filesystem.record("read:" + filepath.Base(path))
	return filesystem.delegate.ReadFile(path)
}

func (filesystem *recordingFS) WriteTemp(directory, pattern string, contents []byte, mode fs.FileMode) (string, error) {
	filesystem.record("write-temp:" + filepath.Base(directory))
	return filesystem.delegate.WriteTemp(directory, pattern, contents, mode)
}

func (filesystem *recordingFS) Append(path string, contents []byte, mode fs.FileMode) error {
	filesystem.record("append:" + filepath.Base(path))
	return filesystem.delegate.Append(path, contents, mode)
}

func (filesystem *recordingFS) SyncFile(path string) error {
	filesystem.record("sync-file:" + filepath.Base(path))
	if filesystem.failSyncFile {
		return errors.New("injected file sync failure")
	}
	return filesystem.delegate.SyncFile(path)
}

func (filesystem *recordingFS) SyncDir(path string) error {
	filesystem.mu.Lock()
	filesystem.syncDirCount++
	count := filesystem.syncDirCount
	filesystem.operations = append(filesystem.operations, "sync-dir:"+filepath.Base(path))
	filesystem.mu.Unlock()
	if filesystem.failSyncDirAt == count {
		return errors.New("injected directory sync failure")
	}
	return filesystem.delegate.SyncDir(path)
}

func (filesystem *recordingFS) Rename(source, destination string) error {
	filesystem.record("rename:" + filepath.Base(destination))
	return filesystem.delegate.Rename(source, destination)
}

func (filesystem *recordingFS) Remove(path string) error {
	filesystem.record("remove:" + filepath.Base(path))
	return filesystem.delegate.Remove(path)
}

func (filesystem *recordingFS) Truncate(path string, size int64) error {
	filesystem.record("truncate:" + filepath.Base(path))
	return filesystem.delegate.Truncate(path, size)
}

func (filesystem *recordingFS) Stat(path string) (fs.FileInfo, error) {
	filesystem.record("stat:" + filepath.Base(path))
	return filesystem.delegate.Stat(path)
}

func (filesystem *recordingFS) operationsFor(base string) []string {
	filesystem.mu.Lock()
	defer filesystem.mu.Unlock()
	var result []string
	for _, operation := range filesystem.operations {
		if strings.HasSuffix(operation, ":"+base) {
			result = append(result, operation)
		}
	}
	return result
}

func (filesystem *recordingFS) journalMutationOperations() []string {
	filesystem.mu.Lock()
	defer filesystem.mu.Unlock()
	var result []string
	for _, operation := range filesystem.operations {
		switch operation {
		case "append:journal.jsonl":
			result = append(result, "append")
		case "sync-file:journal.jsonl":
			result = append(result, "sync-file")
		}
	}
	return result
}

func (filesystem *recordingFS) publicationOperations() []string {
	filesystem.mu.Lock()
	defer filesystem.mu.Unlock()
	var result []string
	for _, operation := range filesystem.operations {
		switch {
		case strings.HasPrefix(operation, "write-temp:"):
			result = append(result, "write-temp")
		case strings.HasPrefix(operation, "sync-file:") && strings.Contains(operation, ".tmp-"):
			result = append(result, "sync-file")
		case strings.HasPrefix(operation, "rename:"):
			result = append(result, "rename")
		case strings.HasPrefix(operation, "sync-dir:"):
			result = append(result, "sync-dir")
		}
	}
	return result
}
