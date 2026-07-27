package workspace

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

type artifactDependencies struct {
	afterCopy func()
}

// IngestArtifact copies one adapter-admitted artifact into immutable run
// storage, then records the instance. The caller owns both receipt addresses.
func (attempt *AttemptWorkspace) IngestArtifact(
	input ArtifactInput,
	publicationAddress faultpoint.ReceiptAddress,
	recordAddress faultpoint.ReceiptAddress,
) (ArtifactInstance, error) {
	return attempt.ingestArtifact(input, publicationAddress, recordAddress, artifactDependencies{})
}

func (attempt *AttemptWorkspace) ingestArtifact(
	input ArtifactInput,
	publicationAddress faultpoint.ReceiptAddress,
	recordAddress faultpoint.ReceiptAddress,
	dependencies artifactDependencies,
) (ArtifactInstance, error) {
	if attempt == nil || attempt.run == nil ||
		input.LogicalOutputID == "" || input.Kind == "" ||
		input.Path == "" || input.SourcePath == "" ||
		publicationAddress == "" || recordAddress == "" {
		return ArtifactInstance{}, ErrIncompleteArtifact
	}
	if input.Kind == "change_set" {
		return ArtifactInstance{}, ErrChangeSetArtifact
	}
	contents, err := stableArtifactContents(input.Path, dependencies.afterCopy)
	if err != nil {
		return ArtifactInstance{}, err
	}
	contentHash := runstate.Hash(rawHash(contents))
	payload, err := json.Marshal(map[string]any{
		"logical_output_id": input.LogicalOutputID,
		"kind":              input.Kind,
		"content_hash":      contentHash,
		"size_bytes":        len(contents),
		"source_path":       input.SourcePath,
	})
	if err != nil {
		return ArtifactInstance{}, err
	}
	path := runstore.Path(filepath.ToSlash(filepath.Join(
		"artifacts",
		input.LogicalOutputID,
		string(attempt.AttemptID),
	)))

	result := ArtifactInstance{
		ContentHash: contentHash,
		SizeBytes:   uint64(len(contents)),
	}
	event := runstate.Event{
		RunID:         attempt.run.id,
		ScoreRevision: attempt.run.scoreRevision,
		MovementID:    attempt.MovementID,
		PartID:        attempt.PartID,
		AttemptID:     attempt.AttemptID,
		Type:          runstate.EventArtifactRecorded,
		Payload:       payload,
	}
	err = attempt.run.mutate(
		func(
			transaction *runstore.Txn,
			state runstate.State,
			authorized bool,
		) error {
			if authorized {
				if _, err := runstate.Apply(state, event); err != nil {
					return err
				}
			}
			var publishErr error
			result.PublicationReceipt, publishErr = transaction.
				At(publicationAddress).
				PublishImmutable(path, contents, contentHash)
			if publishErr != nil {
				return publishErr
			}
			var appendErr error
			result.RecordReceipt, appendErr = transaction.
				At(recordAddress).
				Append(event)
			return appendErr
		},
	)
	return result, err
}

func stableArtifactContents(path string, afterCopy func()) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	before, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, ErrArtifactNotRegular
	}
	contents, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	if afterCopy != nil {
		afterCopy()
	}
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	named, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if before.Size() != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) ||
		before.Mode() != after.Mode() ||
		!os.SameFile(before, named) {
		return nil, ErrArtifactChanged
	}
	return contents, nil
}
