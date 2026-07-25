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
		Inputs:            []protocol.ArtifactRef{{ArtifactID: "design", Kind: "document", Path: "/input", Hash: "sha256:x"}},
		Feedback:          []protocol.Feedback{{PreviousAttemptID: "a-1", Kind: "task_failed", ArtifactID: "failure"}},
		ResolvedDecisions: []protocol.ResolvedDecision{{DecisionID: "q-1", Answer: "yes"}},
		Workdir:           "/work",
		OutputDir:         "/output",
		Grants:            protocol.Grants{PathsRW: []string{"internal/**"}, PathsRO: []string{"docs/**"}, Shell: true},
		Budget:            protocol.Budget{ActiveWallClockMin: 12.5},
	}
	prompt := RenderPrompt(request)
	for _, expected := range []string{
		"Ship the change",
		"Repository context",
		"artifact_id=\"report\" kind=\"document\"",
		"previous_attempt_id=\"a-1\"",
		"decision_id=\"q-1\"",
		"12.5 minutes",
		"/output/" + ResultFilename,
		`"version": 1`,
		"requires_decision",
		"reserved file",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt missing %q:\n%s", expected, prompt)
		}
	}
}
