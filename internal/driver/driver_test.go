package driver

import (
	"context"
	"encoding/json"
	"errors"
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

	"github.com/BeomSeogKim/Partitur/internal/acceptance"
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

func TestSelectSliceRejectsEachUnsupportedShape(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   error
	}{
		{
			name: "more than one movement",
			mutate: func(score map[string]any) {
				final := score["movements"].([]any)[0].(map[string]any)
				final["needs"] = []any{"prepare"}
				final["inputs"] = []any{"notes"}
				score["movements"] = append(
					[]any{
						map[string]any{
							"id":          "prepare",
							"part":        "reader",
							"grants":      []any{"repo_read"},
							"instruction": "Prepare notes.",
							"outputs": []any{
								map[string]any{
									"id":   "notes",
									"kind": "artifact",
								},
							},
							"acceptance": map[string]any{
								"hard": []any{
									map[string]any{
										"id":       "notes-present",
										"artifact": "notes",
									},
								},
							},
						},
					},
					map[string]any{
						"id": "sentinel",
					},
				)
				movements := score["movements"].([]any)
				movements[len(movements)-1] = final
			},
			want: ErrUnsupportedSlice,
		},
		{
			name: "external hard criterion",
			mutate: func(score map[string]any) {
				movement := score["movements"].([]any)[0].(map[string]any)
				movement["acceptance"] = map[string]any{
					"hard": []any{
						map[string]any{"id": "external", "run": []any{"true"}},
					},
				}
			},
			want: acceptance.ErrUnsupportedCriteria,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			score := sliceScore()
			test.mutate(score)
			preparation := prepareFixture(t, score)
			_, _, _, _, err := selectSlice(preparation)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
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
		func() {
			if err := store.RequestCancellation(start.RunID); err != nil {
				t.Fatal(err)
			}
		},
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
	if !allCallsBefore(functions["run"], "selectLiveBetweenUnit", "ExecuteAttempt") {
		t.Fatal("initial live entry must remain before ExecuteAttempt")
	}
	if !allCallsBefore(functions["ExecuteAttempt"], "Execute", "realizeRecordedNoneDisposition") {
		t.Fatal("post-effect live entry must be reachable only after Client.Execute returns")
	}
}

func approvedLiveBetweenUnitCall(function, callee string) bool {
	switch callee {
	case "selectLiveBetweenUnit":
		return function == "run"
	case "liveBetweenUnitEntry":
		return function == "selectLiveBetweenUnit" || function == "realizeRecordedNoneDisposition"
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
