package amendmentexec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/adapter"
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

func TestDispositionerRejectsBlockingProposalBeforeAttemptBlocked(t *testing.T) {
	preparation, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	disposition, err := New().PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, true, []any{map[string]any{"op": "replace", "path": "/revision", "value": float64(2)}}))
	if err != nil {
		t.Fatal(err)
	}
	if disposition.RouteDescriptor != nil || disposition.AppendRoute != nil {
		t.Fatalf("rejected disposition = %#v", disposition)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	last := journal.Events[len(journal.Events)-1]
	if last.Type != runstate.EventAmendmentRejected {
		t.Fatalf("last event = %s, want amendment.rejected before attempt.blocked", last.Type)
	}
	payload := eventPayload(t, last)
	if payload["decision_id"] != "dec-1" || payload["reason"] != "reserved_field" {
		t.Fatalf("rejection payload = %#v", payload)
	}
}

func TestDispositionerPublishesFrozenRouteThenAppendsItAfterDriverSource(t *testing.T) {
	preparation, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	disposition, err := New().PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, true, []any{map[string]any{"op": "replace", "path": "/policy/budget/active_wall_clock_min", "value": float64(9)}}))
	if err != nil {
		t.Fatal(err)
	}
	route := disposition.RouteDescriptor
	if route == nil || route["reason"] != "requires_decision" || route["decision_type"] != "amendment" || route["proposal_record_hash"] == "" {
		t.Fatalf("route descriptor = %#v", route)
	}
	if disposition.AppendRoute == nil {
		t.Fatal("routed disposition has no append closure")
	}
	if err := disposition.AppendRoute(context.Background()); err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	last := journal.Events[len(journal.Events)-1]
	if last.Type != runstate.EventAmendmentRoutedHuman {
		t.Fatalf("last event = %s, want amendment.routed_human", last.Type)
	}
	payload := eventPayload(t, last)
	if payload["decision_id"] != "dec-1" || payload["proposal_record_hash"] != route["proposal_record_hash"] || payload["reason"] != "requires_decision" {
		t.Fatalf("routed payload = %#v", payload)
	}
	record := filepath.Join(preparation.RepositoryRoot, ".partitur", "runs", string(started.RunID), "proposals", "prp-1.json")
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("published proposal record: %v", err)
	}
}

func TestDispositionerAppendsRoutedHumanAfterDriverBlockedSource(t *testing.T) {
	preparation, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	attempt, err := started.Run.CreateAttempt("inspect")
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []runstate.EventType{runstate.EventMovementReady, runstate.EventMovementStarted} {
		if _, err := authority.Append(runstate.Event{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", Type: eventType, Payload: []byte(`{}`)}, faultpoint.ReceiptAddress("test."+string(eventType))); err != nil {
			t.Fatal(err)
		}
	}
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	result := driver.ExecuteAttempt(context.Background(), driver.AttemptExecution{
		RepositoryRoot: preparation.RepositoryRoot, Score: input.Score, Cast: input.Cast, RunID: started.RunID,
		Attempt: attempt, BaseTree: input.BaseTree, CandidateTree: input.BaseTree, Authority: authority,
		PerformerID: "worker", SelectionReason: "initial", RemainingMS: input.Projection.Scheduler.RemainingTime,
	}, driver.ExecutionDependencies{Probe: faultpoint.Nop{}, Client: waitingProposalExecutor{t: t, baseHash: hash}, ResolveTrampoline: func() (string, error) { return "/fixture/trampoline", nil }, Now: time.Now, NewID: workspace.NewID, ProposalDisposition: New()})
	if result.Outcome != driver.OutcomeWaitingHuman || result.Err != nil {
		t.Fatalf("execute result = %+v", result)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Events) < 2 || journal.Events[len(journal.Events)-2].Type != runstate.EventAttemptBlocked || journal.Events[len(journal.Events)-1].Type != runstate.EventAmendmentRoutedHuman {
		t.Fatalf("terminal ordering = %#v", journal.Events[len(journal.Events)-2:])
	}
}

func TestDispositionerRefusesAutoApprovalExplicitly(t *testing.T) {
	preparation, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	_, err = New().PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, false, []any{map[string]any{"op": "replace", "path": "/policy/budget/active_wall_clock_min", "value": float64(9)}}))
	if err != ErrAutoApprovalUnimplemented {
		t.Fatalf("auto approval error = %v, want %v", err, ErrAutoApprovalUnimplemented)
	}
}

