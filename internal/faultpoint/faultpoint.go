// Package faultpoint defines the neutral fault-injection signals from DESIGN
// Appendix E.
package faultpoint

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"syscall"
)

// EdgeID identifies an ordered-step edge in DESIGN Appendix E.2.
type EdgeID string

const (
	EdgePrepareSnapshotToPlan                            EdgeID = "prepare.snapshot_to_plan"
	EdgePreparePlanToPrepared                            EdgeID = "prepare.plan_to_prepared"
	EdgePreparePreparedToObserved                        EdgeID = "prepare.prepared_to_observed"
	EdgeQuiesceSweptToLeaseMoved                         EdgeID = "quiesce.swept_to_lease_moved"
	EdgeQuiesceLeaseMovedToCommitLock                    EdgeID = "quiesce.lease_moved_to_commit_lock"
	EdgePrepareQuarantinedToAbandoned                    EdgeID = "prepare.quarantined_to_abandoned"
	EdgeCancelSweptToTerminal                            EdgeID = "cancel.swept_to_terminal"
	EdgeCancelSweptToQuarantined                         EdgeID = "cancel.swept_to_quarantined"
	EdgeCancelIntervalStoppedToTerminal                  EdgeID = "cancel.interval_stopped_to_terminal"
	EdgeCancelFenceDecidedToTerminal                     EdgeID = "cancel.fence_decided_to_terminal"
	EdgeCancelTerminalToLeaseRemoved                     EdgeID = "cancel.terminal_to_lease_removed"
	EdgeSupersedeSweptToApproved                         EdgeID = "supersede.swept_to_approved"
	EdgeSupersedeIntervalStoppedToApproved               EdgeID = "supersede.interval_stopped_to_approved"
	EdgeSupersedeFenceDecidedToApproved                  EdgeID = "supersede.fence_decided_to_approved"
	EdgeSupersedeApprovedToLeaseRemoved                  EdgeID = "supersede.approved_to_lease_removed"
	EdgeAuthorityGrantedToLeaseCreated                   EdgeID = "authority.granted_to_lease_created"
	EdgeLaunchAdapterMarkerHeldToIdentity                EdgeID = "launch.adapter.marker_held_to_identity_published"
	EdgeLaunchAdapterIdentityPublishedToRecorded         EdgeID = "launch.adapter.identity_published_to_recorded"
	EdgeLaunchAdapterRecordedToGate                      EdgeID = "launch.adapter.recorded_to_gate"
	EdgeLaunchCriterionMarkerHeldToIdentity              EdgeID = "launch.criterion.marker_held_to_identity_published"
	EdgeLaunchCriterionIdentityToRecorded                EdgeID = "launch.criterion.identity_published_to_recorded"
	EdgeLaunchCriterionRecordedToGate                    EdgeID = "launch.criterion.recorded_to_gate"
	EdgeExecuteAdapterSweptToIntervalStopped             EdgeID = "execute.adapter_swept_to_interval_stopped"
	EdgeExecuteIntervalStoppedToOutcome                  EdgeID = "execute.interval_stopped_to_outcome"
	EdgeChangeSetCapturedToRecorded                      EdgeID = "change_set.captured_to_recorded"
	EdgeCompositionMovementEvidenceToTerminal            EdgeID = "composition.movement_evidence_to_terminal"
	EdgeCompositionCandidateEvidenceToTerminal           EdgeID = "composition.candidate_evidence_to_terminal"
	EdgeLifecycleAttemptCompletedToMovementSucceeded     EdgeID = "lifecycle.attempt_completed_to_movement_succeeded"
	EdgeLifecycleMovementFailedToRunFailed               EdgeID = "lifecycle.movement_failed_to_run_failed"
	EdgeAcceptanceCriterionErrorToFailed                 EdgeID = "acceptance.criterion_error_to_failed"
	EdgeAcceptanceEvaluationCompletedToDecisionRequested EdgeID = "acceptance.evaluation_completed_to_decision_requested"
)

// PointID identifies a BoundaryReached point from DESIGN Appendix E.
type PointID string

