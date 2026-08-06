// Package amendmentexec realizes the durable §9 disposition of a single
// adapter proposal, including the §6 barrier and auto-approval preparation.
package amendmentexec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/amendment"
	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/driver"
	"github.com/BeomSeogKim/Partitur/internal/executiondep"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/recoveryconsequence"
	"github.com/BeomSeogKim/Partitur/internal/recoveryobs"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

var (
	ErrPreparePending        = errors.New("amendment approval prepare is already pending")
	ErrBarrierDidNotConverge = errors.New("amendment durable-consequence barrier did not converge")
)

const (
	defaultBarrierLimit       = 64
	quiesceSilenceLimitMillis = 60_000
)

var errBarrierRecheck = errors.New("amendment barrier requires another locked recheck")

// ProposalDispositioner is the one driver callback that evaluates adapter
// proposals. It is stateless so later origins can use the same consequence
// boundary without inventing another approval-policy implementation.
type ProposalDispositioner struct {
	NewID        func() (string, error)
	barrierLimit int
	// afterBarrier is a test-only interleave seam. Production leaves it nil.
	afterBarrier func()
}

func New() ProposalDispositioner { return ProposalDispositioner{NewID: workspace.NewID} }

type amendmentProposal struct {
	driver.AdapterProposal
	origin string
	mutate func(func(*runstore.Txn, runstate.State) error) error
}

// CLIProposal is the command-origin value captured before §9 admission takes
// the repository state lock. Operations retain their submitted JSON bytes.
type CLIProposal struct {
	RunID         runstate.RunID
	BaseRevision  uint64
	BaseHash      runstate.Hash
	Operations    json.RawMessage
	Reason        string
	ClaimedImpact json.RawMessage
}

// CLIResult reports the durable disposition of one command-origin proposal.
// A routed proposal includes the allocated non-blocking human decision.
type CLIResult struct {
	ProposalID runstate.ProposalID
	DecisionID string
	Outcome    amendment.Outcome
}

// SubmitCLI runs the same §9 evaluator, barrier, prepare, and commit table as
// an adapter proposal, while deliberately never acquiring driver authority.
func (dispositioner ProposalDispositioner) SubmitCLI(ctx context.Context, store *runstore.Store, submission CLIProposal) (CLIResult, error) {
	if store == nil || submission.RunID == "" {
		return CLIResult{}, errors.New("CLI amendment requires store and run id")
	}
	if strings.TrimSpace(submission.Reason) == "" {
		return CLIResult{}, errors.New("CLI amendment reason is required")
	}
	newID := dispositioner.NewID
	if newID == nil {
		newID = workspace.NewID
	}
	proposalID, err := newID()
	if err != nil {
		return CLIResult{}, fmt.Errorf("allocate proposal id: %w", err)
	}
	decisionID, err := newID()
	if err != nil {
		return CLIResult{}, fmt.Errorf("allocate decision id: %w", err)
	}
	value := map[string]any{
		"base_revision": submission.BaseRevision,
		"base_hash":     submission.BaseHash,
		"operations":    submission.Operations,
		"reason":        submission.Reason,
	}
	if len(submission.ClaimedImpact) != 0 {
		value["claimed_impact"] = submission.ClaimedImpact
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return CLIResult{}, fmt.Errorf("encode CLI amendment: %w", err)
	}
	proposal := amendmentProposal{
		AdapterProposal: driver.AdapterProposal{
			Store: store, RunID: submission.RunID, ProposalID: runstate.ProposalID(proposalID),
			DecisionID: decisionID, Event: protocol.ProposalEvent{Amendment: encoded, RequiresDecision: false},
		},
		origin: "cli",
	}
	proposal.mutate = func(mutation func(*runstore.Txn, runstate.State) error) error {
		return store.MutateProjected(submission.RunID, mutation)
	}
	prepared, outcome, err := dispositioner.prepare(ctx, proposal)
	if err != nil {
		return CLIResult{}, err
	}
	result := CLIResult{ProposalID: proposal.ProposalID, DecisionID: decisionID, Outcome: outcome}
	if outcome.Kind == amendment.Routed {
		if prepared.AppendRoute == nil {
			return CLIResult{}, errors.New("CLI routed amendment has no route append")
		}
		if err := prepared.AppendRoute(ctx); err != nil {
			return CLIResult{}, err
		}
		return result, nil
	}
	if outcome.Kind != amendment.Approved {
		return result, nil
	}
	if prepared.PreparedReceipt == nil {
		return CLIResult{}, errors.New("CLI approved amendment has no prepare receipt")
	}
	if err := store.CompleteOrAbandonPrepare(ctx, submission.RunID); err != nil {
		return CLIResult{}, err
	}
	return result, nil
}

