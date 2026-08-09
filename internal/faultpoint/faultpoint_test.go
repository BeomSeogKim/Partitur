//go:build faultprobe

package faultpoint

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	probeEnvironmentHelper        = "PARTITUR_FAULTPOINT_PROBE_HELPER"
	probeEnvironmentHelperTimeout = 30 * time.Second
)

func TestProbeFromEnvironmentGuardsDescriptors(t *testing.T) {
	switch os.Getenv(probeEnvironmentHelper) {
	case "standard":
		probe := ProbeFromEnvironment()
		if _, ok := probe.(Nop); !ok {
			fmt.Fprintf(os.Stderr, "probe type = %T, want Nop\n", probe)
			os.Exit(2)
		}
		probe.Reached("test.standard")
		os.Exit(0)
	case "pipe":
		probe := ProbeFromEnvironment()
		if _, ok := probe.(*pipeProbe); !ok {
			fmt.Fprintf(os.Stderr, "probe type = %T, want *pipeProbe\n", probe)
			os.Exit(2)
		}
		probe.Reached("test.pipe")
		os.Exit(0)
	case "closed_release":
		probe := ProbeFromEnvironment()
		if _, ok := probe.(*pipeProbe); !ok {
			fmt.Fprintf(os.Stderr, "probe type = %T, want *pipeProbe\n", probe)
			os.Exit(2)
		}
		probe.Reached("test.closed_release")
		os.Exit(0)
	case "closed_notify":
		probe := ProbeFromEnvironment()
		if _, ok := probe.(*pipeProbe); !ok {
			fmt.Fprintf(os.Stderr, "probe type = %T, want *pipeProbe\n", probe)
			os.Exit(2)
		}
		probe.Reached("test.closed_notify")
		os.Exit(0)
	}

	t.Run("rejects standard descriptors without stdout or block", testProbeRejectsStandardDescriptors)
	t.Run("rejects non-pipe descriptor", testProbeRejectsNonPipeDescriptor)
	t.Run("accepts pipe at fd 3", testProbeAcceptsPipeAtFirstInheritedFD)
	t.Run("exits when release pipe closes", testProbeExitsWhenReleaseCloses)
	t.Run("exits when notify pipe closes", testProbeExitsWhenNotifyCloses)
}

func TestRequireHarnessBuildAcceptsFaultProbeBuild(t *testing.T) {
	t.Setenv(probeHarnessRequiredEnv, "1")
	if err := RequireHarnessBuild(); err != nil {
		t.Fatalf("RequireHarnessBuild() error = %v", err)
	}
}

func TestProbeFromEnvironmentRejectsDescriptorsWithoutTakingOwnership(t *testing.T) {
	t.Run("non-pipe", testProbeRejectsNonPipeDescriptorWithoutTakingOwnership)
	t.Run("mixed pair", testProbeRejectsMixedPairWithoutTakingOwnership)
}

func testProbeRejectsStandardDescriptors(t *testing.T) {
	t.Helper()
	notifyRead, notifyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer notifyRead.Close()
	defer notifyWrite.Close()
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseRead.Close()
	defer releaseWrite.Close()

	ctx, cancel := context.WithTimeout(context.Background(), probeEnvironmentHelperTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProbeFromEnvironmentGuardsDescriptors$")
	command.Env = append(os.Environ(),
		probeEnvironmentHelper+"=standard",
		probeNotifyFDEnv+"=1",
		probeReleaseFDEnv+"=0",
	)
	command.Stdin = releaseRead
	command.Stdout = notifyWrite
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if err := releaseRead.Close(); err != nil {
		t.Fatal(err)
	}
	if err := notifyWrite.Close(); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	output, readErr := io.ReadAll(notifyRead)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("rejects standard descriptors helper blocked for %s", probeEnvironmentHelperTimeout)
	}
	if err != nil {
		t.Fatalf("helper failed: %v; stdout=%q stderr=%q", err, output, stderr.String())
	}
	if string(output) != "" {
		t.Fatalf("stdout=%q, want empty", output)
	}
}

func testProbeRejectsNonPipeDescriptor(t *testing.T) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "not-a-pipe")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	t.Setenv(probeNotifyFDEnv, strconv.Itoa(int(file.Fd())))
	t.Setenv(probeReleaseFDEnv, strconv.Itoa(int(file.Fd())))
	probe := ProbeFromEnvironment()
	if _, ok := probe.(Nop); !ok {
		t.Fatalf("probe type = %T, want Nop", probe)
	}
}

