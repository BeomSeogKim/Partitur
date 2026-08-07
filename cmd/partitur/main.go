package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/acceptance"
	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/amendment"
	"github.com/BeomSeogKim/Partitur/internal/amendmentexec"
	"github.com/BeomSeogKim/Partitur/internal/cancelwait"
	"github.com/BeomSeogKim/Partitur/internal/driver"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	logstream "github.com/BeomSeogKim/Partitur/internal/logs"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/recoveryexec"
	"github.com/BeomSeogKim/Partitur/internal/recoveryobs"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	statusprojection "github.com/BeomSeogKim/Partitur/internal/status"
	validation "github.com/BeomSeogKim/Partitur/internal/validate"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

var version = "dev"

var errOutputStream = errors.New("output stream is unwritable")

// afterAmendmentBaseCapture is an interleave seam for command tests. Production
// leaves it as a no-op; the capture itself remains before §9 admission.
var afterAmendmentBaseCapture = func() {}

type validateRunner func() validation.Result
type prepareRunner func() (*validation.Preparation, validation.Result)
type runDriver func(
	context.Context,
	*validation.Preparation,
	driver.StartedObserver,
) driver.Result

type statusReader func(string) (statusprojection.Report, error)
type resumeRunner func(context.Context, string) (recoveryexec.Result, error)

type resumeSelectionError struct {
	err error
}

func (err resumeSelectionError) Error() string { return err.err.Error() }

func (err resumeSelectionError) Unwrap() error { return err.err }

type logsReader func(string) (logstream.Snapshot, error)
type logsStreamer func(
	context.Context,
	func() (logstream.Snapshot, error),
	io.Writer,
	logstream.StreamOptions,
) error

func main() {
	if err := faultpoint.RequireHarnessBuild(); err != nil {
		fmt.Fprintf(os.Stderr, "partitur: %v\n", err)
		os.Exit(2)
	}
	// A recovery process has no SIGUSR1 relay. Ignore the optional wake before
	// command dispatch, which is before any command can make a lease durable.
	signal.Ignore(syscall.SIGUSR1)
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWithRunners(
		args,
		stdout,
		stderr,
		validation.Run,
		validation.Prepare,
		productionRunDriver,
	)
}

// productionRunDriver is the command composition root for a live run. It is
// kept outside driver because amendmentexec implements driver's callback.
func productionRunDriver(
	ctx context.Context,
	preparation *validation.Preparation,
	started driver.StartedObserver,
) driver.Result {
	return driver.RunWithExecutionDependencies(
		ctx,
		preparation,
		started,
		productionExecutionDependencies(faultpoint.ProbeFromEnvironment()),
	)
}

func productionExecutionDependencies(probe faultpoint.Probe) driver.ExecutionDependencies {
	execution := driver.DefaultExecutionDependencies(probe)
	execution.ReceiptObserver = runstore.ReceiptObserverFromEnvironment()
	execution.ProposalDisposition = amendmentexec.New()
	return execution
}

func runWithValidate(
	args []string,
	stdout, stderr io.Writer,
	validate validateRunner,
) int {
	return runWithRunners(
		args,
		stdout,
		stderr,
		validate,
		func() (*validation.Preparation, validation.Result) {
			return nil, validation.Result{}
		},
		func(
			context.Context,
			*validation.Preparation,
			driver.StartedObserver,
		) driver.Result {
			return driver.Result{Err: errors.New("run driver unavailable")}
		},
	)
}

func runWithRunners(
	args []string,
	stdout, stderr io.Writer,
	validate validateRunner,
	prepare prepareRunner,
	drive runDriver,
) int {
	return runWithStatusReader(args, stdout, stderr, validate, prepare, drive, readStatus)
}

func runWithStatusReader(
	args []string,
	stdout, stderr io.Writer,
	validate validateRunner,
	prepare prepareRunner,
	drive runDriver,
	read statusReader,
) int {
	return runWithReaders(args, stdout, stderr, validate, prepare, drive, read, readLogs, logstream.Stream)
}