// RequiresSingleRaisedForAuto prevents a prepare from suppressing the durable
// source event for another raised adapter decision.
func (ProposalDispositioner) RequiresSingleRaisedForAuto() bool { return true }

func (dispositioner ProposalDispositioner) PrepareAdapterProposal(ctx context.Context, proposal driver.AdapterProposal) (driver.AdapterProposalDisposition, error) {
	if err := ctx.Err(); err != nil {
		return driver.AdapterProposalDisposition{}, err
	}
	if proposal.Store == nil || proposal.Authority == nil {
		return driver.AdapterProposalDisposition{}, errors.New("amendment dispositioner requires store and authority")
	}
	prepared, _, err := dispositioner.prepare(ctx, amendmentProposal{
		AdapterProposal: proposal, origin: "adapter",
		mutate: func(mutation func(*runstore.Txn, runstate.State) error) error {
			return proposal.Authority.Mutate(mutation)
		},
	})
	return prepared, err
}

func (dispositioner ProposalDispositioner) prepare(ctx context.Context, proposal amendmentProposal) (driver.AdapterProposalDisposition, amendment.Outcome, error) {
	if err := ctx.Err(); err != nil {
		return driver.AdapterProposalDisposition{}, amendment.Outcome{}, err
	}
	submission, err := decodeSubmission(proposal.Event)
	if err != nil {
		return driver.AdapterProposalDisposition{}, amendment.Outcome{}, err
	}
	approved, _, err := dispositioner.hasApprovalIntent(proposal, submission)
	if err != nil {
		return driver.AdapterProposalDisposition{}, amendment.Outcome{}, err
	}
	if !approved {
		disposition, outcome, err := dispositioner.finalizeDisposition(proposal, submission, false)
		if errors.Is(err, errBarrierRecheck) {
			return dispositioner.prepareAtBarrierFixedPoint(ctx, proposal, submission)
		}
		return disposition, outcome, err
	}
	return dispositioner.prepareAtBarrierFixedPoint(ctx, proposal, submission)
}

func (dispositioner ProposalDispositioner) hasApprovalIntent(proposal amendmentProposal, input submission) (bool, amendment.Outcome, error) {
	approved := false
	var result amendment.Outcome
	err := proposal.mutate(func(_ *runstore.Txn, state runstate.State) error {
		if err := requireNoPendingPrepare(state); err != nil {
			return err
		}
		outcome, _, err := dispositioner.evaluate(proposal, input, state)
		if err != nil {
			return err
		}
		result = outcome
		approved = outcome.Kind == amendment.Approved
		return nil
	})
	return approved, result, err
}

