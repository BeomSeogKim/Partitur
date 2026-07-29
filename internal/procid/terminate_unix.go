//go:build linux || darwin

package procid

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

// Terminate sends signals only to one identity-checked process. It never
// sweeps a session: the driver is not a session leader owned by Partitur.
func Terminate(ctx context.Context, pid int, start runstate.StartIdentity, grace time.Duration) error {
	if err := signal(pid, start, syscall.SIGTERM); err != nil {
		if errors.Is(err, errGone) {
			return nil
		}
		return err
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		gone, err := gone(ctx, pid, start)
		if err != nil || gone {
			return err
		}
		if err := pause(ctx); err != nil {
			return err
		}
	}
	for {
		gone, err := gone(ctx, pid, start)
		if err != nil || gone {
			return err
		}
		if err := signal(pid, start, syscall.SIGKILL); err != nil {
			if errors.Is(err, errGone) {
				return nil
			}
			return err
		}
		if err := pause(ctx); err != nil {
			return err
		}
	}
}

// Wake is intentionally best effort. A caller must not use its error as a
// correctness signal because polling the journal remains the record path.
func Wake(pid int, start runstate.StartIdentity) {
	_ = signal(pid, start, syscall.SIGUSR1)
}

var errGone = errors.New("process is gone")

// ErrUnverifiable marks a process that cannot safely be named for a signal.
var ErrUnverifiable = errors.New("process identity is unverifiable")

func signal(pid int, start runstate.StartIdentity, value syscall.Signal) error {
	match := Matches(pid, start)
	switch match.Status {
	case MatchingAndLive:
		if err := syscall.Kill(pid, value); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("signal pid %d: %w", pid, err)
		}
		return nil
	case GoneOrReused:
		return errGone
	default:
		return fmt.Errorf("%w: re-verify pid %d: %v", ErrUnverifiable, pid, match.Err)
	}
}

func gone(ctx context.Context, pid int, start runstate.StartIdentity) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	match := Matches(pid, start)
	switch match.Status {
	case GoneOrReused:
		return true, nil
	case MatchingAndLive:
		return false, nil
	default:
		return false, fmt.Errorf("%w: re-verify pid %d: %v", ErrUnverifiable, pid, match.Err)
	}
}

func pause(ctx context.Context) error {
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
