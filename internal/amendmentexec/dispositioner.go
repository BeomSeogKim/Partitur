// Package amendmentexec realizes the durable rejection and human-routing
// consequences of a single adapter proposal. Auto approval deliberately
// remains outside this package until the §6 prepare transaction exists.
package amendmentexec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/BeomSeogKim/Partitur/internal/amendment"
	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/driver"
	"github.com/BeomSeogKim/Partitur/internal/executiondep"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

var ErrAutoApprovalUnimplemented = errors.New("amendment auto approval is not implemented: barrier, prepare, quiesce, and commit remain required")

// ProposalDispositioner is the one driver callback that evaluates adapter
// proposals. It is stateless so later origins can use the same consequence
// boundary without inventing another approval-policy implementation.
type ProposalDispositioner struct{}

func New() ProposalDispositioner { return ProposalDispositioner{} }

func (ProposalDispositioner) PrepareAdapterProposal(ctx context.Context, proposal driver.AdapterProposal) (driver.AdapterProposalDisposition, error) {
	if err := ctx.Err(); err != nil {
		return driver.AdapterProposalDisposition{}, err
	}
	if proposal.Store == nil || proposal.Authority == nil {
		return driver.AdapterProposalDisposition{}, errors.New("amendment dispositioner requires store and authority")
	}
	submission, err := decodeSubmission(proposal.Event)
	if err != nil {
		return driver.AdapterProposalDisposition{}, err
	}

	var disposition driver.AdapterProposalDisposition
	err = proposal.Authority.Mutate(func(transaction *runstore.Txn, state runstate.State) error {
		proposal.ScoreRevision = state.ScoreHead.Revision
		attempts, err := executiondep.Collect(proposal.Store, proposal.RunID)
		if err != nil {
			return fmt.Errorf("collect execution dependencies: %w", err)
		}
		base, err := proposal.Store.LoadScoreSnapshot(proposal.RunID, state.ScoreHead.Revision)
		if err != nil {
			return fmt.Errorf("load amendment base snapshot: %w", err)
		}
		outcome, err := amendment.Evaluate(amendment.Input{
			State: state, Base: base, BaseRevision: submission.BaseRevision, BaseHash: submission.BaseHash,
			Operations: submission.Operations, ClaimedImpact: submission.ClaimedImpact,
			HasClaimedImpact: submission.HasClaimedImpact, Attempts: attempts,
			RequiresDecision: proposal.Event.RequiresDecision,
		})
		if err != nil {
			return err
		}
		versions, err := amendmentIdentityVersions(outcome.Patched != nil)
		if err != nil {
			return err
		}
		switch outcome.Kind {
		case amendment.Rejected:
			event, err := rejectionEvent(proposal, submission, outcome, versions)
			if err != nil {
				return err
			}
			if _, err := transaction.At("amendment.rejected").Append(event); err != nil {
				return err
			}
			return nil
		case amendment.Routed:
			record, err := submission.record(proposal)
			if err != nil {
				return err
			}
			hash := rawHash(record)
			if _, err := transaction.At("proposal.record.published").PublishImmutable(
				runstore.Path("proposals/"+string(proposal.ProposalID)+".json"), record, runstore.Hash(hash),
			); err != nil {
				return err
			}
			descriptor := routeDescriptor(hash, outcome, submission, versions)
			routeEvent, err := routedEvent(proposal, descriptor)
			if err != nil {
				return err
			}
			disposition = driver.AdapterProposalDisposition{
				RouteDescriptor: descriptor,
				AppendRoute: func(context.Context) error {
					_, err := proposal.Authority.Append(routeEvent, faultpoint.ReceiptAddress("amendment.routed_human"))
					return err
				},
			}
			return nil
		case amendment.Approved:
			return ErrAutoApprovalUnimplemented
		default:
			return fmt.Errorf("unknown amendment outcome %q", outcome.Kind)
		}
	})
	return disposition, err
}

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

func (value submission) record(proposal driver.AdapterProposal) ([]byte, error) {
	fields := []recordField{
		stringField("schema", "partitur/proposal-record+json;v=1"), stringField("proposal_id", string(proposal.ProposalID)),
		stringField("origin", "adapter"), stringField("attempt_id", string(proposal.AttemptID)), stringField("emitted_id", proposal.Event.ID),
		valueField("base_revision", value.BaseRevision), stringField("base_hash", string(value.BaseHash)), rawField("operations", value.OperationsRaw),
		stringField("reason", value.Reason), valueField("requires_decision", proposal.Event.RequiresDecision),
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

func rejectionEvent(proposal driver.AdapterProposal, input submission, outcome amendment.Outcome, versions map[string]any) (runstate.Event, error) {
	payload := map[string]any{"proposal_id": string(proposal.ProposalID), "emitted_id": proposal.Event.ID, "reason": outcome.Reason, "base_revision": input.BaseRevision, "base_hash": string(input.BaseHash), "classifier_version": canonical.AmendmentClassifierVersion, "identity_versions": versions}
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

func routedEvent(proposal driver.AdapterProposal, descriptor map[string]any) (runstate.Event, error) {
	payload := make(map[string]any, len(descriptor)+5)
	for key, value := range descriptor {
		payload[key] = value
	}
	payload["proposal_id"] = string(proposal.ProposalID)
	payload["emitted_id"] = proposal.Event.ID
	payload["decision_id"] = proposal.DecisionID
	payload["blocking"] = proposal.Event.RequiresDecision
	return amendmentEvent(proposal, runstate.EventAmendmentRoutedHuman, payload)
}

func amendmentEvent(proposal driver.AdapterProposal, eventType runstate.EventType, payload map[string]any) (runstate.Event, error) {
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
