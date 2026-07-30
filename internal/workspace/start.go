package workspace

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/validate"
)

const (
	receiptScoreSnapshot = faultpoint.ReceiptAddress("run.start.score_snapshot")
	receiptResolvedCast  = faultpoint.ReceiptAddress("run.start.resolved_cast")
	receiptBaseRef       = faultpoint.ReceiptAddress("run.start.base_ref")
	receiptRunStarted    = faultpoint.ReceiptAddress("run.start.event")
	receiptCandidateRef  = faultpoint.ReceiptAddress("candidate.identity.ref")
	receiptCandidate     = faultpoint.ReceiptAddress("candidate.identity.event")
)

type startDependencies struct {
	git   gitCommand
	probe faultpoint.Probe
	newID func() (string, error)
}

type refExistingValuePolicy uint8

const (
	refExistingMustMatchObject refExistingValuePolicy = iota
	// A changeset ref is attempt-scoped: before change_set.recorded names it,
	// recovery may move it to the surviving worktree checkpoint, but only by CAS.
	refExistingMayMove
	// A composed movement base is run- and movement-scoped. A replay may have
	// created another wrapper commit for the same tree, but it must never move
	// the pin to different content.
	refExistingMustMatchTree
)

// Start checks the Git preconditions and durably records the prepared run.
// It returns no RunID until run.started is fsynced.
func Start(
	preparation *validate.Preparation,
	probe faultpoint.Probe,
) (StartResult, error) {
	git, err := newSystemGit()
	if err != nil {
		return StartResult{}, err
	}
	return start(preparation, startDependencies{
		git:   git,
		probe: probe,
		newID: newUUIDv7,
	})
}

func start(
	preparation *validate.Preparation,
	dependencies startDependencies,
) (StartResult, error) {
	if preparation == nil || preparation.RepositoryRoot == "" ||
		preparation.Score == nil || preparation.Cast == nil ||
		len(preparation.ScoreSource()) == 0 {
		return StartResult{}, ErrIncompletePreparation
	}
	if dependencies.git == nil || dependencies.probe == nil ||
		dependencies.newID == nil {
		return StartResult{}, errors.New("workspace: incomplete dependencies")
	}
	facts, err := inspectRepository(
		dependencies.git,
		preparation.RepositoryRoot,
	)
	if err != nil {
		return StartResult{}, err
	}

	runIDValue, err := dependencies.newID()
	if err != nil {
		return StartResult{}, err
	}
	runID := runstate.RunID(runIDValue)
	if err := refuseRunCollision(
		dependencies.git,
		preparation.RepositoryRoot,
		runID,
	); err != nil {
		return StartResult{}, err
	}

	scoreSource := preparation.ScoreSource()
	scoreHash, err := preparation.Score.Hash()
	if err != nil {
		return StartResult{}, err
	}
	scoreFileHash := rawHash(scoreSource)
	resolvedCast, err := preparation.Cast.ProjectionBytes()
	if err != nil {
		return StartResult{}, err
	}
	resolvedCastHash, err := preparation.Cast.Hash()
	if err != nil {
		return StartResult{}, err
	}
	resolvedCastFileHash := rawHash(resolvedCast)
	versions, err := identityVersions(
		canonical.DomainScore,
		canonical.DomainResolvedCast,
	)
	if err != nil {
		return StartResult{}, err
	}

	store, err := runstore.New(preparation.RepositoryRoot, dependencies.probe)
	if err != nil {
		return StartResult{}, err
	}
	payload, err := json.Marshal(map[string]any{
		"base_commit":        facts.commitQualified,
		"base_tree":          facts.treeQualified,
		"score_hash":         scoreHash,
		"score_file_hash":    scoreFileHash,
		"resolved_cast_hash": resolvedCastHash,
		"identity_versions":  versions,
	})
	if err != nil {
		return StartResult{}, err
	}
	var receipt faultpoint.DurabilityReceipt
	err = store.Mutate(runID, "", func(transaction *runstore.Txn) error {
		if _, err := ensureRef(
			dependencies.git,
			preparation.RepositoryRoot,
			baseRef(runID),
			facts.commit,
			runID,
			receiptBaseRef,
			refExistingMustMatchObject,
		); err != nil {
			return err
		}
		scorePath := runstore.Path(filepath.ToSlash(filepath.Join(
			"scores",
			fmt.Sprintf("revision-%d.yaml", preparation.Score.Revision()),
		)))
		if _, err := transaction.At(receiptScoreSnapshot).PublishImmutable(
			scorePath,
			scoreSource,
			runstate.Hash(scoreFileHash),
		); err != nil {
			return err
		}
		if _, err := transaction.At(receiptResolvedCast).PublishImmutable(
			"resolved-cast.yaml",
			resolvedCast,
			runstate.Hash(resolvedCastFileHash),
		); err != nil {
			return err
		}
		receipt, err = transaction.At(receiptRunStarted).Append(runstate.Event{
			RunID:         runID,
			ScoreRevision: preparation.Score.Revision(),
			Type:          runstate.EventRunStarted,
			Payload:       payload,
		})
		return err
	})
	if err != nil {
		return StartResult{}, err
	}
	run := &Run{
		id:                runID,
		repositoryRoot:    preparation.RepositoryRoot,
		scoreRevision:     preparation.Score.Revision(),
		baseCommit:        facts.commit,
		baseTreeQualified: facts.treeQualified,
		movements:         preparation.Score.Movements(),
		store:             store,
		git:               dependencies.git,
		newID:             dependencies.newID,
	}
	return StartResult{RunID: runID, Receipt: receipt, Run: run}, nil
}

