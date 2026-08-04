package adapterkit

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

func TestRenderPromptIncludesContractAndBrief(t *testing.T) {
	request := &protocol.ExecuteRequest{
		Brief: protocol.Brief{
			Goal:                    "Ship the change",
			Context:                 "Repository context",
			Instruction:             "Implement it",
			VerificationExpectation: json.RawMessage(`{"intent":"pass-existing-tests"}`),
			Acceptance:              json.RawMessage(`{"hard":[{"id":"test"}]}`),
			GlobalInvariants:        json.RawMessage(`["keep compatibility"]`),
			Outputs:                 []protocol.OutputSpec{{ArtifactID: "report", Kind: "document"}},
		},
		Inputs:   []protocol.ArtifactRef{{ArtifactID: "design", Kind: "document", Path: "/input", Hash: "sha256:x"}},
		Feedback: []protocol.Feedback{{PreviousAttemptID: "a-1", Kind: "task_failed", ArtifactInstanceID: "failure@a-1", Path: "/feedback/failure", ContentHash: "sha256:failure"}},
		ResolvedDecisions: []protocol.ResolvedDecision{
			{
				DecisionID: "q-1",
				Kind:       protocol.ResolvedDecisionAnswer,
				Answer:     "yes",
			},
			{
				DecisionID: "p-1",
				Kind:       protocol.ResolvedDecisionAmendmentRejected,
				Reason:     "human_rejected",
			},
		},
		Workdir:   "/work",
		OutputDir: "/output",
		Grants:    protocol.Grants{PathsRW: []string{"internal/**"}, PathsRO: []string{"docs/**"}, Shell: true},
		Budget:    protocol.Budget{RemainingMS: 750_001},
	}
	prompt := RenderPrompt(request)
	for _, expected := range []string{
		"Ship the change",
		"Repository context",
		"artifact_id=\"report\" kind=\"document\"",
		"previous_attempt_id=\"a-1\"",
		"decision_id=\"q-1\" kind=\"answer\" answer=\"yes\"",
		"decision_id=\"p-1\" kind=\"amendment_rejected\" reason=\"human_rejected\"",
		"750001 milliseconds",
		"/output/" + ResultFilename,
		`"version": 1`,
		"reserved file",
		"proposal_without_authority",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt missing %q:\n%s", expected, prompt)
		}
	}
}

func TestRenderPromptReservedInputContracts(t *testing.T) {
	request := &protocol.ExecuteRequest{
		Brief: protocol.Brief{
			Goal:        "Review",
			Instruction: "Follow the contracts",
			Outputs:     []protocol.OutputSpec{{ArtifactID: "findings", Kind: "findings"}},
		},
		Inputs: []protocol.ArtifactRef{
			{ArtifactID: "ordinary", Kind: "document", Path: "/inputs/ordinary", Hash: "sha256:ordinary"},
			{ArtifactID: scoreBaseArtifactID, Kind: scoreBaseKind, Path: "/inputs/score-base", Hash: "sha256:score"},
			{ArtifactID: subjectTreeArtifactID, Kind: subjectTreeArtifactKind, Path: "/inputs/subject-tree", Hash: "sha256:tree"},
		},
		Workdir:   "/work",
		OutputDir: "/output",
	}

	prompt := RenderPrompt(request)
	for _, expected := range []string{
		`artifact_id="ordinary"`,
		"SCORE BASE",
		"SUBJECT TREE",
		"raw SHA-256 of the exact file bytes",
		"base_revision and base_hash",
		"exact subject_tree",
		"cover every listed rubric",
		"match its findings_schema",
		"requires_decision",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt missing %q:\n%s", expected, prompt)
		}
	}
	inputsStart := strings.Index(prompt, "## Inputs")
	reservedStart := strings.Index(prompt, "## Reserved input contracts")
	if inputsStart < 0 || reservedStart < 0 || inputsStart >= reservedStart {
		t.Fatalf("input sections missing or out of order:\n%s", prompt)
	}
	ordinarySection := prompt[inputsStart:reservedStart]
	if strings.Contains(ordinarySection, scoreBaseArtifactID) || strings.Contains(ordinarySection, subjectTreeArtifactID) {
		t.Fatalf("reserved input rendered as ordinary:\n%s", ordinarySection)
	}
	if strings.Contains(prompt, "proposal_without_authority") {
		t.Fatalf("authorized prompt denied proposal authority:\n%s", prompt)
	}
}

func TestRenderPromptExcludesChangeSetOutputs(t *testing.T) {
	tests := []struct {
		name              string
		outputs           []protocol.OutputSpec
		wantArtifact      string
		wantNoArtifacts   bool
		forbiddenArtifact string
	}{
		{
			name: "mixed outputs",
			outputs: []protocol.OutputSpec{
				{ArtifactID: "report", Kind: "document"},
				{ArtifactID: "changes", Kind: "change_set"},
			},
			wantArtifact:      `artifact_id="report" kind="document"`,
			forbiddenArtifact: `artifact_id="changes"`,
		},
		{
			name:              "change set only",
			outputs:           []protocol.OutputSpec{{ArtifactID: "changes", Kind: "change_set"}},
			wantNoArtifacts:   true,
			forbiddenArtifact: `artifact_id="changes"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := &protocol.ExecuteRequest{
				Brief: protocol.Brief{
					Goal:        "Implement",
					Instruction: "Make the change",
					Outputs:     test.outputs,
				},
				Workdir:   "/work",
				OutputDir: "/output",
			}
			prompt := RenderPrompt(request)
			if test.wantArtifact != "" && !strings.Contains(prompt, test.wantArtifact) {
				t.Fatalf("ordinary output missing:\n%s", prompt)
			}
			if test.wantNoArtifacts && !strings.Contains(prompt, "No declared artifacts.") {
				t.Fatalf("no-artifacts contract missing:\n%s", prompt)
			}
			if strings.Contains(prompt, test.forbiddenArtifact) {
				t.Fatalf("change_set output leaked into prompt:\n%s", prompt)
			}
			if !strings.Contains(prompt, "kind is change_set must never appear in artifacts") {
				t.Fatalf("change_set envelope prohibition missing:\n%s", prompt)
			}
		})
	}
}
