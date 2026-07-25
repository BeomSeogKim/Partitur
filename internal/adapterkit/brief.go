package adapterkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

const ResultFilename = "partitur-result.json"

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
	if len(request.Brief.Outputs) == 0 {
		outputs.WriteString("No declared artifacts.")
	} else {
		for _, output := range request.Brief.Outputs {
			fmt.Fprintf(&outputs, "- artifact_id=%q kind=%q\n", output.ArtifactID, output.Kind)
		}
	}
	section("Declared outputs", strings.TrimSpace(outputs.String()))

	var inputs strings.Builder
	if len(request.Inputs) == 0 {
		inputs.WriteString("No input artifacts.")
	} else {
		for _, input := range request.Inputs {
			fmt.Fprintf(&inputs, "- artifact_id=%q kind=%q path=%q hash=%q\n", input.ArtifactID, input.Kind, input.Path, input.Hash)
		}
	}
	section("Inputs", strings.TrimSpace(inputs.String()))

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
			fmt.Fprintf(&decisions, "- decision_id=%q answer=%q\n", decision.DecisionID, decision.Answer)
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

	budget := strconv.FormatFloat(request.Budget.ActiveWallClockMin, 'f', -1, 64)
	section("Budget", "The remaining active wall-clock budget at attempt start is "+budget+" minutes. This is advisory context; Partitur enforces termination.")

	resultPath := filepath.Join(request.OutputDir, ResultFilename)
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

When proposing an amendment, proposal must instead be:
{"id": "proposal-id", "amendment": { ... }, "requires_decision": true}

All IDs must be unique. Artifact paths must be relative to the writable artifact directory and resolve to regular files inside it. The reserved file %q cannot be emitted as an artifact. Unknown fields are forbidden. Empty artifacts and questions arrays, a null proposal, and an empty summary form a valid completed result.`, resultPath, ResultFilename)
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
