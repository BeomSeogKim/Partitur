package runstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

// ReadLease reads the current driver lease.
func (transaction *Txn) ReadLease() (Lease, bool, error) {
	return readLease(transaction.store.fs, transaction.runRoot())
}

// ReadLease observes the current driver lease without taking the state lock or
// creating any run-store paths. It is suitable for recovery observations.
func (store *Store) ReadLease(runID runstate.RunID) (Lease, bool, error) {
	if err := validateRunID(runID); err != nil {
		return Lease{}, false, err
	}
	return readLease(store.fs, filepath.Join(store.root, ".partitur", "runs", string(runID)))
}

// WakeLeaseOwner sends the optional control wake only after re-reading the
// exact recorded lease owner. Journal polling remains authoritative.
func (store *Store) WakeLeaseOwner(runID runstate.RunID) {
	lease, present, err := store.ReadLease(runID)
	if err != nil || !present || lease.MatchOwner().Status != procid.MatchingAndLive {
		return
	}
	procid.Wake(lease.PID, lease.Start)
}

type ResumeLeaseStatus uint8

const (
	ResumeLeaseUnverifiable ResumeLeaseStatus = iota
	ResumeLeaseAvailable
	ResumeLeaseLiveOwner
	ResumeLeaseProjectionMismatch
)

// ResumeLeaseSnapshot is the current lifecycle and lease classification from
// one projected state-lock observation.
type ResumeLeaseSnapshot struct {
	Projection  DecisionResolution
	LeaseStatus ResumeLeaseStatus
}

// ClassifyCurrentResumeLease projects lifecycle and authority and reads the
// lease under one state lock so advisory output cannot combine observations
// from different lifecycle or authority epochs.
func (store *Store) ClassifyCurrentResumeLease(runID runstate.RunID) (ResumeLeaseSnapshot, error) {
	var result ResumeLeaseSnapshot
	err := store.MutateProjected(runID, func(transaction *Txn, state runstate.State) error {
		lease, present, err := transaction.ReadLease()
		result = ResumeLeaseSnapshot{
			Projection:  decisionResolution(state),
			LeaseStatus: classifyResumeLease(lease, present, err, state.Authority),
		}
		return nil
	})
	return result, err
}

func classifyResumeLease(lease Lease, present bool, err error, authority runstate.Authority) ResumeLeaseStatus {
	if err != nil {
		return ResumeLeaseUnverifiable
	}
	if !present {
		return ResumeLeaseAvailable
	}
	if lease.Epoch != authority.Epoch || authority.Owner == nil {
		return ResumeLeaseProjectionMismatch
	}
	switch lease.MatchOwner().Status {
	case procid.MatchingAndLive:
		return ResumeLeaseLiveOwner
	case procid.GoneOrReused:
		return ResumeLeaseAvailable
	default:
		return ResumeLeaseUnverifiable
	}
}

// TerminateLeaseOwner applies §6 step 6 to exactly one still-matching lease
// owner. It takes no state lock, so the driver can release its lock while
// responding to cancellation.
func (store *Store) TerminateLeaseOwner(ctx context.Context, runID runstate.RunID, expected LeaseIdentity, grace time.Duration) error {
	lease, present, err := store.ReadLease(runID)
	if err != nil {
		return err
	}
	if !present || !leaseMatches(lease, expected) {
		return ErrLeaseConflict
	}
	return procid.Terminate(ctx, lease.PID, lease.Start, grace)
}

func readLease(fileSystem fsOperations, runRoot string) (Lease, bool, error) {
	path := filepath.Join(runRoot, "driver.lease")
	contents, err := fileSystem.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Lease{}, false, nil
	}
	if err != nil {
		return Lease{}, false, fmt.Errorf("read driver lease: %w", err)
	}
	lease, err := decodeLease(contents)
	if err != nil {
		return Lease{}, false, fmt.Errorf("decode driver lease: %w", err)
	}
	return lease, true, nil
}

