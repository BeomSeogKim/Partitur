package main

import (
	"fmt"
	"io"
	"os"

	validation "github.com/BeomSeogKim/Partitur/internal/validate"
)

var version = "dev"

type validateRunner func() validation.Result

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWithValidate(args, stdout, stderr, validation.Run)
}

func runWithValidate(
	args []string,
	stdout, stderr io.Writer,
	validate validateRunner,
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
	printUsage(stderr)
	return 1
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: partitur <command>")
	fmt.Fprintln(w, "commands: version, validate")
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
