//go:build linux

package adapter

import (
	"fmt"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

func processStartIdentity(identity runstate.StartIdentity) (string, error) {
	linux, ok := identity.(runstate.LinuxStartIdentity)
	if !ok || linux.BootID == "" || linux.StartTicks == "" {
		return "", fmt.Errorf("recorded start identity cannot be inspected on linux")
	}
	return "linux-proc-start:" + linux.BootID + ":" + linux.StartTicks, nil
}