func runWithReaders(
	args []string,
	stdout, stderr io.Writer,
	validate validateRunner,
	prepare prepareRunner,
	drive runDriver,
	readStatus statusReader,
	readLogs logsReader,
	streamLogs logsStreamer,
) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintln(stdout, version)
		return 0
	}
	if len(args) == 1 && args[0] == "validate" {
		result := validate()
		if result.Refusal != nil {
			renderRefusal(stderr, *result.Refusal)
			return 2
		}
		for _, entry := range result.Entries {
			renderEntry(stderr, entry)
		}
		if result.HasDiagnostics() {
			return 3
		}
		return 0
	}
	if requestedID, jsonOutput, ok := parseStatusArgs(args); ok {
		report, err := readStatus(requestedID)
		if err != nil {
			renderStatusError(stderr, err)
			return statusErrorCode(err)
		}
		if jsonOutput {
			if err := json.NewEncoder(stdout).Encode(report); err != nil {
				return observationOutputCode(stderr, err)
			}
		} else {
			if err := renderStatus(stdout, report); err != nil {
				return observationOutputCode(stderr, err)
			}
		}
		return 0
	}
	if requestedID, jsonlOutput, follow, ok := parseLogsArgs(args); ok {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		err := streamLogs(ctx, func() (logstream.Snapshot, error) {
			return readLogs(requestedID)
		}, stdout, logstream.StreamOptions{
			JSONL:        jsonlOutput,
			Follow:       follow,
			PollInterval: 100 * time.Millisecond,
		})
		if err != nil {
			if errors.Is(err, logstream.ErrOutput) {
				return observationOutputCode(stderr, err)
			}
			renderStatusError(stderr, err)
			return statusErrorCode(err)
		}
		return 0
	}
	if requestedID, ok := parseResumeArgs(args); ok {
		return runResume(requestedID, stdout, stderr, resume)
	}
	if requestedID, ok := parseCancelArgs(args); ok {
		return runCancel(requestedID, stdout, stderr, cancel)
	}
	if decisionID, answerText, ok := parseAnswerArgs(args); ok {
		return runAnswer(decisionID, answerText, stderr)
	}
	if decisionID, approved, overridden, reason, ok := parseApproveArgs(args); ok {
		return runApprove(decisionID, approved, overridden, reason, stderr)
	}
	if requestedID, patchPath, reason, claimedImpactPath, ok := parseAmendArgs(args); ok {
		return runAmend(requestedID, patchPath, reason, claimedImpactPath, stdout, stderr)
	}
	if len(args) == 1 && args[0] == "run" {
		preparation, preparationResult := prepare()
		if preparationResult.Refusal != nil {
			renderRefusal(stderr, *preparationResult.Refusal)
			return 2
		}
		for _, entry := range preparationResult.Entries {
			renderEntry(stderr, entry)
		}
		if preparationResult.HasDiagnostics() {
			return 3
		}
		result := drive(
			context.Background(),
			preparation,
			func(runID runstate.RunID) error {
				_, err := fmt.Fprintln(stdout, runID)
				return err
			},
		)
		if result.RunID == "" {
			switch {
			case errors.Is(result.Err, workspace.ErrDirtySource),
				errors.Is(result.Err, workspace.ErrExternalMergeDriver),
				errors.Is(result.Err, acceptance.ErrUnsupportedCriteria):
				fmt.Fprintf(stderr, "run validation failed: %v\n", result.Err)
				return 3
			default:
				fmt.Fprintf(stderr, "precondition refused: detail=%q\n", errorText(result.Err))
				return 2
			}
		}
		switch result.Outcome {
		case driver.OutcomeSucceeded, driver.OutcomeWaitingHuman:
			return 0
		case driver.OutcomeFailed, driver.OutcomeCancelled:
			fmt.Fprintf(
				stderr,
				"run terminal: state=%q reason=%q\n",
				result.Outcome,
				result.Reason,
			)
			return 4
		case driver.OutcomeHalted:
			fmt.Fprintf(stderr, "recovery halted: reason=%q\n", result.Reason)
			return 5
		case driver.OutcomeInterrupted:
			renderRunInterruption(stderr, result)
			return 6
		}
	}
	printUsage(stderr)
	return 1
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: partitur <command>")
	fmt.Fprintln(w, "commands: version, validate, run, resume, answer, approve, amend, cancel, status, logs")
}

func parseAmendArgs(args []string) (runID, patchPath, reason, claimedImpactPath string, ok bool) {
	if len(args) < 5 || args[0] != "amend" {
		return "", "", "", "", false
	}
	index := 1
	if !strings.HasPrefix(args[index], "-") {
		if args[index] == "" {
			return "", "", "", "", false
		}
		runID = args[index]
		index++
	}
	if index+4 > len(args) || args[index] != "--patch" || args[index+1] == "" || args[index+2] != "--reason" || args[index+3] == "" {
		return "", "", "", "", false
	}
	patchPath, reason = args[index+1], args[index+3]
	index += 4
	if index == len(args) {
		return runID, patchPath, reason, "", true
	}
	if index+2 == len(args) && args[index] == "--claimed-impact" && args[index+1] != "" {
		return runID, patchPath, reason, args[index+1], true
	}
	return "", "", "", "", false
}

