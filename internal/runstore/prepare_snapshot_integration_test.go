package runstore_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/amendmentexec"
	"github.com/BeomSeogKim/Partitur/internal/driver"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
	"github.com/BeomSeogKim/Partitur/internal/validate"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

func TestApprovePreparePinsTheSnapshotItWrites(t *testing.T) {
	root := t.TempDir()
	writePrepareFixture(t, root, "partitur.yaml", `{"score":"0.2","name":"prepare-snapshot","revision":1,"status":"draft","goal":"prepare a writer","open_questions":[],"draft":{"interview_movement":"interview"},"parts":{"interview":{"capabilities":["repo_read"],"read_only":true}},"movements":[{"id":"interview","phase":"draft","part":"interview","grants":["repo_read"],"may_propose":true,"instruction":"prepare","outputs":[],"acceptance":{"hard":[],"human_gate":"never","review":[]}}],"policy":{"allowed_paths":[],"budget":{"active_wall_clock_min":10,"retries_per_movement":0},"amendment":{"auto":"off"},"side_effects":[]}}`)
	writePrepareFixture(t, root, ".partitur/cast.yaml", `{"cast":"0.1","performers":{"worker":{"adapter":"fixture","model":"fixture"}},"bindings":{"interview":{"performer":"worker"}}}`)
	for _, args := range [][]string{{"init"}, {"config", "user.name", "Partitur Test"}, {"config", "user.email", "partitur@example.invalid"}, {"add", "partitur.yaml", ".partitur/cast.yaml"}, {"commit", "-m", "fixture"}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	preparation, result := validate.Prepare()
	if preparation == nil || result.HasDiagnostics() {
		t.Fatalf("prepare result = %#v", result)
	}
	started, err := workspace.Start(preparation, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(root, faultpoint.Nop{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.AcquireDriver(started.RunID, prepareMovementSeeds(preparation.Score))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Release()
	baseHash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	operations := []any{map[string]any{"op": "replace", "path": "/movements/0/instruction", "value": "prepare the writer\n"}}
	amendment, err := json.Marshal(map[string]any{"base_revision": 1, "base_hash": baseHash, "operations": operations, "reason": "adapter request"})
	if err != nil {
		t.Fatal(err)
	}
	proposal := driver.AdapterProposal{Store: store, Authority: authority, RunID: started.RunID, AttemptID: "attempt-1", MovementID: "interview", PartID: "interview", ProposalID: "proposal-1", DecisionID: "decision-1", Event: protocol.ProposalEvent{ID: "emitted-1", Amendment: amendment, RequiresDecision: true}}
	dispositioner := amendmentexec.New()
	disposition, err := dispositioner.PrepareAdapterProposal(context.Background(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	if disposition.RouteDescriptor == nil || disposition.AppendRoute == nil {
		t.Fatal("adapter-origin draft amendment was not routed")
	}
	appendPrepareProposalSource(t, authority, started.RunID, disposition.RouteDescriptor)
	if err := disposition.AppendRoute(context.Background()); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"decision_id": "decision-1", "decision_type": "amendment", "proposal_id": "proposal-1", "routed_reason": "draft_phase", "blocking": true, "emitted_id": "emitted-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Append(runstate.Event{RunID: started.RunID, ScoreRevision: 1, MovementID: "interview", AttemptID: "attempt-1", Type: runstate.EventDecisionRequested, Payload: payload}, "test.decision.requested"); err != nil {
		t.Fatal(err)
	}
	approveContext, cancelApprove := context.WithCancel(context.Background())
	defer cancelApprove()
	done := make(chan error, 1)
	go func() { done <- dispositioner.ApproveRouted(approveContext, store, started.RunID, "decision-1") }()
	var prepare runstate.PendingPrepare
	deadline := time.After(5 * time.Second)
	for {
		input, err := store.LoadRunInput(started.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if input.Projection.State.PendingPrepare != nil {
			prepare = *input.Projection.State.PendingPrepare
			break
		}
		select {
		case err := <-done:
			input, loadErr := store.LoadRunInput(started.RunID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if input.Projection.State.PendingPrepare == nil {
				t.Fatalf("approve returned before preparing: %v", err)
			}
			prepare = *input.Projection.State.PendingPrepare
			goto prepared
		case <-deadline:
			t.Fatal("approve did not prepare the adapter-origin amendment")
		case <-time.After(10 * time.Millisecond):
		}
	}

prepared:
	snapshot, err := os.ReadFile(filepath.Join(root, ".partitur", "runs", string(started.RunID), "scores", "revision-2.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	compiled, diagnostics := score.Compile(snapshot)
	if len(diagnostics) != 0 {
		t.Fatalf("written snapshot diagnostics = %v", diagnostics)
	}
	writtenHash, err := compiled.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if string(prepare.NewHead.SemanticHash) != writtenHash {
		t.Fatalf("prepare semantic hash = %s, compiled written snapshot hash = %s", prepare.NewHead.SemanticHash, writtenHash)
	}
	cancelApprove()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("approving command did not stop after cancellation")
	}
}

func writePrepareFixture(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func prepareMovementSeeds(compiled *score.Score) []runstate.MovementSeed {
	movements := compiled.Movements()
	result := make([]runstate.MovementSeed, 0, len(movements))
	for _, movement := range movements {
		repoWrite := false
		for _, grant := range movement.Grants {
			repoWrite = repoWrite || grant == "repo_write"
		}
		result = append(result, runstate.MovementSeed{ID: runstate.MovementID(movement.ID), Initial: runstate.MovementPending, RepoWrite: repoWrite, HasDependencies: len(movement.Needs) != 0})
	}
	return result
}

func appendPrepareProposalSource(t *testing.T, authority *runstore.Driver, runID runstate.RunID, route map[string]any) {
	t.Helper()
	versions := map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}
	events := []runstate.Event{
		{RunID: runID, ScoreRevision: 1, MovementID: "interview", Type: runstate.EventMovementReady, Payload: []byte(`{}`)},
		{RunID: runID, ScoreRevision: 1, MovementID: "interview", Type: runstate.EventMovementStarted, Payload: []byte(`{}`)},
		{RunID: runID, ScoreRevision: 1, MovementID: "interview", PartID: "interview", AttemptID: "attempt-1", Type: runstate.EventPerformerSelected, Payload: preparePayload(t, map[string]any{"reason": "initial", "performer_id": "worker", "adapter_id": "fixture", "model": "fixture"})},
		{RunID: runID, ScoreRevision: 1, MovementID: "interview", PartID: "interview", AttemptID: "attempt-1", Type: runstate.EventAttemptStarted, Payload: preparePayload(t, map[string]any{"attempt_number": 1, "adapter_process": map[string]any{"pid": os.Getpid(), "session_id": os.Getpid(), "start_identity": map[string]any{"platform": "linux", "boot_id": "fixture", "start_ticks": "1"}}, "granted_authority": map[string]any{"paths_rw": []any{}, "paths_ro": []any{"**"}, "shell": false, "network": false}, "identity_versions": versions})},
		{RunID: runID, ScoreRevision: 1, MovementID: "interview", PartID: "interview", AttemptID: "attempt-1", Type: runstate.EventAdapterProbed, Payload: preparePayload(t, map[string]any{"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": false, "shell": false, "network": false, "resumable_sessions": false}, "enforcement": map[string]any{"path_grants": true, "read_only": true, "network_grants": true, "shell_grants": true, "read_grants": true}, "negotiated_features": []any{}, "truncated_resolutions": []any{}, "delivered_resolutions": []any{}, "delivered_feedback": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:dependency", "identity_versions": versions})},
		{RunID: runID, ScoreRevision: 1, MovementID: "interview", PartID: "interview", AttemptID: "attempt-1", Type: runstate.EventAttemptBlocked, Payload: preparePayload(t, map[string]any{"raised": []any{map[string]any{"decision_id": "decision-1", "emitted_id": "emitted-1", "kind": "proposal", "proposal_id": "proposal-1", "blocking": true, "route": route}}, "pending_decision_ids": []any{"decision-1"}})},
	}
	for _, event := range events {
		if _, err := authority.Append(event, faultpoint.ReceiptAddress("test."+string(event.Type))); err != nil {
			t.Fatal(err)
		}
	}
}

func preparePayload(t *testing.T, value any) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
