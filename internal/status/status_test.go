package status

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

func TestReadProjectsPinnedRunWithoutMutatingIt(t *testing.T) {
	root, runID := statusFixture(t)
	journal := filepath.Join(root, ".partitur", "runs", runID, "journal.jsonl")
	before, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}

	report, err := Read(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("status mutated journal\nbefore=%q\nafter=%q", before, after)
	}
	if report.Schema != "partitur/status+json;v=1" ||
		report.Run.ID != runID ||
		report.Run.Lifecycle != string(runstate.RunRunning) ||
		len(report.Run.Movements) != 1 ||
		report.Run.Movements[0].State != string(runstate.MovementPending) ||
		report.Journal.Integrity != "INTACT" || report.Recovery.State != "NOT_REQUIRED" {
		t.Fatalf("report = %+v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || string(encoded) == "null" {
		t.Fatalf("encoded report = %q", encoded)
	}
}

func TestReadReportsTornTailWithoutRepairingIt(t *testing.T) {
	root, runID := statusFixture(t)
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

	report, err := Read(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || report.Journal.Integrity != "TAIL_UNPARSEABLE" ||
		report.Recovery.State != "NOT_REQUIRED" || report.Recovery.Reason != "" ||
		report.Journal.TruncatedSeq != 2 {
		t.Fatalf("report=%+v before=%q after=%q", report, before, after)
	}
}

func TestReadRejectsCorruptJournalPrefix(t *testing.T) {
	root, runID := statusFixture(t)
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

func TestProjectCarriesVerifiedProvenanceAndShippingRecovery(t *testing.T) {
	compiled := mustCompile(t, statusScore())
	state := runstate.NewState(movementSeeds(compiled))
	state.Run = runstate.RunSucceeded
	state.ScoreHead = runstate.ScoreHead{Revision: 1, SemanticHash: "sha256:score", FileHash: "sha256:file"}
	state.Movements["inspect"] = runstate.MovementSucceeded
	state.Attempts["failed"] = runstate.Attempt{MovementID: "inspect", State: runstate.AttemptFailed}
	state.Attempts["passed"] = runstate.Attempt{MovementID: "inspect", State: runstate.AttemptCompleted}
	state.Acceptances["passed"] = runstate.Acceptance{
		Started:             true,
		EvaluationCompleted: true,
		SubjectTree:         "tree-1",
		PlannedCriterionIDs: []runstate.CriterionID{"report-present", "partitur.artifact.report"},
		Criteria: map[runstate.CriterionID]runstate.CriterionRecord{
			"report-present":           {Completed: true, Outcome: "PASS", SpecHash: "sha256:declared"},
			"partitur.artifact.report": {Completed: true, Outcome: "PASS", SpecHash: "sha256:generated"},
		},
	}
	state.Application = runstate.ApplicationProjection{
		State: runstate.ApplicationRecoveryRequired, TransactionID: "txn", CandidateID: "candidate", Reason: "tree mismatch",
	}

	report := project("run-1", compiled, runstore.ReadReplayResult{State: state})
	marks := report.Run.Movements[0].Marks
	if len(marks) != 1 || marks[0].Grade != "VERIFIED" || marks[0].AttemptID != "passed" ||
		len(marks[0].Criteria) != 2 || marks[0].SubjectTree != "tree-1" ||
		marks[0].ScoreRevision != 1 || marks[0].FailedAttempts != 1 ||
		report.Application.State != "RECOVERY_REQUIRED" || report.Recovery != (Recovery{State: "RECOVERY_REQUIRED", Reason: "tree mismatch"}) {
		t.Fatalf("report = %+v", report)
	}
}

func TestProjectRendersFailureCancellationAndSupersessionStates(t *testing.T) {
	compiled := mustCompile(t, statusScore())
	for _, test := range []struct {
		name          string
		movementState runstate.MovementState
		attemptState  runstate.AttemptState
	}{
		{"failed", runstate.MovementFailed, runstate.AttemptFailed},
		{"cancelled", runstate.MovementCancelled, runstate.AttemptCancelled},
		{"superseded", runstate.MovementRunning, runstate.AttemptSuperseded},
		{"blocked", runstate.MovementRunning, runstate.AttemptBlocked},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := runstate.NewState(movementSeeds(compiled))
			state.Run = runstate.RunRunning
			state.Movements["inspect"] = test.movementState
			state.Attempts["attempt-1"] = runstate.Attempt{
				MovementID: "inspect",
				State:      test.attemptState,
			}

			report := project("run-1", compiled, runstore.ReadReplayResult{State: state})
			movement := report.Run.Movements[0]
			if movement.State != string(test.movementState) || len(movement.Attempts) != 1 ||
				movement.Attempts[0].State != string(test.attemptState) {
				t.Fatalf("movement = %+v", movement)
			}
		})
	}
}

func TestProjectRendersPendingDecisions(t *testing.T) {
	compiled := mustCompile(t, statusScore())
	state := runstate.NewState(movementSeeds(compiled))
	state.Run = runstate.RunWaitingHuman
	state.Movements["inspect"] = runstate.MovementWaitingHuman
	state.PendingDecisions["decision-b"] = runstate.PendingDecision{
		ID: "decision-b", Type: "human_gate", Blocking: true, MovementID: "inspect", AttemptID: "attempt-1", ScoreRevision: 2,
	}
	state.PendingDecisions["decision-a"] = runstate.PendingDecision{
		ID: "decision-a", Type: "question", Blocking: true, MovementID: "inspect", AttemptID: "attempt-1", ScoreRevision: 1,
	}

	report := project("run-1", compiled, runstore.ReadReplayResult{State: state})
	if report.Run.Lifecycle != string(runstate.RunWaitingHuman) || report.Run.Movements[0].State != string(runstate.MovementWaitingHuman) ||
		len(report.Run.PendingDecisions) != 2 || report.Run.PendingDecisions[0] != (PendingDecision{
		ID: "decision-a", Type: "question", MovementID: "inspect", AttemptID: "attempt-1", ScoreRevision: 1,
	}) || report.Run.PendingDecisions[1].ID != "decision-b" {
		t.Fatalf("report = %+v", report)
	}
}

func TestReadWithoutExactlyOneActiveRunIsRefused(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".partitur", "runs", "orphan"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root, ""); !errors.Is(err, ErrNoActiveRun) {
		t.Fatalf("error = %v, want no active run", err)
	}
}

func statusFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	runID := "run-1"
	runRoot := filepath.Join(root, ".partitur", "runs", runID)
	if err := os.MkdirAll(filepath.Join(runRoot, "scores"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRoot, "scores", "revision-1.yaml"), []byte(statusScore()), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	err = store.Mutate(runstate.RunID(runID), "", func(transaction *runstore.Txn) error {
		payload, err := json.Marshal(map[string]any{
			"base_commit": "base", "base_tree": "tree", "score_hash": "sha256:score",
			"score_file_hash": "sha256:file", "resolved_cast_hash": "sha256:cast",
			"identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}},
		})
		if err != nil {
			return err
		}
		_, err = transaction.At("status.fixture").Append(runstate.Event{
			RunID: runstate.RunID(runID), ScoreRevision: 1, Type: runstate.EventRunStarted, Payload: payload,
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return root, runID
}

func mustCompile(t *testing.T, source string) *score.Score {
	t.Helper()
	compiled, diagnostics := score.Compile([]byte(source))
	if compiled == nil || len(diagnostics) != 0 {
		t.Fatalf("compile diagnostics = %v", diagnostics)
	}
	return compiled
}

func statusScore() string {
	return `score: "0.2"
name: status-fixture
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