func runAmend(requestedID, patchPath, reason, claimedImpactPath string, stdout, stderr io.Writer) int {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
		return 2
	}
	report, err := statusprojection.Read(root, requestedID)
	if err != nil {
		renderStatusError(stderr, err)
		return statusErrorCode(err)
	}
	store, err := runstore.New(root, faultpoint.ProbeFromEnvironment(), runstore.ReceiptObserverFromEnvironment())
	if err != nil {
		fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
		return 2
	}
	runID := runstate.RunID(report.Run.ID)
	// Capture this exact head before reading the operator's files or taking the
	// admission lock. SubmitCLI rechecks it and never substitutes a newer head.
	input, err := store.LoadRunInput(runID)
	if err != nil {
		fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
		return 2
	}
	afterAmendmentBaseCapture()
	patch, err := readAmendmentPath(root, patchPath, true)
	if err != nil {
		fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
		return 2
	}
	var claimedImpact []byte
	if claimedImpactPath != "" {
		claimedImpact, err = readAmendmentPath(root, claimedImpactPath, false)
		if err != nil {
			fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
			return 2
		}
	}
	result, err := amendmentexec.New().SubmitCLI(context.Background(), store, amendmentexec.CLIProposal{
		RunID: runID, BaseRevision: input.Projection.State.ScoreHead.Revision, BaseHash: input.Projection.State.ScoreHead.SemanticHash,
		Operations: patch, Reason: reason, ClaimedImpact: claimedImpact,
	})
	if err != nil {
		fmt.Fprintf(stderr, "run interrupted: run_id=%q state=%q resume=%q detail=%q\n", runID, "nonterminal", "partitur resume "+string(runID), err.Error())
		return 6
	}
	switch result.Outcome.Kind {
	case amendment.Rejected:
		fmt.Fprintf(stderr, "amendment rejected: proposal_id=%q reason=%q\n", result.ProposalID, result.Outcome.Reason)
		return 3
	case amendment.Routed:
		impact, _ := json.Marshal(result.Outcome.Impact.Value())
		diagnostic := fmt.Sprintf("amendment routed: proposal_id=%q decision_id=%q reason=%q actual_impact=%s", result.ProposalID, result.DecisionID, result.Outcome.Reason, impact)
		if result.Outcome.Class != "" {
			diagnostic += fmt.Sprintf(" envelope_evaluation={class:%q guard_passed:%t}", result.Outcome.Class, result.Outcome.GuardPass)
		}
		fmt.Fprintln(stderr, diagnostic)
		return 0
	case amendment.Approved:
		return 0
	default:
		fmt.Fprintf(stderr, "run interrupted: run_id=%q state=%q resume=%q detail=%q\n", runID, "nonterminal", "partitur resume "+string(runID), "amendment produced no disposition")
		return 6
	}
}