func (dispositioner ProposalDispositioner) prepareAtBarrierFixedPoint(ctx context.Context, proposal amendmentProposal, input submission) (driver.AdapterProposalDisposition, amendment.Outcome, error) {
	limit := dispositioner.barrierLimit
	if limit == 0 {
		limit = defaultBarrierLimit
	}
	applied := 0
	for {
		open, err := dispositioner.applyBarrier(ctx, proposal)
		if err != nil {
			return driver.AdapterProposalDisposition{}, amendment.Outcome{}, err
		}
		if open {
			applied++
			if applied > limit {
				return driver.AdapterProposalDisposition{}, amendment.Outcome{}, ErrBarrierDidNotConverge
			}
			continue
		}
		if dispositioner.afterBarrier != nil {
			dispositioner.afterBarrier()
		}
		disposition, outcome, err := dispositioner.finalizeDisposition(proposal, input, true)
		if errors.Is(err, errBarrierRecheck) {
			continue
		}
		return disposition, outcome, err
	}
}

func (dispositioner ProposalDispositioner) finalizeDisposition(proposal amendmentProposal, input submission, requireBarrierFixedPoint bool) (driver.AdapterProposalDisposition, amendment.Outcome, error) {
	var disposition driver.AdapterProposalDisposition
	var result amendment.Outcome
	err := proposal.mutate(func(transaction *runstore.Txn, state runstate.State) error {
		if err := requireNoPendingPrepare(state); err != nil {
			return err
		}
		outcome, versions, err := dispositioner.evaluate(proposal, input, state)
		if err != nil {
			return err
		}
		result = outcome
		proposal.ScoreRevision = state.ScoreHead.Revision
		if !requireBarrierFixedPoint && outcome.Kind == amendment.Approved {
			return errBarrierRecheck
		}
		if requireBarrierFixedPoint && outcome.Kind == amendment.Approved {
			barrier, err := recoveryInput(proposal.Store, proposal.RunID, proposal.Authority)
			if err != nil {
				return err
			}
			decision := barrierDecision(barrier)
			if decision.Action != nil && recoveryconsequence.Handles(decision.CaseID) {
				return errBarrierRecheck
			}
		}
		switch outcome.Kind {
		case amendment.Rejected:
			event, err := rejectionEvent(proposal, input, outcome, versions)
			if err != nil {
				return err
			}
			if _, err := transaction.At("amendment.rejected").Append(event); err != nil {
				return err
			}
			return nil
		case amendment.Routed:
			record, err := input.record(proposal)
			if err != nil {
				return err
			}
			hash := rawHash(record)
			if _, err := transaction.At("proposal.record.published").PublishImmutable(
				runstore.Path("proposals/"+string(proposal.ProposalID)+".json"), record, runstore.Hash(hash),
			); err != nil {
				return err
			}
			descriptor := routeDescriptor(hash, outcome, input, versions)
			routeEvent, err := routedEvent(proposal, descriptor)
			if err != nil {
				return err
			}
			disposition = driver.AdapterProposalDisposition{
				RouteDescriptor: descriptor,
				AppendRoute: func(context.Context) error {
					if proposal.origin == "adapter" {
						_, err := proposal.Authority.Append(routeEvent, faultpoint.ReceiptAddress("amendment.routed_human"))
						return err
					}
					return proposal.mutate(func(transaction *runstore.Txn, current runstate.State) error {
						if _, err := runstate.Apply(current, routeEvent); err != nil {
							return err
						}
						if _, err := transaction.At("amendment.routed_human").Append(routeEvent); err != nil {
							return err
						}
						request, err := decisionRequestedEvent(proposal, outcome.Reason)
						if err != nil {
							return err
						}
						next, err := runstate.Apply(current, routeEvent)
						if err != nil {
							return err
						}
						if _, err := runstate.Apply(next, request); err != nil {
							return err
						}
						_, err = transaction.At("amendment.decision.requested").Append(request)
						return err
					})
				},
			}
			return nil
		case amendment.Approved:
			return dispositioner.appendPrepare(transaction, state, proposal, input, outcome, versions, &disposition)
		default:
			return fmt.Errorf("unknown amendment outcome %q", outcome.Kind)
		}
	})
	return disposition, result, err
}

func requireNoPendingPrepare(state runstate.State) error {
	if state.PendingPrepare != nil {
		return ErrPreparePending
	}
	return nil
}