func TestRecoveryCompletesFrozenBlockingProposalRouteThenRequest(t *testing.T) {
	store, authority, started := interruptedBlockingProposalFixture(t)
	if err := authority.Release(); err != nil {
		t.Fatal(err)
	}
	executor := recoveryExecutor(store, started.RunID)
	result, err := executor.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != recoveryexec.OutcomeQuiescent || !reflect.DeepEqual(result.Kinds, []recovery.ActionKind{recovery.ActionAppendBlockedProposalRoute, recovery.ActionAppendRoutedRequest, recovery.ActionReturnWaitingHuman}) {
		t.Fatalf("recovery result = %+v", result)
	}

	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	blocked, routed, requested := blockingRouteEvents(t, journal.Events)
	blockedPayload := eventPayload(t, blocked)
	raised := blockedPayload["raised"].([]any)[0].(map[string]any)
	descriptor := raised["route"].(map[string]any)
	routedPayload := eventPayload(t, routed)
	for _, key := range []string{"proposal_record_hash", "reason", "decision_type", "base_revision", "base_hash", "classifier_version", "typed_delta", "actual_impact", "identity_versions"} {
		if !reflect.DeepEqual(routedPayload[key], descriptor[key]) {
			t.Fatalf("routed %s = %#v, want frozen descriptor %#v", key, routedPayload[key], descriptor[key])
		}
	}
	if routedPayload["proposal_id"] != raised["proposal_id"] || routedPayload["emitted_id"] != raised["emitted_id"] || routedPayload["decision_id"] != raised["decision_id"] || routedPayload["blocking"] != true {
		t.Fatalf("routed payload does not preserve frozen source = %#v", routedPayload)
	}
	requestedPayload := eventPayload(t, requested)
	for key, want := range map[string]any{
		"decision_id": routedPayload["decision_id"], "decision_type": routedPayload["decision_type"], "proposal_id": routedPayload["proposal_id"],
		"routed_reason": routedPayload["reason"], "blocking": routedPayload["blocking"], "emitted_id": routedPayload["emitted_id"],
	} {
		if !reflect.DeepEqual(requestedPayload[key], want) {
			t.Fatalf("requested %s = %#v, want routed source %#v", key, requestedPayload[key], want)
		}
	}

	before := len(journal.Events)
	second, err := executor.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Outcome != recoveryexec.OutcomeQuiescent {
		t.Fatalf("second recovery result = %+v", second)
	}
	journal, err = store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Events) != before {
		t.Fatalf("idempotent recovery appended %d events", len(journal.Events)-before)
	}
}

var errInterruptedRouteAppend = fmt.Errorf("fixture crash after attempt.blocked")

type interruptedRouteDispositioner struct{}

func (interruptedRouteDispositioner) PrepareAdapterProposal(ctx context.Context, proposal driver.AdapterProposal) (driver.AdapterProposalDisposition, error) {
	disposition, err := New().PrepareAdapterProposal(ctx, proposal)
	if err != nil || disposition.AppendRoute == nil {
		return disposition, err
	}
	disposition.AppendRoute = func(context.Context) error { return errInterruptedRouteAppend }
	return disposition, nil
}

