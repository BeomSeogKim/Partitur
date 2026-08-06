package runstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

// QuarantineUnreferencedProposalRecords quarantines proposal records that were
// published before either durable route reference made them authoritative.
// Callers hold the run mutation lock through the transaction that invokes it.
func (transaction *Txn) QuarantineUnreferencedProposalRecords() error {
	if err := transaction.requireReceiptAddress(); err != nil {
		return err
	}
	entries, err := transaction.loadJournal(filepath.Join(transaction.runRoot(), "journal.jsonl"))
	if err != nil {
		return err
	}
	directory := filepath.Join(transaction.runRoot(), "proposals")
	files, err := os.ReadDir(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read proposal records: %w", err)
	}
	for _, file := range files {
		if !file.Type().IsRegular() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		proposalID := strings.TrimSuffix(file.Name(), ".json")
		path := Path(filepath.ToSlash(filepath.Join("proposals", file.Name())))
		contents, err := transaction.store.fs.ReadFile(filepath.Join(directory, file.Name()))
		if err != nil {
			return fmt.Errorf("read proposal record: %w", err)
		}
		if proposalRecordReferenced(entries, proposalID, rawHash(contents)) {
			continue
		}
		if _, err := transaction.QuarantineAs("unreferenced_proposal_record").At("recovery.cleanup_proposal_records").Quarantine(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("quarantine unreferenced proposal record: %w", err)
		}
	}
	return nil
}

func proposalRecordReferenced(entries []journalEntry, proposalID, recordHash string) bool {
	for _, entry := range entries {
		var payload map[string]any
		if err := json.Unmarshal(entry.event.Payload, &payload); err != nil {
			continue
		}
		switch entry.event.Type {
		case runstate.EventAmendmentRoutedHuman:
			if stringValue(payload, "proposal_id") == proposalID && stringValue(payload, "proposal_record_hash") == recordHash {
				return true
			}
		case runstate.EventAttemptBlocked:
			for _, value := range arrayValue(payload, "raised") {
				raised, ok := value.(map[string]any)
				if !ok || stringValue(raised, "kind") != "proposal" || !boolValue(raised, "blocking") || stringValue(raised, "proposal_id") != proposalID {
					continue
				}
				route, ok := raised["route"].(map[string]any)
				if ok && stringValue(route, "proposal_record_hash") == recordHash {
					return true
				}
			}
		}
	}
	return false
}
