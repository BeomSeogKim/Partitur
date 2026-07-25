package adapterkit

import (
	"context"
	"errors"
	"io"
	"time"
)

const ProcessTerminationGrace = 10 * time.Second

// ErrProcessUnsupported reports that process-group supervision is unavailable
// because PR1 supports vendor processes only on macOS and Linux.
var ErrProcessUnsupported = errors.New("vendor process runner supports only macOS and Linux")

type ProcessSpec struct {
	Path   string
	Args   []string
	Dir    string
	Env    []string
	Stdin  io.Reader
	Stderr io.Writer
}

type ProcessResult struct {
	ExitCode int
}

// RunProcess starts a vendor CLI in its own process group, scans bounded stdout
// lines, and terminates the full group when the context is cancelled.
func RunProcess(ctx context.Context, spec ProcessSpec, onStdoutLine func([]byte) error) (ProcessResult, error) {
	return runProcess(ctx, spec, onStdoutLine, ProcessTerminationGrace)
}
