package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

var (
	errOutputStream    = errors.New("output stream is unwritable")
	errApproveOperands = errors.New("approve operands are invalid")
)

const initIgnoreContents = "runs/\nwork/\n"

const initScoreContents = `score: "0.2"
name: draft
revision: 1
status: draft
goal: Start the interview.
draft:
  interview_movement: interview
parts:
  interview:
    capabilities: [repo_read]
movements:
  - id: interview
    phase: draft
    part: interview
    grants: [repo_read]
    instruction: Clarify the score.
policy:
  budget:
    active_wall_clock_min: 10
`

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
type recoveryCommandResult struct {
	runID  runstate.RunID
	result recoveryexec.Result
}

type resumeRunner func(context.Context, string) (recoveryCommandResult, error)

var newRunStore = runstore.New

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
	if len(args) == 1 && args[0] == "init" {
		if err := initializeRepository(); err != nil {
			fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
			return 2
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
	if decisionID, answerText, answerPath, ok := parseAnswerArgs(args); ok {
		return runAnswerSource(decisionID, answerText, answerPath, stderr)
	}
	if decisionID, approved, overridden, reason, ok := parseApproveArgs(args); ok {
		return runApprove(decisionID, approved, overridden, reason, stderr)
	}
	if requestedID, patchPath, reason, claimedImpactPath, ok := parseAmendArgs(args); ok {
		return runAmend(requestedID, patchPath, reason, claimedImpactPath, stdout, stderr)
	}
	if requestedID, recoverOnly, ok := parseApplyArgs(args); ok {
		return runApply(requestedID, recoverOnly, stderr)
	}
	if requestedID, recoverOnly, ok := parsePromoteScoreArgs(args); ok {
		return runPromoteScore(requestedID, recoverOnly, stderr)
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
		if errors.Is(result.Err, runstore.ErrJournalDurabilityUnconfirmed) {
			renderDurabilityUnconfirmed(stderr, result.Err)
			return 7
		}
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
	fmt.Fprintln(w, "commands: version, init, validate, run, resume, answer, approve, amend, apply, promote-score, cancel, status, logs")
}

func initializeRepository() error {
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve invocation directory: %w", err)
	}
	statePath := filepath.Join(root, ".partitur")
	stateInfo, err := os.Lstat(statePath)
	stateExists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect .partitur: %w", err)
	}
	if stateExists && !stateInfo.IsDir() {
		return errors.New(".partitur is not a directory")
	}

	scorePath := filepath.Join(root, "partitur.yaml")
	scoreInfo, err := os.Lstat(scorePath)
	scoreExists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect partitur.yaml: %w", err)
	}
	if scoreExists && !scoreInfo.Mode().IsRegular() {
		return errors.New("partitur.yaml is not a regular file")
	}

	ignorePath := filepath.Join(statePath, ".gitignore")
	ignoreExists := false
	if stateExists {
		ignoreInfo, err := os.Lstat(ignorePath)
		ignoreExists = err == nil
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("inspect .partitur/.gitignore: %w", err)
		}
		if ignoreExists && !ignoreInfo.Mode().IsRegular() {
			return errors.New(".partitur/.gitignore is not a regular file")
		}
		if ignoreExists {
			contents, err := os.ReadFile(ignorePath)
			if err != nil {
				return fmt.Errorf("read .partitur/.gitignore: %w", err)
			}
			if !bytes.Equal(contents, []byte(initIgnoreContents)) {
				return errors.New(".partitur/.gitignore has different contents")
			}
		}
	}

	if !stateExists {
		if err := os.Mkdir(statePath, 0o700); err != nil {
			return fmt.Errorf("create .partitur: %w", err)
		}
	}
	if !ignoreExists {
		if err := os.WriteFile(ignorePath, []byte(initIgnoreContents), 0o600); err != nil {
			return fmt.Errorf("write .partitur/.gitignore: %w", err)
		}
	}
	if !scoreExists {
		if err := os.WriteFile(scorePath, []byte(initScoreContents), 0o600); err != nil {
			return fmt.Errorf("write partitur.yaml: %w", err)
		}
	}
	return nil
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

