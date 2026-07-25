package main

import (
	"fmt"
	"os"

	"github.com/BeomSeogKim/Partitur/internal/adapterkit"
	"github.com/BeomSeogKim/Partitur/internal/adapters/claude"
)

func main() {
	handler := claude.New(os.Stderr)
	if err := adapterkit.Serve(handler, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "partitur-adapter-claude: %v\n", err)
		os.Exit(1)
	}
}
