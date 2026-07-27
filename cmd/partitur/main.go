package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/BeomSeogKim/Partitur/internal/acceptance"
	"github.com/BeomSeogKim/Partitur/internal/driver"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	validation "github.com/BeomSeogKim/Partitur/internal/validate"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

var version = "dev"

type validateRunner func() validation.Result
type prepareRunner func() (*validation.Preparation, validation.Result)
type runDriver func(
	context.Context,
	*validation.Preparation,
	driver.StartedObserver,
) driver.Result

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
	fmt.Fprintln(w, "commands: version, validate, run")
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
