package main

import (
	"fmt"
	"os"

	"github.com/BeomSeogKim/Partitur/internal/adapterkit"
	"github.com/BeomSeogKim/Partitur/internal/adapters/codex"
)

func main() {
	handler := codex.New(os.Stderr)
	if err := adapterkit.ServeProcess(handler); err != nil {
		fmt.Fprintf(os.Stderr, "partitur-adapter-codex: %v\n", err)
		os.Exit(1)
	}
}
