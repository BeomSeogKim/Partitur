// Package faultpoint defines the neutral fault-injection signals from DESIGN
// Appendix E.
package faultpoint

// EdgeID identifies an ordered-step edge in DESIGN Appendix E.2.
type EdgeID string

const (
	EdgePrepareSnapshotToPlan                    EdgeID = "prepare.snapshot_to_plan"
	EdgePreparePlanToPrepared                    EdgeID = "prepare.plan_to_prepared"
	EdgePreparePreparedToObserved                EdgeID = "prepare.prepared_to_observed"
	EdgeQuiesceSweptToLeaseMoved                 EdgeID = "quiesce.swept_to_lease_moved"
	EdgeQuiesceLeaseMovedToCommitLock            EdgeID = "quiesce.lease_moved_to_commit_lock"
	EdgePrepareQuarantinedToAbandoned            EdgeID = "prepare.quarantined_to_abandoned"
	EdgeCancelSweptToTerminal                    EdgeID = "cancel.swept_to_terminal"
	EdgeCancelSweptToQuarantined                 EdgeID = "cancel.swept_to_quarantined"
	EdgeCancelIntervalStoppedToTerminal          EdgeID = "cancel.interval_stopped_to_terminal"
	EdgeCancelFenceDecidedToTerminal             EdgeID = "cancel.fence_decided_to_terminal"
	EdgeCancelTerminalToLeaseRemoved             EdgeID = "cancel.terminal_to_lease_removed"
	EdgeSupersedeSweptToApproved                 EdgeID = "supersede.swept_to_approved"
	EdgeSupersedeIntervalStoppedToApproved       EdgeID = "supersede.interval_stopped_to_approved"
	EdgeSupersedeFenceDecidedToApproved          EdgeID = "supersede.fence_decided_to_approved"
	EdgeSupersedeApprovedToLeaseRemoved          EdgeID = "supersede.approved_to_lease_removed"
	EdgeAuthorityGrantedToLeaseCreated           EdgeID = "authority.granted_to_lease_created"
	EdgeLaunchAdapterMarkerHeldToIdentity        EdgeID = "launch.adapter.marker_held_to_identity_published"
	EdgeLaunchAdapterIdentityPublishedToRecorded EdgeID = "launch.adapter.identity_published_to_recorded"
	EdgeLaunchAdapterRecordedToGate              EdgeID = "launch.adapter.recorded_to_gate"
	EdgeLaunchCriterionMarkerHeldToIdentity      EdgeID = "launch.criterion.marker_held_to_identity_published"
	EdgeLaunchCriterionIdentityToRecorded        EdgeID = "launch.criterion.identity_published_to_recorded"
	EdgeLaunchCriterionRecordedToGate            EdgeID = "launch.criterion.recorded_to_gate"
)

// PointID identifies a BoundaryReached point from DESIGN Appendix E.
type PointID string

const (
	PointPrepareObserved             PointID = "prepare.observed"
	PointQuiesceSessionsSwept        PointID = "quiesce.sessions_swept"
	PointQuiesceCommitLockHeld       PointID = "quiesce.commit_lock_held"
	PointCancelSessionsSwept         PointID = "cancel.sessions_swept"
	PointCancelFenceDecided          PointID = "cancel.fence_decided"
	PointSupersedeSessionsSwept      PointID = "supersede.sessions_swept"
	PointSupersedeFenceDecided       PointID = "supersede.fence_decided"
	PointLaunchAdapterMarkerHeld     PointID = "launch.adapter.marker_held"
	PointLaunchAdapterGateReleased   PointID = "launch.adapter.gate_released"
	PointLaunchCriterionMarkerHeld   PointID = "launch.criterion.marker_held"
	PointLaunchCriterionGateReleased PointID = "launch.criterion.gate_released"
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

// GitRefCreation has no producer in this implementation; see DESIGN Appendix E.1.

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
