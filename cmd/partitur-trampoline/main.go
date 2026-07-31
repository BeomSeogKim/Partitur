package main

import (
	"fmt"
	"os"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/launch"
)

func main() {
	if err := faultpoint.RequireHarnessBuild(); err != nil {
		fmt.Fprintf(os.Stderr, "partitur-trampoline: %v\n", err)
		os.Exit(2)
	}
	if err := launch.RunTrampoline(os.Args[1:], faultpoint.ProbeFromEnvironment()); err != nil {
		fmt.Fprintf(os.Stderr, "partitur-trampoline: %v\n", err)
		os.Exit(1)
	}
}
