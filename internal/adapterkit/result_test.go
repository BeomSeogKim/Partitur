package adapterkit

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

type recordingSink struct {
	artifacts []protocol.ArtifactEvent
	questions []protocol.QuestionEvent
	proposals []protocol.ProposalEvent
}

func (s *recordingSink) Log(string, string) error { return nil }
func (s *recordingSink) Progress(string) error    { return nil }
func (s *recordingSink) Artifact(id, path string) error {
	s.artifacts = append(s.artifacts, protocol.ArtifactEvent{ArtifactID: id, Path: path})
	return nil
}
func (s *recordingSink) Proposal(id string, amendment json.RawMessage, required bool) error {
	s.proposals = append(s.proposals, protocol.ProposalEvent{
		ID:               id,
		Amendment:        amendment,
		RequiresDecision: required,
	})
	return nil
}
func (s *recordingSink) Question(id, question string) error {
	s.questions = append(s.questions, protocol.QuestionEvent{ID: id, Question: question})
	return nil
}

func TestCollectResultZeroOutputsCompleted(t *testing.T) {
	outputDir := t.TempDir()
	writeResult(t, outputDir, `{
		"version": 1,
		"artifacts": [],
		"questions": [],
		"proposal": null,
		"summary": ""
	}`)
	sink := &recordingSink{}
	result := CollectResult(outputDir, sink)
	if result.Outcome != protocol.OutcomeCompleted || result.Failure != nil {
		t.Fatalf("result = %+v", result)
	}
	if len(sink.artifacts)+len(sink.questions)+len(sink.proposals) != 0 {
		t.Fatalf("unexpected events: %+v", sink)
	}
}

func TestCollectResultEmitsAndWaitsForDecisions(t *testing.T) {
	outputDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outputDir, "report.txt"), []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeResult(t, outputDir, `{
		"version": 1,
		"artifacts": [{"artifact_id":"report","path":"report.txt"}],
		"questions": [{"id":"q-1","question":"Continue?"}],
		"proposal": {"id":"p-1","amendment":{"base_revision":1},"requires_decision":true},
		"summary": "waiting"
	}`)
	sink := &recordingSink{}
	result := CollectResult(outputDir, sink)
	if result.Outcome != protocol.OutcomeWaitingHuman {
		t.Fatalf("result = %+v", result)
	}
	if got := strings.Join(result.PendingDecisionIDs, ","); got != "q-1,p-1" {
		t.Fatalf("pending IDs = %q", got)
	}
	if len(sink.artifacts) != 1 || len(sink.questions) != 1 || len(sink.proposals) != 1 {
		t.Fatalf("events = %+v", sink)
	}
}

func TestCollectResultMissingIsTaskFailure(t *testing.T) {
	result := CollectResult(t.TempDir(), &recordingSink{})
	if result.Outcome != protocol.OutcomeFailed || result.Failure == nil ||
		result.Failure.Kind != protocol.FailureTaskFailed ||
		result.Failure.Detail != "result envelope missing" {
		t.Fatalf("result = %+v", result)
	}
}

