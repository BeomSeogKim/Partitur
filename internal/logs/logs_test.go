package logs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
	statusprojection "github.com/BeomSeogKim/Partitur/internal/status"
)

func TestReadNormalizesObservationsAndLeavesTornTailUntouched(t *testing.T) {
	root, runID := logsFixture(t)
	journal := filepath.Join(root, ".partitur", "runs", runID, "journal.jsonl")
	file, err := os.OpenFile(journal, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"seq":`); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := Read(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("logs mutated journal\nbefore=%q\nafter=%q", before, after)
	}
	if snapshot.Lifecycle != string(runstate.RunRunning) || len(snapshot.Entries) != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if got := snapshot.Entries[0]; got.Schema != schema || got.RunID != runID || got.Seq != 2 ||
		got.Type != "log" || got.Level != "info" || got.Message != "started" {
		t.Fatalf("log entry = %+v", got)
	}
	if got := snapshot.Entries[1]; got.Seq != 3 || got.Type != "progress" || got.Level != "" || got.Message != "halfway" {
		t.Fatalf("progress entry = %+v", got)
	}
}

func TestReadRejectsCorruptPrefix(t *testing.T) {
	root, runID := logsFixture(t)
	journal := filepath.Join(root, ".partitur", "runs", runID, "journal.jsonl")
	file, err := os.OpenFile(journal, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{\"seq\":\n{}\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root, runID); !errors.Is(err, runstore.ErrJournalCorrupt) {
		t.Fatalf("error = %v, want journal corruption", err)
	}
}

func TestReadRejectsIllegalLifecycleTransition(t *testing.T) {
	root, runID := logsFixture(t)
	journal := filepath.Join(root, ".partitur", "runs", runID, "journal.jsonl")
	file, err := os.OpenFile(journal, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	payload, err := json.Marshal(map[string]any{"reason": "fixture failure"})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []runstate.Event{
		{EventID: "event-failed-1", RunID: runstate.RunID(runID), Seq: 4, Timestamp: "2026-07-28T00:00:04.000Z", ScoreRevision: 1, Type: runstate.EventRunFailed, Payload: payload},
		{EventID: "event-failed-2", RunID: runstate.RunID(runID), Seq: 5, Timestamp: "2026-07-28T00:00:05.000Z", ScoreRevision: 1, Type: runstate.EventRunFailed, Payload: payload},
	} {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(append(encoded, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Read(root, runID); !errors.Is(err, runstore.ErrJournalCorrupt) {
		t.Fatalf("error = %v, want illegal lifecycle rejection", err)
	}
}

func TestReadRejectsSubstitutedScoreSnapshot(t *testing.T) {
	root, runID := logsFixture(t)
	snapshotPath := filepath.Join(root, ".partitur", "runs", runID, "scores", "revision-1.yaml")
	source, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	substituted := strings.Replace(string(source), "Write the report.", "Write a different report.", 1)
	if err := os.WriteFile(snapshotPath, []byte(substituted), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root, runID); !errors.Is(err, statusprojection.ErrSnapshot) {
		t.Fatalf("error = %v, want substituted snapshot rejection", err)
	}
}

func TestReadProjectsImmediateCancellationThroughStatusProjection(t *testing.T) {
	root, runID := immediateCancellationFixture(t)
	status, err := statusprojection.Read(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Run.Lifecycle != string(runstate.RunCancelled) {
		t.Fatalf("status lifecycle = %q, want %q", status.Run.Lifecycle, runstate.RunCancelled)
	}
	logs, err := Read(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	if logs.Lifecycle != status.Run.Lifecycle {
		t.Fatalf("logs lifecycle = %q, want status lifecycle %q", logs.Lifecycle, status.Run.Lifecycle)
	}
}

func TestReadRejectsCancelledPhantomMovementID(t *testing.T) {
	root, runID := immediateCancellationFixture(t, []string{"first", "phantom"})
	if _, err := Read(root, runID); !errors.Is(err, runstore.ErrJournalCorrupt) {
		t.Fatalf("error = %v, want cancelled phantom movement rejection", err)
	}
}

func TestStreamFollowTerminatesAtTerminalLifecycle(t *testing.T) {
	base := Snapshot{
		RunID:     "run-1",
		Lifecycle: string(runstate.RunRunning),
		Entries:   []Entry{{Schema: schema, RunID: "run-1", Seq: 2, TS: "2026-07-28T00:00:00.000Z", Type: "log", Level: "info", Message: "started"}},
	}
	terminal := base
	terminal.Lifecycle = string(runstate.RunSucceeded)
	terminal.Entries = append(terminal.Entries, Entry{Schema: schema, RunID: "run-1", Seq: 3, TS: "2026-07-28T00:00:01.000Z", Type: "progress", Message: "complete"})
	for _, test := range []struct {
		name       string
		snapshots  []Snapshot
		wantReads  int
		wantWaits  int
		wantOutput string
	}{
		{
			name:      "already terminal streams history then exits",
			snapshots: []Snapshot{terminal},
			wantReads: 1,
			wantOutput: "[2 2026-07-28T00:00:00.000Z] LOG info: started\n" +
				"[3 2026-07-28T00:00:01.000Z] PROGRESS: complete\n",
		},
		{
			name:      "active run follows until terminal",
			snapshots: []Snapshot{base, terminal},
			wantReads: 2,
			wantWaits: 1,
			wantOutput: "[2 2026-07-28T00:00:00.000Z] LOG info: started\n" +
				"[3 2026-07-28T00:00:01.000Z] PROGRESS: complete\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads := 0
			waits := 0
			var output bytes.Buffer
			err := stream(context.Background(), func() (Snapshot, error) {
				if reads >= len(test.snapshots) {
					t.Fatal("stream read after terminal lifecycle")
				}
				snapshot := test.snapshots[reads]
				reads++
				return snapshot, nil
			}, &output, StreamOptions{Follow: true}, func(context.Context, time.Duration) error {
				waits++
				return nil
			})
			if err != nil || reads != test.wantReads || waits != test.wantWaits {
				t.Fatalf("error=%v reads=%d waits=%d", err, reads, waits)
			}
			if got := output.String(); got != test.wantOutput {
				t.Fatalf("output=%q want=%q", got, test.wantOutput)
			}
		})
	}
}

func logsFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	runID := "run-1"
	runRoot := filepath.Join(root, ".partitur", "runs", runID)
	if err := os.MkdirAll(filepath.Join(runRoot, "scores"), 0o700); err != nil {
		t.Fatal(err)
	}
	source := logsScore()
	if err := os.WriteFile(filepath.Join(runRoot, "scores", "revision-1.yaml"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	compiled, diagnostics := score.Compile([]byte(source))
	if compiled == nil || len(diagnostics) != 0 {
		t.Fatalf("compile diagnostics = %v", diagnostics)
	}
	semanticHash, err := compiled.Hash()
	if err != nil {
		t.Fatal(err)
	}
	fileHash := sha256.Sum256([]byte(source))
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	err = store.Mutate(runstate.RunID(runID), "", func(transaction *runstore.Txn) error {
		for _, event := range []struct {
			eventType runstate.EventType
			payload   any
		}{
			{runstate.EventRunStarted, map[string]any{
				"base_commit": "base", "base_tree": "tree", "score_hash": semanticHash,
				"score_file_hash": fmt.Sprintf("sha256:%x", fileHash), "resolved_cast_hash": "sha256:cast",
				"identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}},
			}},
			{runstate.EventLog, map[string]any{"level": "info", "message": "started"}},
			{runstate.EventProgress, map[string]any{"message": "halfway"}},
		} {
			payload, err := json.Marshal(event.payload)
			if err != nil {
				return err
			}
			if _, err := transaction.At("logs.fixture").Append(runstate.Event{
				RunID: runstate.RunID(runID), ScoreRevision: 1, Type: event.eventType, Payload: payload,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return root, runID
}

func immediateCancellationFixture(t *testing.T, cancellation ...[]string) (string, string) {
	t.Helper()
	root := t.TempDir()
	runID := "run-cancelled"
	runRoot := filepath.Join(root, ".partitur", "runs", runID)
	if err := os.MkdirAll(filepath.Join(runRoot, "scores"), 0o700); err != nil {
		t.Fatal(err)
	}
	source := immediateCancellationScore()
	if err := os.WriteFile(filepath.Join(runRoot, "scores", "revision-1.yaml"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	compiled, diagnostics := score.Compile([]byte(source))
	if compiled == nil || len(diagnostics) != 0 {
		t.Fatalf("compile diagnostics = %v", diagnostics)
	}
	semanticHash, err := compiled.Hash()
	if err != nil {
		t.Fatal(err)
	}
	fileHash := sha256.Sum256([]byte(source))
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	cancelledMovements := []string{"first", "second"}
	if len(cancellation) == 1 {
		cancelledMovements = cancellation[0]
	}
	err = store.Mutate(runstate.RunID(runID), "", func(transaction *runstore.Txn) error {
		for _, event := range []runstate.Event{
			{RunID: runstate.RunID(runID), ScoreRevision: 1, Type: runstate.EventRunStarted, Payload: mustJSON(t, map[string]any{
				"base_commit": "base", "base_tree": "tree", "score_hash": semanticHash,
				"score_file_hash": fmt.Sprintf("sha256:%x", fileHash), "resolved_cast_hash": "sha256:cast",
				"identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}},
			})},
			{RunID: runstate.RunID(runID), ScoreRevision: 1, Type: runstate.EventRunCancelled, Payload: mustJSON(t, map[string]any{
				"cancelled_movement_ids": cancelledMovements,
				"cancelled_attempt_ids":  []string{},
				"obsoleted_decision_ids": []string{},
			})},
		} {
			if _, err := transaction.At("logs.immediate-cancellation").Append(event); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return root, runID
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func logsScore() string {
	return `score: "0.2"
name: logs-fixture
revision: 1
status: finalized
goal: Produce one report.
verification:
  expectation:
    intent: pass-existing-tests
    apply_gate:
      require: [verified]
  final_movement: inspect
parts:
  reader:
    capabilities: [repo_read, shell, network]
    read_only: true
movements:
  - id: inspect
    part: reader
    grants: [repo_read, shell, network]
    instruction: Write the report.
    outputs:
      - id: report
        kind: artifact
    acceptance:
      hard:
        - id: report-present
          artifact: report
policy:
  allowed_paths: ["**"]
  budget:
    active_wall_clock_min: 10
`
}

func immediateCancellationScore() string {
	return `score: "0.2"
name: immediate-cancellation
revision: 1
status: finalized
goal: Cancel before execution.
verification:
  expectation:
    intent: pass-existing-tests
    apply_gate:
      require: [verified]
  final_movement: second
parts:
  reader:
    capabilities: [repo_read]
    read_only: true
movements:
  - id: first
    part: reader
    grants: [repo_read]
    instruction: Do not start.
    outputs:
      - id: first-report
        kind: artifact
    acceptance:
      hard:
        - id: first-report-present
          artifact: first-report
  - id: second
    part: reader
    needs: [first]
    grants: [repo_read]
    instruction: Do not start.
    outputs:
      - id: second-report
        kind: artifact
    acceptance:
      hard:
        - id: second-report-present
          artifact: second-report
policy:
  allowed_paths: ["**"]
  budget:
    active_wall_clock_min: 10
`
}