func readAmendmentPath(root, path string, allowStdin bool) ([]byte, error) {
	if allowStdin && path == "-" {
		return io.ReadAll(os.Stdin)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return os.ReadFile(path)
}

func parseApproveArgs(args []string) (decisionID string, approved bool, overridden []runstate.FindingReference, reason string, ok bool) {
	if len(args) < 3 || args[0] != "approve" || args[1] == "" || strings.HasPrefix(args[1], "-") {
		return "", false, nil, "", false
	}
	switch args[2] {
	case "--approve":
		if len(args) == 3 {
			return args[1], true, nil, "", true
		}
		for index := 3; index < len(args); {
			if args[index] == "--reason" {
				if len(overridden) == 0 || index+2 != len(args) || args[index+1] == "" {
					return "", false, nil, "", false
				}
				return args[1], true, overridden, args[index+1], true
			}
			if args[index] != "--override" || index+1 >= len(args) {
				return "", false, nil, "", false
			}
			pair, pairOK := parseFindingReference(args[index+1])
			if !pairOK || findingReferencePresent(overridden, pair) {
				return "", false, nil, "", false
			}
			overridden = append(overridden, pair)
			index += 2
		}
		return "", false, nil, "", false
	case "--reject":
		if len(args) == 3 {
			return args[1], false, nil, "", true
		}
		if len(args) == 5 && args[3] == "--reason" && args[4] != "" {
			return args[1], false, nil, args[4], true
		}
		return "", false, nil, "", false
	default:
		return "", false, nil, "", false
	}
}

func parseFindingReference(operand string) (runstate.FindingReference, bool) {
	parts := strings.SplitN(operand, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return runstate.FindingReference{}, false
	}
	return runstate.FindingReference{ArtifactInstanceID: parts[0], FindingID: parts[1]}, true
}

func findingReferencePresent(references []runstate.FindingReference, candidate runstate.FindingReference) bool {
	for _, reference := range references {
		if reference == candidate {
			return true
		}
	}
	return false
}

func runApprove(decisionID string, approved bool, overridden []runstate.FindingReference, reason string, stderr io.Writer) int {
	if err := resolveApproval(decisionID, approved, overridden, reason); err != nil {
		if errors.Is(err, runstore.ErrDecisionResolutionNotAllowed) {
			fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
			return 2
		}
		var selectionErr answerSelectionError
		if errors.As(err, &selectionErr) {
			renderStatusError(stderr, err)
			return statusErrorCode(err)
		}
		fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
		return 2
	}
	return 0
}

func resolveApproval(decisionID string, approved bool, overridden []runstate.FindingReference, reason string) error {
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve invocation directory: %w", err)
	}
	report, err := statusprojection.Read(root, "")
	if err != nil {
		return answerSelectionError{err: err}
	}
	store, err := runstore.New(root, faultpoint.ProbeFromEnvironment(), runstore.ReceiptObserverFromEnvironment())
	if err != nil {
		return err
	}
	runID := runstate.RunID(report.Run.ID)
	decisionType := ""
	for _, decision := range report.Run.PendingDecisions {
		if decision.ID == decisionID {
			decisionType = decision.Type
			break
		}
	}
	switch decisionType {
	case "human_gate":
		err = store.ResolveHumanGate(runID, decisionID, approved, overridden, reason)
	case "amendment":
		if approved {
			return runstore.ErrDecisionResolutionNotAllowed
		}
		err = store.RejectRoutedAmendment(runID, decisionID, reason)
	default:
		return runstore.ErrDecisionResolutionNotAllowed
	}
	if err != nil {
		return err
	}
	store.WakeLeaseOwner(runID)
	return nil
}

func parseAnswerArgs(args []string) (decisionID, answer string, ok bool) {
	if len(args) != 4 || args[0] != "answer" || args[1] == "" || strings.HasPrefix(args[1], "-") || args[2] != "--answer" {
		return "", "", false
	}
	return args[1], args[3], true
}

func runAnswer(decisionID, answer string, stderr io.Writer) int {
	if err := answerQuestion(decisionID, answer); err != nil {
		if errors.Is(err, runstore.ErrDecisionResolutionNotAllowed) {
			fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
			return 2
		}
		var selectionErr answerSelectionError
		if errors.As(err, &selectionErr) {
			renderStatusError(stderr, err)
			return statusErrorCode(err)
		}
		fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
		return 2
	}
	return 0
}

type answerSelectionError struct{ err error }

func (err answerSelectionError) Error() string { return err.err.Error() }
func (err answerSelectionError) Unwrap() error { return err.err }

func answerQuestion(decisionID, answer string) error {
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve invocation directory: %w", err)
	}
	report, err := statusprojection.Read(root, "")
	if err != nil {
		return answerSelectionError{err: err}
	}
	store, err := runstore.New(root, faultpoint.ProbeFromEnvironment(), runstore.ReceiptObserverFromEnvironment())
	if err != nil {
		return err
	}
	runID := runstate.RunID(report.Run.ID)
	if err := store.ResolveQuestion(runID, decisionID, answer); err != nil {
		return err
	}
	store.WakeLeaseOwner(runID)
	return nil
}

func parseCancelArgs(args []string) (string, bool) {
	if len(args) == 1 {
		if args[0] == "cancel" {
			return "", true
		}
		return "", false
	}
	if len(args) != 2 {
		return "", false
	}
	if args[0] != "cancel" {
		return "", false
	}
	if args[1] == "" {
		return "", false
	}
	if strings.HasPrefix(args[1], "-") {
		return "", false
	}
	return args[1], true
}

func parseResumeArgs(args []string) (string, bool) {
	if len(args) == 1 && args[0] == "resume" {
		return "", true
	}
	if len(args) == 2 && args[0] == "resume" && args[1] != "" && !strings.HasPrefix(args[1], "-") {
		return args[1], true
	}
	return "", false
}