func parseApplyArgs(args []string) (runID string, recoverOnly bool, ok bool) {
	if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] != "apply" || args[1] == "" || strings.HasPrefix(args[1], "-") {
		return "", false, false
	}
	if len(args) == 2 {
		return args[1], false, true
	}
	if args[2] != "--recover" {
		return "", false, false
	}
	return args[1], true, true
}

func runApply(requestedID string, recoverOnly bool, stderr io.Writer) int {
	if err := statusprojection.ValidateRunID(requestedID); err != nil {
		renderStatusError(stderr, err)
		return statusErrorCode(err)
	}
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
		return 2
	}
	store, err := newRunStore(root, faultpoint.ProbeFromEnvironment(), runstore.ReceiptObserverFromEnvironment())
	if err != nil {
		fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
		return 2
	}
	result, err := store.Apply(context.Background(), runstate.RunID(requestedID), recoverOnly)
	if err != nil {
		if errors.Is(err, runstore.ErrJournalDurabilityUnconfirmed) {
			renderDurabilityUnconfirmed(stderr, err)
			return 7
		}
		// Only a failure after apply.started may advertise --recover: before it
		// the projection is still NOT_APPLIED, which recovery rightly refuses, so
		// exit 6 would name a continuation the caller cannot use.
		if errors.Is(err, runstore.ErrApplicationInterrupted) {
			fmt.Fprintf(stderr, "run interrupted: run_id=%q state=%q resume=%q detail=%q\n", requestedID, "application", "partitur apply "+requestedID+" --recover", err.Error())
			return 6
		}
		fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
		return 2
	}
	switch result.Outcome {
	case runstore.ApplicationOutcomeApplied, runstore.ApplicationOutcomeAlreadyApplied:
		return 0
	case runstore.ApplicationOutcomeFailedClean:
		if result.Detail != "" {
			fmt.Fprintf(stderr, "application failed cleanly: detail=%q\n", result.Detail)
		}
		return 4
	case runstore.ApplicationOutcomeRecoveryRequired:
		fmt.Fprintf(stderr, "recovery halted: run_id=%q reason=%q\n", requestedID, result.Detail)
		return 5
	default:
		fmt.Fprintf(stderr, "run interrupted: run_id=%q state=%q resume=%q detail=%q\n", requestedID, "application", "partitur apply "+requestedID+" --recover", "application produced no outcome")
		return 6
	}
}

func parsePromoteScoreArgs(args []string) (runID string, recoverOnly bool, ok bool) {
	if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] != "promote-score" || args[1] == "" || strings.HasPrefix(args[1], "-") {
		return "", false, false
	}
	if len(args) == 2 {
		return args[1], false, true
	}
	if args[2] != "--recover" {
		return "", false, false
	}
	return args[1], true, true
}

