package adapterkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

var ErrResultMissing = errors.New("result envelope is missing")

type ResultArtifact struct {
	ArtifactID string `json:"artifact_id"`
	Path       string `json:"path"`
}

type ResultQuestion struct {
	ID       string `json:"id"`
	Question string `json:"question"`
}

type ResultProposal struct {
	ID               string          `json:"id"`
	Amendment        json.RawMessage `json:"amendment"`
	RequiresDecision bool            `json:"requires_decision"`
}

type rawResultProposal struct {
	ID               string          `json:"id"`
	Amendment        json.RawMessage `json:"amendment"`
	RequiresDecision *bool           `json:"requires_decision"`
}

type ResultEnvelope struct {
	Version   int
	Artifacts []ResultArtifact
	Questions []ResultQuestion
	Proposal  *ResultProposal
	Summary   string
}

type rawResultEnvelope struct {
	Version   int               `json:"version"`
	Artifacts *[]ResultArtifact `json:"artifacts"`
	Questions *[]ResultQuestion `json:"questions"`
	Proposal  json.RawMessage   `json:"proposal"`
	Summary   *string           `json:"summary"`
}

// LoadResult reads and validates the reserved result envelope in outputDir.
func LoadResult(outputDir string) (*ResultEnvelope, error) {
	resultPath := filepath.Join(outputDir, ResultFilename)
	info, err := os.Lstat(resultPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrResultMissing
	}
	if err != nil {
		return nil, fmt.Errorf("inspect result envelope: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("result envelope must be a regular non-symlink file")
	}

	file, err := os.Open(resultPath)
	if err != nil {
		return nil, fmt.Errorf("open result envelope: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, protocol.MaxFrameBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read result envelope: %w", err)
	}
	if len(data) > protocol.MaxFrameBytes {
		return nil, errors.New("result envelope exceeds size limit")
	}

	var raw rawResultEnvelope
	if err := protocol.DecodeStrict(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid result envelope: %w", err)
	}
	if raw.Version != 1 {
		return nil, fmt.Errorf("unsupported result envelope version %d", raw.Version)
	}
	if raw.Artifacts == nil || raw.Questions == nil || raw.Proposal == nil || raw.Summary == nil {
		return nil, errors.New("result envelope requires artifacts, questions, proposal, and summary")
	}

	envelope := &ResultEnvelope{
		Version:   raw.Version,
		Artifacts: *raw.Artifacts,
		Questions: *raw.Questions,
		Summary:   *raw.Summary,
	}
	if !bytes.Equal(bytes.TrimSpace(raw.Proposal), []byte("null")) {
		var rawProposal rawResultProposal
		if err := protocol.DecodeStrict(raw.Proposal, &rawProposal); err != nil {
			return nil, fmt.Errorf("invalid proposal: %w", err)
		}
		if rawProposal.RequiresDecision == nil {
			return nil, errors.New("proposal requires requires_decision")
		}
		if len(bytes.TrimSpace(rawProposal.Amendment)) == 0 || bytes.TrimSpace(rawProposal.Amendment)[0] != '{' || !json.Valid(rawProposal.Amendment) {
			return nil, errors.New("proposal amendment must be a JSON object")
		}
		envelope.Proposal = &ResultProposal{
			ID:               rawProposal.ID,
			Amendment:        rawProposal.Amendment,
			RequiresDecision: *rawProposal.RequiresDecision,
		}
	}

	if err := validateResultEnvelope(outputDir, envelope); err != nil {
		return nil, err
	}
	return envelope, nil
}

// CollectResult maps a vendor result envelope to protocol events and an
// execute result. Missing or invalid envelopes are task failures.
func CollectResult(outputDir string, sink EventSink) *protocol.ExecuteResult {
	envelope, err := LoadResult(outputDir)
	if err != nil {
		detail := "invalid result envelope"
		if errors.Is(err, ErrResultMissing) {
			detail = "result envelope missing"
		}
		return &protocol.ExecuteResult{
			Outcome: protocol.OutcomeFailed,
			Failure: &protocol.Failure{Kind: protocol.FailureTaskFailed, Detail: detail},
		}
	}

	for _, artifact := range envelope.Artifacts {
		if err := sink.Artifact(artifact.ArtifactID, artifact.Path); err != nil {
			return eventFailure(err)
		}
	}

	pending := make([]string, 0, len(envelope.Questions)+1)
	for _, question := range envelope.Questions {
		if err := sink.Question(question.ID, question.Question); err != nil {
			return eventFailure(err)
		}
		pending = append(pending, question.ID)
	}
	if envelope.Proposal != nil {
		if err := sink.Proposal(envelope.Proposal.ID, envelope.Proposal.Amendment, envelope.Proposal.RequiresDecision); err != nil {
			return eventFailure(err)
		}
		if envelope.Proposal.RequiresDecision {
			pending = append(pending, envelope.Proposal.ID)
		}
	}

	result := &protocol.ExecuteResult{
		Outcome: protocol.OutcomeCompleted,
		Detail:  SanitizeMessage(envelope.Summary),
	}
	if len(pending) > 0 {
		result.Outcome = protocol.OutcomeWaitingHuman
		result.PendingDecisionIDs = pending
	}
	return result
}

func eventFailure(err error) *protocol.ExecuteResult {
	return &protocol.ExecuteResult{
		Outcome: protocol.OutcomeFailed,
		Failure: &protocol.Failure{
			Kind:   protocol.FailureProtocolError,
			Detail: SanitizeMessage(err.Error()),
		},
	}
}

func validateResultEnvelope(outputDir string, envelope *ResultEnvelope) error {
	resolvedOutput, err := filepath.EvalSymlinks(outputDir)
	if err != nil {
		return fmt.Errorf("resolve output directory: %w", err)
	}
	resolvedOutput, err = filepath.Abs(resolvedOutput)
	if err != nil {
		return fmt.Errorf("make output directory absolute: %w", err)
	}

	ids := make(map[string]struct{}, len(envelope.Artifacts)+len(envelope.Questions)+1)
	addID := func(id string) error {
		if id == "" {
			return errors.New("result IDs must not be empty")
		}
		if _, exists := ids[id]; exists {
			return fmt.Errorf("duplicate result ID %q", id)
		}
		ids[id] = struct{}{}
		return nil
	}

	for _, artifact := range envelope.Artifacts {
		if err := addID(artifact.ArtifactID); err != nil {
			return err
		}
		if err := validateArtifactPath(resolvedOutput, artifact.Path); err != nil {
			return fmt.Errorf("artifact %q: %w", artifact.ArtifactID, err)
		}
	}
	for _, question := range envelope.Questions {
		if err := addID(question.ID); err != nil {
			return err
		}
		if strings.TrimSpace(question.Question) == "" {
			return fmt.Errorf("question %q is empty", question.ID)
		}
	}
	if envelope.Proposal != nil {
		if err := addID(envelope.Proposal.ID); err != nil {
			return err
		}
	}
	return nil
}

func validateArtifactPath(outputDir, artifactPath string) error {
	if artifactPath == "" || filepath.IsAbs(artifactPath) {
		return errors.New("path must be output-relative")
	}
	for _, segment := range strings.FieldsFunc(artifactPath, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if segment == ".." {
			return errors.New("path must not contain '..'")
		}
	}
	cleaned := filepath.Clean(artifactPath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return errors.New("path escapes output directory")
	}
	if cleaned == ResultFilename {
		return errors.New("reserved result file cannot be an artifact")
	}

	resolved, err := filepath.EvalSymlinks(filepath.Join(outputDir, cleaned))
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return fmt.Errorf("make path absolute: %w", err)
	}
	relative, err := filepath.Rel(outputDir, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("resolved path escapes output directory")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("stat path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("path is not a regular file")
	}
	return nil
}