func (dispositioner ProposalDispositioner) evaluate(proposal amendmentProposal, input submission, state runstate.State) (amendment.Outcome, map[string]any, error) {
	proposal.ScoreRevision = state.ScoreHead.Revision
	attempts, err := executiondep.Collect(proposal.Store, proposal.RunID)
	if err != nil {
		return amendment.Outcome{}, nil, fmt.Errorf("collect execution dependencies: %w", err)
	}
	base, err := proposal.Store.LoadScoreSnapshot(proposal.RunID, state.ScoreHead.Revision)
	if err != nil {
		return amendment.Outcome{}, nil, fmt.Errorf("load amendment base snapshot: %w", err)
	}
	outcome, err := amendment.Evaluate(amendment.Input{
		State: state, Base: base, BaseRevision: input.BaseRevision, BaseHash: input.BaseHash,
		Operations: input.Operations, ClaimedImpact: input.ClaimedImpact,
		HasClaimedImpact: input.HasClaimedImpact, Attempts: attempts,
		RequiresDecision: proposal.Event.RequiresDecision,
	})
	if err != nil {
		return amendment.Outcome{}, nil, err
	}
	versions, err := amendmentIdentityVersions(outcome.Patched != nil)
	if err != nil {
		return amendment.Outcome{}, nil, err
	}
	return outcome, versions, nil
}

func (dispositioner ProposalDispositioner) applyBarrier(ctx context.Context, proposal amendmentProposal) (bool, error) {
	input, err := recoveryInput(proposal.Store, proposal.RunID, proposal.Authority)
	if err != nil {
		return false, err
	}
	decision := barrierDecision(input)
	if decision.Action == nil || !recoveryconsequence.Handles(decision.CaseID) {
		return false, nil
	}
	if err := recoveryconsequence.Apply(ctx, recoveryconsequence.HandlerContext{Store: proposal.Store, Driver: proposal.Authority, RunID: proposal.RunID, Input: input}, decision.CaseID, *decision.Action); err != nil {
		return false, fmt.Errorf("apply amendment barrier %s: %w", decision.CaseID, err)
	}
	return true, nil
}

func recoveryInput(store *runstore.Store, runID runstate.RunID, authority *runstore.Driver) (recovery.Input, error) {
	loaded, err := store.LoadRunInput(runID)
	if err != nil {
		return recovery.Input{}, err
	}
	observations, err := recoveryobs.Collect(store, runID, loaded.Projection)
	if err != nil {
		return recovery.Input{}, err
	}
	if identity := observations.Lease.Identity; identity != nil && authority != nil && authority.MatchesLease(runstore.LeaseIdentity{Epoch: identity.Epoch, Token: identity.Token, PID: identity.PID, Start: identity.Start}) {
		observations.Lease.Owner = recovery.OwnerCurrentDriver
	}
	return recovery.Input{Projection: loaded.Projection, Observations: observations}, nil
}

func barrierDecision(input recovery.Input) recovery.Decision {
	decision := recovery.Plan(input)
	for decision.Action != nil {
		switch decision.Action.Continuation {
		case recovery.ContinuationC2:
			decision = recovery.PlanAttempt(input)
		case recovery.ContinuationC3:
			decision = recovery.PlanAcceptance(input)
		case recovery.ContinuationC4:
			decision = recovery.PlanScheduler(input)
		default:
			return decision
		}
	}
	return decision
}

