package runstore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

const (
	receiptAuthorityGranted = faultpoint.ReceiptAddress("authority.granted")
	receiptDriverLease      = faultpoint.ReceiptAddress("authority.driver_lease")
	receiptDriverReleased   = faultpoint.ReceiptAddress("authority.driver_released")
)

// RunID returns the run whose authority this handle owns.
func (driver *Driver) RunID() runstate.RunID {
	if driver == nil {
		return ""
	}
	return driver.runID
}

// MatchesLease reports whether identity is the full lease owned by this driver.
func (driver *Driver) MatchesLease(identity LeaseIdentity) bool {
	return driver != nil && leaseMatches(driver.lease, identity)
}

// AcquireDriver records the next authority epoch before durably creating its
// lease. The state lock spans both writes; a crash can still land at the
// Appendix E edge between them.
func (store *Store) AcquireDriver(
	runID runstate.RunID,
	seed []runstate.MovementSeed,
) (*Driver, error) {
	pid := os.Getpid()
	start, err := procid.Read(pid)
	if err != nil {
		return nil, fmt.Errorf("read driver identity: %w", err)
	}
	token, err := newDriverToken()
	if err != nil {
		return nil, err
	}
	var acquired Lease
	err = store.Mutate(runID, "", func(transaction *Txn) error {
		state, err := transaction.project(seed)
		if err != nil {
			return err
		}
		if state.Run == runstate.RunNotStarted || state.Run.Terminal() {
			return ErrLeaseConflict
		}
		if _, present, err := transaction.ReadLease(); err != nil {
			return err
		} else if present {
			return ErrLeaseConflict
		}
		epoch := state.Authority.Epoch + 1
		payload, err := json.Marshal(map[string]any{
			"authority_epoch":      epoch,
			"owner_pid":            pid,
			"owner_start_identity": encodeDriverStart(start),
		})
		if err != nil {
			return err
		}
		event := runstate.Event{
			RunID:         runID,
			ScoreRevision: state.ScoreHead.Revision,
			Type:          runstate.EventAuthorityGranted,
			Payload:       payload,
		}
		if _, err := runstate.Apply(state, event); err != nil {
			return err
		}
		if _, err := transaction.At(receiptAuthorityGranted).Append(event); err != nil {
			return err
		}
		store.probe.Reached(faultpoint.PointAuthorityGranted)
		acquired = Lease{Epoch: epoch, Token: token, PID: pid, Start: start}
		if _, err := transaction.At(receiptDriverLease).CreateLease(true, acquired); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	store.probe.Reached(faultpoint.PointAuthorityLeaseCreated)
	return &Driver{
		store: store,
		runID: runID,
		seed:  append([]runstate.MovementSeed(nil), seed...),
		lease: acquired,
	}, nil
}

// Append applies and durably appends one driver-authorized event after
// rechecking the full lease and journal epoch under the state lock.
func (driver *Driver) Append(
	event runstate.Event,
	address faultpoint.ReceiptAddress,
) (faultpoint.DurabilityReceipt, error) {
	var receipt faultpoint.DurabilityReceipt
	err := driver.Mutate(func(transaction *Txn, state runstate.State) error {
		if _, err := runstate.Apply(state, event); err != nil {
			return err
		}
		var err error
		receipt, err = transaction.At(address).Append(event)
		return err
	})
	return receipt, err
}

// Mutate runs one driver-authorized mutation with the current projection.
func (driver *Driver) Mutate(
	mutation func(*Txn, runstate.State) error,
) error {
	if driver == nil || driver.store == nil || mutation == nil {
		return errors.New("runstore: incomplete driver mutation")
	}
	return driver.store.Mutate(driver.runID, "", func(transaction *Txn) error {
		state, err := transaction.project(driver.seed)
		if err != nil {
			return err
		}
		if state.Run.Terminal() ||
			state.Authority.Epoch != driver.lease.Epoch ||
			state.Authority.Owner == nil ||
			state.Authority.Owner.PID != driver.lease.PID ||
			!startIdentitiesEqual(state.Authority.Owner.Start, driver.lease.Start) {
			return ErrLeaseConflict
		}
		lease, present, err := transaction.ReadLease()
		if err != nil {
			return err
		}
		if !present || !leaseMatches(lease, driver.lease.Identity()) {
			return ErrLeaseConflict
		}
		match := lease.MatchOwner()
		if match.Status == procid.Unverifiable {
			return fmt.Errorf("%w: %v", ErrLeaseOwnerUnverifiable, match.Err)
		}
		if match.Status != procid.MatchingAndLive {
			return ErrLeaseConflict
		}
		return mutation(transaction, state)
	})
}

// State returns the current journal projection.
func (driver *Driver) State() (runstate.State, error) {
	if driver == nil || driver.store == nil {
		return runstate.State{}, errors.New("runstore: nil driver")
	}
	result, err := driver.store.Replay(driver.runID, driver.seed, "driver.replay.repair")
	return result.State, err
}

// Release removes the matching lease after this driver has stopped mutating,
// whether the run terminalized or this invocation was interrupted.
func (driver *Driver) Release() error {
	if driver == nil || driver.store == nil {
		return errors.New("runstore: nil driver")
	}
	return driver.store.Mutate(driver.runID, "", func(transaction *Txn) error {
		_, err := transaction.At(receiptDriverReleased).
			CompareRemoveLease(driver.lease.Identity())
		return err
	})
}

func newDriverToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("allocate driver token: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func encodeDriverStart(identity runstate.StartIdentity) map[string]any {
	switch identity := identity.(type) {
	case runstate.LinuxStartIdentity:
		return map[string]any{
			"platform":    "linux",
			"boot_id":     identity.BootID,
			"start_ticks": identity.StartTicks,
		}
	case runstate.DarwinStartIdentity:
		return map[string]any{
			"platform":     "darwin",
			"start_tvsec":  identity.StartTVSec,
			"start_tvusec": identity.StartTVUsec,
		}
	default:
		panic(fmt.Sprintf("unsupported driver start identity %T", identity))
	}
}
