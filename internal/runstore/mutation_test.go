//go:build mutation

package runstore

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

func TestMutationSyncFileOpensReadOnly(t *testing.T) {
	goEnvironment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	copyRoot := copyRunstoreMutationRepository(t)
	sourcePath := filepath.Join(copyRoot, "internal", "runstore", "fs.go")
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	anchor := "func (realFS) SyncFile(path string) error {\n\tfile, err := os.Open(path)\n"
	if count := strings.Count(string(contents), anchor); count != 1 {
		t.Fatalf("mutation anchor count = %d, want 1", count)
	}
	mutated := strings.Replace(string(contents), anchor, "func (realFS) SyncFile(path string) error {\n\tfile, err := os.OpenFile(path, os.O_RDWR, 0)\n", 1)
	if err := os.WriteFile(sourcePath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, "internal", "runstore"),
		Package:     ".",
		TestPattern: "TestIdempotentPublicationSyncsReadOnlyTarget",
		TestNames:   []string{"TestIdempotentPublicationSyncsReadOnlyTarget"},
		Environment: goEnvironment.ChildEnvironment(os.Environ()),
	})
	cancel()
	switch result.Outcome {
	case mutationtest.Killed:
		return
	case mutationtest.Survived:
		t.Fatalf("mutation survived: SyncFile regained a write-access requirement\n%s", result.Diagnostic())
	default:
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}

func TestMutationIdempotentPublicationRequiresEverySync(t *testing.T) {
	goEnvironment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		anchor      string
		replacement string
		witness     string
	}{
		{
			name: "file sync",
			anchor: "\t\tif err := transaction.store.fs.SyncFile(target); err != nil {\n" +
				"\t\t\treturn DurabilityReceipt{}, fmt.Errorf(\"sync existing publication: %w\", err)\n",
			replacement: "\t\tif err := error(nil); err != nil {\n" +
				"\t\t\treturn DurabilityReceipt{}, fmt.Errorf(\"sync existing publication: %w\", err)\n",
			witness: "file_sync",
		},
		{
			name: "directory sync",
			anchor: "\t\tif err := transaction.store.fs.SyncDir(parent); err != nil {\n" +
				"\t\t\treturn DurabilityReceipt{}, fmt.Errorf(\"sync publication directory: %w\", err)\n",
			replacement: "\t\tif err := error(nil); err != nil {\n" +
				"\t\t\treturn DurabilityReceipt{}, fmt.Errorf(\"sync publication directory: %w\", err)\n",
			witness: "directory_sync",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			copyRoot := copyRunstoreMutationRepository(t)
			sourcePath := filepath.Join(copyRoot, "internal", "runstore", "files.go")
			contents, err := os.ReadFile(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			if count := strings.Count(string(contents), test.anchor); count != 1 {
				t.Fatalf("mutation anchor count = %d, want 1", count)
			}
			mutated := strings.Replace(string(contents), test.anchor, test.replacement, 1)
			if err := os.WriteFile(sourcePath, []byte(mutated), 0o600); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			witness := "TestIdempotentPublicationRequiresSyncsBeforeReceipt/" + test.witness
			result := mutationtest.Run(ctx, mutationtest.Child{
				Dir:         filepath.Join(copyRoot, "internal", "runstore"),
				Package:     ".",
				TestPattern: witness,
				TestNames:   []string{witness},
				Environment: goEnvironment.ChildEnvironment(os.Environ()),
			})
			cancel()
			switch result.Outcome {
			case mutationtest.Killed:
				return
			case mutationtest.Survived:
				t.Fatalf("mutation survived: idempotent publication returned without %s\n%s", test.name, result.Diagnostic())
			default:
				t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
			}
		})
	}
}

func TestMutationPrepareCommitUsesLatestDurableReceiptForSilence(t *testing.T) {
	goEnvironment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	copyRoot := copyRunstoreMutationRepository(t)
	sourcePath := filepath.Join(copyRoot, "internal", "runstore", "prepare.go")
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	anchor := "\tif prepare.LatestQuiesceObservedAt != \"\" {\n"
	if count := strings.Count(string(contents), anchor); count != 1 {
		t.Fatalf("mutation anchor count = %d, want 1", count)
	}
	mutated := strings.Replace(string(contents), anchor, "\tif false {\n", 1)
	if err := os.WriteFile(sourcePath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := os.ReadFile(sourcePath)
	if err != nil || !strings.Contains(string(changed), "\tif false {\n") || strings.Contains(string(changed), anchor) {
		t.Fatalf("silence-baseline mutation did not apply: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, "internal", "runstore"),
		Package:     ".",
		TestPattern: "TestPrepareCommitUsesLatestDurableReceiptWithoutRecoveryRefresh",
		TestNames:   []string{"TestPrepareCommitUsesLatestDurableReceiptWithoutRecoveryRefresh"},
		Environment: goEnvironment.ChildEnvironment(os.Environ()),
	})
	cancel()
	switch result.Outcome {
	case mutationtest.Killed:
		return
	case mutationtest.Survived:
		t.Fatalf("mutation survived: latest durable quiesce receipt no longer controls silence\n%s", result.Diagnostic())
	default:
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}

func copyRunstoreMutationRepository(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve runstore test source directory")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	copyRoot := filepath.Join(t.TempDir(), "partitur-mutation-copy")
	if err := filepath.WalkDir(repositoryRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(repositoryRoot, path)
		if err != nil {
			return err
		}
		if relative == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(copyRoot, relative)
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
	}); err != nil {
		t.Fatal(err)
	}
	return copyRoot
}
