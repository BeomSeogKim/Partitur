// Package logs renders the read-only observational journal stream used by partitur logs.
package logs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	statusprojection "github.com/BeomSeogKim/Partitur/internal/status"
)

const schema = "partitur/logs+jsonl;v=1"

// ErrOutput marks a failure to write an otherwise valid observation stream.
var ErrOutput = errors.New("logs output failed")

// Entry is one stable observational event in the logs JSONL surface.
type Entry struct {
	Schema  string `json:"schema"`
	RunID   string `json:"run_id"`
	Seq     uint64 `json:"seq"`
	TS      string `json:"ts"`
	Type    string `json:"type"`
	Level   string `json:"level,omitempty"`
	Message string `json:"message"`
}

// Snapshot is one read-only view of the selected run's observations.
type Snapshot struct {
	RunID     string
	Lifecycle string
	Entries   []Entry
}

// StreamOptions selects the logs rendering and whether it follows new durable
// observations. PollInterval is used only when Follow is true.
type StreamOptions struct {
	JSONL        bool
	Follow       bool
	PollInterval time.Duration
}

// Read selects and validates a run through the status read-only projection,
// then returns its durable log and progress observations without mutating it.
func Read(repositoryRoot, requestedID string) (Snapshot, error) {
	report, err := statusprojection.Read(repositoryRoot, requestedID)
	if err != nil {
		return Snapshot{}, err
	}
	store, err := runstore.New(repositoryRoot, faultpoint.Nop{})
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: %v", statusprojection.ErrRequiredInput, err)
	}
	journal, err := store.ReadJournal(runstate.RunID(report.Run.ID))
	if err != nil {
		return Snapshot{}, err
	}
	entries := make([]Entry, 0)
	for _, event := range journal.Events {
		entry, ok, err := observation(event)
		if err != nil {
			return Snapshot{}, err
		}
		if ok {
			entries = append(entries, entry)
		}
	}
	return Snapshot{RunID: report.Run.ID, Lifecycle: report.Run.Lifecycle, Entries: entries}, nil
}

func observation(event runstate.Event) (Entry, bool, error) {
	entry := Entry{Schema: schema, RunID: string(event.RunID), Seq: event.Seq, TS: event.Timestamp, Type: string(event.Type)}
	switch event.Type {
	case runstate.EventLog:
		var payload struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return Entry{}, false, fmt.Errorf("%w: log payload: %v", runstore.ErrJournalCorrupt, err)
		}
		entry.Level, entry.Message = payload.Level, payload.Message
		return entry, true, nil
	case runstate.EventProgress:
		var payload struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return Entry{}, false, fmt.Errorf("%w: progress payload: %v", runstore.ErrJournalCorrupt, err)
		}
		entry.Message = payload.Message
		return entry, true, nil
	default:
		return Entry{}, false, nil
	}
}

// Stream writes the current observation history, then follows durable entries
// until the selected run is terminal or ctx is cancelled.
func Stream(ctx context.Context, read func() (Snapshot, error), output io.Writer, options StreamOptions) error {
	return stream(ctx, read, output, options, wait)
}

func stream(
	ctx context.Context,
	read func() (Snapshot, error),
	output io.Writer,
	options StreamOptions,
	waitFor func(context.Context, time.Duration) error,
) error {
	if read == nil || output == nil || waitFor == nil {
		return errors.New("logs stream is unavailable")
	}
	interval := options.PollInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	var lastSeq uint64
	for {
		snapshot, err := read()
		if err != nil {
			return err
		}
		for _, entry := range snapshot.Entries {
			if entry.Seq <= lastSeq {
				continue
			}
			if err := render(output, entry, options.JSONL); err != nil {
				return fmt.Errorf("%w: %w", ErrOutput, err)
			}
			lastSeq = entry.Seq
		}
		if !options.Follow || terminal(snapshot.Lifecycle) {
			return nil
		}
		if err := waitFor(ctx, interval); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
	}
}

func render(output io.Writer, entry Entry, jsonl bool) error {
	if jsonl {
		return json.NewEncoder(output).Encode(entry)
	}
	if entry.Type == string(runstate.EventLog) {
		_, err := fmt.Fprintf(output, "[%d %s] LOG %s: %s\n", entry.Seq, entry.TS, entry.Level, entry.Message)
		return err
	}
	_, err := fmt.Fprintf(output, "[%d %s] PROGRESS: %s\n", entry.Seq, entry.TS, entry.Message)
	return err
}

func terminal(lifecycle string) bool {
	return lifecycle == string(runstate.RunSucceeded) ||
		lifecycle == string(runstate.RunFailed) ||
		lifecycle == string(runstate.RunCancelled)
}

func wait(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