func runPromoteScore(requestedID string, recoverOnly bool, stderr io.Writer) int {
	if err := statusprojection.ValidateRunID(requestedID); err != nil {
		renderStatusError(stderr, err)
		return statusErrorCode(err)
	}
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
		return 2
	}
	store, err := newRunStore(root, faultpoint.ProbeFromEnvironment(), runstore.ReceiptObserverFromEnvironment())
	if err != nil {
		fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
		return 2
	}
	result, err := store.PromoteScore(context.Background(), runstate.RunID(requestedID), recoverOnly)
	if err != nil {
		if errors.Is(err, runstore.ErrJournalDurabilityUnconfirmed) {
			renderDurabilityUnconfirmed(stderr, err)
			return 7
		}
		if errors.Is(err, runstore.ErrPromotionInterrupted) {
			fmt.Fprintf(stderr, "run interrupted: run_id=%q state=%q resume=%q detail=%q\n", requestedID, "promotion", "partitur promote-score "+requestedID+" --recover", err.Error())
			return 6
		}
		fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
		return 2
	}
	switch result.Outcome {
	case runstore.PromotionOutcomePromoted, runstore.PromotionOutcomeAlreadyPromoted:
		return 0
	case runstore.PromotionOutcomeRecoveryRequired:
		fmt.Fprintf(stderr, "recovery halted: run_id=%q reason=%q\n", requestedID, result.Detail)
		return 5
	default:
		fmt.Fprintf(stderr, "run interrupted: run_id=%q state=%q resume=%q detail=%q\n", requestedID, "promotion", "partitur promote-score "+requestedID+" --recover", "promotion produced no outcome")
		return 6
	}
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
	store, err := newRunStore(root, faultpoint.ProbeFromEnvironment(), runstore.ReceiptObserverFromEnvironment())
	if err != nil {
		fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
		return 2
	}
	runID := runstate.RunID(report.Run.ID)
	// Capture this exact head before reading the operator's files or taking the
	// admission lock. SubmitCLI rechecks it and never substitutes a newer head.
	input, err := store.LoadRunInput(runID)
	if err != nil {
		if unavailableProjection(err) {
			renderStatusError(stderr, err)
			return statusErrorCode(err)
		}
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
		if errors.Is(err, runstore.ErrJournalDurabilityUnconfirmed) {
			renderDurabilityUnconfirmed(stderr, err)
			return 7
		}
		if unavailableProjection(err) {
			renderStatusError(stderr, err)
			return statusErrorCode(err)
		}
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
	resolution, err := resolveApproval(decisionID, approved, overridden, reason)
	if err != nil {
		if errors.Is(err, errApproveOperands) {
			fmt.Fprintf(stderr, "usage error: %v\n", err)
			return 1
		}
		if errors.Is(err, runstore.ErrJournalDurabilityUnconfirmed) {
			renderDurabilityUnconfirmed(stderr, err)
			return 7
		}
		var rejected *amendmentexec.DecisionRejectedError
		if errors.As(err, &rejected) {
			fmt.Fprintf(stderr, "amendment rejected: proposal_id=%q reason=%q\n", rejected.ProposalID, rejected.Reason)
			renderDecisionResumeHint(stderr, resolution)
			return 3
		}
		if errors.Is(err, runstore.ErrDecisionResolutionNotAllowed) {
			fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
			return 2
		}
		if errors.Is(err, runstate.ErrInvalidEvent) || errors.Is(err, runstate.ErrIllegalTransition) {
			fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
			return 2
		}
		if unavailableProjection(err) {
			renderStatusError(stderr, err)
			return statusErrorCode(err)
		}
		var selectionErr answerSelectionError
		if errors.As(err, &selectionErr) {
			renderStatusError(stderr, err)
			return statusErrorCode(err)
		}
		fmt.Fprintf(stderr, "run interrupted: state=%q resume=%q detail=%q\n", "nonterminal", "partitur resume", err.Error())
		return 6
	}
	renderDecisionResumeHint(stderr, resolution)
	return 0
}

type decisionResolution struct {
	runID             runstate.RunID
	resumeEligible    bool
	ownerUnverifiable bool
}

func renderDecisionResumeHint(stderr io.Writer, resolution decisionResolution) {
	if resolution.ownerUnverifiable {
		fmt.Fprintf(stderr, "run blocked: run_id=%q state=%q reason=%q\n", string(resolution.runID), "nonterminal", "owner_unverifiable")
		return
	}
	if resolution.resumeEligible {
		fmt.Fprintf(stderr, "run waiting: state=%q resume=%q\n", "nonterminal", "partitur resume "+string(resolution.runID))
	}
}

func resolveApproval(decisionID string, approved bool, overridden []runstate.FindingReference, reason string) (decisionResolution, error) {
	root, err := os.Getwd()
	if err != nil {
		return decisionResolution{}, fmt.Errorf("resolve invocation directory: %w", err)
	}
	report, err := statusprojection.Read(root, "")
	if err != nil {
		return decisionResolution{}, answerSelectionError{err: err}
	}
	runID := runstate.RunID(report.Run.ID)
	decisionType := ""
	for _, decision := range report.Run.PendingDecisions {
		if decision.ID == decisionID {
			decisionType = decision.Type
			break
		}
	}
	if err := validateApproveOperands(decisionType, approved, overridden, reason); err != nil {
		return decisionResolution{}, err
	}
	store, err := newRunStore(root, faultpoint.ProbeFromEnvironment(), runstore.ReceiptObserverFromEnvironment())
	if err != nil {
		return decisionResolution{}, err
	}
	switch decisionType {
	case "human_gate":
		err = store.ResolveHumanGate(runID, decisionID, approved, overridden, reason)
	case "amendment", "finalization":
		if approved {
			if err := amendmentexec.New().ApproveRouted(context.Background(), store, runID, decisionID); err != nil {
				var rejected *amendmentexec.DecisionRejectedError
				if errors.As(err, &rejected) {
					return classifyDecisionResolution(store, runID, true), err
				}
				return decisionResolution{}, err
			}
			return classifyDecisionResolution(store, runID, false), nil
		}
		err = store.RejectRoutedAmendment(runID, decisionID, reason)
	default:
		return decisionResolution{}, runstore.ErrDecisionResolutionNotAllowed
	}
	if err != nil {
		return decisionResolution{}, err
	}
	return classifyDecisionResolution(store, runID, true), nil
}

