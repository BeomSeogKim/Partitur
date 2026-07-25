// Package protocol defines the Partitur adapter wire contract from DESIGN §4.
package protocol

import "encoding/json"

const (
	ProtocolVersion = 1
	MaxFrameBytes   = 1 << 20
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

type ResolvedDecision struct {
	DecisionID string `json:"decision_id"`
	Answer     string `json:"answer"`
}

type Grants struct {
	PathsRW []string `json:"paths_rw"`
	PathsRO []string `json:"paths_ro"`
	Shell   bool     `json:"shell"`
	Network bool     `json:"network"`
}

type Budget struct {
	ActiveWallClockMin float64 `json:"active_wall_clock_min"`
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