func (dispositioner ProposalDispositioner) appendPrepare(transaction *runstore.Txn, state runstate.State, proposal amendmentProposal, input submission, outcome amendment.Outcome, versions map[string]any, disposition *driver.AdapterProposalDisposition) error {
	newID := dispositioner.NewID
	if newID == nil {
		newID = workspace.NewID
	}
	prepareID, err := newID()
	if err != nil {
		return fmt.Errorf("allocate prepare id: %w", err)
	}
	prepared, err := preparedScore(outcome.Patched, state.ScoreHead.Revision+1)
	if err != nil {
		return err
	}
	snapshot, err := prepared.CanonicalYAML()
	if err != nil {
		return fmt.Errorf("serialize amendment snapshot: %w", err)
	}
	semanticHash, err := prepared.Hash()
	if err != nil {
		return err
	}
	fileHash := rawHash(snapshot)
	envelope := string(outcome.Class)
	plan := runstate.ApprovalPlan{
		Schema: runstate.ApprovalPlanSchema, ProposalID: proposal.ProposalID,
		Mode: "auto", EnvelopeClass: &envelope, BaseRevision: input.BaseRevision, BaseHash: input.BaseHash,
		ClassifierVersion: canonical.AmendmentClassifierVersion, NewRevision: state.ScoreHead.Revision + 1,
		NewSnapshotHash: runstate.Hash(semanticHash), NewSnapshotFileHash: runstate.Hash(fileHash),
		TypedDelta: outcome.Impact.TypedDelta(), ActualImpact: outcome.Impact.Value(), HeadMovements: approvalHeadMovements(prepared),
		SupersededAttemptIDs: cancellableAttempts(state), ObsoletedDecisionIDs: pendingDecisions(state),
		Finalization: false, IdentityVersions: versions,
	}
	if proposal.origin == "adapter" {
		plan.EmittedID = stringPointer(proposal.Event.ID)
	}
	if state.ApplicationCandidate != nil {
		plan.CandidateID = stringPointer(state.ApplicationCandidate.ID)
	}
	planBytes, err := runstate.EncodeApprovalPlan(plan)
	if err != nil {
		return err
	}
	if _, err := transaction.At("amendment.approval.snapshot").PublishImmutable(runstore.Path(fmt.Sprintf("scores/revision-%d.yaml", plan.NewRevision)), snapshot, runstore.Hash(fileHash)); err != nil {
		return err
	}
	if _, err := transaction.At("amendment.approval.plan").PublishImmutable(runstore.Path("prepares/"+prepareID+".json"), planBytes, runstore.Hash(rawHash(planBytes))); err != nil {
		return err
	}
	payload := map[string]any{
		"prepare_id": prepareID, "proposal_id": string(proposal.ProposalID), "mode": "auto", "envelope_class": envelope,
		"base_revision": input.BaseRevision, "base_hash": string(input.BaseHash), "new_revision": plan.NewRevision,
		"new_snapshot_hash": semanticHash, "new_snapshot_file_hash": fileHash, "plan_record_hash": rawHash(planBytes),
		"target_attempt_ids": attemptStrings(plan.SupersededAttemptIDs), "observed_authority_epoch": state.Authority.Epoch,
		"quiesce_silence_limit_ms": quiesceSilenceLimitMillis,
		"classifier_version":       canonical.AmendmentClassifierVersion, "identity_versions": versions,
	}
	event, err := amendmentEvent(proposal, runstate.EventAmendmentApprovalPrepared, payload)
	if err != nil {
		return err
	}
	receipt, err := transaction.At("amendment.approval_prepared").Append(event)
	if err != nil {
		return err
	}
	disposition.PreparedReceipt = &receipt
	return nil
}

func preparedScore(patched *score.Score, revision uint64) (*score.Score, error) {
	if patched == nil {
		return nil, errors.New("approved amendment has no patched score")
	}
	bytes, err := patched.ProjectionBytes()
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(bytes, &value); err != nil {
		return nil, err
	}
	value["revision"] = float64(revision)
	prepared, diagnostics := score.CompileValue(value)
	if len(diagnostics) != 0 {
		return nil, fmt.Errorf("compile prepared score: %v", diagnostics)
	}
	return prepared, nil
}

