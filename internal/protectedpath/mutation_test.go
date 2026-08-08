//go:build mutation

package protectedpath

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationConsumerBehaviors(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve protected-path test source directory")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	baseline := filepath.Join(t.TempDir(), "partitur-baseline")
	if err := copyRepository(baseline, repository); err != nil {
		t.Fatal(err)
	}

	for _, mutation := range []struct {
		name, source, directory, before, after, test string
	}{
		{
			name:      "presence detection",
			source:    filepath.Join("internal", "workspace", "change_set.go"),
			directory: filepath.Join("internal", "workspace"),
			before:    "protectedpath.WorktreeNames()",
			after:     "[]string{}",
			test:      "TestProtectedPathsPresentFindsExistingProtectedWorktreePaths",
		},
		{
			name:      "capture exclusion",
			source:    filepath.Join("internal", "workspace", "change_set.go"),
			directory: filepath.Join("internal", "workspace"),
			before:    "protectedpath.CaptureExclusions()",
			after:     "[]string{}",
			test:      "TestCaptureChangeSetExcludesProtectedRootScore",
		},
		{
			name:      "historical A.5 recomputation",
			source:    filepath.Join("internal", "executiondep", "executiondep.go"),
			directory: filepath.Join("internal", "executiondep"),
			before:    "protectedpath.AdvertisedGlobs()",
			after:     "func(paths func() []string) []string { values := paths(); return []string{values[1], values[0], values[2]} }(protectedpath.AdvertisedGlobs)",
			test:      "TestEqualPreservesRecordedProtectedPathsHash",
		},
		{
			name:      "brief payload",
			source:    filepath.Join("internal", "driver", "driver.go"),
			directory: filepath.Join("internal", "driver"),
			before:    "protectedpath.AdvertisedGlobs()",
			after:     "func(paths func() []string) []string { values := paths(); return []string{values[1], values[0], values[2]} }(protectedpath.AdvertisedGlobs)",
			test:      "TestExecuteBriefProjectsProtectedPaths",
		},
		{
			name:      "gitdir indirection",
			source:    filepath.Join("internal", "protectedpath", "protectedpath.go"),
			directory: filepath.Join("internal", "workspace"),
			before:    "names(gitDirectory, rootScore, partiturDirectory)",
			after:     "names(rootScore, partiturDirectory)",
			test:      "TestVerifyProtectedPathsRejectsChangedGitDirIndirection",
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			copyRoot := filepath.Join(t.TempDir(), "partitur")
			if err := copyRepository(copyRoot, baseline); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(copyRoot, mutation.source)
			contents, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			if count := strings.Count(string(contents), mutation.before); count != 1 {
				t.Fatalf("mutation anchor count=%d, want 1 for %q", count, mutation.before)
			}
			if err := os.WriteFile(source, []byte(strings.Replace(string(contents), mutation.before, mutation.after, 1)), 0o600); err != nil {
				t.Fatal(err)
			}
			mutated, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(mutated), mutation.after) || strings.Contains(string(mutated), mutation.before) {
				t.Fatalf("mutation did not apply to %s", source)
			}
			buildContext, cancelBuild := context.WithTimeout(context.Background(), 90*time.Second)
			build := exec.CommandContext(buildContext, "go", "build", "./...")
			build.Dir = copyRoot
			build.Env = environment.ChildEnvironment(os.Environ(), "PARTITUR_MUTATION_CHILD=1")
			buildOutput, buildErr := build.CombinedOutput()
			cancelBuild()
			if buildErr != nil {
				t.Fatalf("mutated tree does not build: %v\n%s", buildErr, buildOutput)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			result := mutationtest.Run(ctx, mutationtest.Child{
				Dir:         filepath.Join(copyRoot, mutation.directory),
				Package:     ".",
				TestPattern: mutation.test,
				TestNames:   []string{mutation.test},
				Environment: environment.ChildEnvironment(os.Environ(), "PARTITUR_MUTATION_CHILD=1"),
			})
			cancel()
			if err := os.WriteFile(source, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			restored, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(restored, contents) {
				t.Fatalf("mutation restore differs for %s", source)
			}
			if result.Outcome != mutationtest.Killed {
				t.Fatalf("mutation non-result: %s", result.Diagnostic())
			}
		})
	}
}

func copyRepository(destination, source string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
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
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, contents, 0o600)
	})
}
