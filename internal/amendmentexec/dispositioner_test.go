package amendmentexec

import (
	"context"
	"encoding/json"
	"errors"
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
	disposition, err := testDispositioner().PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, true, []any{map[string]any{"op": "replace", "path": "/revision", "value": float64(2)}}))
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
	for _, path := range []string{"scores/revision-2.yaml", "prepares"} {
		if _, err := os.Stat(filepath.Join(preparation.RepositoryRoot, ".partitur", "runs", string(started.RunID), path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rejection reserved preparation path %s: %v", path, err)
		}
	}
}

func TestDispositionerPublishesFrozenRouteThenAppendsItAfterDriverSource(t *testing.T) {
	preparation, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	disposition, err := testDispositioner().PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, true, []any{map[string]any{"op": "replace", "path": "/policy/budget/active_wall_clock_min", "value": float64(9)}}))
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
	}, driver.ExecutionDependencies{Probe: faultpoint.Nop{}, Client: waitingProposalExecutor{t: t, baseHash: hash, requiresDecision: true}, ResolveTrampoline: func() (string, error) { return "/fixture/trampoline", nil }, Now: time.Now, NewID: workspace.NewID, ProposalDisposition: testDispositioner()})
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

func TestDispositionerPreparesAutoApproval(t *testing.T) {
	preparation, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	disposition, err := testDispositioner().PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, false, []any{map[string]any{"op": "replace", "path": "/policy/budget/active_wall_clock_min", "value": float64(9)}}))
	if err != nil {
		t.Fatal(err)
	}
	if disposition.PreparedReceipt == nil || disposition.PreparedReceipt.Mutation.EventType != string(runstate.EventAmendmentApprovalPrepared) {
		t.Fatalf("auto disposition = %#v, want approval_prepared receipt", disposition)
	}
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.PendingPrepare == nil {
		t.Fatal("auto approval did not leave a pending prepare")
	}
	prepare := input.Projection.State.PendingPrepare
	planBytes, err := os.ReadFile(filepath.Join(preparation.RepositoryRoot, ".partitur", "runs", string(started.RunID), "prepares", string(prepare.ID)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := runstate.DecodeApprovalPlan(planBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.MatchesPrepare(*prepare) {
		t.Fatalf("plan does not match pending prepare: plan=%+v prepare=%+v", plan, *prepare)
	}
	snapshot, err := os.ReadFile(filepath.Join(preparation.RepositoryRoot, ".partitur", "runs", string(started.RunID), "scores", "revision-2.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	compiled, diagnostics := score.Compile(snapshot)
	if len(diagnostics) != 0 || compiled.Revision() != 2 {
		t.Fatalf("prepared snapshot = revision %d diagnostics %v\n%s", compiled.Revision(), diagnostics, snapshot)
	}
	_, err = testDispositioner().PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, false, []any{map[string]any{"op": "replace", "path": "/policy/budget/active_wall_clock_min", "value": float64(9)}}))
	if !errors.Is(err, ErrPreparePending) {
		t.Fatalf("second auto prepare error = %v, want ErrPreparePending", err)
	}
}

func TestDispositionerUsesFixedQuiesceSilenceLimit(t *testing.T) {
	preparation, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = New().PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, false, []any{map[string]any{"op": "replace", "path": "/policy/budget/active_wall_clock_min", "value": float64(9)}})); err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.PendingPrepare == nil || input.Projection.State.PendingPrepare.QuiesceSilenceLimitMS != quiesceSilenceLimitMillis {
		t.Fatalf("fixed-silence prepare = %+v", input.Projection.State.PendingPrepare)
	}
}

