package workspace

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

const (
	testArtifactPublication = faultpoint.ReceiptAddress("artifact.report.publication")
	testArtifactRecord      = faultpoint.ReceiptAddress("artifact.report.event")
)

func TestIngestArtifactPublishesRawBytesThenRecordsInstance(t *testing.T) {
	repository, started, attempt := artifactAttemptFixture(t)
	sourcePath := "reports/final.bin"
	source := filepath.Join(attempt.OutputDir, filepath.FromSlash(sourcePath))
	contents := []byte{0x00, 0xff, 0x10, '\n'}
	writeFile(t, source, contents, 0o600)

	instance, err := attempt.IngestArtifact(
		ArtifactInput{
			LogicalOutputID: "report",
			Kind:            "artifact",
			Path:            source,
			SourcePath:      sourcePath,
		},
		testArtifactPublication,
		testArtifactRecord,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := runstate.Hash(rawHash(contents))
	if instance.ContentHash != wantHash ||
		instance.SizeBytes != uint64(len(contents)) {
		t.Fatalf("instance = %#v", instance)
	}
	if instance.PublicationReceipt.Address != testArtifactPublication ||
		instance.PublicationReceipt.Mutation.Kind != faultpoint.FilePublication {
		t.Fatalf("publication receipt = %#v", instance.PublicationReceipt)
	}
	if instance.RecordReceipt.Address != testArtifactRecord ||
		instance.RecordReceipt.Mutation.Kind != faultpoint.JournalAppend ||
		instance.RecordReceipt.Mutation.EventType !=
			string(runstate.EventArtifactRecorded) {
		t.Fatalf("record receipt = %#v", instance.RecordReceipt)
	}

	destination := artifactDestination(repository, attempt, "report")
	if got := readFile(t, destination); !bytes.Equal(got, contents) {
		t.Fatalf("immutable copy = %x, want %x", got, contents)
	}
	assertOutside(t, destination, attempt.OutputDir)
	assertOutside(t, destination, attempt.Worktree)

	events := readJournal(t, repository, started.RunID)
	event := events[len(events)-1]
	if event.Type != runstate.EventArtifactRecorded {
		t.Fatalf("last event = %s", event.Type)
	}
	payload := decodePayload(t, event)
	if payload["logical_output_id"] != "report" ||
		payload["kind"] != "artifact" ||
		payload["content_hash"] != string(wantHash) ||
		payload["size_bytes"] != float64(len(contents)) ||
		payload["source_path"] != sourcePath {
		t.Fatalf("artifact payload = %#v", payload)
	}

	replay, err := started.Run.store.Replay(
		started.RunID,
		[]runstate.MovementSeed{{
			ID:      attempt.MovementID,
			Initial: runstate.MovementPending,
		}},
		"test.artifact.repair",
	)
	if err != nil {
		t.Fatal(err)
	}
	record, ok := replay.State.Artifacts[runstate.ArtifactInstanceID(
		"report@"+string(attempt.AttemptID),
	)]
	if !ok || record.ContentHash != wantHash ||
		record.SizeBytes != uint64(len(contents)) ||
		record.Source != sourcePath {
		t.Fatalf("projected artifact = %#v, present=%t", record, ok)
	}
}

func TestIngestArtifactEquivalentReplayIsIdempotent(t *testing.T) {
	repository, _, attempt := artifactAttemptFixture(t)
	source := filepath.Join(attempt.OutputDir, "report.txt")
	writeFile(t, source, []byte("final\n"), 0o600)
	input := ArtifactInput{
		LogicalOutputID: "report",
		Kind:            "artifact",
		Path:            source,
		SourcePath:      "report.txt",
	}

	first, err := attempt.IngestArtifact(
		input,
		testArtifactPublication,
		testArtifactRecord,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := attempt.IngestArtifact(
		input,
		testArtifactPublication,
		testArtifactRecord,
	)
	if err != nil {
		t.Fatal(err)
	}
	if second.RecordReceipt.Mutation.EventID != first.RecordReceipt.Mutation.EventID ||
		second.RecordReceipt.Mutation.Sequence != first.RecordReceipt.Mutation.Sequence {
		t.Fatalf("idempotent receipts differ: first=%#v second=%#v", first, second)
	}
	count := 0
	for _, event := range readJournal(t, repository, testRunID) {
		if event.Type == runstate.EventArtifactRecorded {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("artifact.recorded count = %d, want 1", count)
	}
}

func TestIngestArtifactRejectsEveryLocalAdmissionGuard(t *testing.T) {
	valid := ArtifactInput{
		LogicalOutputID: "report",
		Kind:            "artifact",
		Path:            "source",
		SourcePath:      "report.txt",
	}
	tests := []struct {
		name        string
		mutate      func(*ArtifactInput, *faultpoint.ReceiptAddress, *faultpoint.ReceiptAddress)
		want        error
		preparePath func(*testing.T, *AttemptWorkspace, *ArtifactInput)
	}{
		{
			name: "logical output id",
			mutate: func(input *ArtifactInput, _, _ *faultpoint.ReceiptAddress) {
				input.LogicalOutputID = ""
			},
			want: ErrIncompleteArtifact,
		},
		{
			name: "kind",
			mutate: func(input *ArtifactInput, _, _ *faultpoint.ReceiptAddress) {
				input.Kind = ""
			},
			want: ErrIncompleteArtifact,
		},
		{
			name: "source path",
			mutate: func(input *ArtifactInput, _, _ *faultpoint.ReceiptAddress) {
				input.SourcePath = ""
			},
			want: ErrIncompleteArtifact,
		},
		{
			name: "path",
			mutate: func(input *ArtifactInput, _, _ *faultpoint.ReceiptAddress) {
				input.Path = ""
			},
			want: ErrIncompleteArtifact,
		},
		{
			name: "publication receipt address",
			mutate: func(_ *ArtifactInput, address, _ *faultpoint.ReceiptAddress) {
				*address = ""
			},
			want: ErrIncompleteArtifact,
		},
		{
			name: "record receipt address",
			mutate: func(_ *ArtifactInput, _, address *faultpoint.ReceiptAddress) {
				*address = ""
			},
			want: ErrIncompleteArtifact,
		},
		{
			name: "change set",
			mutate: func(input *ArtifactInput, _, _ *faultpoint.ReceiptAddress) {
				input.Kind = "change_set"
			},
			want: ErrChangeSetArtifact,
		},
		{
			name:   "regular file",
			mutate: func(*ArtifactInput, *faultpoint.ReceiptAddress, *faultpoint.ReceiptAddress) {},
			want:   ErrArtifactNotRegular,
			preparePath: func(t *testing.T, attempt *AttemptWorkspace, input *ArtifactInput) {
				input.Path = filepath.Join(attempt.OutputDir, "directory")
				if err := os.Mkdir(input.Path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, _, attempt := artifactAttemptFixture(t)
			input := valid
			input.Path = filepath.Join(attempt.OutputDir, "source")
			writeFile(t, input.Path, []byte("final\n"), 0o600)
			if test.preparePath != nil {
				test.preparePath(t, attempt, &input)
			}
			publicationAddress := testArtifactPublication
			recordAddress := testArtifactRecord
			test.mutate(&input, &publicationAddress, &recordAddress)

			_, err := attempt.IngestArtifact(
				input,
				publicationAddress,
				recordAddress,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if _, statErr := os.Stat(artifactDestination(
				repository,
				attempt,
				"report",
			)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("immutable artifact exists after rejection: %v", statErr)
			}
			for _, event := range readJournal(t, repository, testRunID) {
				if event.Type == runstate.EventArtifactRecorded {
					t.Fatalf("artifact event appended after rejection: %#v", event)
				}
			}
		})
	}
}

func TestIngestArtifactRejectsIncompleteAttempt(t *testing.T) {
	input := ArtifactInput{
		LogicalOutputID: "report",
		Kind:            "artifact",
		Path:            "source",
		SourcePath:      "report.txt",
	}
	for _, attempt := range []*AttemptWorkspace{nil, {}} {
		_, err := attempt.IngestArtifact(
			input,
			testArtifactPublication,
			testArtifactRecord,
		)
		if !errors.Is(err, ErrIncompleteArtifact) {
			t.Fatalf("attempt=%#v error = %v, want ErrIncompleteArtifact", attempt, err)
		}
	}
}

func TestStableStatRejectsEachChangedFileFact(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string, os.FileInfo)
	}{
		{
			name: "size",
			mutate: func(t *testing.T, path string, before os.FileInfo) {
				writeFile(t, path, []byte("longer contents\n"), 0o600)
				if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "modification time",
			mutate: func(t *testing.T, path string, before os.FileInfo) {
				changed := before.ModTime().Add(2 * time.Second)
				if err := os.Chtimes(path, changed, changed); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "mode",
			mutate: func(t *testing.T, path string, _ os.FileInfo) {
				if err := os.Chmod(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "named file identity",
			mutate: func(t *testing.T, path string, before os.FileInfo) {
				old := path + ".old"
				if err := os.Rename(path, old); err != nil {
					t.Fatal(err)
				}
				writeFile(t, path, []byte("final\n"), before.Mode().Perm())
				if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "artifact")
			writeFile(t, path, []byte("final\n"), 0o600)
			fixed := time.Unix(1_700_000_000, 0)
			if err := os.Chtimes(path, fixed, fixed); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = stableArtifactContents(path, func() {
				test.mutate(t, path, before)
			})
			if !errors.Is(err, ErrArtifactChanged) {
				t.Fatalf("error = %v, want ErrArtifactChanged", err)
			}
		})
	}
}

func TestIngestArtifactDeterministicallyRejectsMutationDuringCopy(t *testing.T) {
	repository, _, attempt := artifactAttemptFixture(t)
	source := filepath.Join(attempt.OutputDir, "report.txt")
	writeFile(t, source, []byte("first\n"), 0o600)
	copied := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)

	go func() {
		_, err := attempt.ingestArtifact(
			ArtifactInput{
				LogicalOutputID: "report",
				Kind:            "artifact",
				Path:            source,
				SourcePath:      "report.txt",
			},
			testArtifactPublication,
			testArtifactRecord,
			artifactDependencies{afterCopy: func() {
				close(copied)
				<-release
			}},
		)
		result <- err
	}()

	select {
	case <-copied:
	case <-time.After(5 * time.Second):
		t.Fatal("ingest did not reach the post-copy seam")
	}
	writeFile(t, source, []byte("still being written\n"), 0o600)
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, ErrArtifactChanged) {
			t.Fatalf("error = %v, want ErrArtifactChanged", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ingest did not return after releasing seam")
	}
	if _, err := os.Stat(artifactDestination(
		repository,
		attempt,
		"report",
	)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unstable artifact was published: %v", err)
	}
}

func TestIngestArtifactDoesNotAppendBeforeImmutablePublication(t *testing.T) {
	repository, _, attempt := artifactAttemptFixture(t)
	source := filepath.Join(attempt.OutputDir, "report.txt")
	writeFile(t, source, []byte("new\n"), 0o600)
	destination := artifactDestination(repository, attempt, "report")
	writeFile(t, destination, []byte("existing\n"), 0o600)

	_, err := attempt.IngestArtifact(
		ArtifactInput{
			LogicalOutputID: "report",
			Kind:            "artifact",
			Path:            source,
			SourcePath:      "report.txt",
		},
		testArtifactPublication,
		testArtifactRecord,
	)
	if !errors.Is(err, runstore.ErrImmutablePublicationConflict) {
		t.Fatalf("error = %v, want ErrImmutablePublicationConflict", err)
	}
	for _, event := range readJournal(t, repository, testRunID) {
		if event.Type == runstate.EventArtifactRecorded {
			t.Fatalf("artifact event preceded failed publication: %#v", event)
		}
	}
}

func artifactAttemptFixture(
	t *testing.T,
) (string, StartResult, *AttemptWorkspace) {
	t.Helper()
	repository, started, attempt := newAttemptFixture(t)
	appendArtifactRunningHistory(t, started.Run, attempt)
	return repository, started, attempt
}

func appendArtifactRunningHistory(
	t *testing.T,
	run *Run,
	attempt *AttemptWorkspace,
) {
	t.Helper()
	events := []runstate.Event{
		attemptEvent(run, attempt, runstate.EventMovementReady, map[string]any{}),
		attemptEvent(run, attempt, runstate.EventMovementStarted, map[string]any{}),
		attemptEvent(run, attempt, runstate.EventPerformerSelected, map[string]any{
			"reason": "initial", "performer_id": "worker",
			"adapter_id": "codex", "model": "fixture",
		}),
		attemptEvent(run, attempt, runstate.EventAttemptStarted, map[string]any{
			"attempt_number": 1,
			"adapter_process": map[string]any{
				"pid": 10, "session_id": 10,
				"start_identity": map[string]any{
					"platform": "linux", "boot_id": "boot", "start_ticks": "12",
				},
			},
			"granted_authority": map[string]any{
				"paths_rw": []any{}, "paths_ro": []any{"**"},
				"shell": false, "network": false,
			},
			"identity_versions": testIdentityVersions(),
		}),
		attemptEvent(run, attempt, runstate.EventAdapterProbed, map[string]any{
			"adapter_version": "1",
			"capabilities": map[string]any{
				"repo_read": true, "repo_write": false, "shell": false,
				"network": false, "resumable_sessions": false,
			},
			"enforcement": map[string]any{
				"path_grants": true, "read_only": true,
				"network_grants": false, "shell_grants": false,
				"read_grants": true,
			},
			"negotiated_features":       []any{},
			"truncated_resolutions":     []any{},
			"delivered_resolutions":     []any{},
			"delivered_feedback":        []any{},
			"advisory_dimensions":       []any{},
			"execution_dependency_hash": "sha256:dependency",
			"identity_versions":         testIdentityVersions(),
		}),
	}
	for _, event := range events {
		err := run.store.Mutate(run.id, "", func(transaction *runstore.Txn) error {
			_, err := transaction.At(faultpoint.ReceiptAddress(
				"test.artifact.history." + string(event.Type),
			)).Append(event)
			return err
		})
		if err != nil {
			t.Fatalf("append prerequisite %s: %v", event.Type, err)
		}
	}
}

func artifactDestination(
	repository string,
	attempt *AttemptWorkspace,
	logicalOutputID string,
) string {
	return filepath.Join(
		repository,
		".partitur",
		"runs",
		string(attempt.RunID),
		"artifacts",
		logicalOutputID,
		string(attempt.AttemptID),
	)
}
