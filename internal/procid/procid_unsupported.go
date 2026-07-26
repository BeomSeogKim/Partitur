//go:build !linux && !darwin

package procid

import (
	"errors"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

func read(int) (runstate.StartIdentity, bool, error) {
	return nil, false, errors.New("process-start identity is unsupported on this platform")
}
