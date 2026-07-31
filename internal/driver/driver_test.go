package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/cancellation"
	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
	"github.com/BeomSeogKim/Partitur/internal/successor"
	"github.com/BeomSeogKim/Partitur/internal/validate"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

func TestMovementSeedsProjectFinality(t *testing.T) {
	final := movementSeeds(prepareFixture(t, sliceScore()).Score)
	if len(final) != 1 || !final[0].Final {
		t.Fatalf("final seeds = %#v", final)
	}

	waivedScore := sliceScore()
	verification := waivedScore["verification"].(map[string]any)
	expectation := verification["expectation"].(map[string]any)
	expectation["apply_gate"] = map[string]any{"waived": true, "reason": "fixture waiver"}
	delete(verification, "final_movement")
	waived := movementSeeds(prepareFixture(t, waivedScore).Score)
	if len(waived) != 1 || waived[0].Final {
		t.Fatalf("waived seeds = %#v", waived)
	}
}

func TestEffectiveAuthorityDoesNotInferReadFromWrite(t *testing.T) {
	score := prepareFixture(t, sliceScore()).Score
	movement := score.Movements()[0]
	movement.Grants = []string{"repo_write"}
	grants := effectiveGrants(movement, score.EffectivePolicy())
	if len(grants.PathsRW) != 1 || len(grants.PathsRO) != 0 {
		t.Fatalf("effective grants = %+v", grants)
	}
}