func classifyDecisionResolution(store *runstore.Store, runID runstate.RunID, wakeOwner bool) decisionResolution {
	result := decisionResolution{runID: runID}
	if wakeOwner {
		store.WakeLeaseOwner(runID)
	}
	snapshot, err := store.ClassifyCurrentResumeLease(runID)
	if err != nil {
		return result
	}
	return classifyDecisionResumeEligibility(result, &snapshot.Projection, snapshot.LeaseStatus)
}

func classifyDecisionResumeEligibility(result decisionResolution, projection *runstore.DecisionResolution, leaseStatus runstore.ResumeLeaseStatus) decisionResolution {
	if projection.Run.Terminal() {
		return result
	}
	switch leaseStatus {
	case runstore.ResumeLeaseUnverifiable:
		result.ownerUnverifiable = true
		return result
	case runstore.ResumeLeaseLiveOwner:
		return result
	case runstore.ResumeLeaseAvailable, runstore.ResumeLeaseProjectionMismatch:
		// Only statuses known to permit a resume attempt may print the hint.
	default:
		return result
	}
	result.resumeEligible = projection.Run == runstate.RunRunning ||
		projection.CancelRequested
	return result
}

func validateApproveOperands(decisionType string, approved bool, overridden []runstate.FindingReference, reason string) error {
	if decisionType != "amendment" && decisionType != "finalization" {
		return nil
	}
	if len(overridden) != 0 {
		return fmt.Errorf("%w for %s decision: --override is invalid", errApproveOperands, decisionType)
	}
	if !approved && reason == "" {
		return fmt.Errorf("%w for %s decision: --reject requires --reason", errApproveOperands, decisionType)
	}
	return nil
}

func parseAnswerArgs(args []string) (decisionID, answer, answerPath string, ok bool) {
	if len(args) != 4 || args[0] != "answer" || args[1] == "" || strings.HasPrefix(args[1], "-") {
		return "", "", "", false
	}
	switch args[2] {
	case "--answer":
		return args[1], args[3], "", true
	case "--answer-file":
		if args[3] != "" {
			return args[1], "", args[3], true
		}
	}
	return "", "", "", false
}

func runAnswerSource(decisionID, answer, answerPath string, stderr io.Writer) int {
	if answerPath != "" {
		root, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
			return 2
		}
		contents, err := readAmendmentPath(root, answerPath, false)
		if err != nil {
			fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
			return 2
		}
		answer = string(contents)
	}
	return runAnswer(decisionID, answer, stderr)
}

func runAnswer(decisionID, answer string, stderr io.Writer) int {
	resolution, err := answerQuestion(decisionID, answer)
	if err != nil {
		if errors.Is(err, runstore.ErrJournalDurabilityUnconfirmed) {
			renderDurabilityUnconfirmed(stderr, err)
			return 7
		}
		if errors.Is(err, runstore.ErrDecisionResolutionNotAllowed) {
			fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
			return 2
		}
		if errors.Is(err, runstate.ErrInvalidEvent) || errors.Is(err, runstate.ErrIllegalTransition) {
			fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
			return 2
		}
		if unavailableProjection(err) {
			renderStatusError(stderr, err)
			return statusErrorCode(err)
		}
		var selectionErr answerSelectionError
		if errors.As(err, &selectionErr) {
			renderStatusError(stderr, err)
			return statusErrorCode(err)
		}
		fmt.Fprintf(stderr, "run interrupted: state=%q resume=%q detail=%q\n", "nonterminal", "partitur resume", err.Error())
		return 6
	}
	renderDecisionResumeHint(stderr, resolution)
	return 0
}

