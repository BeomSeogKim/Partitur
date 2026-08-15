package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

type crossEdgeCancellationObservation struct {
	cancellationRequests int
	cancelled            int
	approvals            int
	cancellationIndex    int
	cancelledIndex       int
	lastApprovalIndex    int
}

// observeCrossEdgeCancellationState retains the cancellation, terminal, and
// approval positions from an ordered decoded journal. An approval is valid
// only before cancellation; the absence alternative keeps pre-cancellation
// approval and no approval distinct.
func observeCrossEdgeCancellationState(events []runstate.Event) crossEdgeCancellationObservation {
	observation := crossEdgeCancellationObservation{
		cancellationIndex: len(events),
		cancelledIndex:    len(events),
		lastApprovalIndex: len(events),
	}
	for index, event := range events {
		switch event.Type {
		case runstate.EventCancelRequested:
			observation.cancellationRequests++
			observation.cancellationIndex = index
		case runstate.EventRunCancelled:
			observation.cancelled++
			observation.cancelledIndex = index
		case runstate.EventAmendmentApproved:
			observation.approvals++
			observation.lastApprovalIndex = index
		}
	}
	return observation
}

func cancellationOutranksApproval(observation crossEdgeCancellationObservation) bool {
	return observation.cancellationRequests == 1 &&
		observation.cancelled == 1 &&
		observation.cancellationIndex < observation.cancelledIndex &&
		(observation.approvals == 0 || observation.lastApprovalIndex < observation.cancellationIndex)
}

func validateCrossEdgeCancellationObservation(observation crossEdgeCancellationObservation) error {
	if !cancellationOutranksApproval(observation) {
		return fmt.Errorf("cancellation does not outrank approval: %+v", observation)
	}
	return nil
}

func TestCrossEdgeCancellationCheckProductionSubprocess(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository, environment, runID, _ := cancellationFixture(t, bin, vendor, cancellationFixturePredicates{preparePending: true}, faultpoint.Nop{})
	code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "cancel", runID)
	if code != 4 || stdout != "" || stderr != "" {
		t.Fatalf("cancel exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	assertCrossEdgeCancellationObservation(t, repository, runstate.RunID(runID))
}

func assertCrossEdgeCancellationObservation(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	observation := observeCrossEdgeCancellationState(journal.Events)
	if err := validateCrossEdgeCancellationObservation(observation); err != nil {
		t.Fatal(err)
	}
}

func TestCrossEdgeCancellationObservationFixtures(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		events  []runstate.Event
		wantErr bool
	}{
		{
			name: "positive_cancellation_without_approval",
			events: []runstate.Event{
				{Type: runstate.EventCancelRequested},
				{Type: runstate.EventAmendmentApprovalAbandoned},
				{Type: runstate.EventRunCancelled},
			},
		},
		{
			name: "duplicate_cancel_requested",
			events: []runstate.Event{
				{Type: runstate.EventCancelRequested},
				{Type: runstate.EventCancelRequested},
				{Type: runstate.EventRunCancelled},
			},
			wantErr: true,
		},
		{
			name: "duplicate_run_cancelled",
			events: []runstate.Event{
				{Type: runstate.EventCancelRequested},
				{Type: runstate.EventRunCancelled},
				{Type: runstate.EventRunCancelled},
			},
			wantErr: true,
		},
		{
			name: "terminal_before_cancellation",
			events: []runstate.Event{
				{Type: runstate.EventRunCancelled},
				{Type: runstate.EventCancelRequested},
			},
			wantErr: true,
		},
		{
			name: "approval_before_cancellation",
			events: []runstate.Event{
				{Type: runstate.EventAmendmentApproved},
				{Type: runstate.EventCancelRequested},
				{Type: runstate.EventRunCancelled},
			},
		},
		{
			// A journal that approves both before and after the request is the
			// only shape that distinguishes the retained last approval from the
			// first one. Without it the observer may keep either and every other
			// fixture still passes, because they hold at most one approval.
			name: "approval_before_and_after_cancellation",
			events: []runstate.Event{
				{Type: runstate.EventAmendmentApproved},
				{Type: runstate.EventCancelRequested},
				{Type: runstate.EventAmendmentApproved},
				{Type: runstate.EventRunCancelled},
			},
			wantErr: true,
		},
	} {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			err := validateCrossEdgeCancellationObservation(observeCrossEdgeCancellationState(fixture.events))
			if (err != nil) != fixture.wantErr {
				t.Fatalf("validator error=%v, wantError=%t", err, fixture.wantErr)
			}
		})
	}
}

func TestCrossEdgeCancellationSyntheticApprovalNegativeControl(t *testing.T) {
	t.Run("approval_after_durable_cancellation", func(t *testing.T) {
		root := repositoryRoot(t)
		bin := t.TempDir()
		partitur := buildE2EBinary(t, root, bin, "partitur")
		buildE2EBinary(t, root, bin, "partitur-adapter-codex")
		buildE2EBinary(t, root, bin, "partitur-trampoline")
		vendor, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		repository, _, runID, driver, approver := preparedHumanApproval(t, partitur, bin, vendor)
		defer driver.stop(t)
		defer approver.stop(t)
		store, err := runstore.New(repository, faultpoint.Nop{})
		if err != nil {
			t.Fatal(err)
		}
		beforeCancel, err := store.LoadRunInput(runID)
		if err != nil {
			t.Fatal(err)
		}
		if beforeCancel.Projection.State.PendingPrepare == nil {
			t.Fatal("production prepare is missing")
		}
		// The paused approver holds the state lock inside its own transaction, so
		// the durable cancellation below cannot acquire it until both production
		// processes are gone. Killing them is what the recovery path itself faces.
		driver.kill(t)
		killPausedRun(t, approver)
		if err := store.RequestCancellation(runID); err != nil {
			t.Fatal(err)
		}
		planPath := filepath.Join(repository, ".partitur", "runs", string(runID), "prepares", string(beforeCancel.Projection.State.PendingPrepare.ID)+".json")
		planBytes, err := os.ReadFile(planPath)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := runstate.DecodeApprovalPlan(planBytes)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := plan.ApprovedPayload(nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Mutate(runID, "", func(transaction *runstore.Txn) error {
			_, err := transaction.At("fixture.synthetic.approved").Append(runstate.Event{
				RunID: runID, ScoreRevision: plan.NewRevision, MovementID: plan.MovementID,
				Type: runstate.EventAmendmentApproved, Payload: payload,
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		approvedInput, err := store.LoadRunInput(runID)
		if err != nil {
			t.Fatal(err)
		}
		terminalPayload, err := json.Marshal(runstate.CancellationPayload(approvedInput.Projection.State, nil))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Mutate(runID, "", func(transaction *runstore.Txn) error {
			_, err := transaction.At("fixture.synthetic.cancelled").Append(runstate.Event{
				RunID: runID, ScoreRevision: approvedInput.Projection.State.ScoreHead.Revision,
				Type: runstate.EventRunCancelled, Payload: terminalPayload,
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		journal, err := store.ReadJournal(runID)
		if err != nil {
			t.Fatal(err)
		}
		input, err := store.LoadRunInput(runID)
		if err != nil {
			t.Fatal(err)
		}
		if input.Projection.State.Run != runstate.RunCancelled || !input.Projection.State.CancelRequested || input.Projection.State.ScoreHead.Revision != plan.NewRevision {
			t.Fatalf("replayed state=%+v", input.Projection.State)
		}
		if err := validateCrossEdgeCancellationObservation(observeCrossEdgeCancellationState(journal.Events)); err == nil {
			t.Fatal("synthetic approval after durable cancellation passed")
		}
	})
}
