// Package recoveryobs gathers the external, read-only half of resume input.
// It deliberately sits beside recovery rather than inside it: recovery remains
// a pure planner with no filesystem or process imports.
package recoveryobs

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/launch"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

// Collect gathers only current external facts for one already replayed run.
// It never takes the run-store mutation lock, writes a file, signals a
// process, or changes a Git reference.
func Collect(store *runstore.Store, runID runstate.RunID, projection recovery.Projection) (recovery.Observations, error) {
	if store == nil || store.RepositoryRoot() == "" {
		return recovery.Observations{}, errors.New("recovery observations: nil store")
	}
	root := store.RepositoryRoot()
	observations := recovery.Observations{
		Handoff:        recovery.HandoffSafe,
		AdapterSweep:   recovery.SweepSafe,
		Worktree:       recovery.WorktreePresent,
		CriterionSweep: recovery.SweepSafe,
	}
	lease, present, err := store.ReadLease(runID)
	if err != nil {
		observations.Lease = recovery.LeaseObservation{Exists: true, Readable: false, Owner: recovery.OwnerUnverifiable}
	} else if present {
		observations.Lease = recovery.LeaseObservation{Exists: true, Readable: true, Epoch: lease.Epoch, Owner: ownerState(lease.MatchOwner())}
	}
	observations.RootSnapshotDivergence, err = rootSnapshotDivergence(filepath.Join(root, "partitur.yaml"), projection.State.ScoreHead)
	if err != nil {
		return recovery.Observations{}, err
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		return recovery.Observations{}, err
	}
	observations.References = collectReferences(root, runID, projection.State, journal.Events)
	if projection.State.PendingPrepare != nil {
		observations.Prepare = collectPrepare(root, runID, *projection.State.PendingPrepare)
	}
	if attempt := projection.CurrentHeadAttempt; attempt != nil {
		attemptRoot := filepath.Join(root, ".partitur", "work", string(runID), string(attempt.AttemptID))
		if attempt.State == runstate.AttemptStarting {
			observations.Handoff = observePendingHandoff(attemptRoot)
		}
		if launch, ok := projection.State.AdapterLaunches[attempt.AttemptID]; ok {
			observations.AdapterSweep = observeSession(launch.Process)
		}
		if attempt.State == runstate.AttemptVerifying {
			observations.Worktree, err = observeWorktree(filepath.Join(attemptRoot, "worktree"))
			if err != nil {
				return recovery.Observations{}, err
			}
		}
		if acceptance, ok := projection.State.Acceptances[attempt.AttemptID]; ok && acceptance.Started {
			observations.CriterionSweep = observeCriteria(projection.State, attempt.AttemptID, acceptance)
			matched, verifyErr := workspace.VerifyRecoverySubject(filepath.Join(attemptRoot, "worktree"), acceptance.SubjectTree)
			switch {
			case verifyErr != nil:
				observations.AcceptanceSubject = recovery.SubjectUnverified
			case matched:
				observations.AcceptanceSubject = recovery.SubjectMatched
			default:
				observations.AcceptanceSubject = recovery.SubjectMismatched
			}
			observations.UnjournaledLaunch = observeUnjournaledLaunch(attemptRoot, projection.State, attempt.AttemptID)
		}
	}
	return observations, nil
}

func ownerState(match procid.MatchResult) recovery.OwnerState {
	switch match.Status {
	case procid.MatchingAndLive:
		return recovery.OwnerLive
	case procid.GoneOrReused:
		return recovery.OwnerDead
	default:
		return recovery.OwnerUnverifiable
	}
}