type answerSelectionError struct{ err error }

func (err answerSelectionError) Error() string { return err.err.Error() }
func (err answerSelectionError) Unwrap() error { return err.err }

func answerQuestion(decisionID, answer string) (decisionResolution, error) {
	root, err := os.Getwd()
	if err != nil {
		return decisionResolution{}, fmt.Errorf("resolve invocation directory: %w", err)
	}
	report, err := statusprojection.Read(root, "")
	if err != nil {
		return decisionResolution{}, answerSelectionError{err: err}
	}
	store, err := newRunStore(root, faultpoint.ProbeFromEnvironment(), runstore.ReceiptObserverFromEnvironment())
	if err != nil {
		return decisionResolution{}, err
	}
	runID := runstate.RunID(report.Run.ID)
	if err := store.ResolveQuestion(runID, decisionID, answer); err != nil {
		return decisionResolution{}, err
	}
	return classifyDecisionResolution(store, runID, true), nil
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
		renderResumeInterruption(stderr, "", "resume unavailable")
		return 6
	}
	commandResult, err := resume(context.Background(), requestedID)
	runID := selectedOrRequestedRunID(commandResult.runID, requestedID)
	if err != nil {
		if errors.Is(err, runstore.ErrJournalDurabilityUnconfirmed) {
			renderDurabilityUnconfirmed(stderr, err)
			return 7
		}
		var selectionErr resumeSelectionError
		if errors.As(err, &selectionErr) {
			code := statusErrorCode(err)
			renderStatusError(stderr, err)
			return code
		}
		renderResumeInterruption(stderr, runID, err.Error())
		return 6
	}
	result := commandResult.result
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
			renderResumeInterruption(stderr, runID, "recovery produced an unknown halt reason")
			return 6
		}
		fmt.Fprintf(stderr, "recovery halted: run_id=%q reason=%q\n", runID, result.Decision.Halt)
		return 5
	default:
		renderResumeInterruption(stderr, runID, "recovery produced no command outcome")
		return 6
	}
}

func selectedOrRequestedRunID(selected runstate.RunID, requested string) string {
	if selected != "" {
		return string(selected)
	}
	return requested
}

func renderResumeInterruption(stderr io.Writer, runID, detail string) {
	if runID == "" {
		fmt.Fprintf(stderr, "run interrupted: state=%q resume=%q detail=%q\n", "nonterminal", "partitur resume", detail)
		return
	}
	fmt.Fprintf(stderr, "run interrupted: run_id=%q state=%q resume=%q detail=%q\n", runID, "nonterminal", "partitur resume "+runID, detail)
}

