//go:build darwin

package adapter

import (
	"fmt"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

func processStartIdentity(identity runstate.StartIdentity) (string, error) {
	darwin, ok := identity.(runstate.DarwinStartIdentity)
	if !ok {
		return "", fmt.Errorf("recorded start identity cannot be inspected on darwin")
	}
	return fmt.Sprintf("darwin-proc-start:%d.%06d", darwin.StartTVSec, darwin.StartTVUsec), nil
}
