package procid

import (
	"os"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

func TestReadAndMatchesCurrentProcess(t *testing.T) {
	identity, err := Read(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	result := Matches(os.Getpid(), identity)
	if result.Status != MatchingAndLive || result.Err != nil {
		t.Fatalf("match = %+v", result)
	}

	var different runstate.StartIdentity
	switch identity := identity.(type) {
	case runstate.LinuxStartIdentity:
		different = runstate.LinuxStartIdentity{
			BootID:     identity.BootID,
			StartTicks: identity.StartTicks + "0",
		}
	case runstate.DarwinStartIdentity:
		different = runstate.DarwinStartIdentity{
			StartTVSec:  identity.StartTVSec + 1,
			StartTVUsec: identity.StartTVUsec,
		}
	default:
		t.Fatalf("unexpected identity type %T", identity)
	}
	result = Matches(os.Getpid(), different)
	if result.Status != GoneOrReused || result.Err != nil {
		t.Fatalf("reused match = %+v", result)
	}
}

func TestInspectionFailureIsUnverifiable(t *testing.T) {
	result := Matches(-1, runstate.LinuxStartIdentity{BootID: "boot", StartTicks: "1"})
	if result.Status != Unverifiable || result.Err == nil {
		t.Fatalf("match = %+v, want unverifiable with cause", result)
	}
}

func TestDifferentPlatformVariantIsUnverifiable(t *testing.T) {
	identity, err := Read(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	var other runstate.StartIdentity
	if identity.Platform() == "linux" {
		other = runstate.DarwinStartIdentity{StartTVSec: 1}
	} else {
		other = runstate.LinuxStartIdentity{BootID: "boot", StartTicks: "1"}
	}
	result := Matches(os.Getpid(), other)
	if result.Status != Unverifiable || result.Err == nil {
		t.Fatalf("match = %+v, want unverifiable platform mismatch", result)
	}
}
