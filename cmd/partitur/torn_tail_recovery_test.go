package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

const tornJournalInjection = `{"event_id":"torn-tail"`

func injectTornJournalTail(t *testing.T, root string, store *runstore.Store) string {
	t.Helper()
	path := filepath.Join(root, ".partitur", "runs", "run-1", "journal.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(tornJournalInjection); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	assertTornJournalTail(t, path, store)
	return path
}

func assertTornJournalTail(t *testing.T, path string, store *runstore.Store) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(contents, []byte(tornJournalInjection)) {
		t.Fatalf("torn-tail injection not present in raw journal suffix: %q", contents)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if !journal.TailUnparseable || journal.DiscardedBytes != len(tornJournalInjection) {
		t.Fatalf("ReadJournal tail_unparseable=%t discarded_bytes=%d, want true and %d", journal.TailUnparseable, journal.DiscardedBytes, len(tornJournalInjection))
	}
}

func assertTailRepairedExactlyOnce(t *testing.T, store *runstore.Store) {
	t.Helper()
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if journal.TailUnparseable {
		t.Fatal("journal tail remains unparseable after resume")
	}
	if got := countEvents(journal.Events, runstate.EventJournalTailTruncated); got != 1 {
		t.Fatalf("journal.tail_truncated count=%d, want exactly 1", got)
	}
}

func TestTornTailOriginatingCommandsUseUnavailableProjectionExit(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T) (string, *runstore.Store, func(*bytes.Buffer) int)
	}{
		{
			name: "cancel",
			setup: func(t *testing.T) (string, *runstore.Store, func(*bytes.Buffer) int) {
				root, store := resumeFixture(t, "")
				return root, store, func(stderr *bytes.Buffer) int {
					var stdout bytes.Buffer
					return runCancel("run-1", &stdout, stderr, cancel)
				}
			},
		},
		{
			name: "answer",
			setup: func(t *testing.T) (string, *runstore.Store, func(*bytes.Buffer) int) {
				root, store := resumeAttemptFixture(t)
				decisionID := appendPendingCLIDecision(t, store, "question")
				return root, store, func(stderr *bytes.Buffer) int { return runAnswer(decisionID, "yes", stderr) }
			},
		},
		{
			name: "approve",
			setup: func(t *testing.T) (string, *runstore.Store, func(*bytes.Buffer) int) {
				root, store := resumeAttemptFixture(t)
				decisionID := appendPendingCLIDecision(t, store, "human_gate")
				return root, store, func(stderr *bytes.Buffer) int { return runApprove(decisionID, true, nil, "", stderr) }
			},
		},
		{
			name: "amend",
			setup: func(t *testing.T) (string, *runstore.Store, func(*bytes.Buffer) int) {
				root, store := amendCommandFixture(t, true)
				patch := filepath.Join(root, "torn-tail-patch.json")
				if err := os.WriteFile(patch, []byte(`[{"op":"replace","path":"/goal","value":"torn-tail"}]`), 0o600); err != nil {
					t.Fatal(err)
				}
				return root, store, func(stderr *bytes.Buffer) int {
					var stdout bytes.Buffer
					return runAmend("run-1", patch, "torn tail", "", &stdout, stderr)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, store, invoke := test.setup(t)
			t.Chdir(root)
			path := injectTornJournalTail(t, root, store)
			var stderr bytes.Buffer
			if code := invoke(&stderr); code != 5 || !strings.HasPrefix(stderr.String(), "recovery halted:") || strings.Contains(stderr.String(), "resume=") {
				t.Fatalf("exit=%d stderr=%q, want unavailable-projection exit 5 without continuation", code, stderr.String())
			}
			assertTornJournalTail(t, path, store)
		})
	}
}

func TestTornTailFormerlyAdvertisedContinuationsNowRepairAtRuntime(t *testing.T) {
	project := repositoryRoot(t)
	partitur := buildE2EBinary(t, project, t.TempDir(), "partitur")
	tests := []struct {
		name         string
		setup        func(*testing.T) (string, *runstore.Store, []string)
		continuation []string
	}{
		{
			name: "cancel explicit run",
			setup: func(t *testing.T) (string, *runstore.Store, []string) {
				root, store := resumeAttemptFixture(t)
				appendPendingCLIDecision(t, store, "question")
				return root, store, []string{"cancel", "run-1"}
			},
			continuation: []string{"resume", "run-1"},
		},
		{
			name: "answer selected run",
			setup: func(t *testing.T) (string, *runstore.Store, []string) {
				root, store := resumeAttemptFixture(t)
				decisionID := appendPendingCLIDecision(t, store, "question")
				return root, store, []string{"answer", decisionID, "--answer", "yes"}
			},
			continuation: []string{"resume"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, store, origin := test.setup(t)
			path := injectTornJournalTail(t, root, store)
			code, stdout, stderr := runCommandBinary(t, partitur, root, nil, origin...)
			if code != 5 || stdout != "" || !strings.HasPrefix(stderr, "recovery halted:") || strings.Contains(stderr, "resume=") {
				t.Fatalf("origin exit=%d stdout=%q stderr=%q, want unavailable-projection exit 5", code, stdout, stderr)
			}
			assertTornJournalTail(t, path, store)

			code, stdout, stderr = runCommandBinary(t, partitur, root, nil, test.continuation...)
			if code != 0 || stdout != "" || stderr != "" {
				t.Fatalf("continuation exit=%d stdout=%q stderr=%q, want successful repair", code, stdout, stderr)
			}
			assertTailRepairedExactlyOnce(t, store)

			code, stdout, stderr = runCommandBinary(t, partitur, root, nil, test.continuation...)
			if code != 0 || stdout != "" || stderr != "" {
				t.Fatalf("second continuation exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			assertTailRepairedExactlyOnce(t, store)
		})
	}
}

func TestResumeDoesNotRepairValidEnvelopeCorruption(t *testing.T) {
	project := repositoryRoot(t)
	partitur := buildE2EBinary(t, project, t.TempDir(), "partitur")
	root, _ := resumeFixture(t, "")
	path := filepath.Join(root, ".partitur", "runs", "run-1", "journal.jsonl")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Replace(before, []byte(`"seq":1`), []byte(`"seq":2`), 1)
	if bytes.Equal(corrupt, before) || !bytes.Contains(corrupt, []byte(`"seq":2`)) {
		t.Fatal("valid-envelope sequence corruption injection did not apply")
	}
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCommandBinary(t, partitur, root, nil, "resume", "run-1")
	if code != 5 || stdout != "" || !strings.Contains(stderr, `reason="journal_corrupt"`) {
		t.Fatalf("resume exit=%d stdout=%q stderr=%q, want journal_corrupt halt", code, stdout, stderr)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, corrupt) || bytes.Contains(after, []byte(`"type":"journal.tail_truncated"`)) {
		t.Fatalf("non-tail corruption was mutated: before=%q after=%q", corrupt, after)
	}
}
