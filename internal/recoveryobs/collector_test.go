package recoveryobs

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

func TestRootSnapshotDivergence(t *testing.T) {
	snapshot := testScore(t, 1, "pinned")
	snapshotHash, err := snapshot.Hash()
	if err != nil {
		t.Fatal(err)
	}
	head := runstate.ScoreHead{Revision: 1, SemanticHash: runstate.Hash(snapshotHash)}
	path := filepath.Join(t.TempDir(), "partitur.yaml")

	for _, test := range []struct {
		name string
		body []byte
		want bool
	}{
		{name: "same revision and semantic hash", body: scoreSource(1, "pinned"), want: false},
		{name: "same revision different semantic hash", body: scoreSource(1, "changed"), want: true},
		{name: "different revision", body: scoreSource(2, "changed"), want: false},
		{name: "malformed root makes no score claim", body: []byte("not: [valid"), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, test.body, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := rootSnapshotDivergence(path, head)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("rootSnapshotDivergence() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestScoreMatchesRejectsChangedBytesAndSemanticContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "revision-1.yaml")
	original := scoreSource(1, "pinned")
	compiled := testScore(t, 1, "pinned")
	semanticHash, err := compiled.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if !scoreMatches(path, fileHash(original), runstate.Hash(semanticHash)) {
		t.Fatal("scoreMatches() rejected the recorded snapshot")
	}
	if err := os.WriteFile(path, scoreSource(1, "changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if scoreMatches(path, fileHash(original), runstate.Hash(semanticHash)) {
		t.Fatal("scoreMatches() accepted changed snapshot content")
	}
}

func testScore(t *testing.T, revision int, goal string) *score.Score {
	t.Helper()
	compiled, diagnostics := score.Compile(scoreSource(revision, goal))
	if len(diagnostics) != 0 {
		t.Fatalf("score diagnostics = %v", diagnostics)
	}
	return compiled
}

func scoreSource(revision int, goal string) []byte {
	return []byte("score: '0.2'\nname: recovery-observation\nrevision: " + fmt.Sprint(revision) + "\nstatus: finalized\ngoal: " + goal + "\nverification:\n  expectation:\n    intent: pass-existing-tests\n    apply_gate:\n      waived: true\n      reason: test\nparts:\n  reviewer:\n    capabilities: [repo_read]\nmovements:\n  - id: review\n    part: reviewer\n    grants: [repo_read]\n    instruction: inspect\npolicy:\n  allowed_paths: ['**']\n  budget:\n    active_wall_clock_min: 10\n")
}

func fileHash(contents []byte) runstate.Hash {
	digest := sha256.Sum256(contents)
	return runstate.Hash(fmt.Sprintf("sha256:%x", digest))
}

func TestCollectDefaultReachableObservations(t *testing.T) {
	store := collectorStore(t)
	state := runstate.NewState(nil)
	state.Run = runstate.RunRunning
	observations, err := Collect(store, "run-1", recovery.Projection{State: state})
	if err != nil {
		t.Fatal(err)
	}
	if observations.Handoff != recovery.HandoffSafe ||
		observations.AdapterSweep != recovery.SweepSafe ||
		observations.Worktree != recovery.WorktreePresent ||
		observations.CriterionSweep != recovery.SweepSafe ||
		observations.UnjournaledLaunch != recovery.UnjournaledLaunchAbsent {
		t.Fatalf("default observations = %+v", observations)
	}
}

func TestCollectReferencesCoversEveryReferenceKind(t *testing.T) {
	root := t.TempDir()
	collectorGit(t, root, "init", "-b", "main")
	collectorGit(t, root, "config", "user.name", "test")
	collectorGit(t, root, "config", "user.email", "test@example.invalid")
	writeCollectorFile(t, filepath.Join(root, "tracked"), []byte("tracked\n"))
	collectorGit(t, root, "add", "tracked")
	collectorGit(t, root, "commit", "-m", "fixture")
	commit := collectorGit(t, root, "rev-parse", "HEAD")
	collectorGit(t, root, "update-ref", "refs/partitur/test", commit)

	runID := runstate.RunID("run-1")
	snapshot := scoreSource(1, "pinned")
	compiled := testScore(t, 1, "pinned")
	snapshotHash, err := compiled.Hash()
	if err != nil {
		t.Fatal(err)
	}
	resolved, diagnostics := cast.Resolve(nil)
	if len(diagnostics) != 0 {
		t.Fatalf("empty resolved cast diagnostics = %v", diagnostics)
	}
	castBytes, err := resolved.ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	castHash, err := resolved.Hash()
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("artifact")
	proposal := []byte("proposal")
	writeCollectorFile(t, filepath.Join(root, ".partitur", "runs", string(runID), "scores", "revision-1.yaml"), snapshot)
	writeCollectorFile(t, filepath.Join(root, ".partitur", "runs", string(runID), "resolved-cast.yaml"), castBytes)
	writeCollectorFile(t, filepath.Join(root, ".partitur", "runs", string(runID), "artifacts", "report", "attempt-1"), artifact)
	writeCollectorFile(t, filepath.Join(root, ".partitur", "runs", string(runID), "proposals", "proposal-1.json"), proposal)
	state := runstate.NewState(nil)
	state.Artifacts["artifact-1"] = runstate.ArtifactRecord{AttemptID: "attempt-1", LogicalOutputID: "report", ContentHash: fileHash(artifact)}
	state.ChangeSets["attempt-1"] = runstate.ChangeSetRecord{AttemptID: "attempt-1", Ref: "refs/partitur/test", Commit: "git-sha1:" + commit}
	events := []runstate.Event{
		{Type: runstate.EventRunStarted, ScoreRevision: 1, Payload: rawJSON(t, map[string]any{
			"score_file_hash": fileHash(snapshot), "score_hash": snapshotHash, "resolved_cast_hash": castHash,
		})},
		{Type: runstate.EventAmendmentRoutedHuman, Payload: rawJSON(t, map[string]any{
			"proposal_id": "proposal-1", "proposal_record_hash": fileHash(proposal),
		})},
	}
	observations := collectReferences(root, runID, state, events)
	if len(observations) != 5 {
		t.Fatalf("reference observations = %#v, want every kind", observations)
	}
	for _, observation := range observations {
		if !observation.Present {
			t.Fatalf("reference %q was not observed", observation.Kind)
		}
	}
}

func TestCollectBindsPendingPrepareToItsPlan(t *testing.T) {
	store := collectorStore(t)
	root := store.RepositoryRoot()
	snapshot := scoreSource(2, "prepared")
	compiled := testScore(t, 2, "prepared")
	semanticHash, err := compiled.Hash()
	if err != nil {
		t.Fatal(err)
	}
	prepare := runstate.PendingPrepare{
		ID: "prepare-1", ProposalID: "proposal-1", Mode: "auto",
		BaseHead:         runstate.ScoreHead{Revision: 1, SemanticHash: "sha256:base"},
		NewHead:          runstate.ScoreHead{Revision: 2, SemanticHash: runstate.Hash(semanticHash), FileHash: fileHash(snapshot)},
		TargetAttemptIDs: []runstate.AttemptID{"attempt-1"},
	}
	plan := preparePlan{
		ProposalID: prepare.ProposalID, BaseRevision: prepare.BaseHead.Revision, BaseHash: prepare.BaseHead.SemanticHash,
		NewRevision: prepare.NewHead.Revision, NewSnapshotHash: prepare.NewHead.SemanticHash,
		NewSnapshotFileHash: prepare.NewHead.FileHash, SupersededAttemptIDs: prepare.TargetAttemptIDs, Mode: prepare.Mode,
	}
	planPath := filepath.Join(root, ".partitur", "runs", "run-1", "prepares", "prepare-1.json")
	writeJSONFile(t, planPath, plan)
	planBytes, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	prepare.PlanRecordHash = fileHash(planBytes)
	writeCollectorFile(t, filepath.Join(root, ".partitur", "runs", "run-1", "scores", "revision-2.yaml"), snapshot)
	state := runstate.NewState(nil)
	state.Run = runstate.RunRunning
	state.PendingPrepare = &prepare

	observations, err := Collect(store, "run-1", recovery.Projection{State: state})
	if err != nil {
		t.Fatal(err)
	}
	if !observations.Prepare.PlanPresent || !observations.Prepare.SnapshotPresent {
		t.Fatalf("valid prepare observation = %+v", observations.Prepare)
	}

	// Discriminator: preserve a valid plan hash but bind it to another proposal.
	plan.ProposalID = "other-proposal"
	writeJSONFile(t, planPath, plan)
	planBytes, err = os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	prepare.PlanRecordHash = fileHash(planBytes)
	state.PendingPrepare = &prepare
	observations, err = Collect(store, "run-1", recovery.Projection{State: state})
	if err != nil {
		t.Fatal(err)
	}
	if observations.Prepare.PlanPresent || !observations.Prepare.SnapshotPresent {
		t.Fatalf("mismatched prepare observation = %+v", observations.Prepare)
	}
}

func TestCollectDiscriminatesJournaledAdapterAndUnjournaledLaunches(t *testing.T) {
	store := collectorStore(t)
	identity := testProcessIdentity(t)
	state := runstate.NewState(nil)
	state.Run = runstate.RunRunning
	state.Acceptances["attempt-1"] = runstate.Acceptance{Started: true}
	state.AdapterLaunches["attempt-1"] = runstate.AdapterLaunch{AttemptID: "attempt-1", Process: identity}
	projection := recovery.Projection{
		State:              state,
		CurrentHeadAttempt: &recovery.AttemptRecovery{AttemptID: "attempt-1", ScoreRevision: 1, State: runstate.AttemptVerifying, AcceptanceStarted: true},
	}
	attemptRoot := filepath.Join(store.RepositoryRoot(), ".partitur", "work", "run-1", "attempt-1")
	writeHandoff(t, filepath.Join(attemptRoot, "adapter-launch"), identity)
	if err := os.MkdirAll(filepath.Join(attemptRoot, "orphan-launch"), 0o700); err != nil {
		t.Fatal(err)
	}

	observations, err := Collect(store, "run-1", projection)
	if err != nil {
		t.Fatal(err)
	}
	if observations.UnjournaledLaunch != recovery.UnjournaledLaunchMarkerFree {
		t.Fatalf("unjournaled launch = %q, want marker-free orphan", observations.UnjournaledLaunch)
	}
	if err := os.RemoveAll(filepath.Join(attemptRoot, "orphan-launch")); err != nil {
		t.Fatal(err)
	}
	observations, err = Collect(store, "run-1", projection)
	if err != nil {
		t.Fatal(err)
	}
	if observations.UnjournaledLaunch != recovery.UnjournaledLaunchAbsent {
		t.Fatalf("journaled adapter launch was classified unjournaled: %q", observations.UnjournaledLaunch)
	}
}

func TestCollectTreatsLiveSessionAsObservedAndInspectionFailureAsUnverifiable(t *testing.T) {
	command := exec.Command("sh", "-c", "exec sleep 5")
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	start, err := procid.Read(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	live := runstate.ProcessIdentity{PID: command.Process.Pid, SessionID: command.Process.Pid, Start: start}
	state := runstate.NewState(nil)
	state.Run = runstate.RunRunning
	state.AdapterLaunches["attempt-1"] = runstate.AdapterLaunch{AttemptID: "attempt-1", Process: live}
	projection := recovery.Projection{State: state, CurrentHeadAttempt: &recovery.AttemptRecovery{AttemptID: "attempt-1", ScoreRevision: 1, State: runstate.AttemptRunning}}

	observations, err := Collect(collectorStore(t), "run-1", projection)
	if err != nil {
		t.Fatal(err)
	}
	if observations.AdapterSweep != recovery.SweepSafe {
		t.Fatalf("live inspectable session = %q, want safe observation", observations.AdapterSweep)
	}
	state.AdapterLaunches["attempt-1"] = runstate.AdapterLaunch{AttemptID: "attempt-1", Process: runstate.ProcessIdentity{PID: 1, SessionID: 1}}
	observations, err = Collect(collectorStore(t), "run-1", projection)
	if err != nil {
		t.Fatal(err)
	}
	if observations.AdapterSweep != recovery.SweepUnverifiable {
		t.Fatalf("incomplete session identity = %q, want unverifiable", observations.AdapterSweep)
	}
}

func TestCollectReachableAlternativeObservationOutcomes(t *testing.T) {
	t.Run("missing worktree and criterion launch", func(t *testing.T) {
		store := collectorStore(t)
		state := runstate.NewState(nil)
		state.Run = runstate.RunRunning
		state.Acceptances["attempt-1"] = runstate.Acceptance{
			Started:  true,
			Criteria: map[runstate.CriterionID]runstate.CriterionRecord{"criterion-1": {Started: true}},
		}
		observations, err := Collect(store, "run-1", recovery.Projection{
			State:              state,
			CurrentHeadAttempt: &recovery.AttemptRecovery{AttemptID: "attempt-1", ScoreRevision: 1, State: runstate.AttemptVerifying, AcceptanceStarted: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		if observations.Worktree != recovery.WorktreeMissing || observations.CriterionSweep != recovery.SweepUnverifiable {
			t.Fatalf("alternative observations = %+v", observations)
		}
	})

	t.Run("unreadable handoffs", func(t *testing.T) {
		store := collectorStore(t)
		attemptRoot := filepath.Join(store.RepositoryRoot(), ".partitur", "work", "run-1", "attempt-1")
		writeCollectorFile(t, filepath.Join(attemptRoot, "launch", "marker"), []byte("nonce"))
		writeCollectorFile(t, filepath.Join(attemptRoot, "launch", "identity.json"), []byte("invalid"))
		state := runstate.NewState(nil)
		state.Run = runstate.RunRunning
		observations, err := Collect(store, "run-1", recovery.Projection{
			State:              state,
			CurrentHeadAttempt: &recovery.AttemptRecovery{AttemptID: "attempt-1", ScoreRevision: 1, State: runstate.AttemptStarting},
		})
		if err != nil {
			t.Fatal(err)
		}
		if observations.Handoff != recovery.HandoffUnverifiable {
			t.Fatalf("handoff = %q, want unverifiable", observations.Handoff)
		}

		state.Acceptances["attempt-1"] = runstate.Acceptance{Started: true}
		observations, err = Collect(store, "run-1", recovery.Projection{
			State:              state,
			CurrentHeadAttempt: &recovery.AttemptRecovery{AttemptID: "attempt-1", ScoreRevision: 1, State: runstate.AttemptVerifying, AcceptanceStarted: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		if observations.UnjournaledLaunch != recovery.UnjournaledLaunchHandoffUnverifiable {
			t.Fatalf("unjournaled launch = %q, want handoff-unverifiable", observations.UnjournaledLaunch)
		}
	})
}

func TestStabilizeHandoffDeadlineAndError(t *testing.T) {
	launchDir := t.TempDir()
	writeCollectorFile(t, filepath.Join(launchDir, "marker"), []byte("nonce"))
	writeCollectorFile(t, filepath.Join(launchDir, "identity.json"), []byte("not-json"))
	if got := stabilizeHandoffUntil(launchDir, time.Now()); got != recovery.HandoffUnverifiable {
		t.Fatalf("invalid handoff = %q, want unverifiable", got)
	}
	if err := os.Remove(filepath.Join(launchDir, "identity.json")); err != nil {
		t.Fatal(err)
	}
	marker, err := os.Open(filepath.Join(launchDir, "marker"))
	if err != nil {
		t.Fatal(err)
	}
	defer marker.Close()
	if err := syscall.Flock(int(marker.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(marker.Fd()), syscall.LOCK_UN)
	if got := stabilizeHandoffUntil(launchDir, time.Now()); got != recovery.HandoffUnverifiable {
		t.Fatalf("expired held handoff = %q, want unverifiable", got)
	}
}

func collectorStore(t *testing.T) *runstore.Store {
	t.Helper()
	store, err := runstore.New(t.TempDir(), faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func writeCollectorFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeCollectorFile(t, path, contents)
}

func testProcessIdentity(t *testing.T) runstate.ProcessIdentity {
	t.Helper()
	start, err := procid.Read(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return runstate.ProcessIdentity{PID: 1, SessionID: 1, Start: start}
}

func writeHandoff(t *testing.T, directory string, identity runstate.ProcessIdentity) {
	t.Helper()
	start := map[string]any{"platform": identity.Start.Platform()}
	switch value := identity.Start.(type) {
	case runstate.LinuxStartIdentity:
		start["boot_id"], start["start_ticks"] = value.BootID, value.StartTicks
	case runstate.DarwinStartIdentity:
		start["start_tvsec"], start["start_tvusec"] = value.StartTVSec, value.StartTVUsec
	default:
		t.Fatalf("unsupported test start identity %T", identity.Start)
	}
	writeCollectorFile(t, filepath.Join(directory, "marker"), []byte("nonce"))
	writeJSONFile(t, filepath.Join(directory, "identity.json"), map[string]any{
		"nonce": "nonce", "pid": identity.PID, "session_id": identity.SessionID, "start_identity": start,
	})
}

func collectorGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return string(bytes.TrimSpace(output))
}

func rawJSON(t *testing.T, value any) []byte {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}
