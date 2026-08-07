package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

// TestPrepareQuarantinedToAbandonedKillCuts cuts the cancellation path after
// the durable snapshot quarantine and after the durable abandonment append.
func TestPrepareQuarantinedToAbandonedKillCuts(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	predicates := cancellationFixturePredicates{preparePending: true}

	t.Run("prepare.quarantined_to_abandoned/quarantined", func(t *testing.T) {
		repository, environment, runID, _ := cancellationFixture(t, bin, vendor, predicates, faultpoint.Nop{})
		snapshot := cancellationPrepareSnapshot(t, repository, runID)
		killAtPoint(t, partitur, repository, environment, faultpoint.PointCancelSnapshotQuarantined, "cancel", runID)

		assertCancellationSnapshotQuarantined(t, repository, runID, snapshot)
		assertCancellationPrepareArtifactsPresent(t, repository, runID)
		assertNoEvent(t, repository, runstate.RunID(runID), runstate.EventAmendmentApprovalAbandoned)
		assertPrepareBarrier(t, repository, runstate.RunID(runID))
		assertCancellationRecoveryFixedPoint(t, partitur, repository, environment, runID, predicates)
	})

	t.Run("prepare.quarantined_to_abandoned/abandoned", func(t *testing.T) {
		repository, environment, runID, _ := cancellationFixture(t, bin, vendor, predicates, faultpoint.Nop{})
		snapshot := cancellationPrepareSnapshot(t, repository, runID)
		child := pauseCommandAtReceipt(t, partitur, repository, environment, "cancellation.amendment.approval_abandoned", "cancel", runID)

		assertCancellationSnapshotQuarantined(t, repository, runID, snapshot)
		assertCancelledAbandonment(t, repository, runID)
		assertCancellationPrepareCleared(t, repository, runID)
		killPausedRun(t, child)
		assertCancellationRecoveryFixedPoint(t, partitur, repository, environment, runID, predicates)
	})
}

func cancellationPrepareSnapshot(t *testing.T, repository, runID string) []byte {
	t.Helper()
	snapshot, err := os.ReadFile(runstorePath(repository, runstate.RunID(runID), "scores/revision-2.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertCancellationSnapshotQuarantined(t *testing.T, repository, runID string, snapshot []byte) {
	t.Helper()
	source := runstorePath(repository, runstate.RunID(runID), "scores/revision-2.yaml")
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepare snapshot remains at immutable path: %v", err)
	}
	hash := strings.TrimPrefix(cancellationFixtureHash(snapshot), "sha256:")
	quarantined := runstorePath(repository, runstate.RunID(runID), filepath.Join("quarantine", "cancelled_prepare", hash, "revision-2.yaml"))
	contents, err := os.ReadFile(quarantined)
	if err != nil || !bytes.Equal(contents, snapshot) {
		t.Fatalf("quarantined prepare snapshot contents=%q error=%v", contents, err)
	}
}

func assertCancellationPrepareArtifactsPresent(t *testing.T, repository, runID string) {
	t.Helper()
	for _, name := range []string{"prepares/prepare-1.json", "driver.quiesced.prepare-1"} {
		if _, err := os.Stat(runstorePath(repository, runstate.RunID(runID), name)); err != nil {
			t.Fatalf("prepare artifact %q removed before abandonment: %v", name, err)
		}
	}
}

func assertCancelledAbandonment(t *testing.T, repository, runID string) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	var abandoned *runstate.Event
	for index := range journal.Events {
		event := &journal.Events[index]
		if event.Type != runstate.EventAmendmentApprovalAbandoned {
			continue
		}
		if abandoned != nil {
			t.Fatalf("approval_abandoned appears more than once")
		}
		abandoned = event
	}
	if abandoned == nil {
		t.Fatal("approval_abandoned is absent after its receipt")
	}
	var payload map[string]any
	if err := json.Unmarshal(abandoned.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["reason"] != "cancelled" {
		t.Fatalf("approval_abandoned reason=%#v, want cancelled", payload["reason"])
	}
}

func assertCancellationPrepareCleared(t *testing.T, repository, runID string) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.PendingPrepare != nil {
		t.Fatalf("approval_abandoned did not lift the prepare barrier: %+v", *input.Projection.State.PendingPrepare)
	}
}