func TestProbeAdmissionRejectsEachFailClosedBoundary(t *testing.T) {
	movement := score.MovementView{
		Grants: []string{"repo_read"},
	}
	part := score.PartView{
		Capabilities: []string{"repo_read"},
		ReadOnly:     true,
	}
	policy := score.PolicyView{AllowedPaths: []string{"**"}}
	performer := cast.PerformerView{}
	probe := protocol.ProbeResult{
		Capabilities: protocol.Capabilities{RepoRead: true},
		Enforcement: protocol.Enforcement{
			ReadOnly:      true,
			NetworkGrants: true,
			ShellGrants:   true,
		},
	}
	t.Run("capability", func(t *testing.T) {
		probe := probe
		probe.Capabilities.RepoRead = false
		if _, err := admitProbe(
			movement,
			part,
			policy,
			performer,
			probe,
		); !errors.Is(err, ErrCapability) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("strict enforcement", func(t *testing.T) {
		probe := probe
		probe.Enforcement.ReadOnly = false
		if _, err := admitProbe(
			movement,
			part,
			policy,
			performer,
			probe,
		); !errors.Is(err, ErrEnforcement) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("advisory records exact dimension", func(t *testing.T) {
		probe := probe
		probe.Enforcement.ReadOnly = false
		performer := performer
		performer.AllowAdvisoryEnforcement = true
		dimensions, err := admitProbe(
			movement,
			part,
			policy,
			performer,
			probe,
		)
		if err != nil || len(dimensions) != 1 ||
			dimensions[0] != "read_only" {
			t.Fatalf("dimensions=%v error=%v", dimensions, err)
		}
	})
}

func TestRecognizedFeaturesIgnoresUndefinedTokens(t *testing.T) {
	recognized := recognizedFeatures([]string{"future_token", "vendor_extension"})
	if recognized == nil || len(recognized) != 0 {
		t.Fatalf("recognized features = %#v, want empty non-nil list", recognized)
	}
}

func TestRunIDWriteFailureStopsBeforeDriverAuthority(t *testing.T) {
	preparation := prepareRunnableFixture(t, sliceScore(), sliceCast())
	writeErr := errors.New("stdout unavailable")
	result := run(
		context.Background(),
		preparation,
		func(runID runstate.RunID) error {
			if runID == "" {
				t.Fatal("observer received empty run id")
			}
			return writeErr
		},
		testDependencies(),
	)
	if result.RunID == "" || result.Outcome != OutcomeInterrupted ||
		result.Reason != "" || !errors.Is(result.Err, writeErr) {
		t.Fatalf("result = %#v", result)
	}
	state := replayDriverState(t, preparation, result.RunID)
	if state.Run.Terminal() || state.Authority.Epoch != 0 {
		t.Fatalf(
			"run=%s authority_epoch=%d",
			state.Run,
			state.Authority.Epoch,
		)
	}
	assertNoDriverLease(t, preparation.RepositoryRoot, result.RunID)
}

func TestUnrepresentableWireBudgetInterruptsBeforeDriverAuthority(t *testing.T) {
	scoreDocument := sliceScore()
	budget := scoreDocument["policy"].(map[string]any)["budget"].(map[string]any)
	budget["active_wall_clock_min"] = float64((1<<53-1)/60_000 + 1)
	preparation := prepareRunnableFixture(t, scoreDocument, sliceCast())
	result := run(
		context.Background(),
		preparation,
		func(runstate.RunID) error { return nil },
		testDependencies(),
	)
	if result.RunID == "" || result.Outcome != OutcomeInterrupted ||
		result.Reason != "" || result.Err == nil {
		t.Fatalf("result = %#v", result)
	}
	state := replayDriverState(t, preparation, result.RunID)
	if state.Run.Terminal() || state.Authority.Epoch != 0 {
		t.Fatalf(
			"run=%s authority_epoch=%d",
			state.Run,
			state.Authority.Epoch,
		)
	}
	assertNoDriverLease(t, preparation.RepositoryRoot, result.RunID)
}

func TestRunStartsWriterMovementsBeforeAttemptExecution(t *testing.T) {
	for _, waived := range []bool{false, true} {
		t.Run(fmt.Sprintf("waived=%t", waived), func(t *testing.T) {
			preparation := prepareRunnableFixture(t, writerSliceScore(waived), sliceCast())
			starts := 0
			startErr := errors.New("workspace start reached")
			dependencies := testDependencies()
			dependencies.workspaceStart = func(*validate.Preparation, faultpoint.Probe) (workspace.StartResult, error) {
				starts++
				return workspace.StartResult{}, startErr
			}
			result := run(context.Background(), preparation, func(runstate.RunID) error { return nil }, dependencies)
			if result.RunID != "" || !errors.Is(result.Err, startErr) || starts != 1 {
				t.Fatalf("result=%+v workspace starts=%d", result, starts)
			}
		})
	}
}

func TestCaptureAndRecordChangeSetPinsRefBeforeOneNoOpEvent(t *testing.T) {
	preparation, store, authority, started, attempt := writerCaptureFixture(t)
	defer authority.Release()
	appends := 0
	first, err := captureAndRecordChangeSet(attempt, authority, func(event runstate.Event) error {
		journal, err := store.ReadJournal(started.RunID)
		if err != nil {
			return err
		}
		for _, previous := range journal.Events {
			if previous.Type == runstate.EventChangeSetRecorded && previous.AttemptID == attempt.AttemptID {
				t.Fatalf("change set event already present before its ref callback: %+v", previous)
			}
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		ref, _ := payload["ref"].(string)
		commit, _ := payload["commit"].(string)
		contents, err := os.ReadFile(filepath.Join(preparation.RepositoryRoot, ".git", filepath.FromSlash(ref)))
		if err != nil || strings.TrimSpace(string(contents)) != commit {
			t.Fatalf("change set ref before event = (%q, %v), want %q", contents, err, commit)
		}
		appends++
		_, err = authority.Append(event, "test.change_set.recorded")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.BaseTree != first.ResultTree {
		t.Fatalf("no-op change set trees = %q and %q, want equal", first.BaseTree, first.ResultTree)
	}
	second, err := CaptureAndRecordChangeSet(attempt, authority)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || appends != 1 {
		t.Fatalf("second capture=%#v appends=%d, want one event and id %q", second, appends, first.ID)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	matching := 0
	for _, event := range journal.Events {
		if event.Type == runstate.EventChangeSetRecorded && event.AttemptID == attempt.AttemptID {
			matching++
		}
	}
	if matching == 0 {
		t.Fatal("capture journal inspection observed zero matching change_set.recorded events")
	}
	if matching != 1 {
		t.Fatalf("change_set.recorded events = %d, want 1", matching)
	}
}

func TestWriterVerificationRejectsProtectedPathWithoutAttestation(t *testing.T) {
	_, store, authority, started, attempt := writerCaptureFixture(t)
	defer authority.Release()
	protected := filepath.Join(attempt.Worktree, "partitur.yaml")
	if err := os.WriteFile(protected, []byte("corrupt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(protected)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "corrupt\n" {
		t.Fatalf("protected mutation = %q, want corrupt content", contents)
	}

	failed, err := completeAttemptVerification(
		attempt,
		true,
		authority,
		writerVerificationAppender(t, authority, started.RunID, attempt),
		grantDeniedClassifier(t),
	)
	if !failed {
		t.Fatal("protected path violation did not record attempt.failed")
	}
	var verification *workspace.VerificationError
	if !errors.As(err, &verification) || verification.Reason != "protected_path_violation" {
		t.Fatalf("verification error = %#v, want protected_path_violation", err)
	}

	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var failedEvent *runstate.Event
	for index := range journal.Events {
		event := &journal.Events[index]
		if event.AttemptID != attempt.AttemptID {
			continue
		}
		if event.Type == runstate.EventVerificationPassed {
			t.Fatal("verification.passed appended for protected path violation")
		}
		if event.Type == runstate.EventChangeSetRecorded {
			t.Fatal("change_set.recorded appended before protected path verification")
		}
		if event.Type == runstate.EventAttemptFailed {
			failedEvent = event
		}
	}
	if failedEvent == nil {
		t.Fatal("attempt.failed event is absent")
	}
	payload := decodeDriverPayload(t, *failedEvent)
	if payload["kind"] != successor.KindGrantDenied || payload["reason"] != "protected_path_violation" {
		t.Fatalf("attempt.failed payload = %#v", payload)
	}
	disposition, ok := payload["disposition"].(map[string]any)
	if !ok || disposition["terminal_reason"] != successor.KindGrantDenied {
		t.Fatalf("attempt.failed disposition = %#v", payload["disposition"])
	}
}

func TestWriterVerificationRecordsAttestationAfterProtectedPathCheck(t *testing.T) {
	_, store, authority, started, attempt := writerCaptureFixture(t)
	defer authority.Release()

	failed, err := completeAttemptVerification(
		attempt,
		true,
		authority,
		writerVerificationAppender(t, authority, started.RunID, attempt),
		grantDeniedClassifier(t),
	)
	if err != nil || failed {
		t.Fatalf("writer verification = failed:%t err:%v", failed, err)
	}

	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	changeSetIndex := -1
	verificationIndex := -1
	for index, event := range journal.Events {
		if event.AttemptID != attempt.AttemptID {
			continue
		}
		switch event.Type {
		case runstate.EventChangeSetRecorded:
			changeSetIndex = index
		case runstate.EventVerificationPassed:
			verificationIndex = index
		}
	}
	if changeSetIndex == -1 || verificationIndex == -1 || changeSetIndex >= verificationIndex {
		t.Fatalf("writer verification event order: change_set=%d verification=%d", changeSetIndex, verificationIndex)
	}
}

func TestPostCreationOperationalFailureLeavesResumableRun(t *testing.T) {
	castDocument := sliceCast()
	worker := castDocument["performers"].(map[string]any)["worker"].(map[string]any)
	worker["adapter"] = "driver-interruption-fixture"
	preparation := prepareRunnableFixture(t, sliceScore(), castDocument)
	result := run(
		context.Background(),
		preparation,
		func(runstate.RunID) error { return nil },
		testDependencies(),
	)
	if result.RunID == "" || result.Outcome != OutcomeInterrupted ||
		result.Reason != "" || result.Err == nil {
		t.Fatalf("result = %#v", result)
	}
	state := replayDriverState(t, preparation, result.RunID)
	if state.Run.Terminal() {
		t.Fatalf("operational interruption terminalized run as %s", state.Run)
	}
	if state.Authority.Epoch != 1 {
		t.Fatalf("authority epoch = %d, want 1", state.Authority.Epoch)
	}
	assertNoDriverLease(t, preparation.RepositoryRoot, result.RunID)
}

func TestRunTerminalizesDurableCancellationBeforeAttemptSetup(t *testing.T) {
	preparation := prepareRunnableFixture(t, sliceScore(), sliceCast())
	result := run(
		context.Background(),
		preparation,
		func(runID runstate.RunID) error {
			store, err := runstore.New(preparation.RepositoryRoot, faultpoint.Nop{})
			if err != nil {
				return err
			}
			return store.RequestCancellation(runID)
		},
		testDependencies(),
	)
	if result.Outcome != OutcomeCancelled || result.Err != nil {
		t.Fatalf("result=%+v", result)
	}
	store, err := runstore.New(preparation.RepositoryRoot, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []runstate.EventType
	var cancelled runstate.Event
	for _, event := range journal.Events {
		kinds = append(kinds, event.Type)
		if event.Type == runstate.EventRunCancelled {
			cancelled = event
		}
	}
	want := []runstate.EventType{
		runstate.EventRunStarted,
		runstate.EventCancelRequested,
		runstate.EventAuthorityGranted,
		runstate.EventRunCancelled,
	}
	if !slices.Equal(kinds, want) {
		t.Fatalf("journal event order=%v want=%v", kinds, want)
	}
	var payload map[string]any
	if err := json.Unmarshal(cancelled.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if fenced, ok := payload["fenced_epoch"].(float64); !ok || fenced != 2 {
		t.Fatalf("run.cancelled payload=%s", cancelled.Payload)
	}
	state := replayDriverState(t, preparation, result.RunID)
	if state.Run != runstate.RunCancelled {
		t.Fatalf("run state=%s", state.Run)
	}
	assertNoDriverLease(t, preparation.RepositoryRoot, result.RunID)
}

func TestStoppedClassifiesOnlyAppendixDHalts(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		outcome Outcome
		reason  string
	}{
		{
			name:    "ordinary operational failure",
			err:     errors.New("filesystem unavailable"),
			outcome: OutcomeInterrupted,
		},
		{
			name:    "journal corrupt",
			err:     runstore.ErrJournalCorrupt,
			outcome: OutcomeHalted,
			reason:  "journal_corrupt",
		},
		{
			name:    "journal idempotency conflict",
			err:     runstore.ErrJournalIdempotencyConflict,
			outcome: OutcomeHalted,
			reason:  "journal_idempotency_conflict",
		},
		{
			name:    "owner unverifiable",
			err:     runstore.ErrLeaseOwnerUnverifiable,
			outcome: OutcomeHalted,
			reason:  "owner_unverifiable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := stopped(Result{RunID: "run-1"}, test.err)
			if result.Outcome != test.outcome ||
				result.Reason != test.reason ||
				!errors.Is(result.Err, test.err) {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestRecoveryHaltRetainsDriverLease(t *testing.T) {
	if releasesDriverLease(OutcomeHalted) {
		t.Fatal("recovery halt would release its safety interlock")
	}
	for _, outcome := range []Outcome{
		OutcomeSucceeded,
		OutcomeFailed,
		OutcomeInterrupted,
	} {
		if !releasesDriverLease(outcome) {
			t.Fatalf("outcome %s would strand a live driver's lease", outcome)
		}
	}
	if releasesDriverLease(OutcomeCancelled) {
		t.Fatal("cancellation oracle already removes the driver lease")
	}
}

func TestBudgetTimeoutDoesNotOverflowWireSafeRemainder(t *testing.T) {
	const maxWireSafeMS = int64(1<<53 - 1)
	const maxWholeMinutes = maxWireSafeMS / 60_000
	remaining, err := initialRemainingMS(maxWholeMinutes)
	if err != nil || remaining != maxWholeMinutes*60_000 {
		t.Fatalf("maximum whole-minute remainder=%d error=%v", remaining, err)
	}
	if _, err := initialRemainingMS(maxWholeMinutes + 1); err == nil {
		t.Fatal("unrepresentable minute budget was admitted to the wire")
	}
	if timeout := budgetTimeout(maxWireSafeMS); timeout != time.Duration(1<<63-1) {
		t.Fatalf("timeout = %s", timeout)
	}
	if timeout := budgetTimeout(600_000); timeout != 10*time.Minute {
		t.Fatalf("ordinary timeout = %s", timeout)
	}
}

func TestLiveSelectionMismatchRejectsEachSelectorConjunct(t *testing.T) {
	want := recovery.ActionAppendMovementReady
	movementID := runstate.MovementID("write")
	for _, test := range []struct {
		name   string
		action *recovery.Action
		want   bool
	}{
		{name: "nil action", want: true},
		{name: "empty action", action: &recovery.Action{MovementID: movementID}, want: true},
		{name: "wrong action", action: &recovery.Action{Kind: recovery.ActionAppendMovementStarted, MovementID: movementID}, want: true},
		{name: "wrong movement", action: &recovery.Action{Kind: want, MovementID: "check"}, want: true},
		{name: "wrong movement above target", action: &recovery.Action{Kind: want, MovementID: "zcheck"}, want: true},
		{name: "exact selection", action: &recovery.Action{Kind: want, MovementID: movementID}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := liveSelectionMismatch(test.action, want, movementID); got != test.want {
				t.Fatalf("liveSelectionMismatch() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestLiveMaterializationMismatchRejectsEachSelectorConjunct(t *testing.T) {
	pending := &recovery.PendingSuccessor{MovementID: "inspect", Performer: "worker", Reason: "quality_retry"}
	for _, test := range []struct {
		name   string
		action *recovery.Action
		want   bool
	}{
		{name: "nil action", want: true},
		{name: "wrong action", action: &recovery.Action{Kind: recovery.ActionAppendMovementReady, MovementID: "inspect", PendingSuccessor: pending}, want: true},
		{name: "movement below target", action: &recovery.Action{Kind: recovery.ActionMaterializeSuccessor, MovementID: "check", PendingSuccessor: pending}, want: true},
		{name: "movement above target", action: &recovery.Action{Kind: recovery.ActionMaterializeSuccessor, MovementID: "zcheck", PendingSuccessor: pending}, want: true},
		{name: "pending successor absent", action: &recovery.Action{Kind: recovery.ActionMaterializeSuccessor, MovementID: "inspect"}, want: true},
		{name: "exact materialization", action: &recovery.Action{Kind: recovery.ActionMaterializeSuccessor, MovementID: "inspect", PendingSuccessor: pending}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := liveMaterializationMismatch(test.action, pending); got != test.want {
				t.Fatalf("liveMaterializationMismatch()=%t want=%t", got, test.want)
			}
		})
	}
}

func TestLiveNoneRealizationMismatchRejectsEachConjunct(t *testing.T) {
	for _, test := range []struct {
		name        string
		realization successor.Realization
		want        bool
	}{
		{name: "wrong action below target", realization: successor.Realization{Charge: successor.ChargeNone}, want: true},
		{name: "wrong action above target", realization: successor.Realization{Action: successor.ActionPendingSuccessor, Charge: successor.ChargeNone}, want: true},
		{name: "wrong charge below target", realization: successor.Realization{Action: successor.ActionMovementFailed}, want: true},
		{name: "wrong charge above target", realization: successor.Realization{Action: successor.ActionMovementFailed, Charge: successor.ChargeFallback}, want: true},
		{name: "wrong charge above fallback", realization: successor.Realization{Action: successor.ActionMovementFailed, Charge: successor.ChargeQualityRetry}, want: true},
		{name: "exact terminal realization", realization: successor.Realization{Action: successor.ActionMovementFailed, Charge: successor.ChargeNone}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := liveNoneRealizationMismatch(test.realization); got != test.want {
				t.Fatalf("liveNoneRealizationMismatch() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestLiveBetweenUnitEntryRejectsTerminalRunWithMatchingLease(t *testing.T) {
	preparation, store, authority, start := liveEntryFixture(t)
	defer authority.Release()
	if _, err := authority.Append(runstate.Event{
		RunID: start.RunID, ScoreRevision: preparation.Score.Revision(),
		Type: runstate.EventRunFailed, Payload: testPayload(t, map[string]any{"reason": "movement_failed"}),
	}, faultpoint.ReceiptAddress("test.run.failed")); err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(start.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !input.Projection.State.Run.Terminal() {
		t.Fatalf("run=%s, want terminal", input.Projection.State.Run)
	}
	if !liveBetweenUnitEntry(input.Projection, store, authority) {
		return
	}
	t.Fatal("terminal run with matching lease entered live between-unit path")
}

func TestLiveBetweenUnitEntryRejectsOpenExecution(t *testing.T) {
	preparation, store, authority, start := liveEntryFixture(t)
	defer authority.Release()
	if _, err := authority.Append(runstate.Event{
		RunID: start.RunID, ScoreRevision: preparation.Score.Revision(),
		Type: runstate.EventExecutionStarted,
		Payload: testPayload(t, map[string]any{
			"interval_id": "adapter-1", "phase": "adapter", "wall_start": "2026-07-30T00:00:00.000Z", "remaining_at_start": 600000,
		}),
	}, faultpoint.ReceiptAddress("test.execution.started")); err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(start.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.OpenExecution == nil {
		t.Fatal("fixture did not retain an open execution interval")
	}
	if !liveBetweenUnitEntry(input.Projection, store, authority) {
		return
	}
	t.Fatal("open execution interval entered live between-unit path")
}

func TestLiveMovementCompositionTerminalBindsItsEvidence(t *testing.T) {
	_, store, authority, started := liveEntryFixture(t)
	defer authority.Release()
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []runstate.EventType{runstate.EventMovementReady, runstate.EventMovementStarted} {
		if _, err := authority.Append(runstate.Event{
			RunID: started.RunID, ScoreRevision: input.Projection.State.ScoreHead.Revision,
			MovementID: "inspect", Type: eventType, Payload: testPayload(t, map[string]any{}),
		}, faultpoint.ReceiptAddress("test.live_composition."+string(eventType))); err != nil {
			t.Fatal(err)
		}
	}
	_, err = composeMovementBase(
		store,
		authority,
		input,
		"inspect",
		[]workspace.CompositionContributor{{
			MovementID: "dependency", ChangeSetID: "sha256:change-set",
			BaseTree: input.BaseTree, ResultTree: "git-sha1:missing-tree",
		}},
		1,
		time.Now,
		func() (string, error) { return "composition-interval", nil },
	)
	if !errors.Is(err, ErrCompositionTerminalized) {
		t.Fatalf("compose movement base error = %v, want terminalized composition", err)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var evidence, terminal runstate.Event
	for _, event := range journal.Events {
		switch event.Type {
		case runstate.EventCompositionFailed:
			evidence = event
		case runstate.EventMovementFailed:
			terminal = event
		}
	}
	if evidence.EventID == "" || terminal.EventID == "" {
		t.Fatalf("live composition evidence=%+v terminal=%+v", evidence, terminal)
	}
	if terminal.CausationID != evidence.EventID {
		t.Fatalf("live movement.failed causation = %q, want evidence %q", terminal.CausationID, evidence.EventID)
	}
	reloaded, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Projection.CompositionTerminals; len(got) != 0 {
		t.Fatalf("bound live composition terminal remains open after reload = %+v", got)
	}
}

func TestLiveMovementCompositionTerminalSerializesCancellationAfterEvidence(t *testing.T) {
	_, store, authority, started := liveEntryFixture(t)
	defer authority.Release()
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []runstate.EventType{runstate.EventMovementReady, runstate.EventMovementStarted} {
		if _, err := authority.Append(runstate.Event{
			RunID: started.RunID, ScoreRevision: input.Projection.State.ScoreHead.Revision,
			MovementID: "inspect", Type: eventType, Payload: testPayload(t, map[string]any{}),
		}, faultpoint.ReceiptAddress("test.live_composition_interleave."+string(eventType))); err != nil {
			t.Fatal(err)
		}
	}
	requestStarted := make(chan struct{})
	requestDone := make(chan error, 1)
	hook := func() {
		go func() {
			close(requestStarted)
			requestDone <- store.RequestCancellation(started.RunID)
		}()
		<-requestStarted
	}
	_, err = composeMovementBaseWithAfterEvidence(
		store,
		authority,
		input,
		"inspect",
		[]workspace.CompositionContributor{{
			MovementID: "dependency", ChangeSetID: "sha256:change-set",
			BaseTree: input.BaseTree, ResultTree: "git-sha1:missing-tree",
		}},
		1,
		time.Now,
		func() (string, error) { return "composition-interval", nil },
		hook,
	)
	if !errors.Is(err, ErrCompositionTerminalized) {
		t.Fatalf("compose movement base error = %v, want terminalized composition", err)
	}
	select {
	case err := <-requestDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation request did not complete after composition terminalized")
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	stoppedIndex, evidenceIndex, terminalIndex, cancellationIndex := -1, -1, -1, -1
	for index, event := range journal.Events {
		switch event.Type {
		case runstate.EventExecutionStopped:
			stoppedIndex = index
		case runstate.EventCompositionFailed:
			evidenceIndex = index
		case runstate.EventMovementFailed:
			terminalIndex = index
		case runstate.EventCancelRequested:
			cancellationIndex = index
		}
	}
	if stoppedIndex < 0 || evidenceIndex < 0 || terminalIndex < 0 || cancellationIndex < 0 {
		t.Fatalf("composition/cancellation journal sequence is incomplete: %+v", journal.Events)
	}
	if !(stoppedIndex < evidenceIndex && evidenceIndex < terminalIndex && terminalIndex < cancellationIndex) {
		t.Fatalf("cancellation interleaved with composition terminal: stopped=%d evidence=%d terminal=%d cancellation=%d", stoppedIndex, evidenceIndex, terminalIndex, cancellationIndex)
	}
}

func TestLiveMovementCompositionConflictTerminalBindsItsEvidence(t *testing.T) {
	preparation, store, authority, started := liveEntryFixture(t)
	defer authority.Release()
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	git := func(arguments ...string) string {
		t.Helper()
		command := exec.Command("git", arguments...)
		command.Dir = preparation.RepositoryRoot
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	path := filepath.Join(preparation.RepositoryRoot, "partitur.yaml")
	if err := os.WriteFile(path, []byte("ours\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "partitur.yaml")
	git("commit", "-m", "conflicting ours")
	ours := "git-sha1:" + git("rev-parse", "HEAD^{tree}")
	git("reset", "--hard", "HEAD~1")
	if err := os.WriteFile(path, []byte("theirs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "partitur.yaml")
	git("commit", "-m", "conflicting theirs")
	theirs := "git-sha1:" + git("rev-parse", "HEAD^{tree}")

	for _, eventType := range []runstate.EventType{runstate.EventMovementReady, runstate.EventMovementStarted} {
		if _, err := authority.Append(runstate.Event{
			RunID: started.RunID, ScoreRevision: input.Projection.State.ScoreHead.Revision,
			MovementID: "inspect", Type: eventType, Payload: testPayload(t, map[string]any{}),
		}, faultpoint.ReceiptAddress("test.live_composition_conflict."+string(eventType))); err != nil {
			t.Fatal(err)
		}
	}
	_, err = composeMovementBase(
		store,
		authority,
		input,
		"inspect",
		[]workspace.CompositionContributor{
			{MovementID: "ours", ChangeSetID: "sha256:ours", BaseTree: input.BaseTree, ResultTree: ours},
			{MovementID: "theirs", ChangeSetID: "sha256:theirs", BaseTree: input.BaseTree, ResultTree: theirs},
		},
		1,
		time.Now,
		func() (string, error) { return "composition-interval", nil },
	)
	if !errors.Is(err, ErrCompositionTerminalized) {
		t.Fatalf("compose movement base error = %v, want terminalized composition", err)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var evidence, terminal runstate.Event
	for _, event := range journal.Events {
		switch event.Type {
		case runstate.EventCompositionConflicted:
			evidence = event
		case runstate.EventMovementFailed:
			terminal = event
		}
	}
	if evidence.EventID == "" || terminal.EventID == "" {
		t.Fatalf("live composition evidence=%+v terminal=%+v", evidence, terminal)
	}
	if terminal.CausationID != evidence.EventID {
		t.Fatalf("live movement.failed causation = %q, want evidence %q", terminal.CausationID, evidence.EventID)
	}
	reloaded, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Projection.CompositionTerminals; len(got) != 0 {
		t.Fatalf("bound live composition conflict remains open after reload = %+v", got)
	}
}

func TestComposeMovementBasePinsCleanComposedTreeAndMergeHash(t *testing.T) {
	preparation, store, authority, started := liveEntryFixture(t)
	defer authority.Release()
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	git := func(arguments ...string) string {
		t.Helper()
		command := exec.Command("git", arguments...)
		command.Dir = preparation.RepositoryRoot
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	oursPath := filepath.Join(preparation.RepositoryRoot, "ours.txt")
	if err := os.WriteFile(oursPath, []byte("ours\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "ours.txt")
	git("commit", "-m", "clean ours")
	ours := "git-sha1:" + git("rev-parse", "HEAD^{tree}")
	git("reset", "--hard", "HEAD~1")
	theirsPath := filepath.Join(preparation.RepositoryRoot, "theirs.txt")
	if err := os.WriteFile(theirsPath, []byte("theirs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "theirs.txt")
	git("commit", "-m", "clean theirs")
	theirs := "git-sha1:" + git("rev-parse", "HEAD^{tree}")
	contributors := []workspace.CompositionContributor{
		{MovementID: "ours", ChangeSetID: "sha256:ours", BaseTree: input.BaseTree, ResultTree: ours},
		{MovementID: "theirs", ChangeSetID: "sha256:theirs", BaseTree: input.BaseTree, ResultTree: theirs},
	}
	expected := workspace.Compose(workspace.CompositionInput{
		RepositoryRoot: preparation.RepositoryRoot,
		BaseTree:       input.BaseTree,
		Contributors:   contributors,
	})
	if expected.ResultTree == "" {
		t.Fatalf("clean composition result = %+v", expected)
	}
	wantHash, err := movementCompositionMergeDependencyHash("inspect", input.BaseTree, contributors, expected.EnvironmentHash)
	if err != nil {
		t.Fatal(err)
	}
	identityHash, err := movementCompositionDependencyHash("inspect", input.BaseTree)
	if err != nil {
		t.Fatal(err)
	}
	composed, err := composeMovementBase(
		store, authority, input, "inspect", contributors, 1, time.Now,
		func() (string, error) { return "composition-interval", nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if composed.Tree != expected.ResultTree || composed.Hash != wantHash || composed.Commit == "" {
		t.Fatalf("composed base = %+v, want tree=%q hash=%q and a wrapper commit", composed, expected.ResultTree, wantHash)
	}
	if composed.Hash == identityHash {
		t.Fatalf("composition hash = %q, want merge variant rather than identity variant %q", composed.Hash, identityHash)
	}
	ref := "refs/partitur/runs/" + string(started.RunID) + "/movements/inspect/base"
	if got := git("rev-parse", ref); got != composed.Commit {
		t.Fatalf("pinned movement base = %q, want wrapper %q", got, composed.Commit)
	}
	if got := "git-sha1:" + git("show", "-s", "--format=%T", composed.Commit); got != composed.Tree {
		t.Fatalf("wrapper tree = %q, want composed tree %q", got, composed.Tree)
	}
	if got := "git-sha1:" + git("rev-parse", composed.Commit+"^"); got != input.BaseCommit {
		t.Fatalf("wrapper parent = %q, want run base %q", got, input.BaseCommit)
	}
}

func TestPrepareMovementBaseUsesIdentityForZeroContributors(t *testing.T) {
	_, store, authority, started := liveEntryFixture(t)
	defer authority.Release()
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	base, err := PrepareMovementBase(store, authority, input, "inspect", 1, time.Now, func() (string, error) { return "unused", nil })
	if err != nil {
		t.Fatal(err)
	}
	wantHash, err := movementCompositionDependencyHash("inspect", input.BaseTree)
	if err != nil {
		t.Fatal(err)
	}
	if base.Commit != "" || base.Tree != input.BaseTree || base.Hash != wantHash {
		t.Fatalf("zero-contributor movement base = %+v, want empty commit, tree %q, hash %q", base, input.BaseTree, wantHash)
	}
}

func TestComposeMovementBaseReportsEachMissingOperand(t *testing.T) {
	_, store, authority, started := liveEntryFixture(t)
	defer authority.Release()
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	contributors := []workspace.CompositionContributor{{
		MovementID: "dependency", ChangeSetID: "sha256:change-set", BaseTree: input.BaseTree, ResultTree: input.BaseTree,
	}}
	validNow := func() time.Time { return time.Now() }
	validNewID := func() (string, error) { return "composition-interval", nil }
	for _, test := range []struct {
		name string
		call func() error
	}{
		{name: "store", call: func() error {
			_, err := composeMovementBase(nil, authority, input, "inspect", contributors, 1, validNow, validNewID)
			return err
		}},
		{name: "authority", call: func() error {
			_, err := composeMovementBase(store, nil, input, "inspect", contributors, 1, validNow, validNewID)
			return err
		}},
		{name: "contributors", call: func() error {
			_, err := composeMovementBase(store, authority, input, "inspect", nil, 1, validNow, validNewID)
			return err
		}},
		{name: "now", call: func() error {
			_, err := composeMovementBase(store, authority, input, "inspect", contributors, 1, nil, validNewID)
			return err
		}},
		{name: "newID", call: func() error {
			_, err := composeMovementBase(store, authority, input, "inspect", contributors, 1, validNow, nil)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, want := test.call().Error(), "driver: incomplete movement composition execution: missing "+test.name; got != want {
				t.Fatalf("composition precondition error = %q, want %q", got, want)
			}
		})
	}
}

func TestLiveCompositionConflictStopsBeforeCreatingTargetAttempt(t *testing.T) {
	_, store, authority, started, ours, theirs := fanInConflictFixture(t)
	defer authority.Release()
	control, err := cancellation.Watch(store, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Stop()
	result := liveRunLoop(context.Background(), Result{RunID: started.RunID}, started.Run, store, authority, control, testDependencies())
	if result.Outcome != OutcomeFailed || result.Reason != "composition_terminal" || result.Err != nil {
		t.Fatalf("live result = %+v, want composition terminal failure", result)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range journal.Events {
		if event.MovementID == "target" && event.Type == runstate.EventPerformerSelected {
			t.Fatalf("target attempt was selected after conflicting %q and %q", ours, theirs)
		}
	}
}

func TestLiveFanInCreatesTargetAtPinnedBaseCommit(t *testing.T) {
	preparation, store, authority, started, _, _ := fanInCleanFixture(t)
	defer authority.Release()
	control, err := cancellation.Watch(store, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Stop()
	result := liveRunLoop(context.Background(), Result{RunID: started.RunID}, started.Run, store, authority, control, testDependencies())
	if result.Outcome != OutcomeInterrupted || result.Err == nil {
		t.Fatalf("live fan-in result = %+v, want ordinary attempt execution stop after creation", result)
	}
	state, err := authority.State()
	if err != nil {
		t.Fatal(err)
	}
	var targetAttempt runstate.AttemptID
	for attemptID, attempt := range state.Attempts {
		if attempt.MovementID == "target" {
			targetAttempt = attemptID
			break
		}
	}
	if targetAttempt == "" {
		t.Fatal("live fan-in did not create a target attempt")
	}
	git := func(directory string, arguments ...string) string {
		t.Helper()
		command := exec.Command("git", arguments...)
		command.Dir = directory
		output, err := command.Output()
		if err != nil {
			t.Fatalf("git %v: %v", arguments, err)
		}
		return strings.TrimSpace(string(output))
	}
	ref := "refs/partitur/runs/" + string(started.RunID) + "/movements/target/base"
	pinned := git(preparation.RepositoryRoot, "rev-parse", ref)
	worktree := filepath.Join(preparation.RepositoryRoot, ".partitur", "work", string(started.RunID), string(targetAttempt), "worktree")
	if got := git(worktree, "rev-parse", "HEAD^{commit}"); got != pinned {
		t.Fatalf("target worktree commit = %q, want pinned fan-in base %q", got, pinned)
	}
}

func TestLiveBetweenUnitEntryRejectsFreshHeadAttempt(t *testing.T) {
	_, store, authority, start := liveEntryFixture(t)
	defer authority.Release()
	input, err := store.LoadRunInput(start.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []runstate.AttemptState{
		runstate.AttemptStarting,
		runstate.AttemptRunning,
		runstate.AttemptVerifying,
	} {
		projection := input.Projection
		projection.CurrentHeadAttempt = &recovery.AttemptRecovery{
			ScoreRevision: projection.State.ScoreHead.Revision,
			State:         state,
		}
		if liveBetweenUnitEntry(projection, store, authority) {
			t.Fatalf("fresh %s attempt entered live between-unit path", state)
		}
	}
	for _, current := range []*recovery.AttemptRecovery{
		nil,
		{ScoreRevision: input.Projection.State.ScoreHead.Revision - 1, State: runstate.AttemptStarting},
		{ScoreRevision: input.Projection.State.ScoreHead.Revision, State: runstate.AttemptFailed},
	} {
		projection := input.Projection
		projection.CurrentHeadAttempt = current
		if !liveBetweenUnitEntry(projection, store, authority) {
			t.Fatalf("non-fresh current attempt %#v was refused", current)
		}
	}
}

func TestRealizeRecordedNoneDispositionCancelsBetweenMovementAndRunFailure(t *testing.T) {
	preparation, store, authority, start := liveEntryFixture(t)
	defer authority.Release()
	movementID := runstate.MovementID(preparation.Score.Movements()[0].ID)
	attemptID := runstate.AttemptID("attempt-1")
	for _, event := range []runstate.Event{
		{RunID: start.RunID, ScoreRevision: preparation.Score.Revision(), MovementID: movementID, Type: runstate.EventMovementReady, Payload: testPayload(t, map[string]any{})},
		{RunID: start.RunID, ScoreRevision: preparation.Score.Revision(), MovementID: movementID, Type: runstate.EventMovementStarted, Payload: testPayload(t, map[string]any{})},
		{RunID: start.RunID, ScoreRevision: preparation.Score.Revision(), MovementID: movementID, AttemptID: attemptID, Type: runstate.EventPerformerSelected, Payload: testPayload(t, map[string]any{"reason": "initial", "performer_id": "worker", "adapter_id": "codex", "model": "gpt-5.6-sol"})},
		{RunID: start.RunID, ScoreRevision: preparation.Score.Revision(), MovementID: movementID, AttemptID: attemptID, Type: runstate.EventAttemptFailed, Payload: testPayload(t, map[string]any{"kind": "grant_denied", "disposition": map[string]any{"charged": "none", "movement_terminal": true, "terminal_reason": "grant_denied"}})},
	} {
		if _, err := authority.Append(event, faultpoint.ReceiptAddress("test."+string(event.Type))); err != nil {
			t.Fatal(err)
		}
	}
	control, err := cancellation.Watch(store, start.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Stop()
	result, handled := realizeRecordedNoneDisposition(
		context.Background(), Result{RunID: start.RunID}, store, authority, control,
		dependencies{probe: faultpoint.Nop{}, afterMovementFailed: func() {
			if err := store.RequestCancellation(start.RunID); err != nil {
				t.Fatal(err)
			}
		}},
	)
	if !handled || result.Outcome != OutcomeCancelled || result.Err != nil {
		t.Fatalf("result=%+v handled=%t", result, handled)
	}
	journal, err := store.ReadJournal(start.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range journal.Events {
		if event.Type == runstate.EventRunFailed {
			t.Fatal("run.failed appended after cancellation became durable")
		}
	}
}

func TestLiveMaterializesRecordedSuccessorByRecordedDisposition(t *testing.T) {
	tests := []struct {
		name          string
		charged       string
		wantReason    string
		wantPerformer string
	}{
		{name: "quality retry keeps current performer", charged: "quality_retry", wantReason: "quality_retry", wantPerformer: "worker"},
		{name: "fallback chooses immediate unvisited performer", charged: "fallback", wantReason: "fallback", wantPerformer: "backup-a"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, authority, runID, input, failed := liveChargedSuccessorFixture(t, test.charged)
			defer authority.Release()

			// This is an independent oracle for the one scheduler step: these
			// constants come from the recorded disposition, not from the planner.
			decision := recovery.PlanBetweenUnit(input.Projection)
			if decision.CaseID != recovery.CaseScheduler || decision.Action == nil ||
				decision.Action.Kind != recovery.ActionMaterializeSuccessor ||
				decision.Action.MovementID != "inspect" || decision.Action.PendingSuccessor == nil ||
				decision.Action.PendingSuccessor.Performer != test.wantPerformer ||
				decision.Action.PendingSuccessor.Reason != test.wantReason {
				t.Fatalf("one-step decision = %+v, want materialized %s successor %s", decision, test.wantReason, test.wantPerformer)
			}

			result := liveMaterializeSuccessor(
				context.Background(), Result{RunID: runID}, store, authority, nil, testDependencies(), input,
			)
			if result.Outcome != OutcomeInterrupted || result.Err == nil {
				t.Fatalf("result=%+v, want adapter-resolution interruption after durable selection", result)
			}
			journal, err := store.ReadJournal(runID)
			if err != nil {
				t.Fatal(err)
			}
			wantOrder := []runstate.EventType{
				runstate.EventRunStarted, runstate.EventAuthorityGranted, runstate.EventApplicationCandidateRecorded,
				runstate.EventMovementReady, runstate.EventMovementStarted, runstate.EventPerformerSelected,
				runstate.EventAttemptFailed, runstate.EventPerformerSelected,
			}
			gotOrder := make([]runstate.EventType, len(journal.Events))
			for index, event := range journal.Events {
				gotOrder[index] = event.Type
			}
			if !slices.Equal(gotOrder, wantOrder) {
				t.Fatalf("durable event order=%v, want %v", gotOrder, wantOrder)
			}
			selected := journal.Events[len(journal.Events)-1]
			var payload map[string]any
			if err := json.Unmarshal(selected.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if selected.CausationID != failed.EventID || payload["reason"] != test.wantReason || payload["performer_id"] != test.wantPerformer {
				t.Fatalf("selection=%+v payload=%v, want causation=%q reason=%q performer=%q", selected, payload, failed.EventID, test.wantReason, test.wantPerformer)
			}
		})
	}
}

func TestLiveFallbackChainNeverRevisitsEarlierPerformer(t *testing.T) {
	store, authority, runID, input, _ := liveChargedSuccessorFixture(t, "fallback")
	defer authority.Release()
	for _, wantPerformer := range []string{"backup-a", "backup-b"} {
		decision := recovery.PlanBetweenUnit(input.Projection)
		if decision.Action == nil || decision.Action.PendingSuccessor == nil || decision.Action.PendingSuccessor.Performer != wantPerformer {
			t.Fatalf("pending successor=%+v, want %q", decision.Action, wantPerformer)
		}
		result := liveMaterializeSuccessor(context.Background(), Result{RunID: runID}, store, authority, nil, testDependencies(), input)
		if result.Outcome != OutcomeInterrupted || result.Err == nil {
			t.Fatalf("materialization result=%+v", result)
		}
		input = appendRecordedFailure(t, store, authority, runID, "rate_limited", "fallback")
	}
	if input.Projection.Scheduler.PendingSuccessor != nil {
		t.Fatalf("fallbacks exhausted, pending successor=%+v", input.Projection.Scheduler.PendingSuccessor)
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	var performers []string
	for _, event := range journal.Events {
		if event.Type != runstate.EventPerformerSelected {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		performers = append(performers, payload["performer_id"].(string))
	}
	if !slices.Equal(performers, []string{"worker", "backup-a", "backup-b"}) {
		t.Fatalf("fallback performers=%v, want no revisit", performers)
	}
}

func TestLiveChainTerminatesWhenBudgetExhaustsMidChain(t *testing.T) {
	store, authority, runID, input, _ := liveChargedSuccessorAcceptanceFixture(t, "quality_retry")
	defer authority.Release()
	current := input.Projection.CurrentHeadAttempt
	if current == nil {
		t.Fatal("failed attempt is absent")
	}
	for _, event := range []runstate.Event{
		{RunID: runID, ScoreRevision: input.Projection.State.ScoreHead.Revision, MovementID: current.MovementID, AttemptID: current.AttemptID, Type: runstate.EventExecutionStopped, Payload: testPayload(t, map[string]any{"interval_id": "acceptance", "reason": "normal", "charging": "measured", "charged_duration": 600000})},
	} {
		if _, err := authority.Append(event, faultpoint.ReceiptAddress("test."+string(event.Type))); err != nil {
			t.Fatal(err)
		}
	}
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.Scheduler.RemainingTime != 0 {
		t.Fatalf("remaining time=%d, want zero after acceptance close", input.Projection.Scheduler.RemainingTime)
	}
	terminal := liveMaterializeSuccessor(context.Background(), Result{RunID: runID}, store, authority, nil, testDependencies(), input)
	if terminal.Outcome != OutcomeFailed || terminal.Reason != "budget_exhausted" || terminal.Err != nil {
		t.Fatalf("terminal result=%+v", terminal)
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]runstate.EventType, len(journal.Events))
	for index, event := range journal.Events {
		got[index] = event.Type
	}
	want := []runstate.EventType{
		runstate.EventRunStarted, runstate.EventAuthorityGranted, runstate.EventApplicationCandidateRecorded,
		runstate.EventMovementReady, runstate.EventMovementStarted, runstate.EventPerformerSelected,
		runstate.EventAttemptStarted, runstate.EventAdapterProbed, runstate.EventPerformerCompleted,
		runstate.EventVerificationPassed, runstate.EventExecutionStarted, runstate.EventAcceptanceStarted,
		runstate.EventAcceptanceFailed, runstate.EventExecutionStopped,
		runstate.EventMovementFailed, runstate.EventRunFailed,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("budget terminal event order=%v want=%v", got, want)
	}
}

func TestLiveLoopFailsRunDirectlyWhenBudgetExhaustsBetweenMovements(t *testing.T) {
	scoreDocument := writerFreeTwoMovementWaivedScore()
	preparation := prepareRunnableFixture(t, scoreDocument, sliceCast())
	started, err := workspace.Start(preparation, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(preparation.RepositoryRoot, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.AcquireDriver(started.RunID, movementSeeds(preparation.Score))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Release()
	if err := started.Run.BindDriver(authority); err != nil {
		t.Fatal(err)
	}
	for _, event := range []runstate.Event{
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", Type: runstate.EventMovementReady, Payload: testPayload(t, map[string]any{})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", Type: runstate.EventMovementStarted, Payload: testPayload(t, map[string]any{})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", AttemptID: "attempt-1", Type: runstate.EventPerformerSelected, Payload: testPayload(t, map[string]any{"reason": "initial", "performer_id": "worker", "adapter_id": "codex", "model": "gpt-5.6-sol"})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", AttemptID: "attempt-1", Type: runstate.EventAttemptStarted, Payload: testPayload(t, map[string]any{"attempt_number": 1, "adapter_process": map[string]any{"pid": 999999, "session_id": 999999, "start_identity": map[string]any{"platform": "linux", "boot_id": "fixture", "start_ticks": "0"}}, "granted_authority": map[string]any{"paths_rw": []any{}, "paths_ro": []any{"**"}, "shell": false, "network": false}, "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", AttemptID: "attempt-1", Type: runstate.EventAdapterProbed, Payload: testPayload(t, map[string]any{"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": false, "shell": false, "network": false, "resumable_sessions": false}, "enforcement": map[string]any{"path_grants": true, "read_only": true, "network_grants": true, "shell_grants": true, "read_grants": true}, "negotiated_features": []any{}, "truncated_resolutions": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:dependency", "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", AttemptID: "attempt-1", Type: runstate.EventPerformerCompleted, Payload: testPayload(t, map[string]any{"session_hint_stored": false})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", AttemptID: "attempt-1", Type: runstate.EventVerificationPassed, Payload: testPayload(t, map[string]any{})},
		{RunID: started.RunID, ScoreRevision: 1, Type: runstate.EventExecutionStarted, Payload: testPayload(t, map[string]any{"interval_id": "acceptance-1", "phase": "acceptance", "wall_start": "2026-07-30T00:00:00.000Z", "remaining_at_start": 60000})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", AttemptID: "attempt-1", Type: runstate.EventAcceptanceStarted, Payload: testPayload(t, map[string]any{"subject_tree": "git-sha1:subject", "acceptance_spec_hash": "sha256:acceptance", "planned_criterion_ids": []any{}, "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", AttemptID: "attempt-1", Type: runstate.EventAcceptanceEvaluationCompleted, Payload: testPayload(t, map[string]any{"subject_tree": "git-sha1:subject", "acceptance_spec_hash": "sha256:acceptance", "criterion_outcomes": []any{}, "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}})},
		{RunID: started.RunID, ScoreRevision: 1, Type: runstate.EventExecutionStopped, Payload: testPayload(t, map[string]any{"interval_id": "acceptance-1", "reason": "normal", "charging": "measured", "charged_duration": 60000})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", AttemptID: "attempt-1", Type: runstate.EventAttemptCompleted, Payload: testPayload(t, map[string]any{})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", AttemptID: "attempt-1", Type: runstate.EventMovementSucceeded, Payload: testPayload(t, map[string]any{"approved_artifact_instance_ids": []any{}, "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}, "run_succeeded": false})},
	} {
		if _, err := authority.Append(event, faultpoint.ReceiptAddress("test."+string(event.Type))); err != nil {
			t.Fatal(err)
		}
	}
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.Scheduler.RemainingTime != 0 || input.Projection.State.Movements["second"] != runstate.MovementPending {
		t.Fatalf("pre-loop scheduler=%+v movements=%+v", input.Projection.Scheduler, input.Projection.State.Movements)
	}
	control, err := cancellation.Watch(store, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Stop()
	result := liveRunLoop(context.Background(), Result{RunID: started.RunID}, started.Run, store, authority, control, testDependencies())
	if result.Outcome != OutcomeFailed || result.Reason != "budget_exhausted" || result.Err != nil {
		t.Fatalf("result=%+v", result)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got := journal.Events[len(journal.Events)-1]; got.Type != runstate.EventRunFailed || got.MovementID != "" {
		t.Fatalf("last event=%+v, want direct run.failed", got)
	}
	for _, event := range journal.Events {
		if event.MovementID == "second" {
			t.Fatalf("pending successor was scheduled after exhaustion: %+v", event)
		}
	}
}

func TestLiveSuccessorDoesNotApplyRetryPolicyAttemptCap(t *testing.T) {
	store, authority, runID, input, _ := liveChargedSuccessorFixture(t, "quality_retry")
	defer authority.Release()
	current := input.Projection.CurrentHeadAttempt
	if current == nil || input.Projection.Scheduler.PendingSuccessor == nil {
		t.Fatal("charged successor fixture is incomplete")
	}
	current.FailureClassification.RetriesPerMovement = 0
	current.FailureClassification.Fallbacks = nil
	result := liveMaterializeSuccessor(context.Background(), Result{RunID: runID}, store, authority, nil, testDependencies(), input)
	if result.Outcome != OutcomeInterrupted || result.Err == nil {
		t.Fatalf("result=%+v, want selection then adapter-resolution interruption", result)
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	if got := journal.Events[len(journal.Events)-1].Type; got != runstate.EventPerformerSelected {
		t.Fatalf("last event=%s, want successor selection without a retry-policy attempt cap", got)
	}
}

func liveChargedSuccessorFixture(t *testing.T, charged string) (*runstore.Store, *runstore.Driver, runstate.RunID, runstore.RunInput, runstate.Event) {
	return liveChargedSuccessorFixtureAtAcceptanceCut(t, charged, false)
}

func liveChargedSuccessorAcceptanceFixture(t *testing.T, charged string) (*runstore.Store, *runstore.Driver, runstate.RunID, runstore.RunInput, runstate.Event) {
	return liveChargedSuccessorFixtureAtAcceptanceCut(t, charged, true)
}

func liveChargedSuccessorFixtureAtAcceptanceCut(t *testing.T, charged string, acceptanceOpen bool) (*runstore.Store, *runstore.Driver, runstate.RunID, runstore.RunInput, runstate.Event) {
	t.Helper()
	score := sliceScore()
	budget := score["policy"].(map[string]any)["budget"].(map[string]any)
	budget["retries_per_movement"] = float64(3)
	cast := sliceCast()
	performers := cast["performers"].(map[string]any)
	performers["backup-a"] = map[string]any{"adapter": "codex", "model": "gpt-5.6-terra"}
	performers["backup-b"] = map[string]any{"adapter": "codex", "model": "gpt-5.6-terra"}
	cast["bindings"].(map[string]any)["reader"] = map[string]any{"performer": "worker", "fallbacks": []any{"backup-a", "backup-b"}}
	preparation := prepareRunnableFixture(t, score, cast)
	start, err := workspace.Start(preparation, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(preparation.RepositoryRoot, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.AcquireDriver(start.RunID, movementSeeds(preparation.Score))
	if err != nil {
		t.Fatal(err)
	}
	if err := start.Run.BindDriver(authority); err != nil {
		t.Fatal(err)
	}
	if _, err := start.Run.RecordZeroWriterCandidate(); err != nil {
		t.Fatal(err)
	}
	movementID := runstate.MovementID("inspect")
	for _, event := range []runstate.Event{
		{RunID: start.RunID, ScoreRevision: preparation.Score.Revision(), MovementID: movementID, Type: runstate.EventMovementReady, Payload: testPayload(t, map[string]any{})},
		{RunID: start.RunID, ScoreRevision: preparation.Score.Revision(), MovementID: movementID, Type: runstate.EventMovementStarted, Payload: testPayload(t, map[string]any{})},
		{RunID: start.RunID, ScoreRevision: preparation.Score.Revision(), MovementID: movementID, AttemptID: "attempt-1", Type: runstate.EventPerformerSelected, Payload: testPayload(t, map[string]any{"reason": "initial", "performer_id": "worker", "adapter_id": "codex", "model": "gpt-5.6-sol"})},
	} {
		if _, err := authority.Append(event, faultpoint.ReceiptAddress("test."+string(event.Type))); err != nil {
			t.Fatal(err)
		}
	}
	kind := "task_failed"
	if charged == "fallback" {
		kind = "rate_limited"
	}
	if acceptanceOpen {
		versions := map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}
		for _, event := range []runstate.Event{
			{RunID: start.RunID, ScoreRevision: preparation.Score.Revision(), MovementID: movementID, AttemptID: "attempt-1", Type: runstate.EventAttemptStarted, Payload: testPayload(t, map[string]any{"attempt_number": 1, "adapter_process": map[string]any{"pid": 999999, "session_id": 999999, "start_identity": map[string]any{"platform": "linux", "boot_id": "boot", "start_ticks": "1"}}, "granted_authority": map[string]any{"paths_rw": []any{}, "paths_ro": []any{}, "shell": false, "network": false}, "identity_versions": versions})},
			{RunID: start.RunID, ScoreRevision: preparation.Score.Revision(), MovementID: movementID, AttemptID: "attempt-1", Type: runstate.EventAdapterProbed, Payload: testPayload(t, map[string]any{"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": false, "shell": false, "network": false, "resumable_sessions": false}, "enforcement": map[string]any{"path_grants": true, "read_only": true, "network_grants": true, "shell_grants": true, "read_grants": true}, "negotiated_features": []any{}, "truncated_resolutions": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:dependency", "identity_versions": versions})},
			{RunID: start.RunID, ScoreRevision: preparation.Score.Revision(), MovementID: movementID, AttemptID: "attempt-1", Type: runstate.EventPerformerCompleted, Payload: testPayload(t, map[string]any{"session_hint_stored": false})},
			{RunID: start.RunID, ScoreRevision: preparation.Score.Revision(), MovementID: movementID, AttemptID: "attempt-1", Type: runstate.EventVerificationPassed, Payload: testPayload(t, map[string]any{})},
			{RunID: start.RunID, ScoreRevision: preparation.Score.Revision(), MovementID: movementID, AttemptID: "attempt-1", Type: runstate.EventExecutionStarted, Payload: testPayload(t, map[string]any{"interval_id": "acceptance", "phase": "acceptance", "wall_start": "2026-07-30T00:00:00.000Z", "remaining_at_start": 600000})},
			{RunID: start.RunID, ScoreRevision: preparation.Score.Revision(), MovementID: movementID, AttemptID: "attempt-1", Type: runstate.EventAcceptanceStarted, Payload: testPayload(t, map[string]any{"subject_tree": "git-sha1:subject", "acceptance_spec_hash": "sha256:acceptance", "planned_criterion_ids": []any{}, "identity_versions": versions})},
		} {
			if _, err := authority.Append(event, faultpoint.ReceiptAddress("test."+string(event.Type))); err != nil {
				t.Fatal(err)
			}
		}
	}
	failureType := runstate.EventAttemptFailed
	if acceptanceOpen {
		failureType = runstate.EventAcceptanceFailed
	}
	payload := map[string]any{"kind": kind, "disposition": map[string]any{"charged": charged, "movement_terminal": false}}
	if acceptanceOpen {
		payload = map[string]any{"reason": "criterion_errored", "subject_tree": "git-sha1:subject", "disposition": map[string]any{"charged": charged, "movement_terminal": false}}
	}
	if _, err := authority.Append(runstate.Event{
		RunID: start.RunID, ScoreRevision: preparation.Score.Revision(), MovementID: movementID, AttemptID: "attempt-1", Type: failureType,
		Payload: testPayload(t, payload),
	}, faultpoint.ReceiptAddress("test.attempt.failed")); err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(start.RunID)
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(start.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return store, authority, start.RunID, input, journal.Events[len(journal.Events)-1]
}

func appendRecordedFailure(t *testing.T, store *runstore.Store, authority *runstore.Driver, runID runstate.RunID, kind, charged string) runstore.RunInput {
	t.Helper()
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	current := input.Projection.CurrentHeadAttempt
	if current == nil {
		t.Fatal("current successor attempt is absent")
	}
	if _, err := authority.Append(runstate.Event{
		RunID: runID, ScoreRevision: input.Projection.State.ScoreHead.Revision,
		MovementID: current.MovementID, AttemptID: current.AttemptID, Type: runstate.EventAttemptFailed,
		Payload: testPayload(t, map[string]any{"kind": kind, "disposition": map[string]any{"charged": charged, "movement_terminal": false}}),
	}, faultpoint.ReceiptAddress("test.attempt.failed")); err != nil {
		t.Fatal(err)
	}
	input, err = store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func TestLiveFailedTerminalProjectionAbsentRejectsBothDirections(t *testing.T) {
	for _, run := range []runstate.RunLifecycle{runstate.RunRunning, runstate.RunSucceeded, runstate.RunCancelled} {
		if !liveFailedTerminalProjectionAbsent(run) {
			t.Fatalf("run %s was accepted as durable run.failed", run)
		}
	}
	if liveFailedTerminalProjectionAbsent(runstate.RunFailed) {
		t.Fatal("run.failed was rejected")
	}
}

func TestLiveBetweenUnitEntryCallSitesRespectPostEffectBoundary(t *testing.T) {
	_, testPath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate driver test source")
	}
	directory := filepath.Dir(testPath)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	functions := map[string]*ast.FuncDecl{}
	parsedFiles := 0
	functionDeclarations := 0
	liveEntryCalls := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, filepath.Join(directory, entry.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		parsedFiles++
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			functionDeclarations++
			functions[function.Name.Name] = function
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee := calledName(call.Fun)
				if callee != "liveBetweenUnitEntry" && callee != "selectLiveBetweenUnit" {
					return true
				}
				liveEntryCalls++
				if !approvedLiveBetweenUnitCall(function.Name.Name, callee) {
					position := fileSet.Position(call.Pos())
					t.Errorf("%s calls %s outside an approved live-entry path at %s", function.Name.Name, callee, position)
				}
				return true
			})
		}
	}
	if parsedFiles == 0 {
		t.Fatal("parsed zero non-test Go files in internal/driver")
	}
	if functionDeclarations == 0 {
		t.Fatal("found zero function declarations in internal/driver non-test files")
	}
	if liveEntryCalls == 0 {
		t.Fatal("found zero live-between-unit entry calls in internal/driver non-test files")
	}
	if !allCallsBefore(functions["liveRunLoop"], "selectLiveBetweenUnit", "ExecuteAttempt") {
		t.Fatal("live scheduler selection must remain before ExecuteAttempt")
	}
	if !allCallsBefore(functions["ExecuteAttempt"], "Execute", "realizeRecordedNoneDisposition") {
		t.Fatal("post-effect live entry must be reachable only after Client.Execute returns")
	}
	if !allCallsBefore(functions["liveMaterializeSuccessor"], "liveBetweenUnitEntry", "ExecuteAttempt") {
		t.Fatal("successor materialization must re-enter only after the durable post-effect boundary")
	}
}

func approvedLiveBetweenUnitCall(function, callee string) bool {
	switch callee {
	case "selectLiveBetweenUnit":
		return function == "liveRunLoop"
	case "liveBetweenUnitEntry":
		return function == "selectLiveBetweenUnit" || function == "realizeRecordedNoneDisposition" || function == "liveMaterializeSuccessor"
	default:
		return false
	}
}

func allCallsBefore(function *ast.FuncDecl, before, after string) bool {
	if function == nil {
		return false
	}
	var beforePositions []token.Pos
	var afterPositions []token.Pos
	ast.Inspect(function.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch calledName(call.Fun) {
		case before:
			beforePositions = append(beforePositions, call.Pos())
		case after:
			afterPositions = append(afterPositions, call.Pos())
		}
		return true
	})
	if len(beforePositions) == 0 || len(afterPositions) == 0 {
		return false
	}
	for _, left := range beforePositions {
		for _, right := range afterPositions {
			if left >= right {
				return false
			}
		}
	}
	return true
}

func calledName(expression ast.Expr) string {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression.Name
	case *ast.SelectorExpr:
		return expression.Sel.Name
	default:
		return ""
	}
}

func testDependencies() dependencies {
	return dependencies{
		probe:             faultpoint.Nop{},
		client:            adapter.NewClient(),
		resolveTrampoline: func() (string, error) { return "/unused/trampoline", nil },
		now:               time.Now,
		newID:             workspace.NewID,
		workspaceStart:    workspace.Start,
	}
}

func liveEntryFixture(t *testing.T) (*validate.Preparation, *runstore.Store, *runstore.Driver, workspace.StartResult) {
	t.Helper()
	preparation := prepareRunnableFixture(t, sliceScore(), sliceCast())
	start, err := workspace.Start(preparation, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(preparation.RepositoryRoot, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.AcquireDriver(start.RunID, movementSeeds(preparation.Score))
	if err != nil {
		t.Fatal(err)
	}
	return preparation, store, authority, start
}

func fanInConflictFixture(t *testing.T) (*validate.Preparation, *runstore.Store, *runstore.Driver, workspace.StartResult, string, string) {
	return fanInFixture(t, true)
}

func fanInCleanFixture(t *testing.T) (*validate.Preparation, *runstore.Store, *runstore.Driver, workspace.StartResult, string, string) {
	return fanInFixture(t, false)
}

func fanInFixture(t *testing.T, conflict bool) (*validate.Preparation, *runstore.Store, *runstore.Driver, workspace.StartResult, string, string) {
	return fanInFixtureWithWriterIDs(t, conflict, "ours", "theirs", nil, true, true)
}

func fanInWaivedFixture(t *testing.T, conflict bool) (*validate.Preparation, *runstore.Store, *runstore.Driver, workspace.StartResult, string, string) {
	return fanInFixtureWithWriterIDs(t, conflict, "ours", "theirs", nil, true, false)
}

func fanInWaivedNoOpWriterFixture(t *testing.T) (*validate.Preparation, *runstore.Store, *runstore.Driver, workspace.StartResult, string, string) {
	return fanInFixtureWithNoOpWriterTrees(t, false, "noop-a", "noop-b", nil, true, false, true)
}

func candidateFanInCleanFixture(t *testing.T) (*validate.Preparation, *runstore.Store, *runstore.Driver, workspace.StartResult, string, string) {
	return fanInFixtureWithWriterIDs(t, false, "ours", "theirs", nil, false, true)
}

func candidateFanInConflictFixture(t *testing.T) (*validate.Preparation, *runstore.Store, *runstore.Driver, workspace.StartResult, string, string) {
	return fanInFixtureWithWriterIDs(t, true, "ours", "theirs", nil, false, true)
}

func fanInFixtureWithWriterIDs(t *testing.T, conflict bool, firstID, secondID string, firstNeeds []any, waived, withTarget bool) (*validate.Preparation, *runstore.Store, *runstore.Driver, workspace.StartResult, string, string) {
	return fanInFixtureWithNoOpWriterTrees(t, conflict, firstID, secondID, firstNeeds, waived, withTarget, false)
}

func fanInFixtureWithNoOpWriterTrees(t *testing.T, conflict bool, firstID, secondID string, firstNeeds []any, waived, withTarget, noOpWriterTrees bool) (*validate.Preparation, *runstore.Store, *runstore.Driver, workspace.StartResult, string, string) {
	t.Helper()
	document := writerSliceScore(waived)
	first := document["movements"].([]any)[0].(map[string]any)
	first["id"] = firstID
	first["outputs"] = []any{map[string]any{"id": firstID + "-change-set", "kind": "change_set"}}
	if firstNeeds != nil {
		first["needs"] = firstNeeds
	}
	second := make(map[string]any, len(first))
	for key, value := range first {
		second[key] = value
	}
	second["id"] = secondID
	delete(second, "needs")
	second["outputs"] = []any{map[string]any{"id": secondID + "-change-set", "kind": "change_set"}}
	if withTarget {
		target := map[string]any{
			"id": "target", "part": "reader", "needs": []any{firstID, secondID}, "grants": []any{"repo_read"},
			"instruction": "inspect the composed base", "outputs": []any{map[string]any{"id": "target-report", "kind": "artifact"}},
			"acceptance": map[string]any{"hard": []any{map[string]any{"id": "target-report-present", "artifact": "target-report"}}},
		}
		document["movements"] = []any{first, second, target}
		if !waived {
			document["verification"].(map[string]any)["final_movement"] = "target"
		}
	} else {
		document["movements"] = []any{first, second}
	}
	preparation := prepareRunnableFixture(t, document, sliceCast())
	started, err := workspace.Start(preparation, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(preparation.RepositoryRoot, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.AcquireDriver(started.RunID, movementSeeds(preparation.Score))
	if err != nil {
		t.Fatal(err)
	}
	if err := started.Run.BindDriver(authority); err != nil {
		authority.Release()
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		authority.Release()
		t.Fatal(err)
	}
	git := func(arguments ...string) string {
		t.Helper()
		command := exec.Command("git", arguments...)
		command.Dir = preparation.RepositoryRoot
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	oursPath := filepath.Join(preparation.RepositoryRoot, "conflict.txt")
	theirsPath := oursPath
	if !conflict {
		oursPath = filepath.Join(preparation.RepositoryRoot, "ours.txt")
		theirsPath = filepath.Join(preparation.RepositoryRoot, "theirs.txt")
	}
	if err := os.WriteFile(oursPath, []byte("ours\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", filepath.Base(oursPath))
	git("commit", "-m", "fan-in ours")
	ours := "git-sha1:" + git("rev-parse", "HEAD^{tree}")
	git("reset", "--hard", "HEAD~1")
	if err := os.WriteFile(theirsPath, []byte("theirs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", filepath.Base(theirsPath))
	git("commit", "-m", "fan-in theirs")
	theirs := "git-sha1:" + git("rev-parse", "HEAD^{tree}")
	if noOpWriterTrees {
		ours = input.BaseTree
		theirs = input.BaseTree
	}
	appendSucceededWriter := func(movementID, attemptID, changeSetID, tree string) {
		attemptStartedPayload := map[string]any{"attempt_number": 1, "adapter_process": map[string]any{"pid": 999999, "session_id": 999999, "start_identity": map[string]any{"platform": "linux", "boot_id": "fixture", "start_ticks": "0"}}, "granted_authority": map[string]any{"paths_rw": []any{"**"}, "paths_ro": []any{}, "shell": false, "network": false}, "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}}
		if movementID == firstID && len(firstNeeds) != 0 {
			attemptStartedPayload["base_composition_hash"] = "sha256:base-composition"
		}
		for _, event := range []runstate.Event{
			{RunID: started.RunID, ScoreRevision: 1, MovementID: runstate.MovementID(movementID), Type: runstate.EventMovementReady, Payload: testPayload(t, map[string]any{})},
			{RunID: started.RunID, ScoreRevision: 1, MovementID: runstate.MovementID(movementID), Type: runstate.EventMovementStarted, Payload: testPayload(t, map[string]any{})},
			{RunID: started.RunID, ScoreRevision: 1, MovementID: runstate.MovementID(movementID), AttemptID: runstate.AttemptID(attemptID), Type: runstate.EventPerformerSelected, Payload: testPayload(t, map[string]any{"reason": "initial", "performer_id": "worker", "adapter_id": "codex", "model": "gpt-5.6-sol"})},
			{RunID: started.RunID, ScoreRevision: 1, MovementID: runstate.MovementID(movementID), AttemptID: runstate.AttemptID(attemptID), Type: runstate.EventAttemptStarted, Payload: testPayload(t, attemptStartedPayload)},
			{RunID: started.RunID, ScoreRevision: 1, MovementID: runstate.MovementID(movementID), AttemptID: runstate.AttemptID(attemptID), Type: runstate.EventAdapterProbed, Payload: testPayload(t, map[string]any{"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": true, "shell": false, "network": false, "resumable_sessions": false}, "enforcement": map[string]any{"path_grants": true, "read_only": true, "network_grants": true, "shell_grants": true, "read_grants": true}, "negotiated_features": []any{}, "truncated_resolutions": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:dependency", "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}})},
			{RunID: started.RunID, ScoreRevision: 1, MovementID: runstate.MovementID(movementID), AttemptID: runstate.AttemptID(attemptID), Type: runstate.EventPerformerCompleted, Payload: testPayload(t, map[string]any{"session_hint_stored": false})},
			{RunID: started.RunID, ScoreRevision: 1, MovementID: runstate.MovementID(movementID), AttemptID: runstate.AttemptID(attemptID), Type: runstate.EventChangeSetRecorded, Payload: testPayload(t, map[string]any{"change_set_id": changeSetID, "base_tree": input.BaseTree, "result_tree": tree, "commit": input.BaseCommit, "ref": "refs/partitur/runs/" + string(started.RunID) + "/attempts/" + attemptID + "/changeset", "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}})},
			{RunID: started.RunID, ScoreRevision: 1, MovementID: runstate.MovementID(movementID), AttemptID: runstate.AttemptID(attemptID), Type: runstate.EventVerificationPassed, Payload: testPayload(t, map[string]any{})},
			{RunID: started.RunID, ScoreRevision: 1, Type: runstate.EventExecutionStarted, Payload: testPayload(t, map[string]any{"interval_id": "acceptance-" + attemptID, "phase": "acceptance", "wall_start": "2026-07-30T00:00:00.000Z", "remaining_at_start": 600000})},
			{RunID: started.RunID, ScoreRevision: 1, MovementID: runstate.MovementID(movementID), AttemptID: runstate.AttemptID(attemptID), Type: runstate.EventAcceptanceStarted, Payload: testPayload(t, map[string]any{"subject_tree": tree, "acceptance_spec_hash": "sha256:acceptance", "planned_criterion_ids": []any{"tests"}, "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}})},
			{RunID: started.RunID, ScoreRevision: 1, MovementID: runstate.MovementID(movementID), AttemptID: runstate.AttemptID(attemptID), Type: runstate.EventCriterionStarted, Payload: testPayload(t, map[string]any{"criterion_id": "tests", "criterion_spec_hash": "sha256:criterion", "subject_tree": tree, "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}})},
			{RunID: started.RunID, ScoreRevision: 1, MovementID: runstate.MovementID(movementID), AttemptID: runstate.AttemptID(attemptID), Type: runstate.EventCriterionCompleted, Payload: testPayload(t, map[string]any{"criterion_id": "tests", "criterion_spec_hash": "sha256:criterion", "subject_tree": tree, "outcome": "PASS", "duration_ms": 1, "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}})},
			{RunID: started.RunID, ScoreRevision: 1, MovementID: runstate.MovementID(movementID), AttemptID: runstate.AttemptID(attemptID), Type: runstate.EventAcceptanceEvaluationCompleted, Payload: testPayload(t, map[string]any{"subject_tree": tree, "acceptance_spec_hash": "sha256:acceptance", "criterion_outcomes": []any{map[string]any{"criterion_id": "tests", "criterion_spec_hash": "sha256:criterion", "outcome": "PASS"}}, "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}})},
			{RunID: started.RunID, ScoreRevision: 1, Type: runstate.EventExecutionStopped, Payload: testPayload(t, map[string]any{"interval_id": "acceptance-" + attemptID, "reason": "normal", "charging": "measured", "charged_duration": 1})},
			{RunID: started.RunID, ScoreRevision: 1, MovementID: runstate.MovementID(movementID), AttemptID: runstate.AttemptID(attemptID), Type: runstate.EventAttemptCompleted, Payload: testPayload(t, map[string]any{})},
			{RunID: started.RunID, ScoreRevision: 1, MovementID: runstate.MovementID(movementID), AttemptID: runstate.AttemptID(attemptID), Type: runstate.EventMovementSucceeded, Payload: testPayload(t, map[string]any{"approved_artifact_instance_ids": []any{}, "approved_change_set_id": changeSetID, "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}, "run_succeeded": false})},
		} {
			if _, err := authority.Append(event, faultpoint.ReceiptAddress("test.fan_in."+string(event.Type))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(firstNeeds) != 0 {
		appendSucceededWriter(secondID, secondID+"-attempt", "sha256:"+secondID, theirs)
		appendSucceededWriter(firstID, firstID+"-attempt", "sha256:"+firstID, ours)
	} else {
		appendSucceededWriter(firstID, firstID+"-attempt", "sha256:"+firstID, ours)
		appendSucceededWriter(secondID, secondID+"-attempt", "sha256:"+secondID, theirs)
	}
	return preparation, store, authority, started, ours, theirs
}

func writerCaptureFixture(t *testing.T) (*validate.Preparation, *runstore.Store, *runstore.Driver, workspace.StartResult, *workspace.AttemptWorkspace) {
	t.Helper()
	preparation := prepareRunnableFixture(t, writerSliceScore(true), sliceCast())
	started, err := workspace.Start(preparation, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(preparation.RepositoryRoot, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.AcquireDriver(started.RunID, movementSeeds(preparation.Score))
	if err != nil {
		t.Fatal(err)
	}
	if err := started.Run.BindDriver(authority); err != nil {
		t.Fatal(err)
	}
	attempt, err := started.Run.CreateAttempt("inspect")
	if err != nil {
		t.Fatal(err)
	}
	versions := map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}
	for _, event := range []runstate.Event{
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", Type: runstate.EventMovementReady, Payload: testPayload(t, map[string]any{})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", Type: runstate.EventMovementStarted, Payload: testPayload(t, map[string]any{})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", PartID: "reader", AttemptID: attempt.AttemptID, Type: runstate.EventPerformerSelected, Payload: testPayload(t, map[string]any{"reason": "initial", "performer_id": "worker", "adapter_id": "codex", "model": "gpt-5.6-sol"})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", PartID: "reader", AttemptID: attempt.AttemptID, Type: runstate.EventAttemptStarted, Payload: testPayload(t, map[string]any{"attempt_number": 1, "adapter_process": map[string]any{"pid": 999999, "session_id": 999999, "start_identity": map[string]any{"platform": "linux", "boot_id": "fixture", "start_ticks": "0"}}, "granted_authority": map[string]any{"paths_rw": []any{"**"}, "paths_ro": []any{}, "shell": false, "network": false}, "identity_versions": versions})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", PartID: "reader", AttemptID: attempt.AttemptID, Type: runstate.EventAdapterProbed, Payload: testPayload(t, map[string]any{"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": true, "shell": false, "network": false, "resumable_sessions": false}, "enforcement": map[string]any{"path_grants": true, "read_only": true, "network_grants": true, "shell_grants": true, "read_grants": true}, "negotiated_features": []any{}, "truncated_resolutions": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:dependency", "identity_versions": versions})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", PartID: "reader", AttemptID: attempt.AttemptID, Type: runstate.EventPerformerCompleted, Payload: testPayload(t, map[string]any{"session_hint_stored": false})},
	} {
		if _, err := authority.Append(event, faultpoint.ReceiptAddress("test.writer_capture."+string(event.Type))); err != nil {
			authority.Release()
			t.Fatal(err)
		}
	}
	return preparation, store, authority, started, attempt
}

func writerVerificationAppender(
	t *testing.T,
	authority *runstore.Driver,
	runID runstate.RunID,
	attempt *workspace.AttemptWorkspace,
) func(runstate.EventType, any, string) (faultpoint.DurabilityReceipt, error) {
	t.Helper()
	return func(eventType runstate.EventType, payload any, address string) (faultpoint.DurabilityReceipt, error) {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return faultpoint.DurabilityReceipt{}, err
		}
		return authority.Append(runstate.Event{
			RunID: runID, ScoreRevision: 1, MovementID: attempt.MovementID,
			PartID: attempt.PartID, AttemptID: attempt.AttemptID,
			Type: eventType, Payload: encoded,
		}, faultpoint.ReceiptAddress(address))
	}
}

func grantDeniedClassifier(t *testing.T) func(successor.FailureCase) (runstate.Disposition, error) {
	t.Helper()
	return func(failure successor.FailureCase) (runstate.Disposition, error) {
		if failure.AttemptKind != successor.KindGrantDenied {
			t.Fatalf("failure case = %#v, want grant_denied", failure)
		}
		return successor.Classify(successor.ClassificationInput{
			Failure: failure, RemainingTimeMS: 1,
		})
	}
}

func decodeDriverPayload(t *testing.T, event runstate.Event) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func testPayload(t *testing.T, value any) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func prepareRunnableFixture(
	t *testing.T,
	scoreDocument map[string]any,
	castDocument map[string]any,
) *validate.Preparation {
	t.Helper()
	preparation := prepareFixtureWithCast(t, scoreDocument, castDocument)
	for _, arguments := range [][]string{
		{"init"},
		{"config", "user.name", "Partitur Test"},
		{"config", "user.email", "partitur@example.invalid"},
		{"add", "partitur.yaml", ".partitur/cast.yaml"},
		{"commit", "-m", "fixture"},
	} {
		command := exec.Command("git", arguments...)
		command.Dir = preparation.RepositoryRoot
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	return preparation
}

func replayDriverState(
	t *testing.T,
	preparation *validate.Preparation,
	runID runstate.RunID,
) runstate.State {
	t.Helper()
	store, err := runstore.New(preparation.RepositoryRoot, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.Replay(
		runID,
		movementSeeds(preparation.Score),
		"driver.test.replay",
	)
	if err != nil {
		t.Fatal(err)
	}
	return replay.State
}

func assertNoDriverLease(
	t *testing.T,
	repositoryRoot string,
	runID runstate.RunID,
) {
	t.Helper()
	path := filepath.Join(
		repositoryRoot,
		".partitur",
		"runs",
		string(runID),
		"driver.lease",
	)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("driver lease after return: %v", err)
	}
}

func prepareFixture(
	t *testing.T,
	score map[string]any,
) *validate.Preparation {
	return prepareFixtureWithCast(t, score, sliceCast())
}

func prepareFixtureWithCast(
	t *testing.T,
	score map[string]any,
	castDocument map[string]any,
) *validate.Preparation {
	t.Helper()
	root := t.TempDir()
	writeJSON(t, filepath.Join(root, "partitur.yaml"), score)
	if err := os.Mkdir(filepath.Join(root, ".partitur"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(root, ".partitur", "cast.yaml"), castDocument)
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
	t.Setenv("HOME", t.TempDir())
	preparation, result := validate.Prepare()
	if result.Refusal != nil || result.HasDiagnostics() || preparation == nil {
		t.Fatalf("preparation=%#v result=%#v", preparation, result)
	}
	return preparation
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func sliceScore() map[string]any {
	return map[string]any{
		"score":    "0.2",
		"name":     "driver-fixture",
		"revision": float64(1),
		"status":   "finalized",
		"goal":     "Produce one report.",
		"verification": map[string]any{
			"expectation": map[string]any{
				"intent": "pass-existing-tests",
				"apply_gate": map[string]any{
					"require": []any{"verified"},
				},
			},
			"final_movement": "inspect",
		},
		"parts": map[string]any{
			"reader": map[string]any{
				"capabilities": []any{"repo_read"},
				"read_only":    true,
			},
		},
		"movements": []any{
			map[string]any{
				"id":          "inspect",
				"part":        "reader",
				"grants":      []any{"repo_read"},
				"instruction": "Write the report.",
				"outputs": []any{
					map[string]any{"id": "report", "kind": "artifact"},
				},
				"acceptance": map[string]any{
					"hard": []any{
						map[string]any{
							"id":       "report-present",
							"artifact": "report",
						},
					},
				},
			},
		},
		"policy": map[string]any{
			"allowed_paths": []any{"**"},
			"budget": map[string]any{
				"active_wall_clock_min": float64(10),
			},
		},
	}
}

func writerSliceScore(waived bool) map[string]any {
	scoreDocument := sliceScore()
	part := scoreDocument["parts"].(map[string]any)["reader"].(map[string]any)
	part["capabilities"] = []any{"repo_read", "repo_write"}
	delete(part, "read_only")
	movement := scoreDocument["movements"].([]any)[0].(map[string]any)
	movement["grants"] = []any{"repo_read", "repo_write"}
	movement["outputs"] = []any{map[string]any{"id": "change-set", "kind": "change_set"}}
	movement["acceptance"] = map[string]any{"hard": []any{map[string]any{"id": "tests", "run": []any{"true"}}}}
	if waived {
		verification := scoreDocument["verification"].(map[string]any)
		verification["expectation"].(map[string]any)["apply_gate"] = map[string]any{"waived": true, "reason": "writer fixture"}
		delete(verification, "final_movement")
	} else {
		final := map[string]any{
			"id": "final", "part": "reader", "needs": []any{"inspect"}, "grants": []any{"repo_read"},
			"instruction": "Verify the result.", "outputs": []any{map[string]any{"id": "final-report", "kind": "artifact"}},
			"acceptance": map[string]any{"hard": []any{map[string]any{"id": "final-report-present", "artifact": "final-report"}}},
		}
		scoreDocument["movements"] = append(scoreDocument["movements"].([]any), final)
		scoreDocument["verification"].(map[string]any)["final_movement"] = "final"
	}
	return scoreDocument
}

func writerFreeTwoMovementWaivedScore() map[string]any {
	scoreDocument := sliceScore()
	verification := scoreDocument["verification"].(map[string]any)
	verification["expectation"].(map[string]any)["apply_gate"] = map[string]any{"waived": true, "reason": "budget fixture"}
	delete(verification, "final_movement")
	first := scoreDocument["movements"].([]any)[0].(map[string]any)
	second := map[string]any{}
	for key, value := range first {
		second[key] = value
	}
	second["id"] = "second"
	second["needs"] = []any{"inspect"}
	second["outputs"] = []any{map[string]any{"id": "second-report", "kind": "artifact"}}
	second["acceptance"] = map[string]any{"hard": []any{map[string]any{"id": "second-report-present", "artifact": "second-report"}}}
	scoreDocument["movements"] = []any{first, second}
	scoreDocument["policy"].(map[string]any)["budget"].(map[string]any)["active_wall_clock_min"] = float64(1)
	return scoreDocument
}

func sliceCast() map[string]any {
	return map[string]any{
		"cast": "0.1",
		"performers": map[string]any{
			"worker": map[string]any{
				"adapter": "codex",
				"model":   "gpt-5.6-sol",
			},
		},
		"bindings": map[string]any{
			"reader": map[string]any{"performer": "worker"},
		},
	}
}
