package driver_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/amendmentexec"
	"github.com/BeomSeogKim/Partitur/internal/driver"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/recoveryexec"
	"github.com/BeomSeogKim/Partitur/internal/recoveryobs"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
	"github.com/BeomSeogKim/Partitur/internal/validate"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

// budgetPrepareRemainingMS is the attempt's remaining budget. The adapter
// fixture reports its prepared auto approval and then holds the adapter open
// until this deadline fires, so the prepare is durable — and the adapter
// interval still open — when the active budget deadline lands.
const budgetPrepareRemainingMS int64 = 2_000

// TestBudgetDeadlineWithPendingPrepareIsSettledByTheLiveDriver reaches the state
// named by #480 item 3: the adapter has reported a prepared auto approval
// (approvalPrepared) and the active budget deadline then fires inside the same
// client.Execute call. DESIGN §6 makes that prepare a mutation barrier whose
// only admitted mutations are "recording a quiesce observation receipt, closing
// an execution interval, sweeping sessions, the lease rename, and cancellation"
// — so the terminal chain's attempt.failed cannot be appended while it is
// pending, and C.1's RC-RESUME-007 "never step[s] past" it either. This test
// pins that the live driver closes its own adapter interval measured and then
// settles its own prepare, leaving recovery nothing to complete or abandon.
func TestBudgetDeadlineWithPendingPrepareIsSettledByTheLiveDriver(t *testing.T) {
	preparation, store, authority, started, attempt, input, hash := budgetPreparePendingFixture(t)

	result := driver.ExecuteAttempt(context.Background(), driver.AttemptExecution{
		RepositoryRoot: preparation.RepositoryRoot, Score: input.Score, Cast: input.Cast, RunID: started.RunID,
		Attempt: attempt, BaseTree: input.BaseTree, CandidateTree: input.BaseTree, Authority: authority,
		PerformerID: "worker", SelectionReason: "initial", RemainingMS: budgetPrepareRemainingMS,
	}, driver.ExecutionDependencies{
		Probe: faultpoint.Nop{}, Client: &preparingThenDeadlineExecutor{t: t, baseHash: hash},
		ResolveTrampoline:   func() (string, error) { return "/fixture/trampoline", nil },
		Now:                 time.Now,
		NewID:               workspace.NewID,
		ProposalDisposition: amendmentexec.New(),
	})

	// The episode ends in the continuation attempt the settled approval selects,
	// not in the barrier refusal "illegal transition: attempt.failed:
	// prepare_pending" that the unguarded terminal branch produced.
	if result.Outcome != driver.OutcomeInterrupted || result.Reason != "" || !errors.Is(result.Err, errContinuedAfterSettledPrepare) {
		t.Fatalf("live result = %+v, want INTERRUPTED at the continued attempt", result)
	}

	live, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	adapterInterval, prepared := budgetPrepareAnchors(t, live.Events)

	// The adapter interval is closed exactly once, by its opener, measured.
	stops := budgetPrepareEventsOfType(live.Events, runstate.EventExecutionStopped)
	if len(stops) != 1 {
		t.Fatalf("live execution.stopped count = %d, want 1", len(stops))
	}
	stop := budgetPreparePayload(t, stops[0])
	if stop["interval_id"] != adapterInterval || stop["reason"] != "budget_exhausted" || stop["charging"] != "measured" {
		t.Fatalf("adapter execution.stopped = %#v, want the opener's measured budget close", stop)
	}

	// §6's barrier refuses attempt.failed while the prepare is pending, so the
	// live driver appends none: the budget decision belongs to the between-unit
	// scheduler once the barrier has lifted.
	if failures := budgetPrepareEventsOfType(live.Events, runstate.EventAttemptFailed); len(failures) != 0 {
		t.Fatalf("live attempt.failed count = %d, want 0 under the pending-prepare barrier", len(failures))
	}

	// The prepare is settled by amendment.approved — the §6 step-3 commit of the
	// plan this driver prepared — and never by amendment.approval_abandoned,
	// whose reasons are exactly base_head_changed, plan_invalidated, cancelled.
	if abandoned := budgetPrepareEventsOfType(live.Events, runstate.EventAmendmentApprovalAbandoned); len(abandoned) != 0 {
		t.Fatalf("live amendment.approval_abandoned count = %d, want 0", len(abandoned))
	}
	approvals := budgetPrepareEventsOfType(live.Events, runstate.EventAmendmentApproved)
	if len(approvals) != 1 {
		t.Fatalf("live amendment.approved count = %d, want 1", len(approvals))
	}
	approved := budgetPreparePayload(t, approvals[0])
	if approved["mode"] != "auto" || approved["proposal_id"] != prepared["proposal_id"] ||
		approved["envelope_class"] != prepared["envelope_class"] || approved["new_revision"] != prepared["new_revision"] {
		t.Fatalf("amendment.approved = %#v, want the prepared auto plan %#v", approved, prepared)
	}
	if _, fenced := approved["fenced_epoch"]; fenced {
		t.Fatalf("amendment.approved = %#v, want the live driver's own unfenced commit", approved)
	}

	// The measured close precedes the settlement: the barrier admits the close,
	// and lifting the barrier is the last thing the settlement does.
	if budgetPrepareIndexOf(live.Events, stops[0]) >= budgetPrepareIndexOf(live.Events, approvals[0]) {
		t.Fatal("amendment.approved precedes the adapter interval's budget close")
	}

	state, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Projection.State.PendingPrepare != nil {
		t.Fatal("ExecuteAttempt returned with the prepare still pending")
	}
	if _, present, leaseErr := store.ReadLease(started.RunID); leaseErr != nil || present {
		t.Fatalf("driver lease present = %v err = %v, want released", present, leaseErr)
	}

	// One recovery pass. It has no prepare to settle: RC-RESUME-007 and its
	// complete_or_abandon_prepare action are never selected, and it appends no
	// second settlement and no second close of the adapter interval.
	decisions := make([]recovery.Decision, 0, 8)
	executor := &recoveryexec.Executor{
		Store: store, RunID: started.RunID,
		AttemptDependencies: driver.DefaultExecutionDependencies(faultpoint.Nop{}),
		ObserveDecision:     func(decision recovery.Decision) { decisions = append(decisions, decision) },
		Load: func(context.Context) (recovery.Input, error) {
			loaded, loadErr := store.LoadRunInput(started.RunID)
			if loadErr != nil {
				return recovery.Input{}, loadErr
			}
			observations, obsErr := recoveryobs.Collect(store, started.RunID, loaded.Projection)
			return recovery.Input{Projection: loaded.Projection, Observations: observations}, obsErr
		},
	}
	_, _ = executor.Execute(context.Background())
	for _, decision := range decisions {
		if decision.CaseID == recovery.CasePendingPrepare {
			t.Fatalf("recovery selected %s after the live settlement", decision.CaseID)
		}
		if decision.Action != nil && decision.Action.Kind == recovery.ActionCompleteOrAbandonPrepare {
			t.Fatalf("recovery selected %s after the live settlement", decision.Action.Kind)
		}
	}
	recovered, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for eventType, want := range map[runstate.EventType]int{
		runstate.EventAmendmentApprovalPrepared:  1,
		runstate.EventAmendmentApproved:          1,
		runstate.EventAmendmentApprovalAbandoned: 0,
	} {
		if got := len(budgetPrepareEventsOfType(recovered.Events, eventType)); got != want {
			t.Fatalf("recovered %s count = %d, want %d", eventType, got, want)
		}
	}
	adapterStops := make([]map[string]any, 0, 1)
	for _, event := range budgetPrepareEventsOfType(recovered.Events, runstate.EventExecutionStopped) {
		payload := budgetPreparePayload(t, event)
		if payload["interval_id"] == adapterInterval {
			adapterStops = append(adapterStops, payload)
		}
	}
	if len(adapterStops) != 1 {
		t.Fatalf("recovered closes of the budget-deadline adapter interval = %d, want 1", len(adapterStops))
	}
	if adapterStops[0]["reason"] != "budget_exhausted" || adapterStops[0]["charging"] != "measured" {
		t.Fatalf("recovered adapter close = %#v, want the live measured budget close", adapterStops[0])
	}
}

