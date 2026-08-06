package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

// TestProposalPublicationKillCuts drives the blocking proposal-publication R->R
// edge through real partitur subprocesses. The left cut proves an orphan is
// quarantined with its original bytes; the right cut proves its durable route
// descriptor retains it at the immutable proposal path.
func TestProposalPublicationKillCuts(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("proposal.published_to_blocked_route/published", func(t *testing.T) {
		repository, environment := routedProposalKillRepository(t, bin, vendor)
		child := pauseRunAtReceipt(t, partitur, repository, environment, "proposal.record.published")
		runID := routedProposalRunID(t, repository)
		record := proposalPublicationRecord(t, repository, runID)
		assertNoEvent(t, repository, runID, runstate.EventAttemptBlocked)
		assertNoEvent(t, repository, runID, runstate.EventAmendmentRoutedHuman)
		killPausedRun(t, child)

		resumeProposalPublication(t, partitur, repository, environment, runID, 4)
		assertProposalPublicationQuarantined(t, record)
	})

	t.Run("proposal.published_to_blocked_route/blocked_route", func(t *testing.T) {
		repository, environment := routedProposalKillRepository(t, bin, vendor)
		child := pauseRunAtReceipt(t, partitur, repository, environment, "attempt.blocked")
		runID := routedProposalRunID(t, repository)
		record := proposalPublicationRecord(t, repository, runID)
		blocked := routedProposalEvent(t, repository, runID, runstate.EventAttemptBlocked)
		assertBlockingDescriptorBindsProposalRecord(t, blocked, record)
		assertNoEvent(t, repository, runID, runstate.EventAmendmentRoutedHuman)
		killPausedRun(t, child)

		resumeProposalPublication(t, partitur, repository, environment, runID, 0)
		assertProposalPublicationRetained(t, record)
	})
}

type proposalPublicationArtifact struct {
	path     string
	contents []byte
}

func proposalPublicationRecord(t *testing.T, repository string, runID runstate.RunID) proposalPublicationArtifact {
	t.Helper()
	directory := filepath.Join(repository, ".partitur", "runs", string(runID), "proposals")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].Type().IsRegular() || !strings.HasSuffix(entries[0].Name(), ".json") {
		t.Fatalf("proposal records = %v, want one immutable proposal record", entries)
	}
	path := filepath.Join(directory, entries[0].Name())
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return proposalPublicationArtifact{path: path, contents: contents}
}

func assertBlockingDescriptorBindsProposalRecord(t *testing.T, blocked runstate.Event, record proposalPublicationArtifact) {
	t.Helper()
	descriptor := blockingRouteDescriptor(t, blocked)
	if got, want := descriptor["proposal_record_hash"], proposalPublicationHash(record.contents); got != want {
		t.Fatalf("blocking descriptor proposal_record_hash=%#v, want %q", got, want)
	}
}

func resumeProposalPublication(t *testing.T, binary, repository string, environment []string, runID runstate.RunID, wantCode int) {
	t.Helper()
	code, stdout, stderr := runCommandBinary(t, binary, repository, environment, "resume", string(runID))
	if code != wantCode || stdout != "" || stderr != "" {
		t.Fatalf("proposal-publication recovery exit=%d stdout=%q stderr=%q, want exit=%d", code, stdout, stderr, wantCode)
	}
}

func assertProposalPublicationQuarantined(t *testing.T, record proposalPublicationArtifact) {
	t.Helper()
	if _, err := os.Stat(record.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced proposal record remains at immutable path: %v", err)
	}
	hash := strings.TrimPrefix(proposalPublicationHash(record.contents), "sha256:")
	quarantined := filepath.Join(filepath.Dir(filepath.Dir(record.path)), "quarantine", "unreferenced_proposal_record", hash, filepath.Base(record.path))
	contents, err := os.ReadFile(quarantined)
	if err != nil || !bytes.Equal(contents, record.contents) {
		t.Fatalf("quarantined proposal record contents=%q error=%v", contents, err)
	}
}

func assertProposalPublicationRetained(t *testing.T, record proposalPublicationArtifact) {
	t.Helper()
	contents, err := os.ReadFile(record.path)
	if err != nil || !bytes.Equal(contents, record.contents) {
		t.Fatalf("referenced proposal record contents=%q error=%v", contents, err)
	}
}

func proposalPublicationHash(contents []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(contents))
}