// RecordZeroWriterCandidate records the vacuous candidate without invoking a
// merge or manufacturing a composition environment.
func (run *Run) RecordZeroWriterCandidate() (Candidate, error) {
	if run == nil {
		return Candidate{}, errors.New("workspace: nil Run")
	}
	for _, movement := range run.movements {
		if hasGrant(movement.Grants, "repo_write") {
			return Candidate{}, fmt.Errorf(
				"%w: %s",
				ErrWriterMovement,
				movement.ID,
			)
		}
	}
	candidateValue := map[string]any{
		"base_tree":           run.baseTreeQualified,
		"result_tree":         run.baseTreeQualified,
		"ordered_change_sets": []any{},
	}
	candidateID, err := canonical.Hash(
		canonical.DomainCandidate,
		candidateValue,
	)
	if err != nil {
		return Candidate{}, err
	}
	compositionValue := map[string]any{
		"composition_mode":              "identity",
		"base_tree":                     run.baseTreeQualified,
		"contributors":                  []any{},
		"composition_algorithm_version": float64(canonical.CompositionAlgorithmVersion),
	}
	compositionHash, err := canonical.Hash(
		canonical.DomainCandidateComposition,
		compositionValue,
	)
	if err != nil {
		return Candidate{}, err
	}
	versions, err := identityVersions(
		canonical.DomainCandidate,
		canonical.DomainCandidateComposition,
	)
	if err != nil {
		return Candidate{}, err
	}
	versions["composition"] = canonical.CompositionAlgorithmVersion
	payload, err := json.Marshal(map[string]any{
		"candidate_id":                          candidateID,
		"base_tree":                             run.baseTreeQualified,
		"result_tree":                           run.baseTreeQualified,
		"ordered_change_sets":                   []any{},
		"contributors":                          []any{},
		"candidate_composition_dependency_hash": compositionHash,
		"identity_versions":                     versions,
	})
	if err != nil {
		return Candidate{}, err
	}
	var receipt faultpoint.DurabilityReceipt
	event := runstate.Event{
		RunID:         run.id,
		ScoreRevision: run.scoreRevision,
		Type:          runstate.EventApplicationCandidateRecorded,
		Payload:       payload,
	}
	err = run.mutate(func(
		transaction *runstore.Txn,
		state runstate.State,
		authorized bool,
	) error {
		if authorized {
			if _, err := runstate.Apply(state, event); err != nil {
				return err
			}
		}
		if _, err := ensureRef(
			run.git,
			run.repositoryRoot,
			candidateRef(run.id),
			run.baseCommit,
			run.id,
			receiptCandidateRef,
			refExistingMustMatchObject,
		); err != nil {
			return err
		}
		receipt, err = transaction.At(receiptCandidate).Append(event)
		return err
	})
	if err != nil {
		return Candidate{}, err
	}
	return Candidate{
		ID:                        candidateID,
		BaseTree:                  run.baseTreeQualified,
		ResultTree:                run.baseTreeQualified,
		CompositionDependencyHash: compositionHash,
		Receipt:                   receipt,
	}, nil
}

// BindDriver makes every subsequent workspace mutation recheck this driver's
// durable authority. A run can be bound only once.
func (run *Run) BindDriver(driver *runstore.Driver) error {
	if run == nil || driver == nil {
		return errors.New("workspace: incomplete driver binding")
	}
	if driver.RunID() != run.id {
		return errors.New("workspace: driver belongs to another run")
	}
	if run.driver != nil {
		return errors.New("workspace: driver already bound")
	}
	run.driver = driver
	return nil
}