func budgetPrepareAnchors(t *testing.T, events []runstate.Event) (string, map[string]any) {
	t.Helper()
	adapterInterval := ""
	for _, event := range events {
		if event.Type != runstate.EventExecutionStarted {
			continue
		}
		payload := budgetPreparePayload(t, event)
		if payload["phase"] == "adapter" && adapterInterval == "" {
			adapterInterval, _ = payload["interval_id"].(string)
		}
	}
	if adapterInterval == "" {
		t.Fatal("adapter execution.started is absent")
	}
	prepares := budgetPrepareEventsOfType(events, runstate.EventAmendmentApprovalPrepared)
	if len(prepares) != 1 {
		t.Fatalf("amendment.approval_prepared count = %d, want 1", len(prepares))
	}
	return adapterInterval, budgetPreparePayload(t, prepares[0])
}

func budgetPrepareEventsOfType(events []runstate.Event, eventType runstate.EventType) []runstate.Event {
	matched := make([]runstate.Event, 0, 1)
	for _, event := range events {
		if event.Type == eventType {
			matched = append(matched, event)
		}
	}
	return matched
}

func budgetPrepareIndexOf(events []runstate.Event, target runstate.Event) int {
	for index, event := range events {
		if event.EventID == target.EventID {
			return index
		}
	}
	return -1
}

