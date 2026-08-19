package runstore

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

type journalEntry struct {
	event            runstate.Event
	canonicalPayload []byte
	idempotencyKey   string
}

const recoveryJournalTailRepairAddress faultpoint.ReceiptAddress = "recovery/journal_tail_truncated"

func (transaction *Txn) project(seed []runstate.MovementSeed) (runstate.State, error) {
	state := runstate.NewState(seed)
	path := filepath.Join(transaction.runRoot(), "journal.jsonl")
	entries, err := transaction.loadJournal(path)
	if errors.Is(err, fs.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	for _, entry := range entries {
		next, err := runstate.Apply(state, entry.event)
		if errors.Is(err, runstate.ErrUnsupportedEventType) {
			return state, err
		}
		if err != nil {
			return state, fmt.Errorf(
				"%w: seq=%d: %v",
				ErrJournalCorrupt,
				entry.event.Seq,
				err,
			)
		}
		state = next
	}
	return state, nil
}

// Replay projects the journal and repairs only a syntactically unparseable
// final line while holding the repository state lock.
func (store *Store) Replay(
	runID runstate.RunID,
	seed []runstate.MovementSeed,
	repairAddress faultpoint.ReceiptAddress,
) (ReplayResult, error) {
	if err := validateRunID(runID); err != nil {
		return ReplayResult{}, err
	}
	var result ReplayResult
	err := store.withLock("", func() error {
		state := runstate.NewState(seed)
		path := filepath.Join(store.root, ".partitur", "runs", string(runID), "journal.jsonl")
		contents, err := store.fs.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			result.State = state
			return nil
		}
		if err != nil {
			return fmt.Errorf("read journal: %w", err)
		}

		lines := journalLines(contents)
		expectedSeq := uint64(1)
		for index, line := range lines {
			event, syntaxErr, err := decodeEvent(line.bytes)
			if syntaxErr != nil {
				if index != len(lines)-1 {
					return fmt.Errorf("%w: unparseable non-final line: %v", ErrJournalCorrupt, syntaxErr)
				}
				if repairAddress == "" {
					return ErrReceiptAddressRequired
				}
				if err := store.fs.Truncate(path, int64(line.offset)); err != nil {
					return fmt.Errorf("truncate torn journal tail: %w", err)
				}
				payload, err := json.Marshal(map[string]any{
					"truncated_seq":   expectedSeq,
					"discarded_bytes": len(contents) - line.offset,
				})
				if err != nil {
					return err
				}
				repairEvent := runstate.Event{
					RunID:         runID,
					ScoreRevision: state.ScoreHead.Revision,
					Type:          runstate.EventJournalTailTruncated,
					Payload:       payload,
				}
				transaction := (&Txn{store: store, runID: runID}).At(repairAddress)
				receipt, err := transaction.Append(repairEvent)
				if err != nil {
					return err
				}
				repairEvent.EventID = receipt.Mutation.EventID
				repairEvent.Seq = receipt.Mutation.Sequence
				repairEvent.Timestamp = receipt.Mutation.Timestamp
				state, err = runstate.Apply(state, repairEvent)
				if err != nil {
					return fmt.Errorf("%w: repair event: %v", ErrJournalCorrupt, err)
				}
				result.State = state
				result.RepairReceipt = &receipt
				return nil
			}
			if err != nil {
				if errors.Is(err, runstate.ErrUnsupportedEventType) {
					return err
				}
				return fmt.Errorf("%w: %v", ErrJournalCorrupt, err)
			}
			if event.Seq != expectedSeq {
				return fmt.Errorf("%w: seq=%d want=%d", ErrJournalCorrupt, event.Seq, expectedSeq)
			}
			if event.RunID != runID {
				return fmt.Errorf("%w: wrong run id %q", ErrJournalCorrupt, event.RunID)
			}
			next, err := runstate.Apply(state, event)
			if errors.Is(err, runstate.ErrUnsupportedEventType) {
				return err
			}
			if err != nil {
				return fmt.Errorf("%w: seq=%d: %v", ErrJournalCorrupt, event.Seq, err)
			}
			state = next
			expectedSeq++
		}
		result.State = state
		return nil
	})
	return result, err
}

// RepairJournalTail runs the existing replay repair with the immutable seed
// bound by run.started. Recovery calls it only after read-only planning has
// identified a syntactically unparseable final line.
func (store *Store) RepairJournalTail(runID runstate.RunID) (ReplayResult, error) {
	initialScore, err := store.LoadInitialScore(runID)
	if err != nil {
		return ReplayResult{}, err
	}
	return store.Replay(runID, movementSeed(initialScore), recoveryJournalTailRepairAddress)
}

// ReadReplay projects a run journal without taking the state lock, creating a
// directory, repairing a torn tail, or otherwise mutating repository state.
// It is consequently suitable for status and other observational surfaces.
func (store *Store) ReadReplay(
	runID runstate.RunID,
	seed []runstate.MovementSeed,
) (ReadReplayResult, error) {
	journal, err := store.ReadJournal(runID)
	if err != nil {
		return ReadReplayResult{}, err
	}
	return replayJournal(journal, seed)
}

