package main

import (
	"fmt"
	"io"
	"os"
)

var version = "dev"

var milestoneCommands = map[string]struct{}{
	"init":          {},
	"validate":      {},
	"run":           {},
	"status":        {},
	"logs":          {},
	"answer":        {},
	"approve":       {},
	"amend":         {},
	"cancel":        {},
	"resume":        {},
	"promote-score": {},
	"apply":         {},
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintln(stdout, version)
		return 0
	}
	if len(args) > 0 {
		if _, ok := milestoneCommands[args[0]]; ok {
			fmt.Fprintln(stderr, "not implemented in this milestone")
			return 2
		}
	}
	printUsage(stderr)
	return 2
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: partitur <command>")
	fmt.Fprintln(w, "commands: version, init, validate, run, status, logs, answer, approve, amend, cancel, resume, promote-score, apply")
}
