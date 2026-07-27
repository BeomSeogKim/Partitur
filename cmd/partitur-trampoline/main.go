package main

import (
	"fmt"
	"os"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/launch"
)

func main() {
	if err := launch.RunTrampoline(os.Args[1:], faultpoint.Nop{}); err != nil {
		fmt.Fprintf(os.Stderr, "partitur-trampoline: %v\n", err)
		os.Exit(1)
	}
}