func replayJournal(journal ReadJournalResult, seed []runstate.MovementSeed) (ReadReplayResult, error) {
	state := runstate.NewState(seed)
	for _, event := range journal.Events {
		next, err := runstate.Apply(state, event)
		if errors.Is(err, runstate.ErrUnsupportedEventType) {
			return ReadReplayResult{}, err
		}
		if err != nil {
			return ReadReplayResult{}, fmt.Errorf("%w: seq=%d: %v", ErrJournalCorrupt, event.Seq, err)
		}
		state = next
	}
	return ReadReplayResult{
		State:          state,
		TailTruncated:  journal.TailUnparseable,
		TruncatedSeq:   journal.TruncatedSeq,
		DiscardedBytes: journal.DiscardedBytes,
	}, nil
}

// ReadJournal reads the journal without taking a state lock, creating a
// directory, or repairing a torn tail. It validates every complete envelope
// and returns only the durable prefix before an unparseable final line.
func (store *Store) ReadJournal(runID runstate.RunID) (ReadJournalResult, error) {
	if err := validateRunID(runID); err != nil {
		return ReadJournalResult{}, err
	}
	path := filepath.Join(store.root, ".partitur", "runs", string(runID), "journal.jsonl")
	contents, err := store.fs.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ReadJournalResult{}, nil
	}
	if err != nil {
		return ReadJournalResult{}, fmt.Errorf("read journal: %w", err)
	}

	lines := journalLines(contents)
	result := ReadJournalResult{Events: make([]runstate.Event, 0, len(lines))}
	expectedSeq := uint64(1)
	for index, line := range lines {
		event, syntaxErr, err := decodeEvent(line.bytes)
		if syntaxErr != nil {
			if index != len(lines)-1 {
				return ReadJournalResult{}, fmt.Errorf(
					"%w: unparseable non-final line: %v",
					ErrJournalCorrupt,
					syntaxErr,
				)
			}
			result.TailUnparseable = true
			result.TruncatedSeq = expectedSeq
			result.DiscardedBytes = len(contents) - line.offset
			return result, nil
		}
		if err != nil {
			if errors.Is(err, runstate.ErrUnsupportedEventType) {
				return ReadJournalResult{}, err
			}
			return ReadJournalResult{}, fmt.Errorf("%w: %v", ErrJournalCorrupt, err)
		}
		if event.Seq != expectedSeq {
			return ReadJournalResult{}, fmt.Errorf(
				"%w: seq=%d want=%d",
				ErrJournalCorrupt,
				event.Seq,
				expectedSeq,
			)
		}
		if event.RunID != runID {
			return ReadJournalResult{}, fmt.Errorf("%w: wrong run id %q", ErrJournalCorrupt, event.RunID)
		}
		result.Events = append(result.Events, event)
		expectedSeq++
	}
	return result, nil
}

// Append allocates the journal envelope and appends one strictly validated
// event. EventID, Seq, and Timestamp must be left empty by the caller.
func (transaction *Txn) Append(event runstate.Event) (DurabilityReceipt, error) {
	if err := transaction.requireReceiptAddress(); err != nil {
		return DurabilityReceipt{}, err
	}
	if event.EventID != "" || event.Seq != 0 || event.Timestamp != "" {
		return DurabilityReceipt{}, fmt.Errorf(
			"%w: event_id, seq, and ts are allocated by runstore",
			ErrJournalCorrupt,
		)
	}
	if event.RunID != transaction.runID {
		return DurabilityReceipt{}, fmt.Errorf("%w: event run id", ErrJournalCorrupt)
	}
	key, err := runstate.IdempotencyKey(event)
	if err != nil {
		return DurabilityReceipt{}, err
	}
	payloadValue, err := canonical.ParseJSON(event.Payload)
	if err != nil {
		return DurabilityReceipt{}, fmt.Errorf("%w: payload: %v", ErrJournalCorrupt, err)
	}
	canonicalPayload, err := canonical.Encode(payloadValue)
	if err != nil {
		return DurabilityReceipt{}, fmt.Errorf("%w: canonical payload: %v", ErrJournalCorrupt, err)
	}

	path := filepath.Join(transaction.runRoot(), "journal.jsonl")
	entries, err := transaction.loadJournal(path)
	if err != nil {
		return DurabilityReceipt{}, err
	}
	for _, existing := range entries {
		if key == "" {
			break
		}
		if existing.event.Type != event.Type || existing.idempotencyKey != key {
			continue
		}
		if !bytes.Equal(existing.canonicalPayload, canonicalPayload) {
			return DurabilityReceipt{}, ErrJournalIdempotencyConflict
		}
		if err := transaction.store.fs.SyncFile(path); err != nil {
			return DurabilityReceipt{}, fmt.Errorf("%w: sync idempotent journal append: %v", ErrJournalDurabilityUnconfirmed, err)
		}
		return transaction.journalReceipt(existing.event, path), nil
	}
	event.EventID = newEventID()
	event.Seq = uint64(len(entries) + 1)
	event.Timestamp = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	if err := validateEnvelope(event); err != nil {
		return DurabilityReceipt{}, err
	}
	line, err := json.Marshal(event)
	if err != nil {
		return DurabilityReceipt{}, fmt.Errorf("encode journal event: %w", err)
	}
	line = append(line, '\n')
	if err := transaction.store.fs.MkdirAll(transaction.runRoot(), 0o700); err != nil {
		return DurabilityReceipt{}, fmt.Errorf("create run root: %w", err)
	}
	if err := transaction.store.fs.Append(path, line, 0o600); err != nil {
		return DurabilityReceipt{}, fmt.Errorf("append journal: %w", err)
	}
	if err := transaction.store.fs.SyncFile(path); err != nil {
		return DurabilityReceipt{}, fmt.Errorf("%w: sync journal: %v", ErrJournalDurabilityUnconfirmed, err)
	}
	return transaction.journalReceipt(event, path), nil
}

