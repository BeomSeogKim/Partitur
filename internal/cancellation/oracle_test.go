package cancellation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

func TestWatcherTreatsTornTailAsNotYetAndObservesDurableRequest(t *testing.T) {
	root := t.TempDir()
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".partitur", "runs", "run-1", "journal.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"event_id":"tail"`), 0o600); err != nil {
		t.Fatal(err)
	}
	watcher := &Watcher{
		store:     store,
		runID:     "run-1",
		cancelled: make(chan struct{}),
	}
	if watcher.observe() {
		t.Fatal("torn tail stopped the watcher")
	}
	if err := watcher.Err(); err != nil {
		t.Fatalf("torn tail error=%v", err)
	}
	select {
	case <-watcher.Cancelled():
		t.Fatal("torn tail requested cancellation")
	default:
	}

	line := `{"event_id":"request","seq":1,"ts":"2026-07-29T00:00:00.000Z","run_id":"run-1","score_revision":1,"type":"cancel.requested","payload":{"requested_by":"cli"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if !watcher.observe() {
		t.Fatal("durable cancellation request was not observed")
	}
	select {
	case <-watcher.Cancelled():
	default:
		t.Fatal("durable cancellation request did not close the signal")
	}
}

func TestWatcherRejectsNonCancellationEventAndStops(t *testing.T) {
	root := t.TempDir()
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".partitur", "runs", "run-1", "journal.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"event_id":"started","seq":1,"ts":"2026-07-29T00:00:00.000Z","run_id":"run-1","score_revision":1,"type":"run.started","payload":{"base_commit":"git-sha1:commit","base_tree":"git-sha1:tree","score_hash":"sha256:score-1","score_file_hash":"sha256:file-1","resolved_cast_hash":"sha256:cast","identity_versions":{"canonical_encoding":1,"projections":{}}}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	watcher := &Watcher{
		store:     store,
		runID:     "run-1",
		cancelled: make(chan struct{}),
	}
	if watcher.observe() {
		t.Fatal("non-cancellation event stopped the watcher")
	}
	select {
	case <-watcher.Cancelled():
		t.Fatal("non-cancellation event closed the signal")
	default:
	}
}

func TestWatcherObservesPendingPrepareWithoutStoppingCancellationWatch(t *testing.T) {
	root := t.TempDir()
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".partitur", "runs", "run-1", "journal.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	prepare := `{"event_id":"prepared","seq":1,"ts":"2026-07-29T00:00:00.000Z","run_id":"run-1","score_revision":1,"type":"amendment.approval_prepared","payload":{"prepare_id":"prepare-1","proposal_id":"proposal-1","mode":"auto","envelope_class":"NARROW_PATHS","base_revision":1,"base_hash":"sha256:base","new_revision":2,"new_snapshot_hash":"sha256:new","new_snapshot_file_hash":"sha256:file","plan_record_hash":"sha256:plan","target_attempt_ids":[],"observed_authority_epoch":0,"quiesce_deadline":"2026-07-29T00:00:00.000Z","classifier_version":1,"identity_versions":{"canonical_encoding":1,"projections":{}}}}` + "\n"
	if err := os.WriteFile(path, []byte(prepare), 0o600); err != nil {
		t.Fatal(err)
	}
	watcher := &Watcher{store: store, runID: "run-1"}
	if watcher.observe() {
		t.Fatal("prepare observation stopped cancellation watch")
	}
	if watcher.PrepareID() != "prepare-1" {
		t.Fatalf("prepare id = %q", watcher.PrepareID())
	}
	select {
	case <-watcher.Prepared():
	default:
		t.Fatal("pending prepare did not close prepared signal")
	}
	cancel := `{"event_id":"cancel","seq":2,"ts":"2026-07-29T00:00:01.000Z","run_id":"run-1","score_revision":1,"type":"cancel.requested","payload":{"requested_by":"cli"}}` + "\n"
	if err := os.WriteFile(path, append([]byte(prepare), []byte(cancel)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if !watcher.observe() {
		t.Fatal("later cancellation did not stop watcher")
	}
	select {
	case <-watcher.Cancelled():
	default:
		t.Fatal("cancellation did not outrank prepared signal")
	}
}

func TestWatcherStopWaitsForItsGoroutine(t *testing.T) {
	store, err := runstore.New(t.TempDir(), faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := Watch(store, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() {
		watcher.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("watcher Stop did not wait for its goroutine")
	}
}

func TestWatcherWakeQueuesObservationForWatchGoroutine(t *testing.T) {
	root := t.TempDir()
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := Watch(store, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Stop()
	path := filepath.Join(root, ".partitur", "runs", "run-1", "journal.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"event_id":"request","seq":1,"ts":"2026-07-29T00:00:00.000Z","run_id":"run-1","score_revision":1,"type":"cancel.requested","payload":{"requested_by":"cli"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	watcher.Wake()
	select {
	case <-watcher.Cancelled():
	case <-time.After(time.Second):
		t.Fatal("watcher did not observe queued wake")
	}
}

func TestExecuteRejectsMissingInputs(t *testing.T) {
	store, err := runstore.New(t.TempDir(), faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		store *runstore.Store
		runID string
	}{
		{name: "missing store", runID: "run-1"},
		{name: "missing run id", store: store},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := Execute(context.Background(), test.store, runstate.RunID(test.runID))
			if err == nil || err.Error() != "cancellation requires store and run id" {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestWatchRejectsMissingInputs(t *testing.T) {
	store, err := runstore.New(t.TempDir(), faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		store *runstore.Store
		runID runstate.RunID
	}{
		{name: "missing store", runID: "run-1"},
		{name: "missing run id", store: store},
	} {
		t.Run(test.name, func(t *testing.T) {
			watcher, err := Watch(test.store, test.runID)
			if watcher != nil || err == nil || err.Error() != "cancellation watcher requires store and run id" {
				t.Fatalf("watcher=%v error=%v", watcher, err)
			}
		})
	}
}
