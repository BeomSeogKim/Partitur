package runstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

// RemoveUnreferencedReviewSubjectInputs removes core-published review inputs
// that were durable before an attempt.started event bound them. Callers hold
// the run mutation lock through the transaction that invokes it.
func (transaction *Txn) RemoveUnreferencedReviewSubjectInputs() error {
	if err := transaction.requireReceiptAddress(); err != nil {
		return err
	}
	entries, err := transaction.loadJournal(filepath.Join(transaction.runRoot(), "journal.jsonl"))
	if err != nil {
		return err
	}
	referenced := make(map[string]bool)
	for _, entry := range entries {
		if entry.event.Type != runstate.EventAttemptStarted {
			continue
		}
		var payload struct {
			ReviewSubjectInput *struct {
				Hash string `json:"hash"`
			} `json:"review_subject_input"`
		}
		if err := json.Unmarshal(entry.event.Payload, &payload); err != nil || payload.ReviewSubjectInput == nil {
			continue
		}
		referenced[filepath.ToSlash(filepath.Join("inputs", string(entry.event.MovementID), fmt.Sprintf("revision-%d", entry.event.ScoreRevision), "subject-tree.json"))] = true
	}

	pattern := filepath.Join(transaction.runRoot(), "inputs", "*", "revision-*", "subject-tree.json")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("glob review subject inputs: %w", err)
	}
	for _, path := range paths {
		relative, err := filepath.Rel(transaction.runRoot(), path)
		if err != nil {
			return fmt.Errorf("relativize review subject input: %w", err)
		}
		relative = filepath.ToSlash(relative)
		if referenced[relative] {
			continue
		}
		if _, err := transaction.RemoveDurable(Path(relative)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove unreferenced review subject input: %w", err)
		}
	}
	return nil
}
