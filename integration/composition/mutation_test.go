//go:build mutation

package composition_test

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMutationCompositionKillCutsRejectMisplacedProbes(t *testing.T) {
	goEnvironment := compositionMutationGoEnvironment(t)
	for _, mutation := range []struct {
		name, source, before, after, testPattern string
		testNames                                []string
	}{
		{
			name: "change-set captured probe after recorded append", source: "internal/driver/change_set.go",
			before:      "\tprobe.Reached(faultpoint.PointChangeSetCaptured)\n\tevent, err := attempt.ChangeSetRecordedEvent(changeSet)\n\tif err != nil {\n\t\treturn workspace.ChangeSet{}, err\n\t}\n\tif err := appendEvent(event); err != nil {\n\t\treturn workspace.ChangeSet{}, err\n\t}\n\tprobe.Reached(faultpoint.PointChangeSetRecorded)\n",
			after:       "\tevent, err := attempt.ChangeSetRecordedEvent(changeSet)\n\tif err != nil {\n\t\treturn workspace.ChangeSet{}, err\n\t}\n\tif err := appendEvent(event); err != nil {\n\t\treturn workspace.ChangeSet{}, err\n\t}\n\tprobe.Reached(faultpoint.PointChangeSetCaptured)\n\tprobe.Reached(faultpoint.PointChangeSetRecorded)\n",
			testPattern: "TestCompositionKillCuts/change_set.captured_to_recorded/before",
			testNames:   []string{"TestCompositionKillCuts/change_set.captured_to_recorded/before"},
		},
		{
			name: "change-set recorded probe before recorded append", source: "internal/driver/change_set.go",
			before:      "\tif err := appendEvent(event); err != nil {\n\t\treturn workspace.ChangeSet{}, err\n\t}\n\tprobe.Reached(faultpoint.PointChangeSetRecorded)\n",
			after:       "\tprobe.Reached(faultpoint.PointChangeSetRecorded)\n\tif err := appendEvent(event); err != nil {\n\t\treturn workspace.ChangeSet{}, err\n\t}\n",
			testPattern: "TestCompositionKillCuts/change_set.captured_to_recorded/after",
			testNames:   []string{"TestCompositionKillCuts/change_set.captured_to_recorded/after"},
		},
		{
			name: "movement evidence probe after terminal append", source: "internal/driver/movement_composition.go",
			before:      "\t\tstore.Reached(faultpoint.PointCompositionMovementEvidence)\n\t\tif afterEvidence != nil {\n\t\t\tafterEvidence()\n\t\t}\n\t\tterminal.CausationID = evidenceReceipt.Mutation.EventID\n\t\tif _, err := runstate.Apply(next, terminal); err != nil {\n\t\t\treturn err\n\t\t}\n\t\tif _, err := transaction.At(terminalAddress).Append(terminal); err != nil {\n\t\t\treturn err\n\t\t}\n\t\tstore.Reached(faultpoint.PointCompositionMovementTerminal)\n",
			after:       "\t\tif afterEvidence != nil {\n\t\t\tafterEvidence()\n\t\t}\n\t\tterminal.CausationID = evidenceReceipt.Mutation.EventID\n\t\tif _, err := runstate.Apply(next, terminal); err != nil {\n\t\t\treturn err\n\t\t}\n\t\tif _, err := transaction.At(terminalAddress).Append(terminal); err != nil {\n\t\t\treturn err\n\t\t}\n\t\tstore.Reached(faultpoint.PointCompositionMovementEvidence)\n\t\tstore.Reached(faultpoint.PointCompositionMovementTerminal)\n",
			testPattern: "TestCompositionKillCuts/composition.movement_evidence_to_terminal/(conflicted|failed)/before",
			testNames: []string{
				"TestCompositionKillCuts/composition.movement_evidence_to_terminal/conflicted/before",
				"TestCompositionKillCuts/composition.movement_evidence_to_terminal/failed/before",
			},
		},
		{
			name: "movement terminal probe before terminal append", source: "internal/driver/movement_composition.go",
			before:      "\t\tif _, err := transaction.At(terminalAddress).Append(terminal); err != nil {\n\t\t\treturn err\n\t\t}\n\t\tstore.Reached(faultpoint.PointCompositionMovementTerminal)\n",
			after:       "\t\tstore.Reached(faultpoint.PointCompositionMovementTerminal)\n\t\tif _, err := transaction.At(terminalAddress).Append(terminal); err != nil {\n\t\t\treturn err\n\t\t}\n",
			testPattern: "TestCompositionKillCuts/composition.movement_evidence_to_terminal/(conflicted|failed)/after",
			testNames: []string{
				"TestCompositionKillCuts/composition.movement_evidence_to_terminal/conflicted/after",
				"TestCompositionKillCuts/composition.movement_evidence_to_terminal/failed/after",
			},
		},
		{
			name: "candidate evidence probe after terminal append", source: "internal/driver/candidate_composition.go",
			before:      "\t\tstore.Reached(faultpoint.PointCompositionCandidateEvidence)\n\t\tterminal.CausationID = evidenceReceipt.Mutation.EventID\n\t\tif _, err := runstate.Apply(next, terminal); err != nil {\n\t\t\treturn err\n\t\t}\n\t\tif _, err := transaction.At(terminalAddress).Append(terminal); err != nil {\n\t\t\treturn err\n\t\t}\n\t\tstore.Reached(faultpoint.PointCompositionCandidateTerminal)\n",
			after:       "\t\tterminal.CausationID = evidenceReceipt.Mutation.EventID\n\t\tif _, err := runstate.Apply(next, terminal); err != nil {\n\t\t\treturn err\n\t\t}\n\t\tif _, err := transaction.At(terminalAddress).Append(terminal); err != nil {\n\t\t\treturn err\n\t\t}\n\t\tstore.Reached(faultpoint.PointCompositionCandidateEvidence)\n\t\tstore.Reached(faultpoint.PointCompositionCandidateTerminal)\n",
			testPattern: "TestCompositionKillCuts/composition.candidate_evidence_to_terminal/(conflicted|failed)/before",
			testNames: []string{
				"TestCompositionKillCuts/composition.candidate_evidence_to_terminal/conflicted/before",
				"TestCompositionKillCuts/composition.candidate_evidence_to_terminal/failed/before",
			},
		},
		{
			name: "candidate terminal probe before terminal append", source: "internal/driver/candidate_composition.go",
			before:      "\t\tif _, err := transaction.At(terminalAddress).Append(terminal); err != nil {\n\t\t\treturn err\n\t\t}\n\t\tstore.Reached(faultpoint.PointCompositionCandidateTerminal)\n",
			after:       "\t\tstore.Reached(faultpoint.PointCompositionCandidateTerminal)\n\t\tif _, err := transaction.At(terminalAddress).Append(terminal); err != nil {\n\t\t\treturn err\n\t\t}\n",
			testPattern: "TestCompositionKillCuts/composition.candidate_evidence_to_terminal/(conflicted|failed)/after",
			testNames: []string{
				"TestCompositionKillCuts/composition.candidate_evidence_to_terminal/conflicted/after",
				"TestCompositionKillCuts/composition.candidate_evidence_to_terminal/failed/after",
			},
		},
	} {
		mutation := mutation
		t.Run(mutation.name, func(t *testing.T) {
			assertCompositionMutationKilled(t, goEnvironment, mutation.source, mutation.before, mutation.after, mutation.testPattern, mutation.testNames)
		})
	}
}