func budgetPreparePayload(t *testing.T, event runstate.Event) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

type preparingThenDeadlineExecutor struct {
	t        *testing.T
	baseHash string
	calls    int
}

var errContinuedAfterSettledPrepare = errors.New("continued attempt after the settled prepare")

func (*preparingThenDeadlineExecutor) Resolve(string) (string, error) { return "/fixture/adapter", nil }

func (fixture *preparingThenDeadlineExecutor) Execute(ctx context.Context, plan adapter.ExecutePlan) (adapter.ExecuteReport, error) {
	fixture.t.Helper()
	fixture.calls++
	if fixture.calls > 1 {
		return adapter.ExecuteReport{}, errContinuedAfterSettledPrepare
	}
	start, err := procid.Read(os.Getpid())
	if err != nil {
		return adapter.ExecuteReport{}, err
	}
	if _, err := plan.RecordIdentity(runstate.ProcessIdentity{PID: os.Getpid(), SessionID: os.Getpid(), Start: start}); err != nil {
		return adapter.ExecuteReport{}, err
	}
	probe := protocol.ProbeResult{
		Protocol:     protocol.ProtocolVersion,
		Adapter:      protocol.AdapterIdentity{ID: plan.AdapterID, Version: "fixture"},
		Capabilities: protocol.Capabilities{RepoRead: true},
		Enforcement:  protocol.Enforcement{PathGrants: true, ReadOnly: true, NetworkGrants: true, ShellGrants: true, ReadGrants: true},
	}
	if _, err := plan.Recorder.RecordProbe(probe); err != nil {
		return adapter.ExecuteReport{}, err
	}
	proposal := protocol.ProposalEvent{
		Type: protocol.EventProposal, ID: "emitted-1",
		Amendment: json.RawMessage(fmt.Sprintf(
			`{"base_revision":1,"base_hash":%q,"operations":[{"op":"replace","path":"/policy/budget/active_wall_clock_min","value":9}],"reason":"adapter request"}`,
			fixture.baseHash,
		)),
	}
	result := protocol.ExecuteResult{Outcome: protocol.OutcomeWaitingHuman}
	if _, err := plan.Recorder.RecordOutcome(adapter.OutcomeObservation{
		EventType: string(runstate.EventAttemptBlocked), Result: result,
		Raised: []adapter.RaisedDecision{{Kind: protocol.EventProposal, Proposal: &proposal}},
	}); err != nil {
		return adapter.ExecuteReport{}, err
	}
	<-ctx.Done()
	return adapter.ExecuteReport{}, ctx.Err()
}

