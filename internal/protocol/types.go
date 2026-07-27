// Package protocol defines the Partitur adapter wire contract from DESIGN §4.
package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

const (
	MinimumProtocolVersion = 2
	ProtocolVersion        = 2
	MaxFrameBytes          = 1 << 20
)

type Outcome string

const (
	OutcomeCompleted    Outcome = "completed"
	OutcomeFailed       Outcome = "failed"
	OutcomeCancelled    Outcome = "cancelled"
	OutcomeWaitingHuman Outcome = "waiting_human"
)

type Model struct {
	ID      string   `json:"id"`
	Aliases []string `json:"aliases,omitempty"`
}

type Capabilities struct {
	RepoRead          bool    `json:"repo_read"`
	RepoWrite         bool    `json:"repo_write"`
	Shell             bool    `json:"shell"`
	Network           bool    `json:"network"`
	ResumableSessions bool    `json:"resumable_sessions"`
	Models            []Model `json:"models"`
}

type Enforcement struct {
	PathGrants    bool `json:"path_grants"`
	ReadOnly      bool `json:"read_only"`
	NetworkGrants bool `json:"network_grants"`
	ShellGrants   bool `json:"shell_grants"`
	ReadGrants    bool `json:"read_grants"`
}

type AdapterIdentity struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type ProbeResult struct {
	Protocol     int             `json:"protocol"`
	Adapter      AdapterIdentity `json:"adapter"`
	Capabilities Capabilities    `json:"capabilities"`
	Enforcement  Enforcement     `json:"enforcement"`
	Features     []string        `json:"features,omitempty"`
}

type OutputSpec struct {
	ArtifactID string `json:"artifact_id"`
	Kind       string `json:"kind"`
}

type Brief struct {
	Goal                    string          `json:"goal"`
	Context                 string          `json:"context,omitempty"`
	Instruction             string          `json:"instruction"`
	VerificationExpectation json.RawMessage `json:"verification_expectation,omitempty"`
	Acceptance              json.RawMessage `json:"acceptance,omitempty"`
	GlobalInvariants        json.RawMessage `json:"global_invariants,omitempty"`
	Outputs                 []OutputSpec    `json:"outputs"`
}

type ArtifactRef struct {
	ArtifactID string `json:"artifact_id"`
	Kind       string `json:"kind"`
	Path       string `json:"path"`
	Hash       string `json:"hash"`
}

type Feedback struct {
	PreviousAttemptID string `json:"previous_attempt_id"`
	Kind              string `json:"kind"`
	ArtifactID        string `json:"artifact_id"`
}

type ResolvedDecisionKind string

const (
	ResolvedDecisionAnswer            ResolvedDecisionKind = "answer"
	ResolvedDecisionAmendmentRejected ResolvedDecisionKind = "amendment_rejected"
)

type ResolvedDecision struct {
	DecisionID string               `json:"decision_id"`
	Kind       ResolvedDecisionKind `json:"kind"`
	Answer     string               `json:"answer,omitempty"`
	Reason     string               `json:"reason,omitempty"`
}

func (decision ResolvedDecision) MarshalJSON() ([]byte, error) {
	switch decision.Kind {
	case ResolvedDecisionAnswer:
		return json.Marshal(struct {
			DecisionID string               `json:"decision_id"`
			Kind       ResolvedDecisionKind `json:"kind"`
			Answer     string               `json:"answer"`
		}{
			DecisionID: decision.DecisionID,
			Kind:       decision.Kind,
			Answer:     decision.Answer,
		})
	case ResolvedDecisionAmendmentRejected:
		return json.Marshal(struct {
			DecisionID string               `json:"decision_id"`
			Kind       ResolvedDecisionKind `json:"kind"`
			Reason     string               `json:"reason"`
		}{
			DecisionID: decision.DecisionID,
			Kind:       decision.Kind,
			Reason:     decision.Reason,
		})
	default:
		return nil, fmt.Errorf("unknown resolved decision kind %q", decision.Kind)
	}
}

type Grants struct {
	PathsRW []string `json:"paths_rw"`
	PathsRO []string `json:"paths_ro"`
	Shell   bool     `json:"shell"`
	Network bool     `json:"network"`
}