func TestDriverAutoApprovalCommitsAndContinuesWithoutWaitingHuman(t *testing.T) {
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
	client := &autoThenInterruptedExecutor{first: waitingProposalExecutor{t: t, baseHash: hash}}
	result := driver.ExecuteAttempt(context.Background(), driver.AttemptExecution{
		RepositoryRoot: preparation.RepositoryRoot, Score: input.Score, Cast: input.Cast, RunID: started.RunID,
		Attempt: attempt, BaseTree: input.BaseTree, CandidateTree: input.BaseTree, Authority: authority,
		PerformerID: "worker", SelectionReason: "initial", RemainingMS: input.Projection.Scheduler.RemainingTime,
	}, driver.ExecutionDependencies{Probe: faultpoint.Nop{}, Client: client, ResolveTrampoline: func() (string, error) { return "/fixture/trampoline", nil }, Now: time.Now, NewID: workspace.NewID, ProposalDisposition: testDispositioner()})
	if result.Outcome != driver.OutcomeInterrupted || !errors.Is(result.Err, errContinuedAutoApproval) || client.calls != 2 {
		t.Fatalf("execute result = %+v calls=%d, want second revision attempt interruption", result, client.calls)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var rounds []uint64
	for _, event := range journal.Events {
		if event.Type != runstate.EventAmendmentQuiesceObserved {
			continue
		}
		payload := eventPayload(t, event)
		rounds = append(rounds, uint64(payload["sweep_round"].(float64)))
	}
	if !reflect.DeepEqual(rounds, []uint64{1, 2}) {
		t.Fatalf("auto prepare quiesce receipts = %v, want [1 2]", rounds)
	}
	quiesced, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if quiesced.Projection.State.PendingPrepare != nil || quiesced.Projection.State.ScoreHead.Revision != 2 {
		t.Fatalf("auto approval state = %+v, want committed revision 2", quiesced.Projection.State)
	}
	journal, err = store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range journal.Events {
		if event.Type == runstate.EventAmendmentApproved {
			if _, present := eventPayload(t, event)["fenced_epoch"]; present {
				t.Fatalf("matching-sidecar approval unexpectedly fenced: %+v", event)
			}
		}
		if event.Type == runstate.EventPerformerSelected && event.ScoreRevision == 2 {
			payload := eventPayload(t, event)
			if payload["reason"] != "revision_restart" {
				t.Fatalf("revision-2 selection = %#v", payload)
			}
		}
	}
}

func TestDriverAutoApprovalFreshAcquisitionFailureKeepsCommittedApprovalForResume(t *testing.T) {
	preparation, store, authority, started, attempt, input, hash := autoApprovalAttemptFixture(t)
	defer authority.Release()
	acquireErr := errors.New("fresh driver acquisition failed")
	result := driver.ExecuteAttempt(context.Background(), driver.AttemptExecution{
		RepositoryRoot: preparation.RepositoryRoot, Score: input.Score, Cast: input.Cast, RunID: started.RunID,
		Attempt: attempt, BaseTree: input.BaseTree, CandidateTree: input.BaseTree, Authority: authority,
		PerformerID: "worker", SelectionReason: "initial", RemainingMS: input.Projection.Scheduler.RemainingTime,
	}, driver.ExecutionDependencies{
		Probe: faultpoint.Nop{}, Client: waitingProposalExecutor{t: t, baseHash: hash},
		ResolveTrampoline: func() (string, error) { return "/fixture/trampoline", nil }, Now: time.Now, NewID: workspace.NewID,
		ProposalDisposition: testDispositioner(),
		AcquireDriver: func(*runstore.Store, runstate.RunID, []runstate.MovementSeed) (*runstore.Driver, error) {
			return nil, acquireErr
		},
	})
	if result.Outcome != driver.OutcomeInterrupted || !errors.Is(result.Err, acquireErr) {
		t.Fatalf("execute result = %+v, want interrupted fresh-acquisition failure", result)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if last := journal.Events[len(journal.Events)-1]; last.Type != runstate.EventAmendmentApproved {
		t.Fatalf("journal head = %s, want durable amendment.approved after fresh-acquisition failure", last.Type)
	}
	committed, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Projection.State.PendingPrepare != nil || committed.Projection.State.ScoreHead.Revision != 2 {
		t.Fatalf("post-acquisition-failure state = %+v, want committed revision 2", committed.Projection.State)
	}

	_, _ = recoveryExecutor(store, started.RunID).Execute(context.Background())
	journal, err = store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRevisionRestartSelection(t, journal.Events, 2) {
		t.Fatal("resume did not perform the committed approval's revision-restart continuation")
	}
}

func TestDriverAutoApprovalCancellationAfterCommitBeforeFreshAcquisition(t *testing.T) {
	preparation, store, authority, started, attempt, input, hash := autoApprovalAttemptFixture(t)
	defer authority.Release()
	acknowledged := false
	result := driver.ExecuteAttempt(context.Background(), driver.AttemptExecution{
		RepositoryRoot: preparation.RepositoryRoot, Score: input.Score, Cast: input.Cast, RunID: started.RunID,
		Attempt: attempt, BaseTree: input.BaseTree, CandidateTree: input.BaseTree, Authority: authority,
		PerformerID: "worker", SelectionReason: "initial", RemainingMS: input.Projection.Scheduler.RemainingTime,
	}, driver.ExecutionDependencies{
		Probe: faultpoint.Nop{}, Client: waitingProposalExecutor{t: t, baseHash: hash},
		ResolveTrampoline: func() (string, error) { return "/fixture/trampoline", nil }, Now: time.Now, NewID: workspace.NewID,
		ProposalDisposition:      testDispositioner(),
		AfterPrepareAcknowledged: func() { acknowledged = true },
		AcquireDriver: func(store *runstore.Store, runID runstate.RunID, seeds []runstate.MovementSeed) (*runstore.Driver, error) {
			if !acknowledged {
				t.Fatal("fresh acquisition began before the acknowledged-prepare seam")
			}
			if err := store.RequestCancellation(runID); err != nil {
				t.Fatal(err)
			}
			return store.AcquireDriver(runID, seeds)
		},
	})
	if result.Outcome != driver.OutcomeCancelled || result.Err != nil {
		t.Fatalf("execute result = %+v, want cancellation after committed approval", result)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	approved, cancelled := -1, -1
	for index, event := range journal.Events {
		switch event.Type {
		case runstate.EventAmendmentApproved:
			approved = index
		case runstate.EventRunCancelled:
			cancelled = index
		}
	}
	if approved < 0 || cancelled <= approved {
		t.Fatalf("approval/cancellation order = approved:%d cancelled:%d, want committed approval before cancellation", approved, cancelled)
	}
	if hasRevisionRestartSelection(t, journal.Events, 2) {
		t.Fatal("cancellation after commit continued into revision restart")
	}
}

func TestRecoveryCommitsPrepareAfterCrashBetweenLeaseMoveAndCommit(t *testing.T) {
	preparation, store, authority, started, attempt, input, hash := autoApprovalAttemptFixture(t)
	defer authority.Release()
	crash := errors.New("crash after lease move")
	func() {
		defer func() {
			if recovered := recover(); recovered != crash {
				t.Fatalf("panic = %#v, want crash after lease move", recovered)
			}
		}()
		_ = driver.ExecuteAttempt(context.Background(), driver.AttemptExecution{
			RepositoryRoot: preparation.RepositoryRoot, Score: input.Score, Cast: input.Cast, RunID: started.RunID,
			Attempt: attempt, BaseTree: input.BaseTree, CandidateTree: input.BaseTree, Authority: authority,
			PerformerID: "worker", SelectionReason: "initial", RemainingMS: input.Projection.Scheduler.RemainingTime,
		}, driver.ExecutionDependencies{
			Probe: faultpoint.Nop{}, Client: waitingProposalExecutor{t: t, baseHash: hash},
			ResolveTrampoline: func() (string, error) { return "/fixture/trampoline", nil }, Now: time.Now, NewID: workspace.NewID,
			ProposalDisposition: testDispositioner(), AfterPrepareAcknowledged: func() { panic(crash) },
		})
	}()
	precrash, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if precrash.Projection.State.PendingPrepare == nil {
		t.Fatal("crash did not retain the pending prepare for RC-RESUME-007")
	}
	prepareID := precrash.Projection.State.PendingPrepare.ID
	planBytes, err := os.ReadFile(filepath.Join(preparation.RepositoryRoot, ".partitur", "runs", string(started.RunID), "prepares", string(prepareID)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := runstate.DecodeApprovalPlan(planBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recoveryExecutor(store, started.RunID).Execute(context.Background()); err == nil {
		t.Fatal("recovery did not reach the recovered continuation adapter boundary")
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range journal.Events {
		if event.Type != runstate.EventAmendmentApproved {
			continue
		}
		want, err := plan.ApprovedPayload(nil)
		if err != nil {
			t.Fatal(err)
		}
		if string(event.Payload) != string(want) {
			t.Fatalf("recovered approval payload = %s, want prepared unfenced plan %s", event.Payload, want)
		}
		if _, fenced := eventPayload(t, event)["fenced_epoch"]; fenced {
			t.Fatalf("recovered approval unexpectedly fenced: %+v", event)
		}
		return
	}
	t.Fatal("RC-RESUME-007 did not commit amendment.approved after the lease-move crash")
}

func TestDispositionerBarrierClosesEachCataloguedConsequenceBeforePrepare(t *testing.T) {
	store, authority, started := interruptedBlockingProposalFixture(t)
	defer authority.Release()
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := input.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	disposition, err := testDispositioner().PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, false, []any{map[string]any{"op": "replace", "path": "/policy/budget/active_wall_clock_min", "value": float64(9)}}))
	if err != nil {
		t.Fatal(err)
	}
	if disposition.PreparedReceipt == nil {
		t.Fatalf("disposition = %#v, want prepared receipt", disposition)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	_, routed, requested := blockingRouteEvents(t, journal.Events)
	if routed.Type != runstate.EventAmendmentRoutedHuman || requested.Type != runstate.EventDecisionRequested {
		t.Fatalf("barrier did not close route then request: routed=%+v requested=%+v", routed, requested)
	}
	if last := journal.Events[len(journal.Events)-1]; last.Type != runstate.EventAmendmentApprovalPrepared {
		t.Fatalf("last event = %s, want approval prepare after barrier closure", last.Type)
	}
}

func TestDispositionerBarrierLimitRejectsASecondSelectedConsequence(t *testing.T) {
	store, authority, started := interruptedBlockingProposalFixture(t)
	defer authority.Release()
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := input.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	dispositioner := testDispositioner()
	dispositioner.barrierLimit = 1
	_, err = dispositioner.PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, false, []any{map[string]any{"op": "replace", "path": "/policy/budget/active_wall_clock_min", "value": float64(9)}}))
	if !errors.Is(err, ErrBarrierDidNotConverge) {
		t.Fatalf("barrier error = %v, want ErrBarrierDidNotConverge", err)
	}
}

func TestDispositionerReestablishesApprovalIntentAfterBarrierInterleave(t *testing.T) {
	preparation, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	dispositioner := testDispositioner()
	var interleaveErr error
	dispositioner.afterBarrier = func() {
		dispositioner.afterBarrier = nil
		for _, event := range []runstate.Event{
			{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", Type: runstate.EventMovementReady, Payload: []byte(`{}`)},
			{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", Type: runstate.EventMovementStarted, Payload: []byte(`{}`)},
			{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", AttemptID: "retry-1", Type: runstate.EventPerformerSelected, Payload: []byte(`{"reason":"quality_retry","performer_id":"worker","adapter_id":"fixture","model":"fixture"}`)},
		} {
			if _, err := authority.Append(event, faultpoint.ReceiptAddress("test.reestablish."+string(event.Type))); err != nil {
				interleaveErr = err
				return
			}
		}
	}
	disposition, err := dispositioner.PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, false, []any{map[string]any{"op": "replace", "path": "/policy/allowed_paths", "value": []any{}}}))
	if interleaveErr != nil {
		t.Fatal(interleaveErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if disposition.RouteDescriptor == nil || disposition.RouteDescriptor["reason"] != "runtime_scope_started" || disposition.PreparedReceipt != nil {
		t.Fatalf("re-established disposition = %#v, want runtime_scope_started route", disposition)
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
	disposition, err := testDispositioner().PrepareAdapterProposal(ctx, proposal)
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
	}, driver.ExecutionDependencies{Probe: faultpoint.Nop{}, Client: waitingProposalExecutor{t: t, baseHash: hash, requiresDecision: true}, ResolveTrampoline: func() (string, error) { return "/fixture/trampoline", nil }, Now: time.Now, NewID: workspace.NewID, ProposalDisposition: interruptedRouteDispositioner{}})
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

func testDispositioner() ProposalDispositioner {
	return New()
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

func autoApprovalAttemptFixture(t *testing.T) (*validate.Preparation, *runstore.Store, *runstore.Driver, workspace.StartResult, *workspace.AttemptWorkspace, runstore.RunInput, string) {
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

func hasRevisionRestartSelection(t *testing.T, events []runstate.Event, revision uint64) bool {
	t.Helper()
	for _, event := range events {
		if event.Type == runstate.EventPerformerSelected && event.ScoreRevision == revision && eventPayload(t, event)["reason"] == "revision_restart" {
			return true
		}
	}
	return false
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
	t                *testing.T
	baseHash         string
	requiresDecision bool
}

var errContinuedAutoApproval = errors.New("continued auto approval attempt")

type autoThenInterruptedExecutor struct {
	first waitingProposalExecutor
	calls int
}

func (fixture *autoThenInterruptedExecutor) Resolve(adapterID string) (string, error) {
	return fixture.first.Resolve(adapterID)
}

func (fixture *autoThenInterruptedExecutor) Execute(ctx context.Context, plan adapter.ExecutePlan) (adapter.ExecuteReport, error) {
	fixture.calls++
	if fixture.calls == 1 {
		return fixture.first.Execute(ctx, plan)
	}
	return adapter.ExecuteReport{}, errContinuedAutoApproval
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
	proposal := protocol.ProposalEvent{Type: protocol.EventProposal, ID: "emitted-1", Amendment: amendmentForPlan(fixture.baseHash), RequiresDecision: fixture.requiresDecision}
	result := protocol.ExecuteResult{Outcome: protocol.OutcomeWaitingHuman}
	if proposal.RequiresDecision {
		result.PendingDecisionIDs = []string{proposal.ID}
	}
	if _, err := plan.Recorder.RecordOutcome(adapter.OutcomeObservation{EventType: string(runstate.EventAttemptBlocked), Result: result, Raised: []adapter.RaisedDecision{{Kind: protocol.EventProposal, Proposal: &proposal}}}); err != nil {
		return adapter.ExecuteReport{}, err
	}
	return adapter.ExecuteReport{Probe: probe, Result: &result, Proposals: []protocol.ProposalEvent{proposal}}, nil
}

func amendmentForPlan(baseHash string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"base_revision":1,"base_hash":%q,"operations":[{"op":"replace","path":"/policy/budget/active_wall_clock_min","value":9}],"reason":"adapter request"}`, baseHash))
}