func testProbeRejectsNonPipeDescriptorWithoutTakingOwnership(t *testing.T) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "not-a-pipe")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	fd := int(file.Fd())
	t.Setenv(probeNotifyFDEnv, strconv.Itoa(fd))
	t.Setenv(probeReleaseFDEnv, "not-a-descriptor")
	if _, ok := ProbeFromEnvironment().(Nop); !ok {
		t.Fatal("probe must reject non-pipe descriptors")
	}
	forceGC()
	assertFDOpenAndUsable(t, fd)
	runtime.KeepAlive(file)
}

func testProbeRejectsMixedPairWithoutTakingOwnership(t *testing.T) {
	t.Helper()
	notifyRead, notifyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer notifyRead.Close()
	defer notifyWrite.Close()
	release, err := os.CreateTemp(t.TempDir(), "not-a-pipe")
	if err != nil {
		t.Fatal(err)
	}
	defer release.Close()
	notifyFD := int(notifyWrite.Fd())
	t.Setenv(probeNotifyFDEnv, strconv.Itoa(notifyFD))
	t.Setenv(probeReleaseFDEnv, strconv.Itoa(int(release.Fd())))
	if _, ok := ProbeFromEnvironment().(Nop); !ok {
		t.Fatal("probe must reject a mixed descriptor pair")
	}
	forceGC()
	assertFDOpen(t, notifyFD)
	if _, err := notifyWrite.Write([]byte{1}); err != nil {
		t.Fatalf("notify pipe write after rejected mixed pair: %v", err)
	}
	var received [1]byte
	if _, err := notifyRead.Read(received[:]); err != nil {
		t.Fatalf("notify pipe read after rejected mixed pair: %v", err)
	}
	runtime.KeepAlive(notifyWrite)
	runtime.KeepAlive(release)
}

func forceGC() {
	runtime.GC()
	runtime.GC()
}

func assertFDOpenAndUsable(t *testing.T, fd int) {
	t.Helper()
	assertFDOpen(t, fd)
	if written, err := syscall.Write(fd, []byte{1}); err != nil || written != 1 {
		t.Fatalf("descriptor write after rejection = %d, %v; want one byte without error", written, err)
	}
}

func assertFDOpen(t *testing.T, fd int) {
	t.Helper()
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		t.Fatalf("descriptor fd %d after rejection: %v", fd, err)
	}
}

func testProbeAcceptsPipeAtFirstInheritedFD(t *testing.T) {
	t.Helper()
	notifyRead, notifyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer notifyRead.Close()
	defer notifyWrite.Close()
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseRead.Close()
	defer releaseWrite.Close()

	ctx, cancel := context.WithTimeout(context.Background(), probeEnvironmentHelperTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProbeFromEnvironmentGuardsDescriptors$")
	command.Env = append(os.Environ(),
		probeEnvironmentHelper+"=pipe",
		probeNotifyFDEnv+"=3",
		probeReleaseFDEnv+"=4",
	)
	command.ExtraFiles = []*os.File{notifyWrite, releaseRead}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if err := notifyWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := releaseRead.Close(); err != nil {
		t.Fatal(err)
	}

	line := make(chan string, 1)
	readErr := make(chan error, 1)
	go func() {
		value, err := bufio.NewReader(notifyRead).ReadString('\n')
		if err != nil {
			readErr <- err
			return
		}
		line <- value
	}()
	select {
	case got := <-line:
		if !strings.HasPrefix(got, "test.pipe ") {
			t.Fatalf("notification=%q, want test.pipe", got)
		}
	case err := <-readErr:
		t.Fatalf("read notification: %v", err)
	case <-ctx.Done():
		t.Fatalf("accepts pipe at fd 3 helper blocked for %s", probeEnvironmentHelperTimeout)
	}
	if _, err := releaseWrite.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("accepts pipe at fd 3 helper blocked for %s", probeEnvironmentHelperTimeout)
		}
		t.Fatalf("helper failed: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("helper stdout=%q stderr=%q, want empty", stdout.String(), stderr.String())
	}
}

func testProbeExitsWhenReleaseCloses(t *testing.T) {
	t.Helper()
	notifyRead, notifyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer notifyRead.Close()
	defer notifyWrite.Close()
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseRead.Close()
	defer releaseWrite.Close()

	ctx, cancel := context.WithTimeout(context.Background(), probeEnvironmentHelperTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProbeFromEnvironmentGuardsDescriptors$")
	command.Env = append(os.Environ(),
		probeEnvironmentHelper+"=closed_release",
		probeNotifyFDEnv+"=3",
		probeReleaseFDEnv+"=4",
	)
	command.ExtraFiles = []*os.File{notifyWrite, releaseRead}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if err := notifyWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := releaseRead.Close(); err != nil {
		t.Fatal(err)
	}
	if err := releaseWrite.Close(); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("closed release helper blocked for %s", probeEnvironmentHelperTimeout)
	}
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 1 {
		t.Fatalf("closed release helper error=%v, want exit 1", err)
	}
}