func interruptedBlockingProposalFixture(t *testing.T) (*runstore.Store, *runstore.Driver, workspace.StartResult) {
	t.Helper()
	preparation, store, authority, started := dispositionFixture(t)
	attempt, err := started.Run.CreateAttempt("inspect")
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []runstate.EventType{runstate.EventMovementReady, runstate.EventMovementStarted} {
		if _, err := authority.Append(runstate.Event{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", Type: eventType, Payload: []byte(`{}`)}, faultpoint.ReceiptAddress("test."+string(eventType))); err != nil {
			t.Fatal(err)
		}
	}
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	result := driver.ExecuteAttempt(context.Background(), driver.AttemptExecution{
		RepositoryRoot: preparation.RepositoryRoot, Score: input.Score, Cast: input.Cast, RunID: started.RunID,
		Attempt: attempt, BaseTree: input.BaseTree, CandidateTree: input.BaseTree, Authority: authority,
		PerformerID: "worker", SelectionReason: "initial", RemainingMS: input.Projection.Scheduler.RemainingTime,
	}, driver.ExecutionDependencies{Probe: faultpoint.Nop{}, Client: waitingProposalExecutor{t: t, baseHash: hash}, ResolveTrampoline: func() (string, error) { return "/fixture/trampoline", nil }, Now: time.Now, NewID: workspace.NewID, ProposalDisposition: interruptedRouteDispositioner{}})
	if result.Err == nil || result.Err.Error() == "" {
		t.Fatalf("interrupted route result = %+v, want append failure after attempt.blocked", result)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	blocked, routed, _ := blockingRouteEvents(t, journal.Events)
	if blocked.Type != runstate.EventAttemptBlocked || routed.Type != "" {
		t.Fatalf("interrupted route journal has routed marker: %#v", journal.Events)
	}
	return store, authority, started
}

func recoveryExecutor(store *runstore.Store, runID runstate.RunID) *recoveryexec.Executor {
	return &recoveryexec.Executor{Store: store, RunID: runID, Load: func(context.Context) (recovery.Input, error) {
		input, err := store.LoadRunInput(runID)
		if err != nil {
			return recovery.Input{}, err
		}
		observations, err := recoveryobs.Collect(store, runID, input.Projection)
		return recovery.Input{Projection: input.Projection, Observations: observations}, err
	}}
}

func blockingRouteEvents(t *testing.T, events []runstate.Event) (runstate.Event, runstate.Event, runstate.Event) {
	t.Helper()
	var blocked, routed, requested runstate.Event
	for _, event := range events {
		switch event.Type {
		case runstate.EventAttemptBlocked:
			blocked = event
		case runstate.EventAmendmentRoutedHuman:
			routed = event
		case runstate.EventDecisionRequested:
			payload := eventPayload(t, event)
			if payload["decision_type"] == "amendment" {
				requested = event
			}
		}
	}
	return blocked, routed, requested
}

func adapterProposal(store *runstore.Store, authority *runstore.Driver, runID runstate.RunID, hash string, requiresDecision bool, operations []any) driver.AdapterProposal {
	amendment, err := json.Marshal(map[string]any{"base_revision": 1, "base_hash": hash, "operations": operations, "reason": "adapter request"})
	if err != nil {
		panic(err)
	}
	return driver.AdapterProposal{Store: store, Authority: authority, RunID: runID, AttemptID: "attempt-1", MovementID: "inspect", PartID: "reader", ProposalID: "prp-1", DecisionID: "dec-1", Event: protocol.ProposalEvent{ID: "emitted-1", Amendment: amendment, RequiresDecision: requiresDecision}}
}

func dispositionFixture(t *testing.T) (*validate.Preparation, *runstore.Store, *runstore.Driver, workspace.StartResult) {
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
	write(filepath.Join(root, "partitur.yaml"), `{"score":"0.2","name":"amendmentexec","revision":1,"status":"finalized","goal":"goal","open_questions":[],"parts":{"reader":{"capabilities":["repo_read"],"read_only":true}},"movements":[{"id":"inspect","part":"reader","grants":["repo_read"],"may_propose":true,"instruction":"inspect","outputs":[{"id":"report","kind":"artifact"}],"acceptance":{"hard":[{"id":"report-present","artifact":"report"}]}}],"policy":{"allowed_paths":["src/**"],"budget":{"active_wall_clock_min":10,"retries_per_movement":2},"amendment":{"auto":"envelope"}},"verification":{"expectation":{"intent":"pass-existing-tests","apply_gate":{"require":["verified"]}},"final_movement":"inspect"}}`)
	write(filepath.Join(root, ".partitur", "cast.yaml"), `{"cast":"0.1","performers":{"worker":{"adapter":"fixture","model":"fixture"}},"bindings":{"reader":{"performer":"worker"}}}`)
	for _, args := range [][]string{{"init"}, {"config", "user.name", "Partitur Test"}, {"config", "user.email", "partitur@example.invalid"}, {"add", "partitur.yaml", ".partitur/cast.yaml"}, {"commit", "-m", "fixture"}} {
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
	preparation, result := validate.Prepare()
	if preparation == nil || result.HasDiagnostics() {
		t.Fatalf("prepare result = %#v", result)
	}
	started, err := workspace.Start(preparation, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.AcquireDriver(started.RunID, movementSeeds(preparation.Score))
	if err != nil {
		t.Fatal(err)
	}
	return preparation, store, authority, started
}

func movementSeeds(compiled *score.Score) []runstate.MovementSeed {
	movements := compiled.Movements()
	result := make([]runstate.MovementSeed, 0, len(movements))
	for _, movement := range movements {
		repoWrite := false
		for _, grant := range movement.Grants {
			repoWrite = repoWrite || grant == "repo_write"
		}
		result = append(result, runstate.MovementSeed{ID: runstate.MovementID(movement.ID), Initial: runstate.MovementPending, RepoWrite: repoWrite, HasDependencies: len(movement.Needs) != 0, Final: movement.ID == compiled.Execution().FinalMovementID})
	}
	return result
}

func eventPayload(t *testing.T, event runstate.Event) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(event.Payload, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

type waitingProposalExecutor struct {
	t        *testing.T
	baseHash string
}

func (waitingProposalExecutor) Resolve(string) (string, error) { return "/fixture/adapter", nil }

func (fixture waitingProposalExecutor) Execute(_ context.Context, plan adapter.ExecutePlan) (adapter.ExecuteReport, error) {
	fixture.t.Helper()
	start, err := procid.Read(os.Getpid())
	if err != nil {
		return adapter.ExecuteReport{}, err
	}
	if _, err := plan.RecordIdentity(runstate.ProcessIdentity{PID: os.Getpid(), SessionID: os.Getpid(), Start: start}); err != nil {
		return adapter.ExecuteReport{}, err
	}
	probe := protocol.ProbeResult{Protocol: protocol.ProtocolVersion, Adapter: protocol.AdapterIdentity{ID: plan.AdapterID, Version: "fixture"}, Capabilities: protocol.Capabilities{RepoRead: true}, Enforcement: protocol.Enforcement{PathGrants: true, ReadOnly: true, NetworkGrants: true, ShellGrants: true, ReadGrants: true}}
	if _, err := plan.Recorder.RecordProbe(probe); err != nil {
		return adapter.ExecuteReport{}, err
	}
	if _, err := plan.Recorder.RecordExecutionStopped(adapter.ExecutionStop{IntervalID: plan.IntervalID, Reason: "normal", Charging: "measured", ObservedAt: plan.IntervalOpened}); err != nil {
		return adapter.ExecuteReport{}, err
	}
	proposal := protocol.ProposalEvent{Type: protocol.EventProposal, ID: "emitted-1", Amendment: amendmentForPlan(fixture.baseHash), RequiresDecision: true}
	result := protocol.ExecuteResult{Outcome: protocol.OutcomeWaitingHuman, PendingDecisionIDs: []string{proposal.ID}}
	if _, err := plan.Recorder.RecordOutcome(adapter.OutcomeObservation{EventType: string(runstate.EventAttemptBlocked), Result: result, Raised: []adapter.RaisedDecision{{Kind: protocol.EventProposal, Proposal: &proposal}}}); err != nil {
		return adapter.ExecuteReport{}, err
	}
	return adapter.ExecuteReport{Probe: probe, Result: &result, Proposals: []protocol.ProposalEvent{proposal}}, nil
}

func amendmentForPlan(baseHash string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"base_revision":1,"base_hash":%q,"operations":[{"op":"replace","path":"/policy/budget/active_wall_clock_min","value":9}],"reason":"adapter request"}`, baseHash))
}
