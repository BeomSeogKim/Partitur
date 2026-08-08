package runstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

var ErrApplicationNotAllowed = errors.New("application is not allowed")

type ApplicationOutcome string

const (
	ApplicationOutcomeApplied          ApplicationOutcome = "applied"
	ApplicationOutcomeAlreadyApplied   ApplicationOutcome = "already_applied"
	ApplicationOutcomeFailedClean      ApplicationOutcome = "failed_clean"
	ApplicationOutcomeRecoveryRequired ApplicationOutcome = "recovery_required"
)

type ApplicationResult struct {
	Outcome ApplicationOutcome
	Detail  string
}

// Apply materializes a succeeded run's candidate in the user's checkout. The
// state lock deliberately covers both the durable application seam and Git
// work: another command cannot change the judgment between them.
func (store *Store) Apply(ctx context.Context, runID runstate.RunID, recoverOnly bool) (ApplicationResult, error) {
	var result ApplicationResult
	err := store.Mutate(runID, "", func(transaction *Txn) error {
		initial, err := store.LoadInitialScore(runID)
		if err != nil {
			return err
		}
		state, err := transaction.project(movementSeed(initial))
		if err != nil {
			return err
		}
		input, err := store.LoadRunInput(runID)
		if err != nil {
			return err
		}
		if recoverOnly {
			result, err = store.recoverApplication(ctx, transaction, &state)
			return err
		}
		result, err = store.applyCandidate(ctx, transaction, &state, input.Score)
		return err
	})
	return result, err
}

func (store *Store) applyCandidate(
	ctx context.Context,
	transaction *Txn,
	state *runstate.State,
	compiled *score.Score,
) (ApplicationResult, error) {
	if state.Application.State == runstate.ApplicationApplied {
		return ApplicationResult{Outcome: ApplicationOutcomeAlreadyApplied}, nil
	}
	if state.Application.State != runstate.ApplicationNotApplied && state.Application.State != runstate.ApplicationFailedClean {
		return ApplicationResult{}, fmt.Errorf("%w: normal apply is refused from %s", ErrApplicationNotAllowed, state.Application.State)
	}
	if err := store.applicationPreconditions(ctx, transaction.runID, *state, compiled); err != nil {
		return ApplicationResult{}, err
	}
	candidate := *state.ApplicationCandidate
	beforeTree, err := applicationWorktreeTree(ctx, store.root)
	if err != nil {
		return ApplicationResult{}, err
	}
	if beforeTree != candidate.BaseTree {
		return ApplicationResult{}, fmt.Errorf("%w: checkout tree %q does not match candidate base %q", ErrApplicationNotAllowed, beforeTree, candidate.BaseTree)
	}
	touched, err := applicationTouchedPaths(ctx, store.root, candidate.BaseTree, candidate.ResultTree)
	if err != nil {
		return ApplicationResult{}, err
	}
	versions, err := applicationIdentityVersions(candidate)
	if err != nil {
		return ApplicationResult{}, err
	}
	txnID, err := applicationTransactionID()
	if err != nil {
		return ApplicationResult{}, err
	}
	if err := appendApplicationEvent(transaction, state, runstate.EventApplyStarted, map[string]any{
		"txn_id": txnID, "candidate_id": candidate.ID, "before_tree": beforeTree, "result_tree": candidate.ResultTree,
		"touched_paths":     stringsToAny(touched),
		"recovery":          map[string]any{"base_tree": candidate.BaseTree, "result_tree": candidate.ResultTree},
		"identity_versions": versions,
	}); err != nil {
		return ApplicationResult{}, err
	}

	patch, patchErr := applicationPatch(ctx, store.root, candidate.BaseTree, candidate.ResultTree)
	if patchErr == nil {
		patchErr = applicationGit(ctx, store.root, nil, patch, "apply", "--check", "--whitespace=nowarn")
	}
	if patchErr == nil {
		patchErr = applicationGit(ctx, store.root, nil, patch, "apply", "--whitespace=nowarn")
	}
	if patchErr == nil {
		afterTree, treeErr := applicationWorktreeTree(ctx, store.root)
		if treeErr == nil && afterTree == candidate.ResultTree {
			err := appendApplicationEvent(transaction, state, runstate.EventApplyCompleted, map[string]any{
				"txn_id": txnID, "candidate_id": candidate.ID, "result_tree": candidate.ResultTree, "identity_versions": versions,
			})
			return ApplicationResult{Outcome: ApplicationOutcomeApplied}, err
		}
		if treeErr != nil {
			patchErr = treeErr
		} else {
			patchErr = fmt.Errorf("applied checkout tree %q does not match candidate result %q", afterTree, candidate.ResultTree)
		}
	}
	if err := applicationRestore(ctx, store.root, candidate.BaseTree, candidate.ResultTree); err != nil {
		detail := "apply failed: " + patchErr.Error() + "; rollback failed: " + err.Error()
		if appendErr := store.applicationRecoveryRequired(transaction, state, candidate, txnID, versions, detail); appendErr != nil {
			return ApplicationResult{}, appendErr
		}
		return ApplicationResult{Outcome: ApplicationOutcomeRecoveryRequired, Detail: detail}, nil
	}
	restored, err := applicationWorktreeTree(ctx, store.root)
	if err != nil {
		detail := "rollback tree unverifiable: " + err.Error()
		if appendErr := store.applicationRecoveryRequired(transaction, state, candidate, txnID, versions, detail); appendErr != nil {
			return ApplicationResult{}, appendErr
		}
		return ApplicationResult{Outcome: ApplicationOutcomeRecoveryRequired, Detail: detail}, nil
	}
	if restored != candidate.BaseTree {
		detail := fmt.Sprintf("rollback tree %q does not match base %q", restored, candidate.BaseTree)
		if appendErr := store.applicationRecoveryRequired(transaction, state, candidate, txnID, versions, detail); appendErr != nil {
			return ApplicationResult{}, appendErr
		}
		return ApplicationResult{Outcome: ApplicationOutcomeRecoveryRequired, Detail: detail}, nil
	}
	err = appendApplicationEvent(transaction, state, runstate.EventApplyFailed, map[string]any{
		"txn_id": txnID, "candidate_id": candidate.ID, "failure_detail": patchErr.Error(), "rollback_verified": true, "identity_versions": versions,
	})
	return ApplicationResult{Outcome: ApplicationOutcomeFailedClean, Detail: patchErr.Error()}, err
}