func runResume(requestedID string, stdout, stderr io.Writer, resume resumeRunner) int {
	if resume == nil {
		fmt.Fprintln(stderr, "run interrupted: run_id=\"\" state=\"nonterminal\" resume=\"partitur resume\" detail=\"resume unavailable\"")
		return 6
	}
	result, err := resume(context.Background(), requestedID)
	if err != nil {
		var selectionErr resumeSelectionError
		if errors.As(err, &selectionErr) {
			code := statusErrorCode(err)
			renderStatusError(stderr, err)
			return code
		}
		fmt.Fprintf(stderr, "run interrupted: run_id=%q state=%q resume=%q detail=%q\n", requestedID, "nonterminal", "partitur resume "+requestedID, err.Error())
		return 6
	}
	switch result.Outcome {
	case recoveryexec.OutcomeSucceeded, recoveryexec.OutcomeQuiescent:
		return 0
	case recoveryexec.OutcomeFailed, recoveryexec.OutcomeCancelled:
		return 4
	case recoveryexec.OutcomeRefused:
		fmt.Fprintln(stderr, "precondition refused: detail=\"driver authority is already held\"")
		return 2
	case recoveryexec.OutcomeHalted:
		if !recovery.IsHaltReason(result.Decision.Halt) {
			fmt.Fprintf(stderr, "run interrupted: run_id=%q state=%q resume=%q detail=%q\n", requestedID, "nonterminal", "partitur resume "+requestedID, "recovery produced an unknown halt reason")
			return 6
		}
		fmt.Fprintf(stderr, "recovery halted: run_id=%q reason=%q\n", requestedID, result.Decision.Halt)
		return 5
	default:
		fmt.Fprintf(stderr, "run interrupted: run_id=%q state=%q resume=%q detail=%q\n", requestedID, "nonterminal", "partitur resume "+requestedID, "recovery produced no command outcome")
		return 6
	}
}

