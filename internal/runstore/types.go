// Package runstore implements the durable, repository-scoped run-state store.
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
	ErrReceiptAddressRequired       = errors.New("receipt address required")
	ErrQuarantineKindRequired       = errors.New("quarantine kind required")
)

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
