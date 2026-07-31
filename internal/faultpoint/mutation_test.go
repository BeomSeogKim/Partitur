//go:build mutation

package faultpoint

import (
	"context"
	"io"
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
		Environment: goEnvironment.ChildEnvironment(os.Environ()),
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

func copyFaultpointMutationRepository(destination, source string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == ".git" {
			return filepath.SkipDir
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		info, err := input.Stat()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}
