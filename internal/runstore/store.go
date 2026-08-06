package runstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

var (
	runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	kindPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
)

type Store struct {
	root                  string
	probe                 faultpoint.Probe
	receiptObserver       ReceiptObserver
	fs                    fsOperations
	sweepSessions         func(context.Context, runstate.State) error
	quiesceReceiptCadence time.Duration
}

// New constructs a store rooted at repositoryRoot.
func New(repositoryRoot string, probe faultpoint.Probe, observers ...ReceiptObserver) (*Store, error) {
	if probe == nil {
		return nil, errors.New("runstore: nil probe")
	}
	if len(observers) > 1 {
		return nil, errors.New("runstore: multiple receipt observers")
	}
	receiptObserver := ReceiptObserver(receiptObserverFunc(func(DurabilityReceipt) {}))
	if len(observers) == 1 && observers[0] != nil {
		receiptObserver = observers[0]
	}
	root, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect repository root: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("repository root is not a directory")
	}
	return &Store{
		root: root, probe: probe, receiptObserver: receiptObserver, fs: realFS{},
		sweepSessions: sweepRecordedSessions, quiesceReceiptCadence: quiesceReceiptCadence,
	}, nil
}

type Txn struct {
	store          *Store
	runID          runstate.RunID
	address        faultpoint.ReceiptAddress
	quarantineKind string
}

// At returns a transaction view scoped to one caller-owned receipt address.
func (transaction *Txn) At(address faultpoint.ReceiptAddress) *Txn {
	scoped := *transaction
	scoped.address = address
	return &scoped
}

// QuarantineAs returns a transaction view with the caller-owned quarantine kind.
func (transaction *Txn) QuarantineAs(kind string) *Txn {
	scoped := *transaction
	scoped.quarantineKind = kind
	return &scoped
}

// Mutate holds the persistent repository state lock for mutation.
func (store *Store) Mutate(
	runID runstate.RunID,
	lockPoint faultpoint.PointID,
	mutation func(*Txn) error,
) error {
	if err := validateRunID(runID); err != nil {
		return err
	}
	if mutation == nil {
		return errors.New("runstore: nil mutation")
	}
	return store.withLock(lockPoint, func() error {
		return mutation(&Txn{store: store, runID: runID})
	})
}

// RunIDs returns the valid run directory names at this store's fixed root.
// It does not create the runs directory, so absence is an empty collection.
func (store *Store) RunIDs() ([]runstate.RunID, error) {
	entries, err := os.ReadDir(filepath.Join(store.root, ".partitur", "runs"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read runs directory: %w", err)
	}
	ids := make([]runstate.RunID, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := runstate.RunID(entry.Name())
		if validateRunID(id) == nil {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func (store *Store) withLock(point faultpoint.PointID, action func() error) error {
	runsRoot := filepath.Join(store.root, ".partitur", "runs")
	if err := store.fs.MkdirAll(runsRoot, 0o700); err != nil {
		return fmt.Errorf("create runs root: %w", err)
	}
	lock, err := acquireFileLock(filepath.Join(runsRoot, ".state.lock"))
	if err != nil {
		return err
	}
	defer lock.release()
	if point != "" {
		store.probe.Reached(point)
	}
	return action()
}

func (transaction *Txn) requireReceiptAddress() error {
	if transaction.address == "" {
		return ErrReceiptAddressRequired
	}
	return nil
}

func (transaction *Txn) newReceipt(kind faultpoint.MutationKind) faultpoint.DurabilityReceipt {
	return faultpoint.DurabilityReceipt{
		Address: transaction.address,
		Mutation: faultpoint.Mutation{
			Kind:  kind,
			RunID: string(transaction.runID),
		},
	}
}

func (transaction *Txn) observeReceipt(receipt DurabilityReceipt) DurabilityReceipt {
	transaction.store.receiptObserver.Observed(receipt)
	return receipt
}

func (transaction *Txn) runRoot() string {
	return filepath.Join(transaction.store.root, ".partitur", "runs", string(transaction.runID))
}

func (transaction *Txn) resolve(path Path) (string, error) {
	value := string(path)
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, `\`) {
		return "", ErrInvalidPath
	}
	clean := filepath.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrInvalidPath
	}
	resolved := filepath.Join(transaction.runRoot(), clean)
	relative, err := filepath.Rel(transaction.runRoot(), resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", ErrInvalidPath
	}
	return resolved, nil
}

func validateRunID(runID runstate.RunID) error {
	if !runIDPattern.MatchString(string(runID)) {
		return fmt.Errorf("%w: run id", ErrInvalidPath)
	}
	return nil
}

func exists(fileSystem fsOperations, path string) (bool, error) {
	_, err := fileSystem.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}
