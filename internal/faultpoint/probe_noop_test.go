//go:build !faultprobe

package faultpoint

import (
	"os"
	"strconv"
	"testing"
)

func TestProductionProbeIsAlwaysNop(t *testing.T) {
	notifyRead, notifyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer notifyRead.Close()
	defer notifyWrite.Close()
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseRead.Close()
	defer releaseWrite.Close()

	t.Setenv("PARTITUR_FAULTPOINT_NOTIFY_FD", strconv.Itoa(int(notifyWrite.Fd())))
	t.Setenv("PARTITUR_FAULTPOINT_RELEASE_FD", strconv.Itoa(int(releaseRead.Fd())))
	if _, ok := ProbeFromEnvironment().(Nop); !ok {
		t.Fatalf("ProbeFromEnvironment() = %T, want Nop", ProbeFromEnvironment())
	}
}

func TestProductionBuildRejectsFaultHarness(t *testing.T) {
	t.Setenv(probeHarnessRequiredEnv, "1")
	if err := RequireHarnessBuild(); err == nil {
		t.Fatal("RequireHarnessBuild() succeeded, want build-tag error")
	}
}
