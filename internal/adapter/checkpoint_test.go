package adapter

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

// While the adapter interval is open the opener writes execution.elapsed_checkpointed
// on the injected cadence, measuring cumulative elapsed as a positive monotonic
// duration; the ticker is stopped when the window closes so it cannot outlive the
// interval.
func TestExecuteWritesElapsedCheckpointsAndStopsTickerOnClose(t *testing.T) {
	directory := t.TempDir()
	installFake(t, directory, "fake")
	client := newClient([]string{
		fakeModeEnv + "=execute_cancel_timeout",
		fakeMarkerEnv + "=" + filepath.Join(t.TempDir(), "response"),
	}, incidentalTestDeadline, 20*time.Millisecond)

	ticks := make(chan time.Time, 8)
	var stopped atomic.Bool
	client.newTicker = func(time.Duration) (<-chan time.Time, func()) {
		return ticks, func() { stopped.Store(true) }
	}

	var order []string
	var mu sync.Mutex
	var checkpoints []int64
	observed := make(chan struct{}, 8)
	recorder := successfulRecorder(&order)
	recorder.RecordProbe = func(protocol.ProbeResult) (faultpoint.DurabilityReceipt, error) {
		order = append(order, "adapter.probed")
		// The adapter hangs after the probe, so no execute frame competes with the
		// ticker: queued ticks are consumed deterministically by the open window.
		ticks <- time.Now()
		ticks <- time.Now()
		return receipt("adapter.probed"), nil
	}
	recorder.RecordElapsedCheckpoint = func(checkpoint ElapsedCheckpoint) (faultpoint.DurabilityReceipt, error) {
		if checkpoint.IntervalID != "interval-1" {
			t.Errorf("checkpoint interval = %q, want interval-1", checkpoint.IntervalID)
		}
		mu.Lock()
		checkpoints = append(checkpoints, checkpoint.CumulativeElapsedMS)
		mu.Unlock()
		observed <- struct{}{}
		return receipt(string(runstate.EventExecutionElapsedCheckpointed)), nil
	}

	cancel := make(chan struct{})
	plan := executePlan(
		"fake",
		filepath.Join(directory, "partitur-adapter-fake"),
		buildTrampoline(t, directory),
		t.TempDir(),
		t.TempDir(),
		t.TempDir(),
		recorder,
		&order,
	)
	plan.Cancel = cancel

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = client.Execute(context.Background(), plan)
	}()

	select {
	case <-observed:
	case <-time.After(10 * time.Second):
		t.Fatal("no execution.elapsed_checkpointed emitted while the interval was open")
	}
	close(cancel)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("execute did not return after the window closed")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(checkpoints) == 0 {
		t.Fatal("no checkpoint recorded")
	}
	for index, value := range checkpoints {
		if value <= 0 {
			t.Fatalf("checkpoint %d cumulative = %d, want a positive duration", index, value)
		}
		if index > 0 && value <= checkpoints[index-1] {
			t.Fatalf("checkpoints are not strictly increasing: %v", checkpoints)
		}
	}
	if !stopped.Load() {
		t.Fatal("checkpoint ticker was not stopped at interval close; it must not outlive the interval")
	}
}

// With no checkpoint callback wired the execute window runs without a ticker and
// never attempts to checkpoint.
func TestExecuteWithoutCheckpointRecorderRunsNoTicker(t *testing.T) {
	directory := t.TempDir()
	installFake(t, directory, "fake")
	client := newClient([]string{
		fakeModeEnv + "=execute_completed",
		fakeMarkerEnv + "=" + filepath.Join(t.TempDir(), "response"),
	}, incidentalTestDeadline, 20*time.Millisecond)

	var built atomic.Bool
	client.newTicker = func(time.Duration) (<-chan time.Time, func()) {
		built.Store(true)
		return nil, func() {}
	}

	var order []string
	recorder := successfulRecorder(&order)
	recorder.RecordElapsedCheckpoint = nil // opener not checkpointing
	plan := executePlan(
		"fake",
		filepath.Join(directory, "partitur-adapter-fake"),
		buildTrampoline(t, directory),
		t.TempDir(),
		t.TempDir(),
		t.TempDir(),
		recorder,
		&order,
	)
	if _, err := client.Execute(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if built.Load() {
		t.Fatal("checkpoint ticker built despite a nil RecordElapsedCheckpoint")
	}
}
