package status

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestMovementSeedsProjectFinality(t *testing.T) {
	final := movementSeeds(mustCompile(t, statusScore()))
	if len(final) != 1 || !final[0].Final {
		t.Fatalf("final seeds = %#v", final)
	}
	waivedSource := strings.Replace(
		statusScore(),
		"      require: [verified]\n  final_movement: inspect",
		"      waived: true\n      reason: fixture waiver",
		1,
	)
	waived := movementSeeds(mustCompile(t, waivedSource))
	if len(waived) != 1 || waived[0].Final {
		t.Fatalf("waived seeds = %#v", waived)
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

func TestStatusRefusesHashMismatchedReviewEvidence(t *testing.T) {
	root := t.TempDir()
	source := strings.Replace(statusScore(), `      - id: report
        kind: artifact`, `      - id: findings
        kind: findings`, 1)
	source = strings.Replace(source, "require: [verified]", "require: [reviewed]", 1)
	compiled := mustCompile(t, strings.Replace(source, "    acceptance:\n      hard:\n        - id: report-present\n          artifact: report", "    acceptance:\n      review:\n        - id: review\n          findings: findings\n          rubric:\n            - coverage", 1))
	state := runstate.NewState(movementSeeds(compiled))
	state.Attempts["attempt-1"] = runstate.Attempt{MovementID: "inspect", ScoreRevision: 1, State: runstate.AttemptCompleted}
	state.Acceptances["attempt-1"] = runstate.Acceptance{EvaluationCompleted: true, SubjectTree: "git-sha1:subject", ReviewOutcome: "CONTESTED"}
	state.Artifacts["findings@attempt-1"] = runstate.ArtifactRecord{AttemptID: "attempt-1", LogicalOutputID: "findings", Kind: "findings", ContentHash: "sha256:validated"}
	path := filepath.Join(root, ".partitur", "runs", "run-1", "artifacts", "findings")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "attempt-1"), []byte(`{"findings":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateReviewArtifacts(root, "run-1", map[uint64]*score.Score{1: compiled}, state); !errors.Is(err, ErrRequiredInput) {
		t.Fatalf("error = %v, want ErrRequiredInput", err)
	}
}

func TestStatusReadRevisionTwoContestedReviewUsesItsPinnedSnapshot(t *testing.T) {
	root := t.TempDir()
	runID := "run-1"
	runRoot := filepath.Join(root, ".partitur", "runs", runID)
	if err := os.MkdirAll(filepath.Join(runRoot, "scores"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRoot, "scores", "revision-1.yaml"), []byte(statusScore()), 0o600); err != nil {
		t.Fatal(err)
	}
	revisionTwo := strings.Replace(statusScore(), "revision: 1", "revision: 2", 1)
	revisionTwo = strings.Replace(revisionTwo, "require: [verified]", "require: [reviewed]", 1)
	revisionTwo = strings.Replace(revisionTwo, `      - id: report
        kind: artifact`, `      - id: findings
        kind: findings`, 1)
	revisionTwo = strings.Replace(revisionTwo, `    acceptance:
      hard:
        - id: report-present
          artifact: report`, `    acceptance:
      review:
        - id: review
          findings: findings
          rubric: [coverage]
      human_gate: on_contested`, 1)
	if compiled := mustCompile(t, revisionTwo); compiled.Revision() != 2 {
		t.Fatalf("revision two score = %d", compiled.Revision())
	}
	if err := os.WriteFile(filepath.Join(runRoot, "scores", "revision-2.yaml"), []byte(revisionTwo), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init")
	runGit(t, root, "config", "user.name", "Partitur Test")
	runGit(t, root, "config", "user.email", "partitur@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "evidence.txt"), []byte("review this line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "evidence.txt")
	runGit(t, root, "commit", "-m", "subject")
	subjectTree := "git-sha1:" + strings.TrimSpace(runGitOutput(t, root, "rev-parse", "HEAD^{tree}"))
	findings := fmt.Sprintf(`{"schema":"partitur/findings+json;v=1","subject_tree":%q,"coverage":[{"rubric":"coverage","conclusion":"findings_raised"}],"findings":[{"id":"fixture-blocker","rubric":"coverage","summary":"fixture blocker","blocking":true,"evidence":[{"path":"evidence.txt","line":1}]}]}`, subjectTree)
	findingsHash := sha256.Sum256([]byte(findings))
	path := filepath.Join(runRoot, "artifacts", "findings")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "attempt-2"), []byte(findings), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Mutate(runstate.RunID(runID), "", func(transaction *runstore.Txn) error {
		for _, event := range revisionTwoContestedEvents(t, runID, subjectTree, fmt.Sprintf("sha256:%x", findingsHash), len(findings), statusScore(), revisionTwo) {
			if _, err := transaction.At("status.revision_two.fixture").Append(event); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	report, err := Read(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	marks := report.Run.Movements[0].Marks
	if len(marks) != 1 || marks[0].Grade != "REVIEWED" || marks[0].AttemptID != "attempt-2" ||
		marks[0].ScoreRevision != 2 || marks[0].ReviewOutcome != "CONTESTED" || marks[0].FindingsInstanceID != "findings@attempt-2" {
		t.Fatalf("revision-two review marks = %#v", marks)
	}
}

func TestReadRejectsSubstitutedAmendedSnapshot(t *testing.T) {
	root := t.TempDir()
	runID := "run-1"
	runRoot := filepath.Join(root, ".partitur", "runs", runID)
	revisionOne := statusScore()
	revisionTwo := strings.Replace(revisionOne, "revision: 1", "revision: 2", 1)
	if err := os.MkdirAll(filepath.Join(runRoot, "scores"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRoot, "scores", "revision-1.yaml"), []byte(revisionOne), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRoot, "scores", "revision-2.yaml"), []byte(revisionTwo), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	events := revisionTwoContestedEvents(t, runID, "git-sha1:subject", "sha256:findings", 0, revisionOne, revisionTwo)
	if err := store.Mutate(runstate.RunID(runID), "", func(transaction *runstore.Txn) error {
		for _, event := range events {
			if _, err := transaction.At("status.substituted_amended_snapshot.fixture").Append(event); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	substituted := strings.Replace(revisionTwo, "Write the report.", "Write a different report.", 1)
	if err := os.WriteFile(filepath.Join(runRoot, "scores", "revision-2.yaml"), []byte(substituted), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root, runID); !errors.Is(err, ErrSnapshot) {
		t.Fatalf("error = %v, want substituted amended snapshot rejection", err)
	}
}

func TestReadRejectsMissingInitialRevisionAfterLegalAmendment(t *testing.T) {
	root := t.TempDir()
	runID := "run-1"
	runRoot := filepath.Join(root, ".partitur", "runs", runID)
	revisionOne := statusScore()
	revisionTwo := strings.Replace(revisionOne, "revision: 1", "revision: 2", 1)
	if err := os.MkdirAll(filepath.Join(runRoot, "scores"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRoot, "scores", "revision-2.yaml"), []byte(revisionTwo), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	events := revisionTwoContestedEvents(t, runID, "git-sha1:subject", "sha256:findings", 0, revisionOne, revisionTwo)
	if err := store.Mutate(runstate.RunID(runID), "", func(transaction *runstore.Txn) error {
		for _, event := range events[:3] {
			if _, err := transaction.At("status.missing_initial_revision.fixture").Append(event); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := Read(root, runID); !errors.Is(err, ErrSnapshot) {
		t.Fatalf("error = %v, want unavailable snapshot", err)
	}
}

func TestReadRejectsRecordedInitialRevisionZero(t *testing.T) {
	root := t.TempDir()
	runID := "run-1"
	runRoot := filepath.Join(root, ".partitur", "runs", runID)
	source := statusScore()
	if err := os.MkdirAll(filepath.Join(runRoot, "scores"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRoot, "scores", "revision-1.yaml"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Mutate(runstate.RunID(runID), "", func(transaction *runstore.Txn) error {
		payload, err := json.Marshal(runStartedPayload(t, source))
		if err != nil {
			return err
		}
		_, err = transaction.At("status.initial_revision_zero.fixture").Append(runstate.Event{
			RunID: runstate.RunID(runID), ScoreRevision: 0, Type: runstate.EventRunStarted, Payload: payload,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := Read(root, runID); !errors.Is(err, ErrSnapshot) {
		t.Fatalf("error = %v, want unavailable snapshot", err)
	}
}

func TestProjectCarriesVerifiedProvenanceAndShippingRecovery(t *testing.T) {
	compiled := mustCompile(t, statusScore())
	state := runstate.NewState(movementSeeds(compiled))
	state.Run = runstate.RunSucceeded
	state.ScoreHead = runstate.ScoreHead{Revision: 1, SemanticHash: "sha256:score", FileHash: "sha256:file"}
	state.Movements["inspect"] = runstate.MovementSucceeded
	state.Attempts["failed"] = runstate.Attempt{MovementID: "inspect", ScoreRevision: 1, State: runstate.AttemptFailed}
	state.Attempts["passed"] = runstate.Attempt{MovementID: "inspect", ScoreRevision: 1, State: runstate.AttemptCompleted}
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

// TestProjectGateOnlyWriteCarriesApprovedMarkWithoutCarryover supplies the
// accepted projection directly. The compiler admits this shape, but the
// current driver interrupts it before requesting the gate because it has no
// hard criterion. This test pins the specified projection behavior, not a
// live driver path; TestRunHumanGateApprovalProjectsApprovedMark covers that.
func TestProjectGateOnlyWriteCarriesApprovedMarkWithoutCarryover(t *testing.T) {
	compiled := mustCompile(t, gateOnlyWriteScore())
	// Mismatched scope and revision are defensive, fail-closed checks. Driver
	// and recovery-produced journals bind both values to the accepted attempt.
	for _, test := range []struct {
		name               string
		attemptState       runstate.AttemptState
		resolutionScope    string
		resolutionRevision uint64
		wantMarks          int
	}{
		{name: "completed", attemptState: runstate.AttemptCompleted, resolutionScope: "git-sha1:subject", resolutionRevision: 1, wantMarks: 1},
		{name: "subject mismatch", attemptState: runstate.AttemptCompleted, resolutionScope: "git-sha1:other", resolutionRevision: 1, wantMarks: 0},
		{name: "revision mismatch", attemptState: runstate.AttemptCompleted, resolutionScope: "git-sha1:subject", resolutionRevision: 2, wantMarks: 0},
		{name: "superseded", attemptState: runstate.AttemptSuperseded, resolutionScope: "git-sha1:subject", resolutionRevision: 1, wantMarks: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := runstate.NewState(movementSeeds(compiled))
			state.Run = runstate.RunSucceeded
			state.Movements["write"] = runstate.MovementSucceeded
			state.Attempts["attempt-1"] = runstate.Attempt{
				MovementID: "write", ScoreRevision: 1, State: test.attemptState,
			}
			state.Acceptances["attempt-1"] = runstate.Acceptance{
				Started: true, EvaluationCompleted: true, SubjectTree: "git-sha1:subject",
				PlannedCriterionIDs: []runstate.CriterionID{}, Criteria: map[runstate.CriterionID]runstate.CriterionRecord{},
			}
			state.ResolvedHumanGates["attempt-1"] = runstate.HumanGateResolution{
				DecisionID: "gate-decision", MovementID: "write", AttemptID: "attempt-1", ScoreRevision: test.resolutionRevision,
				GateID: "gate-attempt-1", Scope: runstate.HumanGateScope{SubjectTree: test.resolutionScope}, Disposition: "approved",
			}

			marks := project("run-1", compiled, runstore.ReadReplayResult{State: state}).Run.Movements[0].Marks
			if len(marks) != test.wantMarks {
				t.Fatalf("marks = %#v, want %d", marks, test.wantMarks)
			}
			if test.wantMarks != 0 && (marks[0].Grade != "APPROVED" || marks[0].GateDecisionID != "gate-decision" ||
				marks[0].AttemptID != "attempt-1" || marks[0].SubjectTree != "git-sha1:subject" ||
				marks[0].ScoreRevision != 1 || len(marks[0].Criteria) != 0) {
				t.Fatalf("approved gate-only mark = %#v", marks[0])
			}
		})
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
		payload, err := json.Marshal(runStartedPayload(t, statusScore()))
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

func runStartedPayload(t *testing.T, source string) map[string]any {
	t.Helper()
	compiled := mustCompile(t, source)
	semanticHash, err := compiled.Hash()
	if err != nil {
		t.Fatal(err)
	}
	fileHash := sha256.Sum256([]byte(source))
	return map[string]any{
		"base_commit": "base", "base_tree": "tree", "score_hash": semanticHash,
		"score_file_hash": fmt.Sprintf("sha256:%x", fileHash), "resolved_cast_hash": "sha256:cast",
		"identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}},
	}
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

func gateOnlyWriteScore() string {
	return `score: "0.2"
name: gate-only-write
revision: 1
status: finalized
goal: Write only after human approval.
verification:
  expectation:
    intent: pass-existing-tests
    apply_gate:
      waived: true
      reason: fixture waiver
parts:
  writer:
    capabilities: [repo_read, repo_write]
movements:
  - id: write
    part: writer
    grants: [repo_read, repo_write]
    instruction: Write the change.
    outputs:
      - id: change-set
        kind: change_set
    acceptance:
      human_gate: always
policy:
  allowed_paths: ["**"]
  budget:
    active_wall_clock_min: 10
`
}

func revisionTwoContestedEvents(t *testing.T, runID, subjectTree, findingsHash string, findingsSize int, revisionOne, revisionTwo string) []runstate.Event {
	t.Helper()
	versions := map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}
	initial := runStartedPayload(t, revisionOne)
	updated := runStartedPayload(t, revisionTwo)
	payload := func(value map[string]any) json.RawMessage {
		encoded, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		return encoded
	}
	event := func(revision uint64, eventType runstate.EventType, value map[string]any) runstate.Event {
		return runstate.Event{RunID: runstate.RunID(runID), ScoreRevision: revision, Type: eventType, Payload: payload(value)}
	}
	return []runstate.Event{
		event(1, runstate.EventRunStarted, initial),
		event(1, runstate.EventAmendmentApprovalPrepared, map[string]any{"prepare_id": "prepare-1", "proposal_id": "proposal-1", "mode": "auto", "envelope_class": "NARROW_PATHS", "base_revision": 1, "base_hash": initial["score_hash"], "new_revision": 2, "new_snapshot_hash": updated["score_hash"], "new_snapshot_file_hash": updated["score_file_hash"], "plan_record_hash": "sha256:plan", "target_attempt_ids": []any{}, "observed_authority_epoch": 0, "quiesce_deadline": "2026-07-26T00:00:00.000Z", "classifier_version": 1, "identity_versions": versions}),
		event(2, runstate.EventAmendmentApproved, map[string]any{"proposal_id": "proposal-1", "mode": "auto", "envelope_class": "NARROW_PATHS", "base_revision": 1, "base_hash": initial["score_hash"], "classifier_version": 1, "new_revision": 2, "new_snapshot_hash": updated["score_hash"], "new_snapshot_file_hash": updated["score_file_hash"], "typed_delta": []any{}, "actual_impact": map[string]any{"score_changes": []any{}, "authority": map[string]any{"allowed_paths": map[string]any{"added": []any{}, "removed": []any{}}, "grants": []any{}, "side_effects": map[string]any{"added": []any{}, "removed": []any{}}}, "budget": map[string]any{}}, "head_movements": []any{map[string]any{"id": "inspect", "initial": "PENDING", "repo_write": false, "has_dependencies": false, "final": true}}, "superseded_attempt_ids": []any{}, "obsoleted_decision_ids": []any{}, "finalization": false, "identity_versions": versions}),
		{RunID: runstate.RunID(runID), ScoreRevision: 2, MovementID: "inspect", Type: runstate.EventMovementReady, Payload: payload(map[string]any{})},
		{RunID: runstate.RunID(runID), ScoreRevision: 2, MovementID: "inspect", Type: runstate.EventMovementStarted, Payload: payload(map[string]any{})},
		{RunID: runstate.RunID(runID), ScoreRevision: 2, MovementID: "inspect", AttemptID: "attempt-2", Type: runstate.EventPerformerSelected, Payload: payload(map[string]any{"reason": "initial", "performer_id": "reader", "adapter_id": "adapter", "model": "model"})},
		{RunID: runstate.RunID(runID), ScoreRevision: 2, MovementID: "inspect", AttemptID: "attempt-2", Type: runstate.EventAttemptStarted, Payload: payload(map[string]any{"attempt_number": 1, "adapter_process": map[string]any{"pid": 1, "session_id": 1, "start_identity": map[string]any{"platform": "linux", "boot_id": "boot", "start_ticks": "1"}}, "granted_authority": map[string]any{"paths_rw": []any{}, "paths_ro": []any{}, "shell": false, "network": false}, "identity_versions": versions})},
		{RunID: runstate.RunID(runID), ScoreRevision: 2, MovementID: "inspect", AttemptID: "attempt-2", Type: runstate.EventAdapterProbed, Payload: payload(map[string]any{"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": false, "shell": false, "network": false, "resumable_sessions": false}, "enforcement": map[string]any{"path_grants": false, "read_only": false, "network_grants": false, "shell_grants": false, "read_grants": false}, "negotiated_features": []any{}, "truncated_resolutions": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:dependency", "identity_versions": versions})},
		{RunID: runstate.RunID(runID), ScoreRevision: 2, MovementID: "inspect", AttemptID: "attempt-2", Type: runstate.EventPerformerCompleted, Payload: payload(map[string]any{"session_hint_stored": false})},
		{RunID: runstate.RunID(runID), ScoreRevision: 2, MovementID: "inspect", AttemptID: "attempt-2", Type: runstate.EventVerificationPassed, Payload: payload(map[string]any{})},
		{RunID: runstate.RunID(runID), ScoreRevision: 2, MovementID: "inspect", AttemptID: "attempt-2", Type: runstate.EventArtifactRecorded, Payload: payload(map[string]any{"logical_output_id": "findings", "kind": "findings", "content_hash": findingsHash, "size_bytes": findingsSize, "source_path": "findings.json"})},
		{RunID: runstate.RunID(runID), ScoreRevision: 2, MovementID: "inspect", AttemptID: "attempt-2", Type: runstate.EventAcceptanceStarted, Payload: payload(map[string]any{"subject_tree": subjectTree, "acceptance_spec_hash": "sha256:acceptance", "planned_criterion_ids": []any{"review", "partitur.artifact.findings"}, "identity_versions": versions})},
		{RunID: runstate.RunID(runID), ScoreRevision: 2, MovementID: "inspect", AttemptID: "attempt-2", Type: runstate.EventCriterionStarted, Payload: payload(map[string]any{"criterion_id": "review", "criterion_spec_hash": "sha256:review", "subject_tree": subjectTree, "identity_versions": versions})},
		{RunID: runstate.RunID(runID), ScoreRevision: 2, MovementID: "inspect", AttemptID: "attempt-2", Type: runstate.EventCriterionCompleted, Payload: payload(map[string]any{"criterion_id": "review", "criterion_spec_hash": "sha256:review", "subject_tree": subjectTree, "outcome": "PASS", "duration_ms": 1, "identity_versions": versions})},
		{RunID: runstate.RunID(runID), ScoreRevision: 2, MovementID: "inspect", AttemptID: "attempt-2", Type: runstate.EventCriterionStarted, Payload: payload(map[string]any{"criterion_id": "partitur.artifact.findings", "criterion_spec_hash": "sha256:generated", "subject_tree": subjectTree, "identity_versions": versions})},
		{RunID: runstate.RunID(runID), ScoreRevision: 2, MovementID: "inspect", AttemptID: "attempt-2", Type: runstate.EventCriterionCompleted, Payload: payload(map[string]any{"criterion_id": "partitur.artifact.findings", "criterion_spec_hash": "sha256:generated", "subject_tree": subjectTree, "outcome": "PASS", "duration_ms": 1, "identity_versions": versions})},
		{RunID: runstate.RunID(runID), ScoreRevision: 2, MovementID: "inspect", AttemptID: "attempt-2", Type: runstate.EventAcceptanceEvaluationCompleted, Payload: payload(map[string]any{"subject_tree": subjectTree, "acceptance_spec_hash": "sha256:acceptance", "criterion_outcomes": []any{map[string]any{"criterion_id": "review", "criterion_spec_hash": "sha256:review", "outcome": "PASS"}, map[string]any{"criterion_id": "partitur.artifact.findings", "criterion_spec_hash": "sha256:generated", "outcome": "PASS"}}, "review_outcome": "CONTESTED", "blocking_findings": []any{map[string]any{"artifact_instance_id": "findings@attempt-2", "finding_id": "fixture-blocker"}}, "identity_versions": versions})},
	}
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

func runGitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", arguments, err)
	}
	return string(output)
}