// A missing or malformed root file makes no valid claim about a score
// revision, so it cannot satisfy the §1 divergence predicate. An unreadable
// existing file is different: the collector returns an error rather than
// manufacturing false for a comparison it could not make.
func rootSnapshotDivergence(path string, snapshot runstate.ScoreHead) (bool, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read root score: %w", err)
	}
	root, diagnostics := score.Compile(contents)
	if len(diagnostics) != 0 {
		return false, nil
	}
	if root.Revision() != snapshot.Revision {
		return false, nil
	}
	hash, err := root.Hash()
	if err != nil {
		return false, fmt.Errorf("hash root score: %w", err)
	}
	return runstate.Hash(hash) != snapshot.SemanticHash, nil
}

func collectReferences(root string, runID runstate.RunID, state runstate.State, events []runstate.Event) []recovery.ReferenceObservation {
	references := make([]recovery.ReferenceObservation, 0, len(state.Artifacts)+len(state.ChangeSets)+len(events))
	for _, artifact := range state.Artifacts {
		path := filepath.Join(root, ".partitur", "runs", string(runID), "artifacts", artifact.LogicalOutputID, string(artifact.AttemptID))
		references = append(references, recovery.ReferenceObservation{Kind: recovery.ReferenceArtifact, Present: fileMatches(path, artifact.ContentHash)})
	}
	for _, changeSet := range state.ChangeSets {
		references = append(references, recovery.ReferenceObservation{Kind: recovery.ReferenceChangeSetRef, Present: refMatches(root, changeSet.Ref, changeSet.Commit)})
	}
	for _, event := range events {
		payload := eventPayload(event)
		switch event.Type {
		case runstate.EventRunStarted:
			references = append(references,
				recovery.ReferenceObservation{Kind: recovery.ReferenceSnapshot, Present: scoreMatches(filepath.Join(root, ".partitur", "runs", string(runID), "scores", fmt.Sprintf("revision-%d.yaml", event.ScoreRevision)), runstate.Hash(stringValue(payload, "score_file_hash")), runstate.Hash(stringValue(payload, "score_hash")))},
				recovery.ReferenceObservation{Kind: recovery.ReferenceResolvedCast, Present: resolvedCastMatches(filepath.Join(root, ".partitur", "runs", string(runID), "resolved-cast.yaml"), stringValue(payload, "resolved_cast_hash"))},
			)
		case runstate.EventAmendmentApproved:
			if revision, ok := uintValue(payload, "new_revision"); ok {
				references = append(references, recovery.ReferenceObservation{Kind: recovery.ReferenceSnapshot, Present: scoreMatches(filepath.Join(root, ".partitur", "runs", string(runID), "scores", fmt.Sprintf("revision-%d.yaml", revision)), runstate.Hash(stringValue(payload, "new_snapshot_file_hash")), runstate.Hash(stringValue(payload, "new_snapshot_hash")))})
			}
		case runstate.EventAmendmentRoutedHuman:
			proposalID := stringValue(payload, "proposal_id")
			references = append(references, recovery.ReferenceObservation{Kind: recovery.ReferenceProposalRecord, Present: fileMatches(filepath.Join(root, ".partitur", "runs", string(runID), "proposals", proposalID+".json"), runstate.Hash(stringValue(payload, "proposal_record_hash")))})
		}
	}
	sort.Slice(references, func(i, j int) bool { return references[i].Kind < references[j].Kind })
	return references
}

func collectPrepare(root string, runID runstate.RunID, prepare runstate.PendingPrepare) recovery.PrepareObservation {
	runRoot := filepath.Join(root, ".partitur", "runs", string(runID))
	planPath := filepath.Join(runRoot, "prepares", string(prepare.ID)+".json")
	return recovery.PrepareObservation{
		PlanPresent:     preparePlanMatches(planPath, prepare),
		SnapshotPresent: scoreMatches(filepath.Join(runRoot, "scores", fmt.Sprintf("revision-%d.yaml", prepare.NewHead.Revision)), prepare.NewHead.FileHash, prepare.NewHead.SemanticHash),
	}
}