func (store *Store) recoverApplication(ctx context.Context, transaction *Txn, state *runstate.State) (ApplicationResult, error) {
	if state.Application.State != runstate.ApplicationApplying && state.Application.State != runstate.ApplicationRecoveryRequired {
		return ApplicationResult{}, fmt.Errorf("%w: --recover is refused from %s", ErrApplicationNotAllowed, state.Application.State)
	}
	if state.ApplicationCandidate == nil || state.Application.CandidateID != state.ApplicationCandidate.ID {
		return ApplicationResult{}, fmt.Errorf("%w: application candidate is unavailable", ErrApplicationNotAllowed)
	}
	candidate := *state.ApplicationCandidate
	versions, err := applicationIdentityVersions(candidate)
	if err != nil {
		return ApplicationResult{}, err
	}
	tree, treeErr := applicationWorktreeTree(ctx, store.root)
	if state.Application.State == runstate.ApplicationApplying {
		detail := "interrupted application requires checkout inspection"
		payload := map[string]any{"txn_id": state.Application.TransactionID, "candidate_id": candidate.ID, "failure_detail": detail, "identity_versions": versions}
		if treeErr == nil {
			payload["observed_tree"] = tree
		} else {
			payload["failure_detail"] = detail + ": " + treeErr.Error()
		}
		if err := appendApplicationEvent(transaction, state, runstate.EventApplyRecoveryRequired, payload); err != nil {
			return ApplicationResult{}, err
		}
	}
	if treeErr != nil {
		return ApplicationResult{Outcome: ApplicationOutcomeRecoveryRequired, Detail: treeErr.Error()}, nil
	}
	switch tree {
	case candidate.BaseTree:
		err := appendApplicationEvent(transaction, state, runstate.EventApplyRecoveryResolved, map[string]any{
			"txn_id": state.Application.TransactionID, "candidate_id": candidate.ID, "outcome": "rolled_back", "identity_versions": versions,
		})
		return ApplicationResult{Outcome: ApplicationOutcomeFailedClean}, err
	case candidate.ResultTree:
		err := appendApplicationEvent(transaction, state, runstate.EventApplyCompleted, map[string]any{
			"txn_id": state.Application.TransactionID, "candidate_id": candidate.ID, "result_tree": candidate.ResultTree, "identity_versions": versions,
		})
		return ApplicationResult{Outcome: ApplicationOutcomeApplied}, err
	default:
		return ApplicationResult{Outcome: ApplicationOutcomeRecoveryRequired, Detail: fmt.Sprintf("checkout tree %q matches neither candidate tree", tree)}, nil
	}
}