func TestLoadResultValidationMatrix(t *testing.T) {
	tests := []struct {
		name    string
		content string
		setup   func(*testing.T, string) string
	}{
		{
			name:    "unknown field",
			content: `{"version":1,"artifacts":[],"questions":[],"proposal":null,"summary":"","extra":true}`,
		},
		{
			name:    "duplicate JSON key",
			content: `{"version":1,"version":1,"artifacts":[],"questions":[],"proposal":null,"summary":""}`,
		},
		{
			name:    "nested duplicate JSON key",
			content: `{"version":1,"artifacts":[],"questions":[],"proposal":{"id":"p","amendment":{"base_hash":"a","base_hash":"b"},"requires_decision":false},"summary":""}`,
		},
		{
			name:    "duplicate result id",
			content: `{"version":1,"artifacts":[{"artifact_id":"same","path":"file"}],"questions":[{"id":"same","question":"Q?"}],"proposal":null,"summary":""}`,
			setup: func(t *testing.T, outputDir string) string {
				t.Helper()
				if err := os.WriteFile(filepath.Join(outputDir, "file"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				return ""
			},
		},
		{
			name:    "missing required field",
			content: `{"version":1,"artifacts":[],"questions":[],"proposal":null}`,
		},
		{
			name:    "null artifacts",
			content: `{"version":1,"artifacts":null,"questions":[],"proposal":null,"summary":""}`,
		},
		{
			name:    "null questions",
			content: `{"version":1,"artifacts":[],"questions":null,"proposal":null,"summary":""}`,
		},
		{
			name:    "null summary",
			content: `{"version":1,"artifacts":[],"questions":[],"proposal":null,"summary":null}`,
		},
		{
			name:    "unsupported version",
			content: `{"version":2,"artifacts":[],"questions":[],"proposal":null,"summary":""}`,
		},
		{
			name:    "reserved artifact",
			content: `{"version":1,"artifacts":[{"artifact_id":"result","path":"partitur-result.json"}],"questions":[],"proposal":null,"summary":""}`,
		},
		{
			name:    "parent escape",
			content: `{"version":1,"artifacts":[{"artifact_id":"file","path":"../outside"}],"questions":[],"proposal":null,"summary":""}`,
		},
		{
			name:    "empty artifact path",
			content: `{"version":1,"artifacts":[{"artifact_id":"file","path":""}],"questions":[],"proposal":null,"summary":""}`,
		},
		{
			name:    "empty artifact id",
			content: `{"version":1,"artifacts":[{"artifact_id":"","path":"file"}],"questions":[],"proposal":null,"summary":""}`,
			setup: func(t *testing.T, outputDir string) string {
				t.Helper()
				if err := os.WriteFile(filepath.Join(outputDir, "file"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				return ""
			},
		},
		{
			name:    "absolute outside",
			content: `{"version":1,"artifacts":[{"artifact_id":"file","path":"REPLACE"}],"questions":[],"proposal":null,"summary":""}`,
			setup: func(t *testing.T, outputDir string) string {
				t.Helper()
				outside := filepath.Join(filepath.Dir(outputDir), "outside")
				if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				return outside
			},
		},
		{
			name:    "non regular",
			content: `{"version":1,"artifacts":[{"artifact_id":"dir","path":"directory"}],"questions":[],"proposal":null,"summary":""}`,
			setup: func(t *testing.T, outputDir string) string {
				t.Helper()
				if err := os.Mkdir(filepath.Join(outputDir, "directory"), 0o700); err != nil {
					t.Fatal(err)
				}
				return ""
			},
		},
		{
			name:    "symlink escape",
			content: `{"version":1,"artifacts":[{"artifact_id":"link","path":"link"}],"questions":[],"proposal":null,"summary":""}`,
			setup: func(t *testing.T, outputDir string) string {
				t.Helper()
				outside := filepath.Join(filepath.Dir(outputDir), "outside")
				if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(outputDir, "link")); err != nil {
					t.Fatal(err)
				}
				return ""
			},
		},
		{
			name:    "empty question",
			content: `{"version":1,"artifacts":[],"questions":[{"id":"q","question":" "}],"proposal":null,"summary":""}`,
		},
		{
			name:    "empty question id",
			content: `{"version":1,"artifacts":[],"questions":[{"id":"","question":"Q?"}],"proposal":null,"summary":""}`,
		},
		{
			name:    "empty proposal id",
			content: `{"version":1,"artifacts":[],"questions":[],"proposal":{"id":"","amendment":{},"requires_decision":false},"summary":""}`,
		},
		{
			name:    "duplicate question and proposal id",
			content: `{"version":1,"artifacts":[],"questions":[{"id":"same","question":"Q?"}],"proposal":{"id":"same","amendment":{},"requires_decision":false},"summary":""}`,
		},
		{
			name:    "invalid amendment",
			content: `{"version":1,"artifacts":[],"questions":[],"proposal":{"id":"p","amendment":[],"requires_decision":false},"summary":""}`,
		},
		{
			name:    "missing proposal decision flag",
			content: `{"version":1,"artifacts":[],"questions":[],"proposal":{"id":"p","amendment":{}},"summary":""}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			outputDir := filepath.Join(root, "output")
			if err := os.Mkdir(outputDir, 0o700); err != nil {
				t.Fatal(err)
			}
			replacement := ""
			if test.setup != nil {
				replacement = test.setup(t, outputDir)
			}
			content := strings.Replace(test.content, "REPLACE", replacement, 1)
			writeResult(t, outputDir, content)
			if _, err := LoadResult(outputDir); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestLoadResultRejectsInvalidUTF8(t *testing.T) {
	outputDir := t.TempDir()
	data := append([]byte(`{"version":1,"artifacts":[],"questions":[],"proposal":null,"summary":"`), 0xff)
	data = append(data, []byte(`"}`)...)
	if err := os.WriteFile(filepath.Join(outputDir, ResultFilename), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadResult(outputDir); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadResultOversize(t *testing.T) {
	outputDir := t.TempDir()
	content := strings.Repeat(" ", protocol.MaxFrameBytes+1)
	writeResult(t, outputDir, content)
	if _, err := LoadResult(outputDir); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadResultMissingSentinel(t *testing.T) {
	if _, err := LoadResult(t.TempDir()); !errors.Is(err, ErrResultMissing) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadResultRejectsSymlinkControlFile(t *testing.T) {
	root := t.TempDir()
	outputDir := filepath.Join(root, "output")
	if err := os.Mkdir(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "result.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"artifacts":[],"questions":[],"proposal":null,"summary":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(outputDir, ResultFilename)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadResult(outputDir); err == nil {
		t.Fatal("expected symlink control file rejection")
	}
}

func writeResult(t *testing.T, outputDir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(outputDir, ResultFilename), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