func testProbeExitsWhenNotifyCloses(t *testing.T) {
	t.Helper()
	notifyRead, notifyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer notifyRead.Close()
	defer notifyWrite.Close()
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseRead.Close()
	defer releaseWrite.Close()

	if err := notifyRead.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeEnvironmentHelperTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProbeFromEnvironmentGuardsDescriptors$")
	command.Env = append(os.Environ(),
		probeEnvironmentHelper+"=closed_notify",
		probeNotifyFDEnv+"=3",
		probeReleaseFDEnv+"=4",
	)
	command.ExtraFiles = []*os.File{notifyWrite, releaseRead}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if err := notifyWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := releaseRead.Close(); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("closed notify helper blocked for %s", probeEnvironmentHelperTimeout)
	}
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 1 {
		t.Fatalf("closed notify helper error=%v, want exit 1", err)
	}
}

func TestAppendixEEdgeIDsAreCompleteAndUnique(t *testing.T) {
	designEdges := edgeIDsFromDesign(t)
	sourceEdges := edgeIDsFromPackageSource(t)
	harnessEdges := edgeIDsFromHarness(t)

	designSet := uniqueEdgeIDs(t, "DESIGN E.2", designEdges)
	sourceSet := uniqueEdgeIDs(t, "Go EdgeID constants", sourceEdges)
	harnessSet := uniqueEdgeIDs(t, "HARNESS selection manifest", harnessEdges)

	requireSameEdgeIDs(t, "DESIGN E.2", designSet, "Go EdgeID constants", sourceSet)
	requireSameEdgeIDs(t, "DESIGN E.2", designSet, "HARNESS selection manifest", harnessSet)
}

func requireSameEdgeIDs(t *testing.T, leftName string, left map[string]bool, rightName string, right map[string]bool) {
	t.Helper()
	var onlyInLeft []string
	for edge := range left {
		if !right[edge] {
			onlyInLeft = append(onlyInLeft, edge)
		}
	}
	var onlyInRight []string
	for edge := range right {
		if !left[edge] {
			onlyInRight = append(onlyInRight, edge)
		}
	}
	sort.Strings(onlyInLeft)
	sort.Strings(onlyInRight)
	if len(onlyInLeft) != 0 || len(onlyInRight) != 0 {
		t.Fatalf("edge ID catalog mismatch: only in %s: %q; only in %s: %q",
			leftName, onlyInLeft, rightName, onlyInRight)
	}
}

func edgeIDsFromDesign(t *testing.T) []string {
	t.Helper()

	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(contents), "\n")

	const catalogHeading = "## E.2 The catalog"
	const nextHeadingPrefix = "## E.3 "
	catalogStart := -1
	catalogHeadingCount := 0
	nextHeading := -1
	nextHeadingCount := 0
	for index, line := range lines {
		if line == catalogHeading {
			catalogStart = index + 1
			catalogHeadingCount++
		}
		if strings.HasPrefix(line, nextHeadingPrefix) {
			nextHeading = index
			nextHeadingCount++
		}
	}
	if catalogHeadingCount != 1 {
		t.Fatalf("%s heading count = %d, want 1", catalogHeading, catalogHeadingCount)
	}
	if nextHeadingCount != 1 {
		t.Fatalf("%s heading count = %d, want 1", strings.TrimSpace(nextHeadingPrefix), nextHeadingCount)
	}
	if nextHeading <= catalogStart {
		t.Fatalf("%s must follow %s", strings.TrimSpace(nextHeadingPrefix), catalogHeading)
	}

	edgeRow := regexp.MustCompile("^\\| `([a-z][a-z0-9_.]*)` \\|")
	var edges []string
	for _, line := range lines[catalogStart:nextHeading] {
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "| Edge |") || strings.HasPrefix(line, "|---") {
			continue
		}
		match := edgeRow.FindStringSubmatch(line)
		if match == nil {
			t.Fatalf("unparseable %s table row %q", catalogHeading, line)
		}
		edges = append(edges, match[1])
	}
	if len(edges) == 0 {
		t.Fatalf("%s extraction produced no edge IDs", catalogHeading)
	}
	return edges
}

