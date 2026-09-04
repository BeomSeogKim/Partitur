package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

func TestLiveRetryAttemptRetainsProposalDispositioner(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	document := runScore()
	document["movements"].([]any)[0].(map[string]any)["may_propose"] = true
	document["policy"].(map[string]any)["budget"].(map[string]any)["retries_per_movement"] = float64(1)
	compiled, diagnostics := score.CompileValue(document)
	if len(diagnostics) != 0 {
		t.Fatalf("compile retry proposal fixture: %v", diagnostics)
	}
	baseHash, err := compiled.Hash()
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	writeValidateInputs(t, repository, document, runCast())
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")
	environment := replaceEnvironment(os.Environ(), map[string]string{
		"HOME":                                  t.TempDir(),
		"PATH":                                  bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PARTITUR_CODEX_BIN":                    vendor,
		runVendorEnvironment:                    "1",
		runVendorProposalBaseHashEnvironment:    baseHash,
		runVendorRetryBeforeProposalEnvironment: filepath.Join(t.TempDir(), "first-attempt-failed"),
	})

	code, stdout, stderr := runCommandBinaryWithin(t, 20*time.Second, partitur, repository, environment, "run")
	runID := runstate.RunID(strings.TrimSpace(stdout))
	if code != 0 || runID == "" || stderr != "" {
		t.Fatalf("retry proposal run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.Run != runstate.RunWaitingHuman || len(input.Projection.State.PendingDecisions) != 1 {
		t.Fatalf("retry proposal projection run=%s pending=%#v", input.Projection.State.Run, input.Projection.State.PendingDecisions)
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	var selections []runstate.Event
	var failure, blocked, routed, requested runstate.Event
	for _, event := range journal.Events {
		switch event.Type {
		case runstate.EventPerformerSelected:
			selections = append(selections, event)
		case runstate.EventAttemptFailed:
			failure = event
		case runstate.EventAttemptBlocked:
			blocked = event
		case runstate.EventAmendmentRoutedHuman:
			routed = event
		case runstate.EventDecisionRequested:
			var payload map[string]any
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["decision_type"] == "amendment" {
				requested = event
			}
		}
	}
	if len(selections) != 2 || failure.AttemptID != selections[0].AttemptID ||
		blocked.AttemptID != selections[1].AttemptID || routed.AttemptID != selections[1].AttemptID ||
		requested.AttemptID != selections[1].AttemptID {
		t.Fatalf("retry proposal journal selections=%+v failure=%+v blocked=%+v routed=%+v requested=%+v", selections, failure, blocked, routed, requested)
	}
	var retry map[string]any
	if err := json.Unmarshal(selections[1].Payload, &retry); err != nil {
		t.Fatal(err)
	}
	if retry["reason"] != "quality_retry" || selections[1].CausationID != failure.EventID {
		t.Fatalf("retry selection=%#v causation=%q, want quality_retry caused by %q", retry, selections[1].CausationID, failure.EventID)
	}
}
