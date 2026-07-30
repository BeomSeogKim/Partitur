package driver

import (
	"encoding/json"
	"errors"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/successor"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

// CaptureAndRecordChangeSet captures a writer attempt's checkpoint before
// appending the event that names its durable ref.
func CaptureAndRecordChangeSet(
	attempt *workspace.AttemptWorkspace,
	authority *runstore.Driver,
) (workspace.ChangeSet, error) {
	return captureAndRecordChangeSet(attempt, authority, func(event runstate.Event) error {
		_, err := authority.Append(
			event,
			faultpoint.ReceiptAddress("attempt."+string(attempt.AttemptID)+".changeset.recorded"),
		)
		return err
	})
}

func captureAndRecordChangeSet(
	attempt *workspace.AttemptWorkspace,
	authority *runstore.Driver,
	appendEvent func(runstate.Event) error,
) (workspace.ChangeSet, error) {
	if attempt == nil || authority == nil || appendEvent == nil {
		return workspace.ChangeSet{}, errors.New("driver: incomplete change set capture")
	}
	state, err := authority.State()
	if err != nil {
		return workspace.ChangeSet{}, err
	}
	if recorded, ok := state.ChangeSets[attempt.AttemptID]; ok {
		versions := make(map[string]any)
		if err := json.Unmarshal(recorded.IdentityVersions, &versions); err != nil {
			return workspace.ChangeSet{}, err
		}
		return workspace.ChangeSet{
			ID: recorded.ChangeSetID, BaseTree: recorded.BaseTree,
			ResultTree: recorded.ResultTree, Commit: recorded.Commit,
			Ref: recorded.Ref, IdentityVersions: versions,
		}, nil
	}
	changeSet, err := attempt.CaptureChangeSet()
	if err != nil {
		return workspace.ChangeSet{}, err
	}
	event, err := attempt.ChangeSetRecordedEvent(changeSet)
	if err != nil {
		return workspace.ChangeSet{}, err
	}
	if err := appendEvent(event); err != nil {
		return workspace.ChangeSet{}, err
	}
	return changeSet, nil
}

// completeAttemptVerification records the verification boundary for either
// kind of attempt. Writer attempts verify protected paths before capture;
// read-only attempts retain their full worktree invariant.
func completeAttemptVerification(
	attempt *workspace.AttemptWorkspace,
	writer bool,
	authority *runstore.Driver,
	appendEvent func(runstate.EventType, any, string) (faultpoint.DurabilityReceipt, error),
	classifyFailure func(successor.FailureCase) (runstate.Disposition, error),
) (verificationFailed bool, err error) {
	if writer {
		err = attempt.VerifyProtectedPaths()
		if err == nil {
			_, err = CaptureAndRecordChangeSet(attempt, authority)
		}
		if err == nil {
			_, err = appendEvent(
				runstate.EventVerificationPassed,
				map[string]any{},
				"attempt.verification",
			)
		}
	} else {
		_, err = attempt.VerifyReadOnlyAndRecord()
	}
	if err == nil {
		return false, nil
	}

	var verification *workspace.VerificationError
	if !errors.As(err, &verification) {
		return false, err
	}
	disposition, classifyErr := classifyFailure(
		successor.FailureCase{AttemptKind: successor.KindGrantDenied},
	)
	if classifyErr != nil {
		return false, classifyErr
	}
	if _, appendErr := appendEvent(runstate.EventAttemptFailed, map[string]any{
		"kind":        successor.KindGrantDenied,
		"reason":      verification.Reason,
		"disposition": dispositionPayload(disposition),
	}, "attempt.failed"); appendErr != nil {
		return false, appendErr
	}
	return true, err
}
