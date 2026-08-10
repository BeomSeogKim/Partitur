package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

// TestProductionApplyIgnoresArmedProbeDescriptors gives a production binary
// the same live pipe descriptors that make a faultprobe build stop at
// apply.transaction_started. The harness marker remains non-affirmative so
// main's production-build rejection does not pre-empt the command; the live
// descriptors still arm ProbeFromEnvironment in a faultprobe build.
func TestProductionApplyIgnoresArmedProbeDescriptors(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	partitur := buildProductionE2EBinary(t, repositoryRoot, t.TempDir(), "partitur")
	root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})

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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, partitur, "apply", "run-1")
	command.Dir = root
	command.Env = replaceEnvironment(applyKillEnvironment(t), map[string]string{
		// A production main rejects only the affirmative marker. The two FDs
		// are nevertheless live and would arm a faultprobe build, whose probe
		// does not consult this marker before blocking at the transaction seam.
		"PARTITUR_FAULTPOINT_HARNESS":    "0",
		"PARTITUR_FAULTPOINT_NOTIFY_FD":  strconv.Itoa(3),
		"PARTITUR_FAULTPOINT_RELEASE_FD": strconv.Itoa(4),
	})
	command.ExtraFiles = []*os.File{notifyWrite, releaseRead}
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if err := notifyWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := releaseRead.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		if ctx.Err() != nil {
			t.Fatalf("production apply blocked on armed probe descriptors: %v\nstdout=%q\nstderr=%q", ctx.Err(), stdout.String(), stderr.String())
		}
		t.Fatalf("production apply: %v\nstdout=%q\nstderr=%q", err, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("production apply wrote stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if contents := applyReadFile(t, root, "applied.txt"); contents != "candidate result\n" {
		t.Fatalf("production apply left checkout=%q", contents)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventApplyStarted) != 1 ||
		countEvents(journal.Events, runstate.EventApplyCompleted) != 1 {
		t.Fatalf("production apply journal=%v", eventKinds(journal.Events))
	}
}

func buildProductionE2EBinary(t *testing.T, root, outputDirectory, name string) string {
	t.Helper()
	output := filepath.Join(outputDirectory, name)
	command := exec.Command("go", "build", "-o", output, "./cmd/"+name)
	command.Dir = root
	command.Env = os.Environ()
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build production %s: %v\n%s", name, err, data)
	}
	return output
}