type Budget struct {
	RemainingMS int64 `json:"remaining_ms"`
}

// UnmarshalJSON keeps the wire duration in the non-negative I-JSON safe
// integer range and rejects negative-zero spellings before they collapse to 0.
func (budget *Budget) UnmarshalJSON(data []byte) error {
	var raw struct {
		RemainingMS json.RawMessage `json:"remaining_ms"`
	}
	if err := DecodeStrict(data, &raw); err != nil {
		return err
	}
	valueBytes := bytes.TrimSpace(raw.RemainingMS)
	if len(valueBytes) == 0 {
		return errors.New("remaining_ms is required")
	}
	if bytes.Equal(valueBytes, []byte("-0")) {
		return errors.New("remaining_ms is negative zero")
	}
	value, err := strconv.ParseInt(string(valueBytes), 10, 64)
	if err != nil {
		return fmt.Errorf("remaining_ms is not an integer: %w", err)
	}
	if value < 0 || value > 1<<53-1 {
		return errors.New("remaining_ms is outside the safe integer range")
	}
	budget.RemainingMS = value
	return nil
}

type ExecuteRequest struct {
	RunID             string                     `json:"run_id"`
	MovementID        string                     `json:"movement_id"`
	AttemptID         string                     `json:"attempt_id"`
	ScoreRevision     int                        `json:"score_revision"`
	Model             string                     `json:"model"`
	Brief             Brief                      `json:"brief"`
	Inputs            []ArtifactRef              `json:"inputs"`
	Feedback          []Feedback                 `json:"feedback"`
	ResolvedDecisions []ResolvedDecision         `json:"resolved_decisions"`
	Workdir           string                     `json:"workdir"`
	OutputDir         string                     `json:"output_dir"`
	Grants            Grants                     `json:"grants"`
	Budget            Budget                     `json:"budget"`
	SessionHint       json.RawMessage            `json:"session_hint,omitempty"`
	Extensions        map[string]json.RawMessage `json:"extensions,omitempty"`
}

type FailureKind string

const (
	FailureAdapterUnavailable FailureKind = "adapter_unavailable"
	FailureModelUnavailable   FailureKind = "model_unavailable"
	FailureProviderTimeout    FailureKind = "provider_timeout"
	FailureRateLimited        FailureKind = "rate_limited"
	FailureAuthentication     FailureKind = "authentication"
	FailureProtocolError      FailureKind = "protocol_error"
	FailureGrantDenied        FailureKind = "grant_denied"
	FailureTaskFailed         FailureKind = "task_failed"
)

type Failure struct {
	Kind   FailureKind `json:"kind"`
	Detail string      `json:"detail,omitempty"`
}

type ExecuteResult struct {
	Outcome            Outcome         `json:"outcome"`
	Failure            *Failure        `json:"failure,omitempty"`
	PendingDecisionIDs []string        `json:"pending_decision_ids,omitempty"`
	SessionHint        json.RawMessage `json:"session_hint,omitempty"`
	Detail             string          `json:"detail,omitempty"`
}

type CancelRequest struct {
	AttemptID string `json:"attempt_id"`
}

type EventType string

const (
	EventLog      EventType = "log"
	EventProgress EventType = "progress"
	EventArtifact EventType = "artifact"
	EventProposal EventType = "proposal"
	EventQuestion EventType = "question"
)

type LogEvent struct {
	Type    EventType `json:"type"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

type ProgressEvent struct {
	Type    EventType `json:"type"`
	Message string    `json:"message"`
}

type ArtifactEvent struct {
	Type       EventType `json:"type"`
	ArtifactID string    `json:"artifact_id"`
	Path       string    `json:"path"`
}

type ProposalEvent struct {
	Type             EventType       `json:"type"`
	ID               string          `json:"id"`
	Amendment        json.RawMessage `json:"amendment"`
	RequiresDecision bool            `json:"requires_decision"`
}

type QuestionEvent struct {
	Type     EventType `json:"type"`
	ID       string    `json:"id"`
	Question string    `json:"question"`
}