type preparePlan struct {
	ProposalID           runstate.ProposalID  `json:"proposal_id"`
	BaseRevision         uint64               `json:"base_revision"`
	BaseHash             runstate.Hash        `json:"base_hash"`
	NewRevision          uint64               `json:"new_revision"`
	NewSnapshotHash      runstate.Hash        `json:"new_snapshot_hash"`
	NewSnapshotFileHash  runstate.Hash        `json:"new_snapshot_file_hash"`
	SupersededAttemptIDs []runstate.AttemptID `json:"superseded_attempt_ids"`
	Mode                 string               `json:"mode"`
}

func preparePlanMatches(path string, prepare runstate.PendingPrepare) bool {
	contents, err := os.ReadFile(path)
	if err != nil || !fileMatches(path, prepare.PlanRecordHash) {
		return false
	}
	var plan preparePlan
	if json.Unmarshal(contents, &plan) != nil {
		return false
	}
	return plan.ProposalID == prepare.ProposalID &&
		plan.BaseRevision == prepare.BaseHead.Revision &&
		plan.BaseHash == prepare.BaseHead.SemanticHash &&
		plan.NewRevision == prepare.NewHead.Revision &&
		plan.NewSnapshotHash == prepare.NewHead.SemanticHash &&
		plan.NewSnapshotFileHash == prepare.NewHead.FileHash &&
		plan.Mode == prepare.Mode &&
		slices.Equal(plan.SupersededAttemptIDs, prepare.TargetAttemptIDs)
}

func fileMatches(path string, want runstate.Hash) bool {
	contents, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(contents)
	return runstate.Hash(fmt.Sprintf("sha256:%x", digest)) == want
}

func scoreMatches(path string, fileHash, semanticHash runstate.Hash) bool {
	contents, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(contents)
	if runstate.Hash(fmt.Sprintf("sha256:%x", digest)) != fileHash {
		return false
	}
	compiled, diagnostics := score.Compile(contents)
	if len(diagnostics) != 0 {
		return false
	}
	hash, err := compiled.Hash()
	return err == nil && runstate.Hash(hash) == semanticHash
}

func resolvedCastMatches(path, semanticHash string) bool {
	contents, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	resolved, diagnostics := cast.Resolve([]cast.Layer{{Origin: "run-owned resolved cast", Data: contents}})
	if len(diagnostics) != 0 {
		return false
	}
	hash, err := resolved.Hash()
	return err == nil && hash == semanticHash
}

func eventPayload(event runstate.Event) map[string]any {
	var payload map[string]any
	_ = json.Unmarshal(event.Payload, &payload)
	return payload
}

func stringValue(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func uintValue(payload map[string]any, key string) (uint64, bool) {
	value, ok := payload[key].(float64)
	return uint64(value), ok && value >= 0 && value == float64(uint64(value))
}

func refMatches(root, ref, want string) bool {
	if ref == "" || want == "" {
		return false
	}
	command := exec.Command("git", "-C", root, "rev-parse", "--verify", ref+"^{commit}")
	output, err := command.Output()
	if err != nil {
		return false
	}
	return string(output[:len(output)-1]) == unqualifiedGitObject(want)
}

func unqualifiedGitObject(value string) string {
	for _, prefix := range []string{"git-sha1:", "git-sha256:"} {
		if len(value) >= len(prefix) && value[:len(prefix)] == prefix {
			return value[len(prefix):]
		}
	}
	return value
}

func observeSession(identity runstate.ProcessIdentity) recovery.SweepState {
	_, err := adapter.SessionEmpty(identity)
	if err != nil {
		return recovery.SweepUnverifiable
	}
	// Both an empty session and a live, inspectable session are successful
	// observations. The executor owns sweeping a live session.
	return recovery.SweepSafe
}

func observeWorktree(path string) (recovery.WorktreeState, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return recovery.WorktreeMissing, nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect recovery worktree: %w", err)
	}
	if !info.IsDir() {
		return recovery.WorktreeMissing, nil
	}
	return recovery.WorktreePresent, nil
}