func (store *Store) applicationRecoveryRequired(transaction *Txn, state *runstate.State, candidate runstate.ApplicationCandidate, txnID string, versions map[string]any, detail string) error {
	return appendApplicationEvent(transaction, state, runstate.EventApplyRecoveryRequired, map[string]any{
		"txn_id": txnID, "candidate_id": candidate.ID, "failure_detail": detail, "identity_versions": versions,
	})
}

func (store *Store) applicationPreconditions(ctx context.Context, selected runstate.RunID, state runstate.State, compiled *score.Score) error {
	if state.Run != runstate.RunSucceeded || state.ApplicationCandidate == nil {
		return fmt.Errorf("%w: selected run is not succeeded with a candidate", ErrApplicationNotAllowed)
	}
	if compiled == nil || compiled.Revision() != state.ScoreHead.Revision || state.ApplicationCandidate.Revision != state.ScoreHead.Revision {
		return fmt.Errorf("%w: candidate or expectation is not bound to the current head", ErrApplicationNotAllowed)
	}
	ids, err := store.RunIDs()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		input, err := store.LoadRunInput(id)
		if err != nil {
			return err
		}
		if id != selected && !input.Projection.State.Run.Terminal() {
			return fmt.Errorf("%w: active run %q exists", ErrApplicationNotAllowed, id)
		}
	}
	if err := applicationJudgment(state, compiled); err != nil {
		return err
	}
	if err := applicationClean(ctx, store.root); err != nil {
		return err
	}
	return nil
}

func applicationJudgment(state runstate.State, compiled *score.Score) error {
	candidate := state.ApplicationCandidate
	if candidate == nil {
		return fmt.Errorf("%w: candidate is missing", ErrApplicationNotAllowed)
	}
	execution := compiled.Execution()
	if execution.GateWaived {
		if execution.WaiverReason == "" {
			return fmt.Errorf("%w: waiver is not recorded", ErrApplicationNotAllowed)
		}
		return nil
	}
	if execution.FinalMovementID == "" {
		return fmt.Errorf("%w: final movement is missing", ErrApplicationNotAllowed)
	}
	marks := applicationMarks(state, compiled, runstate.MovementID(execution.FinalMovementID), candidate.ResultTree)
	for _, required := range execution.ApplyGateRequire {
		if !marks[strings.ToUpper(required)] {
			return fmt.Errorf("%w: required %s evidence is absent", ErrApplicationNotAllowed, required)
		}
	}
	for _, predicate := range execution.ApplyGatePredicates {
		switch predicate {
		case "no_unresolved_blocking_findings":
			if !marks["REVIEW_CLEAN_OR_OVERRIDDEN"] {
				return fmt.Errorf("%w: %s is not satisfied", ErrApplicationNotAllowed, predicate)
			}
		case "no_blocking_findings":
			if !marks["REVIEW_CLEAN"] {
				return fmt.Errorf("%w: %s is not satisfied", ErrApplicationNotAllowed, predicate)
			}
		default:
			return fmt.Errorf("%w: unknown apply predicate %q", ErrApplicationNotAllowed, predicate)
		}
	}
	return nil
}