func edgeIDsFromPackageSource(t *testing.T) []string {
	t.Helper()

	pkg := parsePackageSource(t)
	var edges []string
	for _, file := range pkg.Files {
		for _, declaration := range file.Decls {
			constants, ok := declaration.(*ast.GenDecl)
			if !ok || constants.Tok != token.CONST {
				continue
			}
			for _, spec := range constants.Specs {
				values := spec.(*ast.ValueSpec)
				hasEdgeName := false
				for _, name := range values.Names {
					if strings.HasPrefix(name.Name, "Edge") {
						hasEdgeName = true
					}
				}
				edgeType, ok := values.Type.(*ast.Ident)
				if !ok || edgeType.Name != "EdgeID" {
					if hasEdgeName {
						t.Fatalf("Edge-prefixed const declaration must have explicit type EdgeID")
					}
					continue
				}
				if len(values.Names) != len(values.Values) {
					t.Fatalf("EdgeID const declaration has %d names and %d values", len(values.Names), len(values.Values))
				}
				for index, expression := range values.Values {
					if !strings.HasPrefix(values.Names[index].Name, "Edge") {
						t.Fatalf("EdgeID const name %q must start with Edge", values.Names[index].Name)
					}
					literal, ok := expression.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						t.Fatalf("EdgeID const value must be a string literal, got %T", expression)
					}
					edge, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatalf("parse EdgeID const value %q: %v", literal.Value, err)
					}
					edges = append(edges, edge)
				}
			}
		}
	}
	if len(edges) == 0 {
		t.Fatal("package source contains no typed EdgeID constants")
	}
	return edges
}

func edgeIDsFromHarness(t *testing.T) []string {
	t.Helper()

	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "HARNESS.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(contents), "\n")

	const manifestHeading = "## Selection manifest"
	const nextHeading = "## Gate-cut dispositions"
	manifestStart := -1
	manifestHeadingCount := 0
	manifestEnd := -1
	nextHeadingCount := 0
	for index, line := range lines {
		if line == manifestHeading {
			manifestStart = index + 1
			manifestHeadingCount++
		}
		if line == nextHeading {
			manifestEnd = index
			nextHeadingCount++
		}
	}
	if manifestHeadingCount != 1 {
		t.Fatalf("%s heading count = %d, want 1", manifestHeading, manifestHeadingCount)
	}
	if nextHeadingCount != 1 {
		t.Fatalf("%s heading count = %d, want 1", nextHeading, nextHeadingCount)
	}
	if manifestEnd <= manifestStart {
		t.Fatalf("%s must follow %s", nextHeading, manifestHeading)
	}

	edgeRow := regexp.MustCompile("^\\| `([a-z][a-z0-9_.]*)` \\|")
	var edges []string
	for _, line := range lines[manifestStart:manifestEnd] {
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "| Edge |") || strings.HasPrefix(line, "|---") {
			continue
		}
		match := edgeRow.FindStringSubmatch(line)
		if match == nil {
			t.Fatalf("unparseable %s table row %q", manifestHeading, line)
		}
		edges = append(edges, match[1])
	}
	if len(edges) == 0 {
		t.Fatalf("%s extraction produced no edge IDs", manifestHeading)
	}
	return edges
}

func uniqueEdgeIDs(t *testing.T, source string, edges []string) map[string]bool {
	t.Helper()

	seen := make(map[string]bool, len(edges))
	for _, edge := range edges {
		if edge == "" {
			t.Fatalf("%s contains an empty edge ID", source)
		}
		if seen[edge] {
			t.Fatalf("%s contains duplicate edge ID %q", source, edge)
		}
		seen[edge] = true
	}
	return seen
}

