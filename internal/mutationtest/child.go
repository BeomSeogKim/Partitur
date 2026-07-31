//go:build mutation

// Package mutationtest classifies mutation child runs from go test JSON events.
package mutationtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

type GoEnvironment struct {
	ModuleCache string
	GoPath      string
	BuildCache  string
}

func SnapshotGoEnvironment() (GoEnvironment, error) {
	output, err := exec.Command("go", "env", "GOMODCACHE", "GOPATH", "GOCACHE").Output()
	if err != nil {
		return GoEnvironment{}, err
	}
	values := strings.Fields(string(output))
	if len(values) != 3 {
		return GoEnvironment{}, fmt.Errorf("go env returned %d cache values, want 3", len(values))
	}
	return GoEnvironment{ModuleCache: values[0], GoPath: values[1], BuildCache: values[2]}, nil
}

func (environment GoEnvironment) ChildEnvironment(parent []string, extra ...string) []string {
	child := make([]string, 0, len(parent)+3+len(extra))
	for _, value := range parent {
		if strings.HasPrefix(value, "GOMODCACHE=") || strings.HasPrefix(value, "GOPATH=") || strings.HasPrefix(value, "GOCACHE=") {
			continue
		}
		child = append(child, value)
	}
	child = append(child,
		"GOMODCACHE="+environment.ModuleCache,
		"GOPATH="+environment.GoPath,
		"GOCACHE="+environment.BuildCache,
	)
	return append(child, extra...)
}

type Outcome string

const (
	Killed    Outcome = "killed"
	Survived  Outcome = "survived"
	NonResult Outcome = "non-result"
)

type Child struct {
	Dir         string
	Package     string
	TestPattern string
	TestNames   []string
	Environment []string
}

type Result struct {
	Outcome  Outcome
	Reason   string
	Output   string
	ExitCode int
}

type event struct {
	Action string
	Test   string
	Output string
}

func Run(ctx context.Context, child Child) Result {
	result := Result{Outcome: NonResult}
	switch {
	case len(child.TestNames) == 0:
		result.Reason = "child has no targeted tests"
		return result
	case child.TestPattern == "":
		result.Reason = "child has an empty test pattern"
		return result
	case child.Package == "":
		result.Reason = "child has an empty package"
		return result
	case child.Dir == "":
		result.Reason = "child has an empty working directory"
		return result
	}
	command := exec.CommandContext(ctx, "go", "test", "-json", child.Package, "-run", "^"+child.TestPattern+"$", "-count=1")
	command.Dir = child.Dir
	command.Env = child.Environment
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	runErr := command.Run()
	result = classify(ctx, child.TestNames, stdout.Bytes(), stderr.Bytes(), runErr)
	return result
}

func (result Result) Diagnostic() string {
	if result.Output == "" {
		return fmt.Sprintf("exit code %d", result.ExitCode)
	}
	return fmt.Sprintf("exit code %d\n%s", result.ExitCode, result.Output)
}

func classify(ctx context.Context, targets []string, stdout, stderr []byte, runErr error) Result {
	output := string(stdout)
	if len(stderr) != 0 {
		output += string(stderr)
	}
	exitCode := 0
	if runErr != nil {
		exitCode = 1
		if exitError, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		}
	}
	result := Result{Outcome: NonResult, Output: output, ExitCode: exitCode}
	if ctx.Err() == context.DeadlineExceeded {
		result.Reason = "child test timed out"
		return result
	}

	decoder := json.NewDecoder(bytes.NewReader(stdout))
	targetSet := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		targetSet[target] = struct{}{}
	}
	ran := make(map[string]bool, len(targets))
	terminal := make(map[string]string, len(targets))
	var unexpectedFailures []string
	var testOutput strings.Builder
	for {
		var current event
		err := decoder.Decode(&current)
		if err == io.EOF {
			break
		}
		if err != nil {
			result.Reason = "child emitted an invalid go test JSON event stream: " + err.Error()
			return result
		}
		if current.Output != "" {
			testOutput.WriteString(current.Output)
		}
		if _, target := targetSet[current.Test]; target && current.Action == "run" {
			ran[current.Test] = true
		}
		if _, target := targetSet[current.Test]; target && (current.Action == "pass" || current.Action == "fail" || current.Action == "skip") {
			terminal[current.Test] = current.Action
		}
		if current.Action == "fail" && current.Test != "" && !isTargetOrAggregate(current.Test, targets) {
			unexpectedFailures = append(unexpectedFailures, current.Test)
		}
	}
	if strings.Contains(testOutput.String(), "[build failed]") {
		result.Reason = "child build failed"
		return result
	}
	if strings.Contains(testOutput.String(), "panic:") {
		result.Reason = "child panicked"
		return result
	}
	if len(unexpectedFailures) != 0 {
		result.Reason = "child failed a non-target test: " + strings.Join(unexpectedFailures, ", ")
		return result
	}
	for _, target := range targets {
		if !ran[target] {
			result.Reason = "child did not run targeted test " + target
			return result
		}
		if terminal[target] == "" {
			result.Reason = "targeted test did not finish " + target
			return result
		}
		if terminal[target] == "skip" {
			result.Reason = "targeted test was skipped " + target
			return result
		}
	}

	allFailed := true
	allPassed := true
	for _, target := range targets {
		allFailed = allFailed && terminal[target] == "fail"
		allPassed = allPassed && terminal[target] == "pass"
	}
	switch {
	case allFailed && runErr != nil:
		result.Outcome = Killed
		result.Reason = "targeted tests ran and failed"
	case allPassed && runErr == nil:
		result.Outcome = Survived
		result.Reason = "targeted tests ran and passed"
	case allFailed:
		result.Reason = "targeted tests failed but child exited zero"
	case allPassed:
		result.Reason = "targeted tests passed but child exited non-zero"
	default:
		result.Reason = "targeted tests had mixed outcomes"
	}
	return result
}

func isTargetOrAggregate(name string, targets []string) bool {
	for _, target := range targets {
		if name == target || strings.HasPrefix(target, name+"/") {
			return true
		}
	}
	return false
}