// CreateLease durably creates driver.lease without overwriting another lease.
func (transaction *Txn) CreateLease(expectedAbsent bool, lease Lease) (DurabilityReceipt, error) {
	if err := transaction.requireReceiptAddress(); err != nil {
		return DurabilityReceipt{}, err
	}
	contents, err := encodeLease(lease)
	if err != nil {
		return DurabilityReceipt{}, err
	}
	path := filepath.Join(transaction.runRoot(), "driver.lease")
	parent := filepath.Dir(path)
	if err := transaction.store.fs.MkdirAll(parent, 0o700); err != nil {
		return DurabilityReceipt{}, fmt.Errorf("create lease directory: %w", err)
	}
	existing, err := transaction.store.fs.ReadFile(path)
	if err == nil {
		if !bytes.Equal(existing, contents) {
			return DurabilityReceipt{}, ErrLeaseConflict
		}
		if err := transaction.store.fs.SyncFile(path); err != nil {
			return DurabilityReceipt{}, fmt.Errorf("sync existing lease: %w", err)
		}
		if err := transaction.store.fs.SyncDir(parent); err != nil {
			return DurabilityReceipt{}, fmt.Errorf("sync lease directory: %w", err)
		}
		receipt := transaction.newReceipt(faultpoint.LeaseCreation)
		receipt.Mutation.Path = relativeToRoot(transaction.store.root, path)
		return transaction.observeReceipt(receipt), nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return DurabilityReceipt{}, fmt.Errorf("read existing lease: %w", err)
	}
	if !expectedAbsent {
		return DurabilityReceipt{}, ErrLeaseConflict
	}
	temporary, err := transaction.store.fs.WriteTemp(parent, ".driver.lease.tmp-*", contents, 0o600)
	if err != nil {
		return DurabilityReceipt{}, fmt.Errorf("write lease temporary: %w", err)
	}
	cleanup := func() { _ = transaction.store.fs.Remove(temporary) }
	if err := transaction.store.fs.SyncFile(temporary); err != nil {
		cleanup()
		return DurabilityReceipt{}, fmt.Errorf("sync lease temporary: %w", err)
	}
	if err := transaction.store.fs.Rename(temporary, path); err != nil {
		cleanup()
		return DurabilityReceipt{}, fmt.Errorf("rename lease: %w", err)
	}
	if err := transaction.store.fs.SyncDir(parent); err != nil {
		return DurabilityReceipt{}, fmt.Errorf("sync lease directory: %w", err)
	}
	receipt := transaction.newReceipt(faultpoint.LeaseCreation)
	receipt.Mutation.Path = relativeToRoot(transaction.store.root, path)
	return transaction.observeReceipt(receipt), nil
}

// CompareMoveLease conditionally moves driver.lease to destination.
func (transaction *Txn) CompareMoveLease(
	expected LeaseIdentity,
	destination Path,
) (DurabilityReceipt, error) {
	if err := transaction.requireReceiptAddress(); err != nil {
		return DurabilityReceipt{}, err
	}
	lease, present, err := transaction.ReadLease()
	if err != nil {
		return DurabilityReceipt{}, err
	}
	if !present || !leaseMatches(lease, expected) {
		return DurabilityReceipt{}, ErrLeaseConflict
	}
	sourcePath := filepath.Join(transaction.runRoot(), "driver.lease")
	destinationPath, err := transaction.resolve(destination)
	if err != nil {
		return DurabilityReceipt{}, err
	}
	if filepath.Dir(destinationPath) != transaction.runRoot() {
		return DurabilityReceipt{}, fmt.Errorf("%w: lease destination must be run-root local", ErrInvalidPath)
	}
	if present, err := exists(transaction.store.fs, destinationPath); err != nil {
		return DurabilityReceipt{}, err
	} else if present {
		return DurabilityReceipt{}, ErrLeaseConflict
	}
	if err := transaction.store.fs.Rename(sourcePath, destinationPath); err != nil {
		return DurabilityReceipt{}, fmt.Errorf("move lease: %w", err)
	}
	if err := transaction.store.fs.SyncDir(transaction.runRoot()); err != nil {
		return DurabilityReceipt{}, fmt.Errorf("sync lease directory: %w", err)
	}
	receipt := transaction.newReceipt(faultpoint.LeaseMove)
	receipt.Mutation.Source = relativeToRoot(transaction.store.root, sourcePath)
	receipt.Mutation.Destination = relativeToRoot(transaction.store.root, destinationPath)
	return transaction.observeReceipt(receipt), nil
}

// CompareRemoveLease conditionally removes driver.lease.
func (transaction *Txn) CompareRemoveLease(expected LeaseIdentity) (DurabilityReceipt, error) {
	if err := transaction.requireReceiptAddress(); err != nil {
		return DurabilityReceipt{}, err
	}
	lease, present, err := transaction.ReadLease()
	if err != nil {
		return DurabilityReceipt{}, err
	}
	if !present || !leaseMatches(lease, expected) {
		return DurabilityReceipt{}, ErrLeaseConflict
	}
	path := filepath.Join(transaction.runRoot(), "driver.lease")
	if err := transaction.store.fs.Remove(path); err != nil {
		return DurabilityReceipt{}, fmt.Errorf("remove lease: %w", err)
	}
	if err := transaction.store.fs.SyncDir(transaction.runRoot()); err != nil {
		return DurabilityReceipt{}, fmt.Errorf("sync lease directory: %w", err)
	}
	receipt := transaction.newReceipt(faultpoint.LeaseRemoval)
	receipt.Mutation.Path = relativeToRoot(transaction.store.root, path)
	return transaction.observeReceipt(receipt), nil
}

