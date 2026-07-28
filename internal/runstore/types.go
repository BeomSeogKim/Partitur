// Package runstore implements the durable, repository-scoped run-state store.
//
// Append currently fsyncs every journal append, so the DESIGN Appendix B sync
// column is not modelled. This is the Appendix E.1 / Appendix B.1 tension:
// movement.ready receives stronger durability than its empty sync column
// requires. Supporting the Appendix B.7 log and progress events will require
// revisiting Append's receipt contract.
//
// Append also reloads and reparses the complete journal for every call. This is
// correct for bounded harness runs, but makes repeated appends quadratic in the
// number of events.
package runstore

import (
	"errors"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

type Path string
type Hash = runstate.Hash
type DurabilityReceipt = faultpoint.DurabilityReceipt

var (
	ErrInvalidPath                  = errors.New("invalid runstore path")
	ErrJournalCorrupt               = errors.New("journal_corrupt")
	ErrJournalIdempotencyConflict   = errors.New("journal_idempotency_conflict")
	ErrImmutablePublicationConflict = errors.New("immutable_publication_conflict")
	ErrHashMismatch                 = errors.New("hash_mismatch")
	ErrLeaseConflict                = errors.New("lease_conflict")
	ErrMissingPinnedSnapshot        = errors.New("missing pinned score snapshot")
	ErrMissingResolvedCast          = errors.New("missing resolved cast")
	ErrLeaseOwnerUnverifiable       = errors.New("owner_unverifiable")
	ErrReceiptAddressRequired       = errors.New("receipt address required")
	ErrQuarantineKindRequired       = errors.New("quarantine kind required")
)

// Driver is one acquired execution authority. Its token remains in this
// process and driver.lease; it is never journaled.
type Driver struct {
	store *Store
	runID runstate.RunID
	seed  []runstate.MovementSeed
	lease Lease
}

type Lease struct {
	Epoch uint64
	Token string
	PID   int
	Start runstate.StartIdentity
}

type LeaseIdentity struct {
	Epoch uint64
	Token string
	PID   int
	Start runstate.StartIdentity
}

func (lease Lease) Identity() LeaseIdentity {
	return LeaseIdentity{
		Epoch: lease.Epoch,
		Token: lease.Token,
		PID:   lease.PID,
		Start: lease.Start,
	}
}

// MatchOwner returns the platform inspection tri-state for the lease owner.
func (lease Lease) MatchOwner() procid.MatchResult {
	return procid.Matches(lease.PID, lease.Start)
}

type QuarantineResult struct {
	Source      Path
	Destination Path
	Receipt     DurabilityReceipt
}

type ReplayResult struct {
	State         runstate.State
	RepairReceipt *faultpoint.DurabilityReceipt
}

// ReadReplayResult is the strictly read-only journal projection used by
// observational commands. A torn final line is deliberately not repaired:
// TailTruncated tells the caller that the returned State is only the durable
// prefix before that line.
type ReadReplayResult struct {
	State          runstate.State
	TailTruncated  bool
	TruncatedSeq   uint64
	DiscardedBytes int
}

// ReadJournalResult is the validated, read-only journal prefix. A torn final
// line is deliberately excluded rather than repaired or exposed as an event.
type ReadJournalResult struct {
	Events          []runstate.Event
	TailUnparseable bool
	TruncatedSeq    uint64
	DiscardedBytes  int
}