const (
	PointPrepareObserved                  PointID = "prepare.observed"
	PointQuiesceSessionsSwept             PointID = "quiesce.sessions_swept"
	PointQuiesceCommitLockHeld            PointID = "quiesce.commit_lock_held"
	PointCancelSessionsSwept              PointID = "cancel.sessions_swept"
	PointCancelSnapshotQuarantined        PointID = "cancel.snapshot_quarantined"
	PointCancelExecutionStopped           PointID = "cancel.execution_stopped"
	PointCancelFenceDecided               PointID = "cancel.fence_decided"
	PointCancelRunCancelled               PointID = "cancel.run_cancelled"
	PointCancelLeaseRemoved               PointID = "cancel.lease_removed"
	PointSupersedeSessionsSwept           PointID = "supersede.sessions_swept"
	PointSupersedeFenceDecided            PointID = "supersede.fence_decided"
	PointLaunchAdapterMarkerHeld          PointID = "launch.adapter.marker_held"
	PointLaunchAdapterGateReleased        PointID = "launch.adapter.gate_released"
	PointLaunchCriterionMarkerHeld        PointID = "launch.criterion.marker_held"
	PointLaunchCriterionGateReleased      PointID = "launch.criterion.gate_released"
	PointAuthorityGranted                 PointID = "authority.granted"
	PointAuthorityLeaseCreated            PointID = "authority.lease_created"
	PointLaunchAdapterIdentityPublished   PointID = "launch.adapter.identity_published"
	PointLaunchAdapterIdentityRecorded    PointID = "launch.adapter.identity_recorded"
	PointLaunchCriterionIdentityPublished PointID = "launch.criterion.identity_published"
	PointLaunchCriterionIdentityRecorded  PointID = "launch.criterion.identity_recorded"
	PointExecuteAdapterSwept              PointID = "execute.adapter_swept"
	PointExecuteIntervalStopped           PointID = "execute.interval_stopped"
	PointExecuteOutcomeRecorded           PointID = "execute.outcome_recorded"
	PointAcceptanceFailureRecorded        PointID = "acceptance.failure_recorded"
	PointLifecycleAttemptCompleted        PointID = "lifecycle.attempt_completed"
	PointLifecycleMovementSucceeded       PointID = "lifecycle.movement_succeeded"
	PointLifecycleMovementFailed          PointID = "lifecycle.movement_failed"
	PointLifecycleRunFailed               PointID = "lifecycle.run_failed"
	PointChangeSetCaptured                PointID = "change_set.captured"
	PointChangeSetRecorded                PointID = "change_set.recorded"
	PointCompositionMovementEvidence      PointID = "composition.movement_evidence_recorded"
	PointCompositionMovementTerminal      PointID = "composition.movement_terminal_recorded"
	PointCompositionCandidateEvidence     PointID = "composition.candidate_evidence_recorded"
	PointCompositionCandidateTerminal     PointID = "composition.candidate_terminal_recorded"
)

// MutationKind identifies a receipt-producing operation from DESIGN Appendix E.1.
type MutationKind string

const (
	JournalAppend     MutationKind = "journal_append"
	FilePublication   MutationKind = "file_publication"
	DurableQuarantine MutationKind = "durable_quarantine"
	DurableRemoval    MutationKind = "durable_removal"
	GitRefCreation    MutationKind = "git_ref_creation"
	LeaseCreation     MutationKind = "lease_creation"
	LeaseMove         MutationKind = "lease_move"
	LeaseRemoval      MutationKind = "lease_removal"
)

// ReceiptAddress names one receipt in its owning protocol step.
type ReceiptAddress string

// Mutation identifies the durable mutation attested by a receipt.
type Mutation struct {
	Kind        MutationKind
	RunID       string
	EventID     string
	EventType   string
	Sequence    uint64
	Timestamp   string
	Path        string
	Source      string
	Destination string
}

// DurabilityReceipt is returned after the mutation's required durability
// boundary and before the next protocol action, as defined by DESIGN Appendix E.1.
type DurabilityReceipt struct {
	Address  ReceiptAddress
	Mutation Mutation
}

// Probe receives ephemeral BoundaryReached points.
type Probe interface {
	Reached(PointID)
}

// Nop is the production no-op probe.
type Nop struct{}

// Reached implements Probe.
func (Nop) Reached(PointID) {}

const (
	probeNotifyFDEnv  = "PARTITUR_FAULTPOINT_NOTIFY_FD"
	probeReleaseFDEnv = "PARTITUR_FAULTPOINT_RELEASE_FD"
)

// ProbeFromEnvironment returns the harness probe carried by inherited file
// descriptors, or Nop when no harness is installed. See DESIGN Appendix E.
func ProbeFromEnvironment() Probe {
	notifyFD, notifyOK := probeFDFromEnvironment(probeNotifyFDEnv)
	releaseFD, releaseOK := probeFDFromEnvironment(probeReleaseFDEnv)
	if !notifyOK || !releaseOK {
		return Nop{}
	}
	return NewPipeProbe(
		os.NewFile(uintptr(notifyFD), probeNotifyFDEnv),
		os.NewFile(uintptr(releaseFD), probeReleaseFDEnv),
	)
}

func probeFDFromEnvironment(name string) (int, bool) {
	value := os.Getenv(name)
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 3 {
		return 0, false
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFIFO {
		return 0, false
	}
	return fd, true
}

// NewPipeProbe reports each reached boundary then waits for the harness.
func NewPipeProbe(notify io.Writer, release io.Reader) Probe {
	if notify == nil || release == nil {
		return Nop{}
	}
	return &pipeProbe{notify: notify, release: release}
}

type pipeProbe struct {
	notify  io.Writer
	release io.Reader
	mu      sync.Mutex
}

func (probe *pipeProbe) Reached(point PointID) {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if _, err := fmt.Fprintln(probe.notify, point, os.Getpid()); err != nil {
		return
	}
	var release [1]byte
	_, _ = io.ReadFull(probe.release, release[:])
}