func applicationMarks(state runstate.State, compiled *score.Score, final runstate.MovementID, subjectTree string) map[string]bool {
	marks := map[string]bool{}
	view, ok := applicationMovement(compiled, final)
	if !ok {
		return marks
	}
	for attemptID, attempt := range state.Attempts {
		if attempt.MovementID != final || attempt.ScoreRevision != state.ScoreHead.Revision || attempt.State != runstate.AttemptCompleted {
			continue
		}
		acceptance, ok := state.Acceptances[attemptID]
		if !ok || !acceptance.EvaluationCompleted || acceptance.SubjectTree != subjectTree || !applicationCriteriaPassed(acceptance) {
			continue
		}
		if len(view.Acceptance.ArtifactCriteria)+len(view.Acceptance.RunCriteria) != 0 {
			marks["VERIFIED"] = true
		}
		if len(view.Acceptance.ReviewCriteria) == 1 {
			marks["REVIEWED"] = true
			if acceptance.ReviewOutcome == "CLEAN" {
				marks["REVIEW_CLEAN"] = true
				marks["REVIEW_CLEAN_OR_OVERRIDDEN"] = true
			}
			if acceptance.ReviewOutcome == "OVERRIDDEN" {
				marks["REVIEW_CLEAN_OR_OVERRIDDEN"] = true
			}
		}
		resolution := state.ResolvedHumanGates[attemptID]
		if resolution.Disposition == "approved" && resolution.ScoreRevision == state.ScoreHead.Revision && resolution.Scope.SubjectTree == subjectTree {
			marks["APPROVED"] = true
		}
	}
	return marks
}

func applicationMovement(compiled *score.Score, id runstate.MovementID) (score.MovementView, bool) {
	for _, movement := range compiled.Movements() {
		if movement.ID == string(id) {
			return movement, true
		}
	}
	return score.MovementView{}, false
}

func applicationCriteriaPassed(acceptance runstate.Acceptance) bool {
	for _, id := range acceptance.PlannedCriterionIDs {
		record, ok := acceptance.Criteria[id]
		if !ok || !record.Completed || record.Outcome != "PASS" {
			return false
		}
	}
	return true
}

func appendApplicationEvent(transaction *Txn, state *runstate.State, eventType runstate.EventType, payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	event := runstate.Event{RunID: transaction.runID, ScoreRevision: state.ScoreHead.Revision, Type: eventType, Payload: encoded}
	next, err := runstate.Apply(*state, event)
	if err != nil {
		return err
	}
	if _, err := transaction.At(faultpoint.ReceiptAddress(eventType)).Append(event); err != nil {
		return err
	}
	*state = next
	return nil
}

func applicationIdentityVersions(candidate runstate.ApplicationCandidate) (map[string]any, error) {
	var versions map[string]any
	if err := json.Unmarshal(candidate.IdentityVersions, &versions); err != nil {
		return nil, fmt.Errorf("candidate identity versions: %w", err)
	}
	if versions == nil {
		return nil, errors.New("candidate identity versions are missing")
	}
	return versions, nil
}

func applicationTransactionID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "apply-" + hex.EncodeToString(bytes[:]), nil
}

func applicationClean(ctx context.Context, root string) error {
	status, err := applicationGitOutput(ctx, root, nil, nil, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return err
	}
	for _, line := range bytes.Split(status, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		path := string(line[3:])
		if path == ".partitur/" || strings.HasPrefix(path, ".partitur/") {
			continue
		}
		return fmt.Errorf("%w: checkout is not clean", ErrApplicationNotAllowed)
	}
	return nil
}

