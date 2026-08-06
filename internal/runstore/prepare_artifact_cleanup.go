package runstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

// RemoveUnreferencedPrepareArtifacts reclaims artifacts that were published
// before amendment.approval_prepared made them authoritative. A current
// pending prepare is the only prepare that retains a plan. Snapshots retain
// their run.started or approval_prepared journal reference.
// Callers hold the run mutation lock through the transaction that invokes it.
func (transaction *Txn) RemoveUnreferencedPrepareArtifacts(pending *runstate.PendingPrepare) error {
	if err := transaction.requireReceiptAddress(); err != nil {
		return err
	}
	entries, err := transaction.loadJournal(filepath.Join(transaction.runRoot(), "journal.jsonl"))
	if err != nil {
		return err
	}
	referencedSnapshots := make(map[uint64]bool)
	for _, entry := range entries {
		switch entry.event.Type {
		case runstate.EventRunStarted:
			referencedSnapshots[entry.event.ScoreRevision] = true
		case runstate.EventAmendmentApprovalPrepared:
			var payload struct {
				NewRevision uint64 `json:"new_revision"`
			}
			if err := json.Unmarshal(entry.event.Payload, &payload); err != nil {
				return fmt.Errorf("read prepared snapshot reference: %w", err)
			}
			if payload.NewRevision == 0 {
				return errors.New("prepared snapshot reference has zero revision")
			}
			referencedSnapshots[payload.NewRevision] = true
		}
	}
	if err := transaction.quarantineUnreferencedPrepareSnapshots(referencedSnapshots); err != nil {
		return err
	}
	if err := transaction.removeOrphanPreparePlans(pending); err != nil {
		return err
	}
	return nil
}

func (transaction *Txn) quarantineUnreferencedPrepareSnapshots(referenced map[uint64]bool) error {
	entries, err := os.ReadDir(filepath.Join(transaction.runRoot(), "scores"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read score snapshots: %w", err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		revision, ok := snapshotRevision(entry.Name())
		if !ok || referenced[revision] {
			continue
		}
		path := Path(filepath.ToSlash(filepath.Join("scores", entry.Name())))
		if _, err := transaction.QuarantineAs("unreferenced_prepare_snapshot").At("recovery.cleanup_prepare_artifacts/snapshot").Quarantine(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("quarantine unreferenced prepare snapshot: %w", err)
		}
	}
	return nil
}

func (transaction *Txn) removeOrphanPreparePlans(pending *runstate.PendingPrepare) error {
	entries, err := os.ReadDir(filepath.Join(transaction.runRoot(), "prepares"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read prepare plans: %w", err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if pending != nil && entry.Name() == string(pending.ID)+".json" {
			continue
		}
		if _, err := transaction.At("recovery.cleanup_prepare_artifacts/plan").RemoveDurable(Path(filepath.ToSlash(filepath.Join("prepares", entry.Name())))); err != nil {
			return fmt.Errorf("remove orphan prepare plan: %w", err)
		}
	}
	return nil
}

func snapshotRevision(name string) (uint64, bool) {
	if !strings.HasPrefix(name, "revision-") || !strings.HasSuffix(name, ".yaml") {
		return 0, false
	}
	revision, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(name, "revision-"), ".yaml"), 10, 64)
	return revision, err == nil
}
