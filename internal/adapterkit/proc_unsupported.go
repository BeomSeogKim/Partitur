//go:build !darwin && !linux

package adapterkit

import (
	"context"
	"time"
)

func runProcess(context.Context, ProcessSpec, func([]byte) error, time.Duration) (ProcessResult, error) {
	return ProcessResult{}, ErrProcessUnsupported
}