func applicationWorktreeTree(ctx context.Context, root string) (string, error) {
	indexPath, err := applicationGitOutput(ctx, root, nil, nil, "rev-parse", "--git-path", "index")
	if err != nil {
		return "", err
	}
	indexDirectory := filepath.Dir(strings.TrimSpace(string(indexPath)))
	if !filepath.IsAbs(indexDirectory) {
		indexDirectory = filepath.Join(root, indexDirectory)
	}
	index, err := os.CreateTemp(indexDirectory, "partitur-apply-index-")
	if err != nil {
		return "", err
	}
	path := index.Name()
	if err := index.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	defer os.Remove(path)
	environment := []string{"GIT_INDEX_FILE=" + path}
	if err := applicationGit(ctx, root, environment, nil, "read-tree", "HEAD"); err != nil {
		return "", err
	}
	if err := applicationGit(ctx, root, environment, nil, "add", "-A"); err != nil {
		return "", err
	}
	if err := applicationGit(ctx, root, environment, nil, "rm", "--cached", "--ignore-unmatch", "-r", ".partitur"); err != nil {
		return "", err
	}
	tree, err := applicationGitOutput(ctx, root, environment, nil, "write-tree")
	if err != nil {
		return "", err
	}
	format, err := applicationGitOutput(ctx, root, environment, nil, "rev-parse", "--show-object-format")
	if err != nil {
		return "", err
	}
	return "git-" + strings.TrimSpace(string(format)) + ":" + strings.TrimSpace(string(tree)), nil
}

func applicationTouchedPaths(ctx context.Context, root, baseTree, resultTree string) ([]string, error) {
	base, err := applicationObjectID(baseTree)
	if err != nil {
		return nil, err
	}
	result, err := applicationObjectID(resultTree)
	if err != nil {
		return nil, err
	}
	output, err := applicationGitOutput(ctx, root, nil, nil, "diff", "--name-only", "-z", base, result)
	if err != nil {
		return nil, err
	}
	paths := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
	if len(paths) == 1 && paths[0] == "" {
		return nil, nil
	}
	sort.Strings(paths)
	return paths, nil
}

func applicationPatch(ctx context.Context, root, baseTree, resultTree string) ([]byte, error) {
	base, err := applicationObjectID(baseTree)
	if err != nil {
		return nil, err
	}
	result, err := applicationObjectID(resultTree)
	if err != nil {
		return nil, err
	}
	return applicationGitOutput(ctx, root, nil, nil, "diff", "--binary", base, result)
}

func applicationRestore(ctx context.Context, root, baseTree, resultTree string) error {
	patch, err := applicationPatch(ctx, root, baseTree, resultTree)
	if err != nil {
		return err
	}
	if err := applicationGit(ctx, root, nil, patch, "apply", "--reverse", "--check", "--whitespace=nowarn"); err != nil {
		return err
	}
	return applicationGit(ctx, root, nil, patch, "apply", "--reverse", "--whitespace=nowarn")
}

func applicationObjectID(tree string) (string, error) {
	prefix, object, ok := strings.Cut(tree, ":")
	if !ok || object == "" || (prefix != "git-sha1" && prefix != "git-sha256") {
		return "", fmt.Errorf("invalid qualified Git tree %q", tree)
	}
	return object, nil
}

func applicationGit(ctx context.Context, root string, environment []string, stdin []byte, arguments ...string) error {
	_, err := applicationGitOutput(ctx, root, environment, stdin, arguments...)
	return err
}

func applicationGitOutput(ctx context.Context, root string, environment []string, stdin []byte, arguments ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	arguments = append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fileMode=true", "-C", root}, arguments...)
	command := exec.CommandContext(commandCtx, "git", arguments...)
	command.Env = applicationGitEnvironment(environment)
	command.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		if commandCtx.Err() != nil {
			return nil, commandCtx.Err()
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(arguments[6:], " "), err, detail)
	}
	return stdout.Bytes(), nil
}

func applicationGitEnvironment(extra []string) []string {
	values := os.Environ()
	for _, name := range []string{"GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_GLOBAL", "GIT_TERMINAL_PROMPT", "LANG", "LC_ALL", "GIT_INDEX_FILE"} {
		prefix := name + "="
		values = slices.DeleteFunc(values, func(value string) bool { return strings.HasPrefix(value, prefix) })
	}
	values = append(values,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "LANG=C", "LC_ALL=C",
	)
	return append(values, extra...)
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}