type compositionMutationGoCaches struct {
	moduleCache string
	goPath      string
	buildCache  string
}

func compositionMutationGoEnvironment(t *testing.T) compositionMutationGoCaches {
	t.Helper()
	command := exec.Command("go", "env", "GOMODCACHE", "GOPATH", "GOCACHE")
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	values := strings.Fields(string(output))
	if len(values) != 3 {
		t.Fatalf("go env returned %d cache values, want 3", len(values))
	}
	return compositionMutationGoCaches{moduleCache: values[0], goPath: values[1], buildCache: values[2]}
}

func assertCompositionMutationKilled(t *testing.T, goEnvironment compositionMutationGoCaches, source, before, after, testPattern string, testNames []string) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve composition mutation source directory")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	copyRoot := filepath.Join(t.TempDir(), "partitur-mutation-copy")
	if err := copyCompositionMutationRepository(copyRoot, repositoryRoot); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(copyRoot, source)
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(contents), before); count != 1 {
		t.Fatalf("mutation anchor count = %d, want 1 for %s", count, source)
	}
	if err := os.WriteFile(sourcePath, []byte(strings.Replace(string(contents), before, after, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "-v", "./integration/composition", "-run", "^"+testPattern+"$", "-count=1")
	command.Dir = copyRoot
	command.Env = append(os.Environ(),
		"GOMODCACHE="+goEnvironment.moduleCache,
		"GOPATH="+goEnvironment.goPath,
		"GOCACHE="+goEnvironment.buildCache,
	)
	output, runErr := command.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("mutation non-result: child test timed out\n%s", output)
	}
	if strings.Contains(string(output), "[no tests to run]") {
		t.Fatalf("mutation non-result: child ran no targeted tests\n%s", output)
	}
	for _, testName := range testNames {
		if !strings.Contains(string(output), "=== RUN   "+testName) {
			t.Fatalf("mutation non-result: child did not run targeted test %s\n%s", testName, output)
		}
	}
	if runErr == nil {
		t.Fatalf("mutation survived: %s still passed after moving its probe\n%s", strings.Join(testNames, ", "), output)
	}
	if strings.Contains(string(output), "[build failed]") {
		t.Fatalf("mutation non-result: child build failed\n%s", output)
	}
	if strings.Contains(string(output), "panic:") {
		t.Fatalf("mutation non-result: child panicked\n%s", output)
	}
	for _, testName := range testNames {
		if !strings.Contains(string(output), "--- FAIL: "+testName) {
			t.Fatalf("mutation non-result: child did not fail the targeted assertion %s: %v\n%s", testName, runErr, output)
		}
	}
}

func copyCompositionMutationRepository(destination, source string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == ".git" && entry.IsDir() {
			return filepath.SkipDir
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
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
