package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
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
	if err := os.WriteFile(filepath.Join(runRoot, "scores", "revision-1.yaml"), []byte(logsScore()), 0o600); err != nil {
		t.Fatal(err)
	}
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
				"base_commit": "base", "base_tree": "tree", "score_hash": "sha256:score",
				"score_file_hash": "sha256:file", "resolved_cast_hash": "sha256:cast",
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
