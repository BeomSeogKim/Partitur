package recoveryobs

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
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
