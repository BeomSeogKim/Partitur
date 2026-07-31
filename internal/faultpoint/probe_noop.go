//go:build !faultprobe

package faultpoint

import (
	"errors"
	"os"
)

const probeHarnessRequiredEnv = "PARTITUR_FAULTPOINT_HARNESS"

// ProbeFromEnvironment selects a Probe. See DESIGN Appendix E.
func ProbeFromEnvironment() Probe { return Nop{} }

// RequireHarnessBuild validates the current build. See DESIGN Appendix E.
func RequireHarnessBuild() error {
	if os.Getenv(probeHarnessRequiredEnv) == "1" {
		return errors.New("faultpoint harness requires a binary built with -tags=faultprobe")
	}
	return nil
}
