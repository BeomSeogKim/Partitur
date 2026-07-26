package adapter

import (
	"os"
	"reflect"
	"syscall"
	"testing"
)

func TestIndividualSignalRequiresMatchingStartIdentity(t *testing.T) {
	process, err := processByPID(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	var signalled []int
	kill := func(pid int, _ syscall.Signal) error {
		signalled = append(signalled, pid)
		return nil
	}

	stale := process
	stale.Start += "-stale"
	if err := signalRecordsWith([]processRecord{stale}, syscall.Signal(0), kill); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(signalled, []int{-process.PGID}) {
		t.Fatalf("stale identity signals = %v", signalled)
	}

	signalled = nil
	if err := signalRecordsWith([]processRecord{process}, syscall.Signal(0), kill); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(signalled, []int{-process.PGID, process.PID}) {
		t.Fatalf("matching identity signals = %v", signalled)
	}
}

func TestSessionLeaderIdentityRejectsPIDReuse(t *testing.T) {
	process, err := processByPID(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := verifySessionLeaderIdentity(process.PID, process.Start); err != nil {
		t.Fatalf("matching leader identity: %v", err)
	}
	if err := verifySessionLeaderIdentity(process.PID, process.Start+"-stale"); err == nil {
		t.Fatal("stale leader identity was accepted")
	}
}