func budgetPreparePendingFixture(t *testing.T) (
	*validate.Preparation, *runstore.Store, *runstore.Driver,
	workspace.StartResult, *workspace.AttemptWorkspace, runstore.RunInput, string,
) {
	t.Helper()
	root := t.TempDir()
	write := func(path, value string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "partitur.yaml"), budgetPrepareScoreDocument)
	write(filepath.Join(root, ".partitur", "cast.yaml"), budgetPrepareCastDocument)
	for _, args := range [][]string{
		{"init"},
		{"config", "user.name", "Partitur Test"},
		{"config", "user.email", "partitur@example.invalid"},
		{"add", "partitur.yaml", ".partitur/cast.yaml"},
		{"commit", "-m", "fixture"},
	} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	preparation, validation := validate.Prepare()
	if preparation == nil || validation.HasDiagnostics() {
		t.Fatalf("prepare result = %#v", validation)
	}
	started, err := workspace.Start(preparation, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.AcquireDriver(started.RunID, budgetPrepareMovementSeeds(preparation.Score))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authority.Release() })
	attempt, err := started.Run.CreateAttempt("inspect")
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []runstate.EventType{runstate.EventMovementReady, runstate.EventMovementStarted} {
		if _, err := authority.Append(runstate.Event{
			RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", Type: eventType, Payload: []byte(`{}`),
		}, faultpoint.ReceiptAddress("test.budget_prepare."+string(eventType))); err != nil {
			t.Fatal(err)
		}
	}
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.RecordRecoveredZeroWriterCandidate(store, authority, input); err != nil {
		t.Fatal(err)
	}
	input, err = store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	return preparation, store, authority, started, attempt, input, hash
}

func budgetPrepareMovementSeeds(compiled *score.Score) []runstate.MovementSeed {
	movements := compiled.Movements()
	seeds := make([]runstate.MovementSeed, 0, len(movements))
	for _, movement := range movements {
		repoWrite := false
		for _, grant := range movement.Grants {
			repoWrite = repoWrite || grant == "repo_write"
		}
		seeds = append(seeds, runstate.MovementSeed{
			ID: runstate.MovementID(movement.ID), Initial: runstate.MovementPending, RepoWrite: repoWrite,
			HasDependencies: len(movement.Needs) != 0, Final: movement.ID == compiled.Execution().FinalMovementID,
		})
	}
	return seeds
}

const budgetPrepareScoreDocument = `{"score":"0.2","name":"budgetprepare","revision":1,"status":"finalized","goal":"goal","open_questions":[],"parts":{"reader":{"capabilities":["repo_read"],"read_only":true}},"movements":[{"id":"inspect","part":"reader","grants":["repo_read"],"may_propose":true,"instruction":"inspect","outputs":[{"id":"report","kind":"artifact"}],"acceptance":{"hard":[{"id":"report-present","artifact":"report"}]}}],"policy":{"allowed_paths":["src/**"],"budget":{"active_wall_clock_min":10,"retries_per_movement":2},"amendment":{"auto":"envelope"}},"verification":{"expectation":{"intent":"pass-existing-tests","apply_gate":{"require":["verified"]}},"final_movement":"inspect"}}`

const budgetPrepareCastDocument = `{"cast":"0.1","performers":{"worker":{"adapter":"fixture","model":"fixture"}},"bindings":{"reader":{"performer":"worker"}}}`