func encodeLease(lease Lease) ([]byte, error) {
	start, err := encodeStartIdentity(lease.Start)
	if err != nil {
		return nil, err
	}
	if lease.Epoch == 0 || lease.Token == "" || lease.PID <= 0 {
		return nil, errors.New("invalid lease identity")
	}
	contents, err := json.Marshal(map[string]any{
		"epoch":          lease.Epoch,
		"token":          lease.Token,
		"pid":            lease.PID,
		"start_identity": start,
	})
	if err != nil {
		return nil, err
	}
	return append(contents, '\n'), nil
}

func decodeLease(contents []byte) (Lease, error) {
	value, err := canonical.ParseJSON(contents)
	if err != nil {
		return Lease{}, err
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != 4 {
		return Lease{}, errors.New("lease must contain exactly four fields")
	}
	epoch, ok := exactUint(object["epoch"])
	if !ok || epoch == 0 {
		return Lease{}, errors.New("invalid lease epoch")
	}
	token, ok := object["token"].(string)
	if !ok || token == "" {
		return Lease{}, errors.New("invalid lease token")
	}
	pid, ok := exactUint(object["pid"])
	if !ok || pid == 0 || pid > uint64(maxInt()) {
		return Lease{}, errors.New("invalid lease pid")
	}
	startObject, ok := object["start_identity"].(map[string]any)
	if !ok {
		return Lease{}, errors.New("invalid lease start identity")
	}
	start, err := decodeStartIdentity(startObject)
	if err != nil {
		return Lease{}, err
	}
	return Lease{Epoch: epoch, Token: token, PID: int(pid), Start: start}, nil
}

func encodeStartIdentity(identity runstate.StartIdentity) (map[string]any, error) {
	switch identity := identity.(type) {
	case runstate.LinuxStartIdentity:
		if identity.BootID == "" || identity.StartTicks == "" {
			return nil, errors.New("invalid Linux start identity")
		}
		return map[string]any{
			"platform":    "linux",
			"boot_id":     identity.BootID,
			"start_ticks": identity.StartTicks,
		}, nil
	case runstate.DarwinStartIdentity:
		return map[string]any{
			"platform":     "darwin",
			"start_tvsec":  identity.StartTVSec,
			"start_tvusec": identity.StartTVUsec,
		}, nil
	default:
		return nil, errors.New("invalid start identity variant")
	}
}

func decodeStartIdentity(value map[string]any) (runstate.StartIdentity, error) {
	platform, ok := value["platform"].(string)
	if !ok {
		return nil, errors.New("start identity platform is absent")
	}
	switch platform {
	case "linux":
		if len(value) != 3 {
			return nil, errors.New("invalid Linux start identity fields")
		}
		bootID, bootOK := value["boot_id"].(string)
		startTicks, ticksOK := value["start_ticks"].(string)
		if !bootOK || !ticksOK || bootID == "" || startTicks == "" {
			return nil, errors.New("invalid Linux start identity")
		}
		return runstate.LinuxStartIdentity{BootID: bootID, StartTicks: startTicks}, nil
	case "darwin":
		if len(value) != 3 {
			return nil, errors.New("invalid Darwin start identity fields")
		}
		seconds, secondsOK := exactUint(value["start_tvsec"])
		useconds, usecondsOK := exactUint(value["start_tvusec"])
		if !secondsOK || !usecondsOK {
			return nil, errors.New("invalid Darwin start identity")
		}
		return runstate.DarwinStartIdentity{StartTVSec: seconds, StartTVUsec: useconds}, nil
	default:
		return nil, fmt.Errorf("unsupported start identity platform %q", platform)
	}
}

func leaseMatches(lease Lease, expected LeaseIdentity) bool {
	return lease.Epoch == expected.Epoch &&
		lease.Token == expected.Token &&
		lease.PID == expected.PID &&
		startIdentitiesEqual(lease.Start, expected.Start)
}

func startIdentitiesEqual(left, right runstate.StartIdentity) bool {
	switch left := left.(type) {
	case runstate.LinuxStartIdentity:
		right, ok := right.(runstate.LinuxStartIdentity)
		return ok && left == right
	case runstate.DarwinStartIdentity:
		right, ok := right.(runstate.DarwinStartIdentity)
		return ok && left == right
	default:
		return false
	}
}

func exactUint(value any) (uint64, bool) {
	number, ok := value.(float64)
	if !ok || number < 0 || number != float64(uint64(number)) {
		return 0, false
	}
	return uint64(number), true
}

func maxInt() int {
	return int(^uint(0) >> 1)
}
