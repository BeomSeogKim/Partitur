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
	"strings"
	"syscall"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/acceptance"
	"github.com/BeomSeogKim/Partitur/internal/driver"
	logstream "github.com/BeomSeogKim/Partitur/internal/logs"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	statusprojection "github.com/BeomSeogKim/Partitur/internal/status"
	validation "github.com/BeomSeogKim/Partitur/internal/validate"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

var version = "dev"

var errOutputStream = errors.New("output stream is unwritable")

type validateRunner func() validation.Result
type prepareRunner func() (*validation.Preparation, validation.Result)
type runDriver func(
	context.Context,
	*validation.Preparation,
	driver.StartedObserver,
) driver.Result

type statusReader func(string) (statusprojection.Report, error)
type logsReader func(string) (logstream.Snapshot, error)
type logsStreamer func(
	context.Context,
	func() (logstream.Snapshot, error),
	io.Writer,
	logstream.StreamOptions,
) error

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWithRunners(
		args,
		stdout,
		stderr,
		validation.Run,
		validation.Prepare,
		driver.Run,
	)
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
				errors.Is(result.Err, acceptance.ErrUnsupportedCriteria),
				errors.Is(result.Err, driver.ErrUnsupportedSlice):
				fmt.Fprintf(stderr, "run validation failed: %v\n", result.Err)
				return 3
			default:
				fmt.Fprintf(stderr, "precondition refused: detail=%q\n", errorText(result.Err))
				return 2
			}
		}
		switch result.Outcome {
		case driver.OutcomeSucceeded:
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
	fmt.Fprintln(w, "commands: version, validate, run, status, logs")
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
	default:
		return 5
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