func (run *Run) mutate(
	mutation func(*runstore.Txn, runstate.State, bool) error,
) error {
	if run.driver != nil {
		return run.driver.Mutate(func(
			transaction *runstore.Txn,
			state runstate.State,
		) error {
			return mutation(transaction, state, true)
		})
	}
	return run.store.Mutate(run.id, "", func(transaction *runstore.Txn) error {
		return mutation(transaction, runstate.State{}, false)
	})
}

func refuseRunCollision(
	git gitCommand,
	root string,
	runID runstate.RunID,
) error {
	runRoot := filepath.Join(root, ".partitur", "runs", string(runID))
	if _, err := os.Lstat(runRoot); err == nil {
		return ErrRunIDCollision
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	result, err := git.Run(
		root,
		nil,
		"show-ref",
		"--verify",
		"--quiet",
		baseRef(runID),
	)
	if err != nil {
		return err
	}
	switch result.exitCode {
	case 0:
		return ErrRunIDCollision
	case 1:
		return nil
	default:
		return gitFailure("inspect run ref", result)
	}
}

func ensureRef(
	git gitCommand,
	root, ref, object string,
	runID runstate.RunID,
	address faultpoint.ReceiptAddress,
	policy refExistingValuePolicy,
) (faultpoint.DurabilityReceipt, error) {
	result, err := git.Run(root, nil, "show-ref", "--verify", "--quiet", ref)
	if err != nil {
		return faultpoint.DurabilityReceipt{}, err
	}
	expected := ""
	switch result.exitCode {
	case 0:
		current, err := gitOutput(git, root, nil, "show-ref", "--hash", "--verify", ref)
		if err != nil {
			return faultpoint.DurabilityReceipt{}, err
		}
		expected = string(bytesTrimSpace(current))
		switch policy {
		case refExistingMustMatchObject:
			if expected != object {
				return faultpoint.DurabilityReceipt{}, ErrRunIDCollision
			}
		case refExistingMustMatchTree:
			currentTree, err := gitOutput(git, root, nil, "rev-parse", expected+"^{tree}")
			if err != nil {
				return faultpoint.DurabilityReceipt{}, err
			}
			targetTree, err := gitOutput(git, root, nil, "rev-parse", object+"^{tree}")
			if err != nil {
				return faultpoint.DurabilityReceipt{}, err
			}
			if string(bytesTrimSpace(currentTree)) != string(bytesTrimSpace(targetTree)) {
				return faultpoint.DurabilityReceipt{}, ErrRunIDCollision
			}
		}
	case 1:
	default:
		return faultpoint.DurabilityReceipt{}, gitFailure("inspect ref", result)
	}
	if _, err := gitOutput(
		git,
		root,
		nil,
		"-c", "core.fsync=reference",
		"-c", "core.fsyncMethod=fsync",
		"update-ref", ref, object, expected,
	); err != nil {
		return faultpoint.DurabilityReceipt{}, err
	}
	return faultpoint.DurabilityReceipt{
		Address: address,
		Mutation: faultpoint.Mutation{
			Kind:  faultpoint.GitRefCreation,
			RunID: string(runID),
			Path:  ref,
		},
	}, nil
}

func identityVersions(domains ...canonical.Domain) (map[string]any, error) {
	projections := make(map[string]any, len(domains))
	for _, domain := range domains {
		versions, err := canonical.CurrentVersions(domain)
		if err != nil {
			return nil, err
		}
		projections[string(domain)] = versions.Projection
	}
	return map[string]any{
		"canonical_encoding": canonical.CanonicalEncodingVersion,
		"projections":        projections,
	}, nil
}

func rawHash(contents []byte) string {
	digest := sha256.Sum256(contents)
	return fmt.Sprintf("sha256:%x", digest)
}

func hasGrant(grants []string, want string) bool {
	for _, grant := range grants {
		if grant == want {
			return true
		}
	}
	return false
}

func baseRef(runID runstate.RunID) string {
	return "refs/partitur/runs/" + string(runID) + "/base"
}

func candidateRef(runID runstate.RunID) string {
	return "refs/partitur/runs/" + string(runID) + "/candidate"
}

func bytesTrimSpace(value []byte) []byte {
	for len(value) != 0 &&
		(value[0] == ' ' || value[0] == '\n' || value[0] == '\r' || value[0] == '\t') {
		value = value[1:]
	}
	for len(value) != 0 {
		last := value[len(value)-1]
		if last != ' ' && last != '\n' && last != '\r' && last != '\t' {
			break
		}
		value = value[:len(value)-1]
	}
	return value
}