func runCancel(requestedID string, stdout, stderr io.Writer, cancel resumeRunner) int {
	if cancel == nil {
		fmt.Fprintln(stderr, "run interrupted: run_id=\"\" state=\"nonterminal\" resume=\"partitur resume\" detail=\"cancel unavailable\"")
		return 6
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	result, err := cancel(ctx, requestedID)
	if err != nil {
		if errors.Is(err, runstore.ErrCancellationNotAllowed) {
			fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
			return 2
		}
		var selectionErr cancelSelectionError
		if errors.As(err, &selectionErr) {
			code := statusErrorCode(err)
			renderStatusError(stderr, err)
			return code
		}
		fmt.Fprintf(stderr, "run interrupted: run_id=%q state=%q resume=%q detail=%q\n", requestedID, "nonterminal", "partitur resume "+requestedID, err.Error())
		return 6
	}
	switch result.Outcome {
	case recoveryexec.OutcomeSucceeded, recoveryexec.OutcomeQuiescent:
		return 0
	case recoveryexec.OutcomeFailed, recoveryexec.OutcomeCancelled:
		return 4
	case recoveryexec.OutcomeRefused:
		fmt.Fprintf(stderr, "run interrupted: run_id=%q state=%q resume=%q detail=%q\n", requestedID, "nonterminal", "partitur resume "+requestedID, "cancellation acknowledgement wait ended without a terminal outcome")
		return 6
	case recoveryexec.OutcomeHalted:
		if !recovery.IsHaltReason(result.Decision.Halt) {
			fmt.Fprintf(stderr, "run interrupted: run_id=%q state=%q resume=%q detail=%q\n", requestedID, "nonterminal", "partitur resume "+requestedID, "recovery produced an unknown halt reason")
			return 6
		}
		fmt.Fprintf(stderr, "recovery halted: run_id=%q reason=%q\n", requestedID, result.Decision.Halt)
		return 5
	default:
		fmt.Fprintf(stderr, "run interrupted: run_id=%q state=%q resume=%q detail=%q\n", requestedID, "nonterminal", "partitur resume "+requestedID, "recovery produced no command outcome")
		return 6
	}
}

func resume(ctx context.Context, requestedID string) (recoveryexec.Result, error) {
	root, err := os.Getwd()
	if err != nil {
		return recoveryexec.Result{}, fmt.Errorf("resolve invocation directory: %w", err)
	}
	store, err := runstore.New(root, faultpoint.ProbeFromEnvironment(), runstore.ReceiptObserverFromEnvironment())
	if err != nil {
		return recoveryexec.Result{}, err
	}
	runID := runstate.RunID(requestedID)
	if requestedID == "" {
		report, err := statusprojection.Read(root, requestedID)
		if err != nil {
			return recoveryexec.Result{}, resumeSelectionError{err: err}
		}
		runID = runstate.RunID(report.Run.ID)
	}
	return executeRecovery(ctx, store, runID)
}

type cancelSelectionError struct {
	err error
}

func (err cancelSelectionError) Error() string { return err.err.Error() }

func (err cancelSelectionError) Unwrap() error { return err.err }

func cancel(ctx context.Context, requestedID string) (recoveryexec.Result, error) {
	root, err := os.Getwd()
	if err != nil {
		return recoveryexec.Result{}, fmt.Errorf("resolve invocation directory: %w", err)
	}
	store, err := runstore.New(root, faultpoint.ProbeFromEnvironment(), runstore.ReceiptObserverFromEnvironment())
	if err != nil {
		return recoveryexec.Result{}, err
	}
	report, err := statusprojection.Read(root, requestedID)
	if err != nil {
		return recoveryexec.Result{}, cancelSelectionError{err: err}
	}
	runID := runstate.RunID(report.Run.ID)
	if err := store.RequestCancellation(runID); err != nil {
		return recoveryexec.Result{}, err
	}
	store.WakeLeaseOwner(runID)
	return waitForCancellation(ctx, store, runID)
}

func waitForCancellation(ctx context.Context, store *runstore.Store, runID runstate.RunID) (recoveryexec.Result, error) {
	return newCancellationWaiter(store, runID).Run(ctx)
}

func newCancellationWaiter(store *runstore.Store, runID runstate.RunID) cancelwait.Waiter {
	return cancelwait.Waiter{
		Execute: func(ctx context.Context) (recoveryexec.Result, error) {
			return executeRecovery(ctx, store, runID)
		},
		Observe: func(context.Context) (cancelwait.Owner, error) {
			durable, err := store.LoadRunInput(runID)
			if err != nil {
				return cancelwait.Owner{}, err
			}
			observations, err := recoveryobs.Collect(store, runID, durable.Projection)
			if err != nil {
				return cancelwait.Owner{}, err
			}
			lease := observations.Lease
			owner := cancelwait.Owner{State: lease.Owner}
			if lease.Exists && lease.Readable && lease.Identity != nil && lease.Epoch == durable.Projection.State.Authority.Epoch {
				owner.Current = true
				owner.Identity = *lease.Identity
			}
			return owner, nil
		},
		Terminate: func(ctx context.Context, identity recovery.LeaseIdentity) error {
			err := store.TerminateLeaseOwner(ctx, runID, runstore.LeaseIdentity{
				Epoch: identity.Epoch, Token: identity.Token, PID: identity.PID, Start: identity.Start,
			}, adapter.OuterTerminationGrace)
			if errors.Is(err, runstore.ErrLeaseConflict) || errors.Is(err, procid.ErrUnverifiable) {
				// The final recovery pass classifies this changed observation at
				// its one normative halt/oracle site.
				return nil
			}
			return err
		},
	}
}

func executeRecovery(ctx context.Context, store *runstore.Store, runID runstate.RunID) (recoveryexec.Result, error) {
	executor := &recoveryexec.Executor{Store: store, RunID: runID}
	executor.Load = func(context.Context) (recovery.Input, error) {
		durable, err := store.LoadRunInput(runID)
		if err != nil {
			return recovery.Input{}, err
		}
		observations, err := recoveryobs.Collect(store, runID, durable.Projection)
		if err != nil {
			return recovery.Input{}, err
		}
		return recovery.Input{Projection: durable.Projection, Observations: observations}, nil
	}
	result, err := executor.Execute(ctx)
	if executor.Driver != nil && result.Outcome != recoveryexec.OutcomeHalted && !terminalCleanupRan(result) {
		if releaseErr := executor.Driver.Release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}
	return result, err
}

func terminalCleanupRan(result recoveryexec.Result) bool {
	for _, kind := range result.Kinds {
		if kind == recovery.ActionTerminalCleanup {
			return true
		}
	}
	return false
}

func readStatus(requestedID string) (statusprojection.Report, error) {
	root, err := os.Getwd()
	if err != nil {
		return statusprojection.Report{}, fmt.Errorf("resolve invocation directory: %w", err)
	}
	return statusprojection.Read(root, requestedID)
}

func readLogs(requestedID string) (logstream.Snapshot, error) {
	root, err := os.Getwd()
	if err != nil {
		return logstream.Snapshot{}, fmt.Errorf("resolve invocation directory: %w", err)
	}
	return logstream.Read(root, requestedID)
}

func parseStatusArgs(args []string) (requestedID string, jsonOutput, ok bool) {
	if len(args) == 0 || args[0] != "status" || len(args) > 3 {
		return "", false, false
	}
	for _, argument := range args[1:] {
		switch argument {
		case "--json":
			if jsonOutput {
				return "", false, false
			}
			jsonOutput = true
		case "":
			return "", false, false
		default:
			if strings.HasPrefix(argument, "-") || requestedID != "" {
				return "", false, false
			}
			requestedID = argument
		}
	}
	return requestedID, jsonOutput, true
}

func parseLogsArgs(args []string) (requestedID string, jsonlOutput, follow, ok bool) {
	if len(args) == 0 || args[0] != "logs" || len(args) > 4 {
		return "", false, false, false
	}
	for _, argument := range args[1:] {
		switch argument {
		case "--jsonl":
			if jsonlOutput {
				return "", false, false, false
			}
			jsonlOutput = true
		case "--follow":
			if follow {
				return "", false, false, false
			}
			follow = true
		case "":
			return "", false, false, false
		default:
			if strings.HasPrefix(argument, "-") || requestedID != "" {
				return "", false, false, false
			}
			requestedID = argument
		}
	}
	return requestedID, jsonlOutput, follow, true
}

func statusErrorCode(err error) int {
	switch {
	case errors.Is(err, statusprojection.ErrInvalidRunID):
		return 1
	case errors.Is(err, statusprojection.ErrNoActiveRun),
		errors.Is(err, statusprojection.ErrRunNotFound),
		errors.Is(err, statusprojection.ErrSnapshot),
		errors.Is(err, statusprojection.ErrRequiredInput),
		errors.Is(err, errOutputStream):
		return 2
	case errors.Is(err, runstore.ErrJournalCorrupt):
		return 5
	default:
		return 6
	}
}

func observationOutputCode(stderr io.Writer, err error) int {
	if errors.Is(err, syscall.EPIPE) || errors.Is(err, io.ErrClosedPipe) {
		return 0
	}
	err = fmt.Errorf("%w: %v", errOutputStream, err)
	renderStatusError(stderr, err)
	return statusErrorCode(err)
}

func renderStatusError(w io.Writer, err error) {
	switch statusErrorCode(err) {
	case 1:
		fmt.Fprintf(w, "usage error: detail=%q\n", err.Error())
	case 2:
		fmt.Fprintf(w, "precondition refused: detail=%q\n", err.Error())
	default:
		fmt.Fprintf(w, "recovery halted: detail=%q\n", err.Error())
	}
}

func renderStatus(w io.Writer, report statusprojection.Report) error {
	var rendered bytes.Buffer
	renderStatusProjection(&rendered, report)
	_, err := w.Write(rendered.Bytes())
	return err
}

func renderStatusProjection(w io.Writer, report statusprojection.Report) {
	fmt.Fprintf(w, "Run: %s (%s)\n", report.Run.ID, report.Run.Lifecycle)
	fmt.Fprintf(
		w,
		"Score: rev %d, semantic %s, file %s\n",
		report.Run.Score.Revision,
		report.Run.Score.SemanticHash,
		report.Run.Score.FileHash,
	)
	fmt.Fprintf(w, "Journal: %s\n", report.Journal.Integrity)
	if report.Journal.Integrity == "TAIL_UNPARSEABLE" {
		fmt.Fprintf(
			w,
			"Journal tail: seq %d, discarded bytes %d\n",
			report.Journal.TruncatedSeq,
			report.Journal.DiscardedBytes,
		)
	}
	fmt.Fprintf(w, "Recovery: %s", report.Recovery.State)
	if report.Recovery.Reason != "" {
		fmt.Fprintf(w, " (%s)", report.Recovery.Reason)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Application: %s\n", report.Application.State)
	if report.Application.Candidate != nil {
		candidate := report.Application.Candidate
		fmt.Fprintf(
			w,
			"Candidate: %s (tree %s, base %s, rev %d)\n",
			candidate.ID,
			candidate.ResultTree,
			candidate.BaseTree,
			candidate.ScoreRevision,
		)
	}
	fmt.Fprintf(w, "Promotion: %s\n", report.Promotion.State)
	if len(report.Run.PendingDecisions) == 0 {
		fmt.Fprintln(w, "Pending decisions: none")
	} else {
		for _, decision := range report.Run.PendingDecisions {
			fmt.Fprintf(w, "Pending decision %s: %s (rev %d)\n", decision.ID, decision.Type, decision.ScoreRevision)
		}
	}
	for _, movement := range report.Run.Movements {
		fmt.Fprintf(w, "Movement %s: %s\n", movement.ID, movement.State)
		for _, attempt := range movement.Attempts {
			fmt.Fprintf(w, "  Attempt %s: %s", attempt.ID, attempt.State)
			if attempt.Failure != nil {
				fmt.Fprintf(w, " (%s", attempt.Failure.Kind)
				if attempt.Failure.Reason != "" {
					fmt.Fprintf(w, ": %s", attempt.Failure.Reason)
				}
				fmt.Fprint(w, ")")
			}
			fmt.Fprintln(w)
		}
		for _, mark := range movement.Marks {
			renderMark(w, mark)
		}
	}
	for _, advisory := range report.EnforcementAdvisories {
		fmt.Fprintf(
			w,
			"Enforcement advisory: attempt %s, unmet %s\n",
			advisory.AttemptID,
			strings.Join(advisory.Dimensions, ", "),
		)
	}
}

func renderMark(w io.Writer, mark statusprojection.Mark) {
	criteria := make([]string, len(mark.Criteria))
	for index, criterion := range mark.Criteria {
		criteria[index] = criterion.ID + " [" + criterion.SpecHash + "]"
	}
	attemptWord := "attempts"
	if mark.FailedAttempts == 1 {
		attemptWord = "attempt"
	}
	fmt.Fprintf(
		w,
		"  Mark: %s (%d criteria: %s; tree %s; rev %d; after %d failed %s",
		mark.Grade,
		len(mark.Criteria),
		strings.Join(criteria, ", "),
		mark.SubjectTree,
		mark.ScoreRevision,
		mark.FailedAttempts,
		attemptWord,
	)
	if mark.FindingsInstanceID != "" {
		fmt.Fprintf(w, "; findings %s; review outcome %s", mark.FindingsInstanceID, mark.ReviewOutcome)
	}
	if mark.GateDecisionID != "" {
		fmt.Fprintf(w, "; gate decision %s", mark.GateDecisionID)
	}
	fmt.Fprintln(w, ")")
}

func errorText(err error) string {
	if err == nil {
		return "run unavailable"
	}
	return err.Error()
}

func renderRunInterruption(w io.Writer, result driver.Result) {
	fmt.Fprintf(
		w,
		"run interrupted: run_id=%q state=%q resume=%q detail=%q\n",
		result.RunID,
		"nonterminal",
		"partitur resume "+string(result.RunID),
		errorText(result.Err),
	)
}

func renderRefusal(w io.Writer, refusal validation.Refusal) {
	fmt.Fprintf(
		w,
		"precondition refused: kind=%q path=%q detail=%q\n",
		refusal.Kind,
		refusal.Path,
		refusal.Detail,
	)
}

func renderEntry(w io.Writer, entry validation.Entry) {
	switch entry.Kind {
	case validation.EntryScore:
		fmt.Fprintf(
			w,
			"score: rule=%q pointer=%q detail=%q\n",
			entry.Rule,
			entry.Pointer,
			entry.Detail,
		)
	case validation.EntryCast:
		fmt.Fprintf(
			w,
			"cast: rule=%q origin=%q pointer=%q detail=%q\n",
			entry.Rule,
			entry.Origin,
			entry.Pointer,
			entry.Detail,
		)
	case validation.EntryAdapterEnvironment:
		fmt.Fprintf(
			w,
			"adapter-environment: adapter=%q kind=%q detail=%q stderr=%q\n",
			entry.AdapterID,
			entry.AdapterKind,
			entry.Detail,
			entry.Stderr,
		)
	case validation.EntryCapability:
		fmt.Fprintf(
			w,
			"capability: part=%q performer=%q missing=%q\n",
			entry.PartID,
			entry.PerformerID,
			entry.MissingCapabilities,
		)
	case validation.EntryEnforcement:
		fmt.Fprintf(
			w,
			"enforcement: movement=%q part=%q performer=%q unmet=%q\n",
			entry.MovementID,
			entry.PartID,
			entry.PerformerID,
			entry.UnmetDimensions,
		)
	case validation.EntryEnforcementAdvisory:
		fmt.Fprintf(
			w,
			"enforcement advisory: movement=%q part=%q performer=%q unmet=%q\n",
			entry.MovementID,
			entry.PartID,
			entry.PerformerID,
			entry.UnmetDimensions,
		)
	}
}