func approvalHeadMovements(value *score.Score) []runstate.HeadMovement {
	execution, movements := value.Execution(), value.Movements()
	result := make([]runstate.HeadMovement, 0, len(movements))
	for _, movement := range movements {
		initial := runstate.MovementPending
		if value.Status() == "finalized" && movement.Phase == "draft" {
			initial = runstate.MovementInapplicable
		}
		repoWrite := false
		for _, grant := range movement.Grants {
			repoWrite = repoWrite || grant == "repo_write"
		}
		result = append(result, runstate.HeadMovement{ID: runstate.MovementID(movement.ID), Initial: initial, RepoWrite: repoWrite, HasDependencies: len(movement.Needs) != 0, Final: movement.ID == execution.FinalMovementID})
	}
	return result
}

func cancellableAttempts(state runstate.State) []runstate.AttemptID {
	result := make([]runstate.AttemptID, 0, len(state.Attempts))
	for id, attempt := range state.Attempts {
		switch attempt.State {
		case runstate.AttemptStarting, runstate.AttemptRunning, runstate.AttemptVerifying:
			result = append(result, id)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func pendingDecisions(state runstate.State) []string {
	result := make([]string, 0, len(state.PendingDecisions))
	for id := range state.PendingDecisions {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}
func attemptStrings(values []runstate.AttemptID) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}
func stringPointer(value string) *string { return &value }

type submission struct {
	BaseRevision     uint64          `json:"base_revision"`
	BaseHash         runstate.Hash   `json:"base_hash"`
	OperationsRaw    json.RawMessage `json:"operations"`
	Reason           string          `json:"reason"`
	Evidence         []string        `json:"evidence,omitempty"`
	ClaimedImpactRaw json.RawMessage `json:"claimed_impact,omitempty"`

	Operations       []any
	ClaimedImpact    score.Impact
	HasClaimedImpact bool
}

func decodeSubmission(event protocol.ProposalEvent) (submission, error) {
	var value submission
	if err := protocol.DecodeStrict(event.Amendment, &value); err != nil {
		return submission{}, fmt.Errorf("decode amendment proposal: %w", err)
	}
	if value.BaseRevision == 0 || value.BaseHash == "" || value.Reason == "" || len(value.OperationsRaw) == 0 {
		return submission{}, errors.New("decode amendment proposal: base_revision, base_hash, operations, and reason are required")
	}
	operations, err := canonical.ParseJSON(value.OperationsRaw)
	if err != nil {
		return submission{}, fmt.Errorf("parse amendment operations: %w", err)
	}
	var ok bool
	if value.Operations, ok = operations.([]any); !ok {
		return submission{}, errors.New("parse amendment operations: operations must be an array")
	}
	if len(value.ClaimedImpactRaw) != 0 {
		claim, err := decodeImpact(value.ClaimedImpactRaw)
		if err != nil {
			return submission{}, fmt.Errorf("decode claimed impact: %w", err)
		}
		value.ClaimedImpact = claim
		value.HasClaimedImpact = true
	}
	return value, nil
}

type impactWire struct {
	ScoreChanges []changeWire  `json:"score_changes"`
	Authority    authorityWire `json:"authority"`
	Budget       budgetWire    `json:"budget"`
}
type changeWire struct {
	Selector   string `json:"selector"`
	Operation  string `json:"operation"`
	BeforeHash string `json:"before_hash,omitempty"`
	AfterHash  string `json:"after_hash,omitempty"`
}
type setWire struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
}
type grantWire struct {
	MovementID string   `json:"movement_id"`
	Added      []string `json:"added"`
	Removed    []string `json:"removed"`
}
type authorityWire struct {
	AllowedPaths setWire     `json:"allowed_paths"`
	Grants       []grantWire `json:"grants"`
	SideEffects  setWire     `json:"side_effects"`
}
type budgetChangeWire struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}
type budgetWire struct {
	ActiveWallClockMin *budgetChangeWire `json:"active_wall_clock_min,omitempty"`
	RetriesPerMovement *budgetChangeWire `json:"retries_per_movement,omitempty"`
}

func decodeImpact(raw json.RawMessage) (score.Impact, error) {
	var value impactWire
	if err := protocol.DecodeStrict(raw, &value); err != nil {
		return score.Impact{}, err
	}
	if value.ScoreChanges == nil || value.Authority.Grants == nil {
		return score.Impact{}, errors.New("impact arrays are required")
	}
	result := score.Impact{
		ScoreChanges: make([]score.Change, 0, len(value.ScoreChanges)),
		Authority: score.AuthorityImpact{
			AllowedPaths: score.SetChange{Added: value.Authority.AllowedPaths.Added, Removed: value.Authority.AllowedPaths.Removed},
			Grants:       make([]score.GrantChange, 0, len(value.Authority.Grants)),
			SideEffects:  score.SetChange{Added: value.Authority.SideEffects.Added, Removed: value.Authority.SideEffects.Removed},
		},
	}
	for _, change := range value.ScoreChanges {
		result.ScoreChanges = append(result.ScoreChanges, score.Change{Selector: change.Selector, Operation: change.Operation, BeforeHash: change.BeforeHash, AfterHash: change.AfterHash})
	}
	for _, grant := range value.Authority.Grants {
		result.Authority.Grants = append(result.Authority.Grants, score.GrantChange{MovementID: grant.MovementID, Added: grant.Added, Removed: grant.Removed})
	}
	if value.Budget.ActiveWallClockMin != nil {
		result.Budget.ActiveWallClockMin = &score.BudgetChange{From: value.Budget.ActiveWallClockMin.From, To: value.Budget.ActiveWallClockMin.To}
	}
	if value.Budget.RetriesPerMovement != nil {
		result.Budget.RetriesPerMovement = &score.BudgetChange{From: value.Budget.RetriesPerMovement.From, To: value.Budget.RetriesPerMovement.To}
	}
	return result, nil
}

func (value submission) record(proposal amendmentProposal) ([]byte, error) {
	fields := []recordField{
		stringField("schema", "partitur/proposal-record+json;v=1"), stringField("proposal_id", string(proposal.ProposalID)),
		stringField("origin", proposal.origin),
		valueField("base_revision", value.BaseRevision), stringField("base_hash", string(value.BaseHash)), rawField("operations", value.OperationsRaw),
		stringField("reason", value.Reason), valueField("requires_decision", proposal.Event.RequiresDecision),
	}
	if proposal.origin == "adapter" {
		fields = append(fields, stringField("attempt_id", string(proposal.AttemptID)), stringField("emitted_id", proposal.Event.ID))
	}
	if value.Evidence != nil {
		fields = append(fields, valueField("evidence", value.Evidence))
	}
	if value.HasClaimedImpact {
		fields = append(fields, rawField("claimed_impact", value.ClaimedImpactRaw))
	}
	return encodeRecord(fields)
}

type recordField struct {
	name string
	raw  []byte
}

func stringField(name, value string) recordField { return valueField(name, value) }
func valueField(name string, value any) recordField {
	raw, _ := json.Marshal(value)
	return recordField{name: name, raw: raw}
}
func rawField(name string, raw json.RawMessage) recordField { return recordField{name: name, raw: raw} }
func encodeRecord(fields []recordField) ([]byte, error) {
	var encoded bytes.Buffer
	encoded.WriteByte('{')
	for index, field := range fields {
		if len(field.raw) == 0 || !json.Valid(field.raw) {
			return nil, fmt.Errorf("proposal record field %q is not JSON", field.name)
		}
		if index != 0 {
			encoded.WriteByte(',')
		}
		name, err := json.Marshal(field.name)
		if err != nil {
			return nil, err
		}
		encoded.Write(name)
		encoded.WriteByte(':')
		encoded.Write(field.raw)
	}
	encoded.WriteByte('}')
	return encoded.Bytes(), nil
}

func rejectionEvent(proposal amendmentProposal, input submission, outcome amendment.Outcome, versions map[string]any) (runstate.Event, error) {
	payload := map[string]any{"proposal_id": string(proposal.ProposalID), "reason": outcome.Reason, "base_revision": input.BaseRevision, "base_hash": string(input.BaseHash), "classifier_version": canonical.AmendmentClassifierVersion, "identity_versions": versions}
	if proposal.origin == "adapter" {
		payload["emitted_id"] = proposal.Event.ID
	}
	if proposal.Event.RequiresDecision {
		payload["decision_id"] = proposal.DecisionID
	}
	if outcome.Condition != "" {
		payload["condition"] = outcome.Condition
	}
	if outcome.Patched != nil {
		payload["typed_delta"] = outcome.Impact.TypedDelta()
		payload["actual_impact"] = outcome.Impact.Value()
	} else {
		hash, err := canonical.Hash(canonical.DomainPatchOperations, input.Operations)
		if err != nil {
			return runstate.Event{}, err
		}
		payload["patch_operations_hash"] = hash
		payload["patch_operations_hash_form"] = "partitur/patch-operations"
		payload["error_location"] = "operations"
	}
	return amendmentEvent(proposal, runstate.EventAmendmentRejected, payload)
}

func routeDescriptor(recordHash string, outcome amendment.Outcome, input submission, versions map[string]any) map[string]any {
	return map[string]any{"proposal_record_hash": recordHash, "reason": outcome.Reason, "decision_type": "amendment", "base_revision": input.BaseRevision, "base_hash": string(input.BaseHash), "classifier_version": canonical.AmendmentClassifierVersion, "typed_delta": outcome.Impact.TypedDelta(), "actual_impact": outcome.Impact.Value(), "identity_versions": versions}
}

func routedEvent(proposal amendmentProposal, descriptor map[string]any) (runstate.Event, error) {
	payload := make(map[string]any, len(descriptor)+5)
	for key, value := range descriptor {
		payload[key] = value
	}
	payload["proposal_id"] = string(proposal.ProposalID)
	if proposal.origin == "adapter" {
		payload["emitted_id"] = proposal.Event.ID
	}
	payload["decision_id"] = proposal.DecisionID
	payload["blocking"] = proposal.Event.RequiresDecision
	return amendmentEvent(proposal, runstate.EventAmendmentRoutedHuman, payload)
}

func decisionRequestedEvent(proposal amendmentProposal, reason string) (runstate.Event, error) {
	payload := map[string]any{"decision_id": proposal.DecisionID, "decision_type": "amendment", "proposal_id": string(proposal.ProposalID), "routed_reason": reason, "blocking": false}
	return amendmentEvent(proposal, runstate.EventDecisionRequested, payload)
}

func amendmentEvent(proposal amendmentProposal, eventType runstate.EventType, payload map[string]any) (runstate.Event, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return runstate.Event{}, err
	}
	return runstate.Event{RunID: proposal.RunID, ScoreRevision: proposal.ScoreRevision, MovementID: proposal.MovementID, PartID: proposal.PartID, AttemptID: proposal.AttemptID, Type: eventType, Payload: encoded}, nil
}

func amendmentIdentityVersions(hasTypedDelta bool) (map[string]any, error) {
	domains := []canonical.Domain{canonical.DomainScore}
	if hasTypedDelta {
		domains = append(domains, canonical.DomainScoreSubtree)
	} else {
		domains = append(domains, canonical.DomainPatchOperations)
	}
	projections := make(map[string]any, len(domains))
	for _, domain := range domains {
		versions, err := canonical.CurrentVersions(domain)
		if err != nil {
			return nil, err
		}
		projections[string(domain)] = versions.Projection
	}
	return map[string]any{"canonical_encoding": canonical.CanonicalEncodingVersion, "projections": projections, "classifier": canonical.AmendmentClassifierVersion}, nil
}

func rawHash(value []byte) string { sum := sha256.Sum256(value); return fmt.Sprintf("sha256:%x", sum) }
