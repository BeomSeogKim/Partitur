package driver

import (
	"encoding/json"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

func TestComposeCandidateRecordsAppliedSequenceAndFullMergeContributors(t *testing.T) {
	preparation, store, authority, started, ours, theirs := candidateFanInCleanFixture(t)
	defer authority.Release()
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	expectedContributors := []workspace.CompositionContributor{
		{MovementID: "ours", ChangeSetID: "sha256:ours", BaseTree: input.BaseTree, ResultTree: ours},
		{MovementID: "theirs", ChangeSetID: "sha256:theirs", BaseTree: input.BaseTree, ResultTree: theirs},
	}
	expected := workspace.Compose(workspace.CompositionInput{RepositoryRoot: preparation.RepositoryRoot, BaseTree: input.BaseTree, Contributors: expectedContributors})
	if expected.ResultTree == "" {
		t.Fatalf("candidate precomposition = %+v", expected)
	}
	if err := ComposeCandidate(store, authority, input, 1, time.Now, func() (string, error) { return "candidate-composition", nil }); err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var event runstate.Event
	for _, candidate := range journal.Events {
		if candidate.Type == runstate.EventApplicationCandidateRecorded {
			event = candidate
		}
	}
	if event.EventID == "" {
		t.Fatal("candidate composition journal inspection observed zero application_candidate.recorded events")
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["result_tree"] != expected.ResultTree {
		t.Fatalf("candidate result tree = %v, want %q", payload["result_tree"], expected.ResultTree)
	}
	if got := payload["ordered_change_sets"].([]any); len(got) != 2 || got[0] != "sha256:ours" || got[1] != "sha256:theirs" {
		t.Fatalf("candidate ordered applied change sets = %#v", got)
	}
	if got := payload["contributors"].([]any); len(got) != 2 || got[0].(map[string]any)["movement_id"] != "ours" || got[1].(map[string]any)["movement_id"] != "theirs" {
		t.Fatalf("candidate full merge contributors = %#v", got)
	}
	command := exec.Command("git", "rev-parse", "refs/partitur/runs/"+string(started.RunID)+"/candidate^{tree}")
	command.Dir = preparation.RepositoryRoot
	output, err := command.Output()
	if err != nil || "git-sha1:"+strings.TrimSpace(string(output)) != expected.ResultTree {
		t.Fatalf("candidate pin = %q, %v; want %q", output, err, expected.ResultTree)
	}
}

func TestComposeCandidateUsesPinnedTopologicalDeclarationOrder(t *testing.T) {
	preparation, store, authority, started, a, z := fanInFixtureWithWriterIDs(t, false, "a", "z", []any{"z"}, false, true)
	defer authority.Release()
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}

	expectedContributors := []workspace.CompositionContributor{
		{MovementID: "z", ChangeSetID: "sha256:z", BaseTree: input.BaseTree, ResultTree: z},
		{MovementID: "a", ChangeSetID: "sha256:a", BaseTree: input.BaseTree, ResultTree: a},
	}
	expected := workspace.Compose(workspace.CompositionInput{
		RepositoryRoot: preparation.RepositoryRoot,
		BaseTree:       input.BaseTree,
		Contributors:   expectedContributors,
	})
	if expected.ResultTree == "" {
		t.Fatalf("independent topological candidate composition = %+v", expected)
	}
	wantDependencyHash, err := canonical.Hash(canonical.DomainCandidateComposition, map[string]any{
		"composition_mode": "merge", "base_tree": input.BaseTree,
		"contributors": []any{
			map[string]any{"movement_id": "z", "change_set_id": "sha256:z"},
			map[string]any{"movement_id": "a", "change_set_id": "sha256:a"},
		},
		"composition_algorithm_version": float64(canonical.CompositionAlgorithmVersion),
		"composition_environment_hash":  expected.EnvironmentHash,
	})
	if err != nil {
		t.Fatal(err)
	}

	contributors, err := CandidateCompositionContributors(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(contributors) != 2 || contributors[0] != expectedContributors[0] || contributors[1] != expectedContributors[1] {
		t.Fatalf("candidate contributors = %#v, want stable topological order %#v", contributors, expectedContributors)
	}
	if err := ComposeCandidate(store, authority, input, 1, time.Now, func() (string, error) { return "candidate-topological", nil }); err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var event runstate.Event
	for _, candidate := range journal.Events {
		if candidate.Type == runstate.EventApplicationCandidateRecorded {
			event = candidate
		}
	}
	if event.EventID == "" {
		t.Fatal("candidate composition recorded no application candidate")
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if got := payload["contributors"].([]any); len(got) != 2 ||
		got[0].(map[string]any)["movement_id"] != "z" || got[0].(map[string]any)["change_set_id"] != "sha256:z" ||
		got[1].(map[string]any)["movement_id"] != "a" || got[1].(map[string]any)["change_set_id"] != "sha256:a" {
		t.Fatalf("candidate full contributors = %#v, want %#v", got, expectedContributors)
	}
	if got := payload["ordered_change_sets"].([]any); len(got) != 2 || got[0] != "sha256:z" || got[1] != "sha256:a" {
		t.Fatalf("candidate deduplicated applied change sets = %#v, want [sha256:z sha256:a]", got)
	}
	if got := payload["result_tree"]; got != expected.ResultTree {
		t.Fatalf("candidate tree = %v, want independently composed %q", got, expected.ResultTree)
	}
	if got := payload["candidate_composition_dependency_hash"]; got != wantDependencyHash {
		t.Fatalf("candidate dependency hash = %v, want %q", got, wantDependencyHash)
	}
}

func TestComposeCandidateWaivedFoldsComposedWriterCandidateIntoRunSucceeded(t *testing.T) {
	preparation, store, authority, started, ours, theirs := fanInWaivedFixture(t, false)
	defer authority.Release()
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	contributors := []workspace.CompositionContributor{
		{MovementID: "ours", ChangeSetID: "sha256:ours", BaseTree: input.BaseTree, ResultTree: ours},
		{MovementID: "theirs", ChangeSetID: "sha256:theirs", BaseTree: input.BaseTree, ResultTree: theirs},
	}
	expected := workspace.Compose(workspace.CompositionInput{
		RepositoryRoot: preparation.RepositoryRoot, BaseTree: input.BaseTree, Contributors: contributors,
	})
	if expected.ResultTree == "" {
		t.Fatalf("waived candidate precomposition = %+v", expected)
	}
	if err := ComposeCandidate(store, authority, input, 1, time.Now, func() (string, error) { return "waived-candidate", nil }); err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var succeeded runstate.Event
	for _, event := range journal.Events {
		if event.Type == runstate.EventApplicationCandidateRecorded {
			t.Fatal("waived candidate composition recorded application_candidate.recorded")
		}
		if event.Type == runstate.EventRunSucceeded {
			succeeded = event
		}
	}
	if succeeded.EventID == "" {
		t.Fatal("waived candidate composition recorded no run.succeeded")
	}
	var payload map[string]any
	if err := json.Unmarshal(succeeded.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	candidate := payload["candidate"].(map[string]any)
	if candidate["result_tree"] != expected.ResultTree {
		t.Fatalf("waived candidate tree = %v, want %q", candidate["result_tree"], expected.ResultTree)
	}
	if got := candidate["contributors"].([]any); len(got) != 2 ||
		got[0].(map[string]any)["movement_id"] != "ours" || got[1].(map[string]any)["movement_id"] != "theirs" {
		t.Fatalf("waived candidate contributors = %#v", got)
	}
	if got := candidate["ordered_change_sets"].([]any); len(got) != 2 || got[0] != "sha256:ours" || got[1] != "sha256:theirs" {
		t.Fatalf("waived candidate ordered change sets = %#v", got)
	}
	command := exec.Command("git", "rev-parse", "refs/partitur/runs/"+string(started.RunID)+"/candidate^{tree}")
	command.Dir = preparation.RepositoryRoot
	output, err := command.Output()
	if err != nil || "git-sha1:"+strings.TrimSpace(string(output)) != expected.ResultTree {
		t.Fatalf("waived candidate pin = %q, %v; want %q", output, err, expected.ResultTree)
	}
	state, err := authority.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Run != runstate.RunSucceeded || state.ApplicationCandidate == nil || state.ApplicationCandidate.ResultTree != expected.ResultTree {
		t.Fatalf("waived composed state = %+v candidate=%+v", state.Run, state.ApplicationCandidate)
	}
}

func TestComposeCandidateWaivedNoOpWriterPinsBaseCandidate(t *testing.T) {
	preparation, store, authority, started, ours, theirs := fanInWaivedNoOpWriterFixture(t)
	defer authority.Release()
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if ours != input.BaseTree || theirs != input.BaseTree {
		t.Fatalf("no-op writer trees = %q, %q; want base %q", ours, theirs, input.BaseTree)
	}
	contributors := []workspace.CompositionContributor{
		{MovementID: "noop-a", ChangeSetID: "sha256:noop-a", BaseTree: input.BaseTree, ResultTree: input.BaseTree},
		{MovementID: "noop-b", ChangeSetID: "sha256:noop-b", BaseTree: input.BaseTree, ResultTree: input.BaseTree},
	}
	expected := workspace.Compose(workspace.CompositionInput{
		RepositoryRoot: preparation.RepositoryRoot, BaseTree: input.BaseTree, Contributors: contributors,
	})
	if expected.ResultTree != input.BaseTree || expected.EnvironmentHash == "" {
		t.Fatalf("no-op writer composition = %+v, want merge-mode base result", expected)
	}
	if err := ComposeCandidate(store, authority, input, 1, time.Now, func() (string, error) { return "waived-noop", nil }); err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var succeeded runstate.Event
	for _, event := range journal.Events {
		if event.Type == runstate.EventApplicationCandidateRecorded {
			t.Fatal("waived no-op writer recorded application_candidate.recorded")
		}
		if event.Type == runstate.EventRunSucceeded {
			succeeded = event
		}
	}
	if succeeded.EventID == "" {
		t.Fatal("waived no-op writer recorded no run.succeeded")
	}
	compositionHash, err := canonical.Hash(canonical.DomainCandidateComposition, map[string]any{
		"composition_mode": "merge", "base_tree": input.BaseTree,
		"contributors": []any{
			map[string]any{"movement_id": "noop-a", "change_set_id": "sha256:noop-a"},
			map[string]any{"movement_id": "noop-b", "change_set_id": "sha256:noop-b"},
		},
		"composition_algorithm_version": float64(canonical.CompositionAlgorithmVersion),
		"composition_environment_hash":  expected.EnvironmentHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidateID, err := canonical.Hash(canonical.DomainCandidate, map[string]any{
		"base_tree": input.BaseTree, "result_tree": input.BaseTree,
		"ordered_change_sets": []any{"sha256:noop-a", "sha256:noop-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(succeeded.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	wantCandidate := map[string]any{
		"candidate_id": candidateID, "base_tree": input.BaseTree, "result_tree": input.BaseTree,
		"ordered_change_sets": []any{"sha256:noop-a", "sha256:noop-b"},
		"contributors": []any{
			map[string]any{"movement_id": "noop-a", "change_set_id": "sha256:noop-a"},
			map[string]any{"movement_id": "noop-b", "change_set_id": "sha256:noop-b"},
		},
		"candidate_composition_dependency_hash": compositionHash,
	}
	if !reflect.DeepEqual(payload["candidate"], wantCandidate) {
		t.Fatalf("waived no-op merge candidate = %#v, want %#v", payload["candidate"], wantCandidate)
	}
	command := exec.Command("git", "rev-parse", "refs/partitur/runs/"+string(started.RunID)+"/candidate^{tree}")
	command.Dir = preparation.RepositoryRoot
	output, err := command.Output()
	if err != nil || "git-sha1:"+strings.TrimSpace(string(output)) != input.BaseTree {
		t.Fatalf("waived no-op candidate pin = %q, %v; want %q", output, err, input.BaseTree)
	}
}

func TestComposeCandidateWaivedConflictFailsRunAtCandidateScope(t *testing.T) {
	_, store, authority, started, _, _ := fanInWaivedFixture(t, true)
	defer authority.Release()
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	err = ComposeCandidate(store, authority, input, 1, time.Now, func() (string, error) { return "waived-candidate-conflict", nil })
	if !errors.Is(err, ErrCompositionTerminalized) {
		t.Fatalf("waived candidate composition error = %v", err)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var evidence, terminal runstate.Event
	for _, event := range journal.Events {
		switch event.Type {
		case runstate.EventApplicationCandidateRecorded:
			t.Fatal("waived conflicted candidate recorded application_candidate.recorded")
		case runstate.EventCompositionConflicted:
			evidence = event
		case runstate.EventRunFailed:
			terminal = event
		}
	}
	if evidence.EventID == "" || terminal.EventID == "" {
		t.Fatalf("waived candidate conflict evidence=%+v terminal=%+v", evidence, terminal)
	}
	var evidencePayload, terminalPayload map[string]any
	if err := json.Unmarshal(evidence.Payload, &evidencePayload); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(terminal.Payload, &terminalPayload); err != nil {
		t.Fatal(err)
	}
	if evidencePayload["scope"] != "candidate" || evidencePayload["target_id"] != string(started.RunID) || terminalPayload["reason"] != "composition_unresolvable" || terminal.CausationID != evidence.EventID {
		t.Fatalf("waived candidate terminal evidence=%v terminal=%+v", evidencePayload, terminal)
	}
}

func TestComposeCandidateConflictFailsRunAtCandidateScope(t *testing.T) {
	_, store, authority, started, _, _ := candidateFanInConflictFixture(t)
	defer authority.Release()
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	err = ComposeCandidate(store, authority, input, 1, time.Now, func() (string, error) { return "candidate-conflict", nil })
	if !errors.Is(err, ErrCompositionTerminalized) {
		t.Fatalf("candidate composition error = %v", err)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var evidence, terminal runstate.Event
	for _, event := range journal.Events {
		switch event.Type {
		case runstate.EventCompositionConflicted:
			evidence = event
		case runstate.EventRunFailed:
			terminal = event
		}
	}
	if evidence.EventID == "" || terminal.EventID == "" {
		t.Fatalf("candidate conflict evidence=%+v terminal=%+v", evidence, terminal)
	}
	var evidencePayload, terminalPayload map[string]any
	if err := json.Unmarshal(evidence.Payload, &evidencePayload); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(terminal.Payload, &terminalPayload); err != nil {
		t.Fatal(err)
	}
	if evidencePayload["scope"] != "candidate" || evidencePayload["target_id"] != string(started.RunID) || terminalPayload["reason"] != "composition_unresolvable" || terminal.CausationID != evidence.EventID {
		t.Fatalf("candidate terminal evidence=%v terminal=%+v", evidencePayload, terminal)
	}
}

func TestCandidateCompositionRequiresRecordedChangeSetEvidence(t *testing.T) {
	_, store, authority, started, _, _ := candidateFanInCleanFixture(t)
	defer authority.Release()
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	input.Projection.State.ChangeSets = map[runstate.AttemptID]runstate.ChangeSetRecord{}
	if _, err := CandidateCompositionContributors(input); err == nil || !strings.Contains(err.Error(), "change_set.recorded") {
		t.Fatalf("candidate contributor evidence error = %v", err)
	}
}