func observeCriteria(state runstate.State, attemptID runstate.AttemptID, acceptance runstate.Acceptance) recovery.SweepState {
	for criterionID, record := range acceptance.Criteria {
		if !record.Started || record.Completed {
			continue
		}
		launch, ok := state.CriterionLaunches[runstate.CriterionLaunchKey{AttemptID: attemptID, CriterionID: criterionID}]
		if !ok {
			return recovery.SweepUnverifiable
		}
		if spawned, ok := launch.(runstate.SpawnedCriterionLaunch); ok && observeSession(spawned.Process) != recovery.SweepSafe {
			return recovery.SweepUnverifiable
		}
	}
	return recovery.SweepSafe
}

// Handoff and unjournaled-launch stabilization are structurally present in
// this collector. The current execution slice has no recovery-readable launch
// directory identity, so a discovered handoff is deliberately fail-closed.
func observePendingHandoff(attemptRoot string) recovery.HandoffState {
	entries, err := os.ReadDir(attemptRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return recovery.HandoffSafe
	}
	if err != nil {
		return recovery.HandoffUnverifiable
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != "worktree" && entry.Name() != "output" {
			return stabilizeHandoff(filepath.Join(attemptRoot, entry.Name()))
		}
	}
	return recovery.HandoffSafe
}

func stabilizeHandoff(launchDir string) recovery.HandoffState {
	return stabilizeHandoffUntil(launchDir, time.Now().Add(30*time.Second))
}

func stabilizeHandoffUntil(launchDir string, deadline time.Time) recovery.HandoffState {
	for {
		observation, err := launch.ObserveHandoff(launchDir)
		if err != nil {
			return recovery.HandoffUnverifiable
		}
		if observation.HasIdentity {
			if observeSession(observation.Identity) == recovery.SweepSafe {
				return recovery.HandoffSafe
			}
			return recovery.HandoffSweepFailed
		}
		if observation.MarkerFree {
			return recovery.HandoffSafe
		}
		if !observation.MarkerHeld || !time.Now().Before(deadline) {
			return recovery.HandoffUnverifiable
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func observeUnjournaledLaunch(attemptRoot string, state runstate.State, attemptID runstate.AttemptID) recovery.UnjournaledLaunchState {
	entries, err := os.ReadDir(attemptRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return recovery.UnjournaledLaunchAbsent
	}
	if err != nil {
		return recovery.UnjournaledLaunchHandoffUnverifiable
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != "worktree" && entry.Name() != "output" {
			launchDir := filepath.Join(attemptRoot, entry.Name())
			observation, observeErr := launch.ObserveHandoff(launchDir)
			if observeErr != nil {
				return recovery.UnjournaledLaunchHandoffUnverifiable
			}
			if adapterLaunch, ok := state.AdapterLaunches[attemptID]; ok &&
				observation.HasIdentity && reflect.DeepEqual(observation.Identity, adapterLaunch.Process) {
				continue
			}
			return stabilizeUnjournaledLaunch(launchDir)
		}
	}
	return recovery.UnjournaledLaunchAbsent
}

func stabilizeUnjournaledLaunch(launchDir string) recovery.UnjournaledLaunchState {
	deadline := time.Now().Add(30 * time.Second)
	for {
		observation, err := launch.ObserveHandoff(launchDir)
		if err != nil {
			return recovery.UnjournaledLaunchHandoffUnverifiable
		}
		if observation.HasIdentity {
			if observeSession(observation.Identity) == recovery.SweepSafe {
				return recovery.UnjournaledLaunchSessionEmpty
			}
			return recovery.UnjournaledLaunchSweepUnverifiable
		}
		if observation.MarkerFree {
			return recovery.UnjournaledLaunchMarkerFree
		}
		if !observation.MarkerHeld || !time.Now().Before(deadline) {
			return recovery.UnjournaledLaunchHandoffUnverifiable
		}
		time.Sleep(10 * time.Millisecond)
	}
}
