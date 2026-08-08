package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/protectedpath"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

// CreateAttempt creates a fresh detached worktree at the run's recorded base
// and a sibling output directory outside authoritative run state.
func (run *Run) CreateAttempt(
	movementID string,
) (*AttemptWorkspace, error) {
	return run.createAttempt(movementID, run.baseCommit)
}

// CreateAttemptAtBase creates an attempt at a previously pinned composed base.
func (run *Run) CreateAttemptAtBase(movementID, baseCommit string) (*AttemptWorkspace, error) {
	if baseCommit == "" {
		return nil, errors.New("workspace: composed attempt base is absent")
	}
	return run.createAttempt(movementID, baseCommit)
}

func (run *Run) createAttempt(
	movementID, baseCommit string,
) (*AttemptWorkspace, error) {
	if run == nil {
		return nil, errors.New("workspace: nil Run")
	}
	movement, found := run.movement(movementID)
	if !found {
		return nil, fmt.Errorf("%w: %s", ErrMovementNotFound, movementID)
	}
	attemptIDValue, err := run.newID()
	if err != nil {
		return nil, err
	}
	attemptID := runstate.AttemptID(attemptIDValue)
	attemptRoot := filepath.Join(
		run.repositoryRoot,
		".partitur",
		"work",
		string(run.id),
		string(attemptID),
	)
	if _, err := os.Lstat(attemptRoot); err == nil {
		return nil, ErrAttemptIDCollision
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	worktree := filepath.Join(attemptRoot, "worktree")
	output := filepath.Join(attemptRoot, "output")
	if _, err := gitOutput(
		run.git,
		run.repositoryRoot,
		nil,
		"worktree",
		"add",
		"--detach",
		worktree,
		baseCommit,
	); err != nil {
		return nil, fmt.Errorf("create attempt worktree: %w", err)
	}
	if err := os.MkdirAll(output, 0o700); err != nil {
		return nil, fmt.Errorf("create attempt output directory: %w", err)
	}
	baseline, err := snapshotProtected(worktree)
	if err != nil {
		return nil, err
	}
	return &AttemptWorkspace{
		RunID:             run.id,
		AttemptID:         attemptID,
		MovementID:        runstate.MovementID(movement.ID),
		PartID:            movement.PartID,
		Worktree:          worktree,
		OutputDir:         output,
		run:               run,
		baseCommit:        baseCommit,
		readOnly:          !hasGrant(movement.Grants, "repo_write"),
		protectedBaseline: baseline,
	}, nil
}

func (run *Run) movement(id string) (score.MovementView, bool) {
	for _, movement := range run.movements {
		if movement.ID == id {
			return movement, true
		}
	}
	return score.MovementView{}, false
}

// VerifyProtectedPaths checks that no protected path changed since this
// attempt's worktree was created.
func (attempt *AttemptWorkspace) VerifyProtectedPaths() error {
	if attempt == nil || attempt.run == nil {
		return errors.New(
			"workspace: incomplete attempt",
		)
	}
	currentProtected, err := snapshotProtected(attempt.Worktree)
	if err != nil {
		return err
	}
	if changed := changedProtected(
		attempt.protectedBaseline,
		currentProtected,
	); len(changed) != 0 {
		return &VerificationError{
			Reason: "protected_path_violation",
			Paths:  changed,
			Cause:  ErrProtectedPathChanged,
		}
	}
	return nil
}

// VerifyReadOnlyAndRecord checks the complete read-only invariant and appends
// verification.passed with the authoritative empty payload.
func (attempt *AttemptWorkspace) VerifyReadOnlyAndRecord() (
	faultpoint.DurabilityReceipt,
	error,
) {
	if attempt == nil || attempt.run == nil {
		return faultpoint.DurabilityReceipt{}, errors.New(
			"workspace: incomplete attempt",
		)
	}
	if !attempt.readOnly {
		return faultpoint.DurabilityReceipt{}, ErrReadOnlyRequired
	}
	if err := attempt.VerifyProtectedPaths(); err != nil {
		return faultpoint.DurabilityReceipt{}, err
	}

	tracked, err := gitOutput(
		attempt.run.git,
		attempt.Worktree,
		nil,
		"diff-index",
		"--name-only",
		"-z",
		attempt.baseCommit,
		"--",
	)
	if err != nil {
		return faultpoint.DurabilityReceipt{}, err
	}
	untracked, err := gitOutput(
		attempt.run.git,
		attempt.Worktree,
		nil,
		"ls-files",
		"--others",
		"--exclude-standard",
		"-z",
	)
	if err != nil {
		return faultpoint.DurabilityReceipt{}, err
	}
	changed := append(splitNUL(tracked), splitNUL(untracked)...)
	slices.Sort(changed)
	changed = slices.Compact(changed)
	if len(changed) != 0 {
		return faultpoint.DurabilityReceipt{}, &VerificationError{
			Reason: "read_only_violation",
			Paths:  changed,
			Cause:  ErrReadOnlyChanged,
		}
	}

	var receipt faultpoint.DurabilityReceipt
	event := runstate.Event{
		RunID:         attempt.run.id,
		ScoreRevision: attempt.run.scoreRevision,
		MovementID:    attempt.MovementID,
		PartID:        attempt.PartID,
		AttemptID:     attempt.AttemptID,
		Type:          runstate.EventVerificationPassed,
		Payload:       json.RawMessage(`{}`),
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
			address := faultpoint.ReceiptAddress(
				"attempt." + string(attempt.AttemptID) + ".verification",
			)
			var appendErr error
			receipt, appendErr = transaction.At(address).Append(event)
			return appendErr
		},
	)
	return receipt, err
}

type protectedEntry struct {
	Mode    fs.FileMode
	Content string
	Target  string
}

func snapshotProtected(root string) (map[string]protectedEntry, error) {
	result := make(map[string]protectedEntry)
	for _, relative := range protectedpath.SnapshotNames() {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		err := filepath.WalkDir(path, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			record := protectedEntry{Mode: info.Mode()}
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				record.Target, err = os.Readlink(path)
			case info.Mode().IsRegular():
				var contents []byte
				contents, err = os.ReadFile(path)
				record.Content = rawHash(contents)
			}
			if err != nil {
				return err
			}
			result[filepath.ToSlash(relative)] = record
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("snapshot protected paths: %w", err)
		}
	}
	return result, nil
}

func changedProtected(
	before, after map[string]protectedEntry,
) []string {
	union := make(map[string]struct{}, len(before)+len(after))
	for path := range before {
		union[path] = struct{}{}
	}
	for path := range after {
		union[path] = struct{}{}
	}
	var changed []string
	for path := range union {
		if before[path] != after[path] {
			changed = append(changed, path)
		}
	}
	slices.Sort(changed)
	return changed
}
