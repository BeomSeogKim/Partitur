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