func (transaction *Txn) journalReceipt(event runstate.Event, path string) DurabilityReceipt {
	receipt := transaction.newReceipt(faultpoint.JournalAppend)
	receipt.Mutation.EventID = event.EventID
	receipt.Mutation.EventType = string(event.Type)
	receipt.Mutation.Sequence = event.Seq
	receipt.Mutation.Timestamp = event.Timestamp
	receipt.Mutation.Path = relativeToRoot(transaction.store.root, path)
	return transaction.observeReceipt(receipt)
}

func (transaction *Txn) loadJournal(path string) ([]journalEntry, error) {
	contents, err := transaction.store.fs.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read journal: %w", err)
	}
	lines := journalLines(contents)
	entries := make([]journalEntry, 0, len(lines))
	for index, line := range lines {
		event, syntaxErr, err := decodeEvent(line.bytes)
		if syntaxErr != nil {
			return nil, fmt.Errorf("%w: unparseable line %d: %v", ErrJournalCorrupt, index+1, syntaxErr)
		}
		if err != nil {
			if errors.Is(err, runstate.ErrUnsupportedEventType) {
				return nil, err
			}
			return nil, fmt.Errorf("%w: line %d: %v", ErrJournalCorrupt, index+1, err)
		}
		if event.RunID != transaction.runID || event.Seq != uint64(index+1) {
			return nil, fmt.Errorf("%w: invalid envelope sequence", ErrJournalCorrupt)
		}
		key, err := runstate.IdempotencyKey(event)
		if err != nil {
			return nil, err
		}
		value, _ := canonical.ParseJSON(event.Payload)
		payload, _ := canonical.Encode(value)
		entries = append(entries, journalEntry{
			event:            event,
			canonicalPayload: payload,
			idempotencyKey:   key,
		})
	}
	return entries, nil
}

type journalLine struct {
	bytes  []byte
	offset int
}

func journalLines(contents []byte) []journalLine {
	var lines []journalLine
	start := 0
	for index, value := range contents {
		if value != '\n' {
			continue
		}
		lines = append(lines, journalLine{bytes: contents[start:index], offset: start})
		start = index + 1
	}
	if start < len(contents) {
		lines = append(lines, journalLine{bytes: contents[start:], offset: start})
	}
	return lines
}

func decodeEvent(line []byte) (runstate.Event, error, error) {
	if !json.Valid(line) {
		return runstate.Event{}, errors.New("invalid JSON"), nil
	}
	if _, err := canonical.ParseJSON(line); err != nil {
		return runstate.Event{}, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var event runstate.Event
	if err := decoder.Decode(&event); err != nil {
		return runstate.Event{}, nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return runstate.Event{}, nil, errors.New("trailing journal value")
	}
	if err := validateEnvelope(event); err != nil {
		return runstate.Event{}, nil, err
	}
	return event, nil, nil
}

func validateEnvelope(event runstate.Event) error {
	if event.EventID == "" || event.Seq == 0 || event.Timestamp == "" ||
		event.RunID == "" || event.Type == "" || len(event.Payload) == 0 {
		return fmt.Errorf("%w: incomplete event envelope", ErrJournalCorrupt)
	}
	timestamp, err := time.Parse("2006-01-02T15:04:05.000Z", event.Timestamp)
	if err != nil || timestamp.Format("2006-01-02T15:04:05.000Z") != event.Timestamp {
		return fmt.Errorf("%w: timestamp must be UTC RFC 3339 with millisecond precision", ErrJournalCorrupt)
	}
	if err := runstate.ValidateEvent(event); err != nil {
		return err
	}
	return nil
}

func newEventID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("recovery-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}

func relativeToRoot(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(relative)
}