func TestBoundaryPointIDsAreSemanticAndUnique(t *testing.T) {
	points := []PointID{
		PointPrepareObserved,
		PointQuiesceSessionsSwept,
		PointQuiesceLeaseMoved,
		PointQuiesceCommitLockHeld,
		PointCancelSessionsSwept,
		PointCancelSnapshotQuarantined,
		PointCancelExecutionStopped,
		PointCancelFenceDecided,
		PointCancelRunCancelled,
		PointCancelLeaseRemoved,
		PointSupersedeSessionsSwept,
		PointSupersedeFenceDecided,
		PointLaunchAdapterMarkerHeld,
		PointLaunchAdapterGateReleased,
		PointLaunchCriterionMarkerHeld,
		PointLaunchCriterionGateReleased,
		PointAuthorityGranted,
		PointAuthorityLeaseCreated,
		PointLaunchAdapterIdentityPublished,
		PointLaunchAdapterIdentityRecorded,
		PointLaunchCriterionIdentityPublished,
		PointLaunchCriterionIdentityRecorded,
		PointExecuteAdapterSwept,
		PointExecuteIntervalStopped,
		PointExecuteOutcomeRecorded,
		PointAcceptanceFailureRecorded,
		PointAcceptanceEvaluationCompleted,
		PointHumanGateDecisionRequested,
		PointLifecycleAttemptCompleted,
		PointLifecycleMovementSucceeded,
		PointLifecycleMovementFailed,
		PointLifecycleRunFailed,
		PointChangeSetCaptured,
		PointChangeSetRecorded,
		PointCompositionMovementEvidence,
		PointCompositionMovementTerminal,
		PointCompositionCandidateEvidence,
		PointCompositionCandidateTerminal,
		PointApplyTransactionStarted,
		PointApplyCheckoutMutated,
	}
	declared := make([]string, len(points))
	for index, point := range points {
		declared[index] = string(point)
	}
	declaredSet := uniquePointIDs(t, "Boundary PointID list", declared)
	sourceSet := uniquePointIDs(t, "Go PointID constants", pointIDsFromPackageSource(t))
	requireSamePointIDs(t, "Boundary PointID list", declaredSet, "Go PointID constants", sourceSet)
}

func pointIDsFromPackageSource(t *testing.T) []string {
	t.Helper()

	pkg := parsePackageSource(t)
	var points []string
	for _, file := range pkg.Files {
		for _, declaration := range file.Decls {
			constants, ok := declaration.(*ast.GenDecl)
			if !ok || constants.Tok != token.CONST {
				continue
			}
			for _, spec := range constants.Specs {
				values := spec.(*ast.ValueSpec)
				hasPointName := false
				for _, name := range values.Names {
					if strings.HasPrefix(name.Name, "Point") {
						hasPointName = true
					}
				}
				pointType, ok := values.Type.(*ast.Ident)
				if !ok || pointType.Name != "PointID" {
					if hasPointName {
						t.Fatalf("Point-prefixed const declaration must have explicit type PointID")
					}
					continue
				}
				if len(values.Names) != len(values.Values) {
					t.Fatalf("PointID const declaration has %d names and %d values", len(values.Names), len(values.Values))
				}
				for index, expression := range values.Values {
					if !strings.HasPrefix(values.Names[index].Name, "Point") {
						t.Fatalf("PointID const name %q must start with Point", values.Names[index].Name)
					}
					literal, ok := expression.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						t.Fatalf("PointID const value must be a string literal, got %T", expression)
					}
					point, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatalf("parse PointID const value %q: %v", literal.Value, err)
					}
					points = append(points, point)
				}
			}
		}
	}
	if len(points) == 0 {
		t.Fatal("package source contains no typed PointID constants")
	}
	return points
}

func uniquePointIDs(t *testing.T, source string, points []string) map[string]bool {
	t.Helper()

	seen := make(map[string]bool, len(points))
	for _, point := range points {
		if point == "" {
			t.Fatalf("%s contains an empty point ID", source)
		}
		if seen[point] {
			t.Fatalf("%s contains duplicate point ID %q", source, point)
		}
		seen[point] = true
	}
	return seen
}

func requireSamePointIDs(t *testing.T, leftName string, left map[string]bool, rightName string, right map[string]bool) {
	t.Helper()

	var onlyInLeft []string
	for point := range left {
		if !right[point] {
			onlyInLeft = append(onlyInLeft, point)
		}
	}
	var onlyInRight []string
	for point := range right {
		if !left[point] {
			onlyInRight = append(onlyInRight, point)
		}
	}
	sort.Strings(onlyInLeft)
	sort.Strings(onlyInRight)
	if len(onlyInLeft) != 0 || len(onlyInRight) != 0 {
		t.Fatalf("point ID catalog mismatch: only in %s: %q; only in %s: %q", leftName, onlyInLeft, rightName, onlyInRight)
	}
}

func TestPackageHasNoGlobalProbeSetter(t *testing.T) {
	pkg := parsePackageSource(t)
	for _, file := range pkg.Files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Recv == nil && function.Name.Name == "SetProbe" {
				t.Fatal("package-level SetProbe is forbidden")
			}
		}
	}
}

func parsePackageSource(t *testing.T) *ast.Package {
	t.Helper()

	set := token.NewFileSet()
	packages, err := parser.ParseDir(set, ".", func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ok := packages["faultpoint"]
	if !ok {
		t.Fatal("faultpoint package not found in package source")
	}
	return pkg
}
