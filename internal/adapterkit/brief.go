package adapterkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

const (
	ResultFilename          = "partitur-result.json"
	scoreBaseArtifactID     = "partitur.score-base"
	scoreBaseKind           = "partitur/score-base+json;v=1"
	subjectTreeArtifactID   = "partitur.subject-tree"
	subjectTreeArtifactKind = "partitur/subject-tree+json;v=1"
)

// RenderPrompt turns the semantic execute request into a vendor-neutral brief.
func RenderPrompt(request *protocol.ExecuteRequest) string {
	var prompt strings.Builder
	section := func(title, body string) {
		fmt.Fprintf(&prompt, "## %s\n\n%s\n\n", title, body)
	}

	section("Goal", request.Brief.Goal)
	if request.Brief.Context != "" {
		section("Context", request.Brief.Context)
	}
	section("Instruction", request.Brief.Instruction)
	section("Verification expectation", renderJSON(request.Brief.VerificationExpectation))
	section("Acceptance", renderJSON(request.Brief.Acceptance))
	section("Global invariants", renderJSON(request.Brief.GlobalInvariants))

	var outputs strings.Builder
	for _, output := range request.Brief.Outputs {
		if output.Kind == "change_set" {
			continue
		}
		fmt.Fprintf(&outputs, "- artifact_id=%q kind=%q\n", output.ArtifactID, output.Kind)
	}
	if outputs.Len() == 0 {
		outputs.WriteString("No declared artifacts.")
	}
	section("Declared outputs", strings.TrimSpace(outputs.String()))

	var inputs strings.Builder
	var reservedInputs strings.Builder
	hasScoreBase := false
	hasSubjectTree := false
	for _, input := range request.Inputs {
		switch {
		case input.ArtifactID == scoreBaseArtifactID && input.Kind == scoreBaseKind:
			hasScoreBase = true
			fmt.Fprintf(&reservedInputs, "- SCORE BASE: artifact_id=%q kind=%q path=%q hash=%q\n", input.ArtifactID, input.Kind, input.Path, input.Hash)
		case input.ArtifactID == subjectTreeArtifactID && input.Kind == subjectTreeArtifactKind:
			hasSubjectTree = true
			fmt.Fprintf(&reservedInputs, "- SUBJECT TREE: artifact_id=%q kind=%q path=%q hash=%q\n", input.ArtifactID, input.Kind, input.Path, input.Hash)
		default:
			fmt.Fprintf(&inputs, "- artifact_id=%q kind=%q path=%q hash=%q\n", input.ArtifactID, input.Kind, input.Path, input.Hash)
		}
	}
	if inputs.Len() == 0 {
		if len(request.Inputs) == 0 {
			inputs.WriteString("No input artifacts.")
		} else {
			inputs.WriteString("No ordinary input artifacts.")
		}
	}
	section("Inputs", strings.TrimSpace(inputs.String()))

	if reservedInputs.Len() == 0 {
		reservedInputs.WriteString("No reserved input artifacts.")
	} else {
		reservedInputs.WriteString("\nEvery inputs[].hash above is the raw SHA-256 of the exact file bytes, for reserved inputs as for ordinary artifacts; it is never a semantic hash.\n")
		if hasScoreBase {
			reservedInputs.WriteString("Any amendment proposal MUST use the base_revision and base_hash from the SCORE BASE file.\n")
		}
		if hasSubjectTree {
			reservedInputs.WriteString("The findings artifact MUST carry the exact subject_tree from the SUBJECT TREE file, cover every listed rubric, and match its findings_schema.")
		}
	}
	section("Reserved input contracts", strings.TrimSpace(reservedInputs.String()))

	var feedback strings.Builder
	if len(request.Feedback) == 0 {
		feedback.WriteString("No prior-attempt feedback.")
	} else {
		for _, item := range request.Feedback {
			fmt.Fprintf(&feedback, "- previous_attempt_id=%q kind=%q artifact_id=%q\n", item.PreviousAttemptID, item.Kind, item.ArtifactID)
		}
	}
	section("Feedback", strings.TrimSpace(feedback.String()))

	var decisions strings.Builder
	if len(request.ResolvedDecisions) == 0 {
		decisions.WriteString("No resolved decisions.")
	} else {
		for _, decision := range request.ResolvedDecisions {
			switch decision.Kind {
			case protocol.ResolvedDecisionAnswer:
				fmt.Fprintf(
					&decisions,
					"- decision_id=%q kind=%q answer=%q\n",
					decision.DecisionID,
					decision.Kind,
					decision.Answer,
				)
			case protocol.ResolvedDecisionAmendmentRejected:
				fmt.Fprintf(
					&decisions,
					"- decision_id=%q kind=%q reason=%q\n",
					decision.DecisionID,
					decision.Kind,
					decision.Reason,
				)
			}
		}
	}
	section("Resolved decisions", strings.TrimSpace(decisions.String()))

	var workspace strings.Builder
	fmt.Fprintf(&workspace, "- Repository worktree: %s\n", request.Workdir)
	fmt.Fprintf(&workspace, "- Writable artifact directory: %s\n", request.OutputDir)
	fmt.Fprintf(&workspace, "- Writable path grants: %s\n", renderStrings(request.Grants.PathsRW))
	fmt.Fprintf(&workspace, "- Readable path grants: %s\n", renderStrings(request.Grants.PathsRO))
	fmt.Fprintf(&workspace, "- Shell granted: %t\n", request.Grants.Shell)
	fmt.Fprintf(&workspace, "- Data-plane network granted: %t\n", request.Grants.Network)
	workspace.WriteString("- Do not modify score, cast, or Partitur run-state files.\n")
	workspace.WriteString("- Do not represent repository code changes as artifact events.")
	section("Workspace and authority", workspace.String())

	section(
		"Budget",
		fmt.Sprintf(
			"The remaining active wall-clock budget at attempt start is %d milliseconds. This is advisory context; Partitur enforces termination.",
			request.Budget.RemainingMS,
		),
	)

	resultPath := filepath.Join(request.OutputDir, ResultFilename)
	proposalContract := `No partitur.score-base input was supplied, so "proposal" MUST be null. A non-null proposal is a protocol error (proposal_without_authority).`
	if hasScoreBase {
		proposalContract = `A non-null proposal MUST use the base_revision and base_hash from the supplied partitur.score-base file. Its shape is:
{"id": "proposal-id", "amendment": { ... }, "requires_decision": true}`
	}
	resultContract := fmt.Sprintf(`Before exiting, write exactly one strict JSON result envelope to:
%s

The file must be at most 1048576 bytes and have exactly this shape:
{
  "version": 1,
  "artifacts": [{"artifact_id": "declared-id", "path": "output-relative/path"}],
  "questions": [{"id": "decision-id", "question": "question text"}],
  "proposal": null,
  "summary": "short summary"
}

%s

All IDs must be unique. Artifact paths must be relative to the writable artifact directory and resolve to regular files inside it. The reserved file %q cannot be emitted as an artifact. The artifact_id of an output whose kind is change_set must never appear in artifacts. Unknown fields are forbidden. Empty artifacts and questions arrays, a null proposal, and an empty summary form a valid completed result.`, resultPath, proposalContract, ResultFilename)
	section("Result envelope", resultContract)

	return strings.TrimSpace(prompt.String()) + "\n"
}

func renderJSON(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "Not specified."
	}
	var compact bytes.Buffer
	if err := json.Indent(&compact, raw, "", "  "); err != nil {
		return string(raw)
	}
	return compact.String()
}

func renderStrings(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	encoded, _ := json.Marshal(values)
	return string(encoded)
}
