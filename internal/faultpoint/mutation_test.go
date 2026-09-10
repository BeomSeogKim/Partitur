//go:build mutation

package faultpoint

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationBoundaryPointIDsRequireCompleteRegistry(t *testing.T) {
	goEnvironment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve faultpoint test source directory")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	copyRoot := filepath.Join(t.TempDir(), "partitur-mutation-copy")
	if err := copyFaultpointMutationRepository(copyRoot, repositoryRoot); err != nil {
		t.Fatal(err)
	}

	sourcePath := filepath.Join(copyRoot, "internal", "faultpoint", "faultpoint.go")
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	anchor := "\tPointCompositionCandidateTerminal     PointID = \"composition.candidate_terminal_recorded\"\n"
	if count := strings.Count(string(contents), anchor); count != 1 {
		t.Fatalf("mutation anchor count = %d, want 1", count)
	}
	mutated := strings.Replace(string(contents), anchor, anchor+"\tPointMutationUnregistered            PointID = \"mutation.unregistered\"\n", 1)
	if err := os.WriteFile(sourcePath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, "internal", "faultpoint"),
		Package:     ".",
		TestPattern: "TestBoundaryPointIDsAreSemanticAndUnique",
		TestNames:   []string{"TestBoundaryPointIDsAreSemanticAndUnique"},
		Environment: goEnvironment.ChildEnvironment(os.Environ(), "GOFLAGS=-tags=faultprobe"),
	})
	cancel()
	switch result.Outcome {
	case mutationtest.Killed:
		return
	case mutationtest.Survived:
		t.Fatalf("mutation survived: unregistered PointID constant did not fail the completeness lock\n%s", result.Diagnostic())
	default:
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}

func TestMutationAppendixEEdgeIDsRequireCompleteRegistry(t *testing.T) {
	goEnvironment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve faultpoint test source directory")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	copyRoot := filepath.Join(t.TempDir(), "partitur-mutation-copy")
	if err := copyFaultpointMutationRepository(copyRoot, repositoryRoot); err != nil {
		t.Fatal(err)
	}

	sourcePath := filepath.Join(copyRoot, "internal", "faultpoint", "faultpoint.go")
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	anchor := "\tEdgeLifecycleDraftPerformerCompletedToNoBlockingFailure EdgeID = \"lifecycle.draft_performer_completed_to_no_blocking_failure\"\n"
	if count := strings.Count(string(contents), anchor); count != 1 {
		t.Fatalf("mutation anchor count = %d, want 1", count)
	}
	mutated := strings.Replace(string(contents), anchor, "", 1)
	if err := os.WriteFile(sourcePath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	applied, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(applied) != mutated {
		t.Fatal("mutation did not persist its intended EdgeID deletion")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, "internal", "faultpoint"),
		Package:     ".",
		TestPattern: "TestAppendixEEdgeIDsAreCompleteAndUnique",
		TestNames:   []string{"TestAppendixEEdgeIDsAreCompleteAndUnique"},
		Environment: goEnvironment.ChildEnvironment(os.Environ(), "GOFLAGS=-tags=faultprobe"),
	})
	cancel()
	switch result.Outcome {
	case mutationtest.Killed:
		return
	case mutationtest.Survived:
		t.Fatalf("mutation survived: missing EdgeID constant did not fail the completeness lock\n%s", result.Diagnostic())
	default:
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}

func TestMutationProbeNotifyFailureDoesNotContinue(t *testing.T) {
	goEnvironment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve faultpoint test source directory")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	copyRoot := filepath.Join(t.TempDir(), "partitur-mutation-copy")
	if err := copyFaultpointMutationRepository(copyRoot, repositoryRoot); err != nil {
		t.Fatal(err)
	}

	sourcePath := filepath.Join(copyRoot, "internal", "faultpoint", "probe_pipe.go")
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	anchor := "\tif _, err := fmt.Fprintln(probe.notify, point, os.Getpid()); err != nil {\n\t\tos.Exit(1)\n\t}\n"
	if count := strings.Count(string(contents), anchor); count != 1 {
		t.Fatalf("mutation anchor count = %d, want 1", count)
	}
	mutated := strings.Replace(string(contents), anchor, "\tif _, err := fmt.Fprintln(probe.notify, point, os.Getpid()); err != nil {\n\t\treturn\n\t}\n", 1)
	if err := os.WriteFile(sourcePath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, "internal", "faultpoint"),
		Package:     ".",
		TestPattern: "TestProbeFromEnvironmentGuardsDescriptors/exits_when_notify_pipe_closes",
		TestNames:   []string{"TestProbeFromEnvironmentGuardsDescriptors/exits_when_notify_pipe_closes"},
		Environment: goEnvironment.ChildEnvironment(os.Environ(), "GOFLAGS=-tags=faultprobe"),
	})
	cancel()
	switch result.Outcome {
	case mutationtest.Killed:
		return
	case mutationtest.Survived:
		t.Fatalf("mutation survived: broken notify channel let the probe continue\n%s", result.Diagnostic())
	default:
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}

func TestCopyFaultpointMutationRepositoryGitFileEntry(t *testing.T) {
	source := t.TempDir()
	destination := filepath.Join(t.TempDir(), "copy")
	if err := os.WriteFile(filepath.Join(source, ".git"), []byte("gitdir: elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "z-after-git"), []byte("copied\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := mutationtest.CopyRepository(destination, source); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "z-after-git")); err != nil {
		t.Fatalf("entry after .git was not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".git")); !os.IsNotExist(err) {
		t.Fatalf("copied .git file: %v", err)
	}
}

func TestCopyFaultpointMutationRepositorySkipsPartiturStateDirectory(t *testing.T) {
	source := t.TempDir()
	destination := filepath.Join(t.TempDir(), "copy")
	for path, contents := range map[string]string{
		filepath.Join(".partitur", "runs", "some-run", "journal.jsonl"):           "journal\n",
		filepath.Join(".partitur", "work", "some-attempt", "worktree", "file.go"): "package fixture\n",
		"z-after-partitur.txt": "copied\n",
	} {
		path = filepath.Join(source, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := mutationtest.CopyRepository(destination, source); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".partitur")); !os.IsNotExist(err) {
		t.Fatalf("copied .partitur directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "z-after-partitur.txt")); err != nil {
		t.Fatalf("entry after .partitur was not copied: %v", err)
	}
}

func copyFaultpointMutationRepository(destination, source string) error {
	return mutationtest.CopyRepository(destination, source)
}
