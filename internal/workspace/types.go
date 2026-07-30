// Package workspace owns Git-backed run and attempt workspaces.
package workspace

import (
	"errors"
	"fmt"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

var (
	ErrIncompletePreparation = errors.New("workspace preparation is incomplete")
	ErrGitTooOld             = errors.New("git version is below 2.47")
	ErrNotRepository         = errors.New("invocation directory is not the repository root")
	ErrBareRepository        = errors.New("bare repositories are unsupported")
	ErrDirtySource           = errors.New("source repository is dirty")
	ErrExternalMergeDriver   = errors.New("external merge driver is unsupported")
	ErrRunIDCollision        = errors.New("run id collision")
	ErrAttemptIDCollision    = errors.New("attempt id collision")
	ErrMovementNotFound      = errors.New("movement is not in the pinned score")
	ErrWriterMovement        = errors.New("zero-writer candidate has a writer movement")
	ErrReadOnlyRequired      = errors.New("movement grants repo_write")
	ErrProtectedPathChanged  = errors.New("protected path changed")
	ErrReadOnlyChanged       = errors.New("read-only worktree changed")
	ErrIncompleteArtifact    = errors.New("artifact ingest is incomplete")
	ErrChangeSetArtifact     = errors.New("change_set is not an artifact instance")
	ErrArtifactNotRegular    = errors.New("artifact source is not a regular file")
	ErrArtifactChanged       = errors.New("artifact source changed during ingest")
)

// StartResult exists only after run.started has crossed its durability
// boundary. A caller may therefore expose RunID immediately on return.
type StartResult struct {
	RunID   runstate.RunID
	Receipt faultpoint.DurabilityReceipt
	Run     *Run
}

// Candidate is the recorded zero-writer application candidate.
type Candidate struct {
	ID                        string
	BaseTree                  string
	ResultTree                string
	CompositionDependencyHash string
	Receipt                   faultpoint.DurabilityReceipt
}

// AttemptWorkspace is one fresh detached worktree plus its sibling output
// directory.
type AttemptWorkspace struct {
	RunID      runstate.RunID
	AttemptID  runstate.AttemptID
	MovementID runstate.MovementID
	PartID     string
	Worktree   string
	OutputDir  string

	run               *Run
	baseCommit        string
	readOnly          bool
	protectedBaseline map[string]protectedEntry
}

// ArtifactInput is one adapter-admitted artifact notification.
type ArtifactInput struct {
	LogicalOutputID string
	Kind            string
	Path            string
	SourcePath      string
}

// ArtifactInstance is the immutable copy and its two durability receipts.
type ArtifactInstance struct {
	ContentHash        runstate.Hash
	SizeBytes          uint64
	PublicationReceipt faultpoint.DurabilityReceipt
	RecordReceipt      faultpoint.DurabilityReceipt
}

// VerificationError is an immediately-terminal grant_denied result.
type VerificationError struct {
	Reason string
	Paths  []string
	Cause  error
}

func (e *VerificationError) Error() string {
	return fmt.Sprintf("grant_denied: %s: %v", e.Reason, e.Paths)
}

func (e *VerificationError) Unwrap() error {
	return e.Cause
}

// Run is a durable run-start handle. Its fields remain private so later
// operations cannot substitute a different repository or base.
type Run struct {
	id                runstate.RunID
	repositoryRoot    string
	scoreRevision     uint64
	baseCommit        string
	baseTreeQualified string
	movements         []score.MovementView
	store             *runstore.Store
	driver            *runstore.Driver
	git               gitCommand
	newID             func() (string, error)
}