func runCancel(requestedID string, stdout, stderr io.Writer, cancel resumeRunner) int {
	if cancel == nil {
		renderResumeInterruption(stderr, "", "cancel unavailable")
		return 6
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	commandResult, err := cancel(ctx, requestedID)
	runID := selectedOrRequestedRunID(commandResult.runID, requestedID)
	if err != nil {
		if errors.Is(err, runstore.ErrJournalDurabilityUnconfirmed) {
			renderDurabilityUnconfirmed(stderr, err)
			return 7
		}
		if errors.Is(err, runstore.ErrCancellationNotAllowed) {
			fmt.Fprintf(stderr, "precondition refused: detail=%q\n", err.Error())
			return 2
		}
		if unavailableProjection(err) {
			renderStatusError(stderr, err)
			return statusErrorCode(err)
		}
		var selectionErr cancelSelectionError
		if errors.As(err, &selectionErr) {
			code := statusErrorCode(err)
			renderStatusError(stderr, err)
			return code
		}
		renderResumeInterruption(stderr, runID, err.Error())
		return 6
	}
	result := commandResult.result
	switch result.Outcome {
	case recoveryexec.OutcomeSucceeded, recoveryexec.OutcomeQuiescent:
		return 0
	case recoveryexec.OutcomeFailed, recoveryexec.OutcomeCancelled:
		return 4
	case recoveryexec.OutcomeRefused:
		renderResumeInterruption(stderr, runID, "cancellation acknowledgement wait ended without a terminal outcome")
		return 6
	case recoveryexec.OutcomeHalted:
		if !recovery.IsHaltReason(result.Decision.Halt) {
			renderResumeInterruption(stderr, runID, "recovery produced an unknown halt reason")
			return 6
		}
		fmt.Fprintf(stderr, "recovery halted: run_id=%q reason=%q\n", runID, result.Decision.Halt)
		return 5
	default:
		renderResumeInterruption(stderr, runID, "recovery produced no command outcome")
		return 6
	}
}

func resume(ctx context.Context, requestedID string) (recoveryCommandResult, error) {
	root, err := os.Getwd()
	if err != nil {
		return recoveryCommandResult{}, fmt.Errorf("resolve invocation directory: %w", err)
	}
	store, err := newRunStore(root, faultpoint.ProbeFromEnvironment(), runstore.ReceiptObserverFromEnvironment())
	if err != nil {
		return recoveryCommandResult{}, err
	}
	runID := runstate.RunID(requestedID)
	if requestedID == "" {
		report, err := statusprojection.Read(root, requestedID)
		if err != nil {
			return recoveryCommandResult{}, resumeSelectionError{err: err}
		}
		runID = runstate.RunID(report.Run.ID)
	} else {
		report, selectionErr := statusprojection.Read(root, requestedID)
		switch {
		case selectionErr == nil:
			runID = runstate.RunID(report.Run.ID)
		case errors.Is(selectionErr, statusprojection.ErrInvalidRunID), errors.Is(selectionErr, statusprojection.ErrRunNotFound):
			return recoveryCommandResult{}, resumeSelectionError{err: selectionErr}
		}
	}
	result, err := executeRecovery(ctx, store, runID)
	return recoveryCommandResult{runID: runID, result: result}, err
}

type cancelSelectionError struct {
	err error
}

func (err cancelSelectionError) Error() string { return err.err.Error() }

func (err cancelSelectionError) Unwrap() error { return err.err }

func cancel(ctx context.Context, requestedID string) (recoveryCommandResult, error) {
	root, err := os.Getwd()
	if err != nil {
		return recoveryCommandResult{}, fmt.Errorf("resolve invocation directory: %w", err)
	}
	store, err := newRunStore(root, faultpoint.ProbeFromEnvironment(), runstore.ReceiptObserverFromEnvironment())
	if err != nil {
		return recoveryCommandResult{}, err
	}
	report, err := statusprojection.Read(root, requestedID)
	if err != nil {
		return recoveryCommandResult{}, cancelSelectionError{err: err}
	}
	runID := runstate.RunID(report.Run.ID)
	if err := store.RequestCancellation(runID); err != nil {
		return recoveryCommandResult{runID: runID}, err
	}
	store.WakeLeaseOwner(runID)
	result, err := waitForCancellation(ctx, store, runID)
	return recoveryCommandResult{runID: runID, result: result}, err
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
	executor := &recoveryexec.Executor{Store: store, RunID: runID, CoreFinalizer: amendmentexec.New().RebuildFinalization}
	trace := recoveryDecisionTraceFromEnvironment()
	executor.ObserveDecision = trace.Observe
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
	if traceErr := trace.Close(); err == nil && traceErr != nil {
		err = traceErr
	}
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
	case errors.Is(err, runstore.ErrJournalCorrupt),
		errors.Is(err, runstate.ErrUnsupportedEventType):
		return 5
	default:
		return 2
	}
}

func unavailableProjection(err error) bool {
	return errors.Is(err, runstore.ErrJournalCorrupt) || errors.Is(err, runstate.ErrUnsupportedEventType)
}

func renderDurabilityUnconfirmed(w io.Writer, err error) {
	fmt.Fprintf(w, "durability unconfirmed: detail=%q\n", err.Error())
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
			"cast: rule=%q origin=%q pointer=%q detail=%q",
			entry.Rule,
			entry.Origin,
			entry.Pointer,
			entry.Detail,
		)
		if entry.Hint != "" {
			fmt.Fprintf(w, " hint=%q", entry.Hint)
		}
		fmt.Fprintln(w)
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
