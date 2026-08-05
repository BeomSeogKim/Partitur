package driver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/acceptance"
	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/cancellation"
	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/criterionexec"
	"github.com/BeomSeogKim/Partitur/internal/executiondep"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
	"github.com/BeomSeogKim/Partitur/internal/successor"
	"github.com/BeomSeogKim/Partitur/internal/validate"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

var (
	ErrAttemptSelectionInconsistent = errors.New("attempt selection is inconsistent with the pinned score or resolved cast.")
	ErrCapability                   = errors.New("capability_unavailable")
	ErrEnforcement                  = errors.New("enforcement_unavailable")
)

type dependencies struct {
	probe             faultpoint.Probe
	client            AdapterExecutor
	resolveTrampoline func() (string, error)
	now               func() time.Time
	newID             func() (string, error)
	workspaceStart    func(*validate.Preparation, faultpoint.Probe) (workspace.StartResult, error)
	// afterMovementFailed is a test-only interleaving hook. Production leaves it nil.
	afterMovementFailed func()
}

func defaultExecutionDependencies(probe faultpoint.Probe) ExecutionDependencies {
	return ExecutionDependencies{
		Probe:  probe,
		Client: adapter.NewClient(),
		ResolveTrampoline: func() (string, error) {
			path, err := exec.LookPath("partitur-trampoline")
			if err != nil {
				return "", fmt.Errorf("resolve partitur-trampoline: %w", err)
			}
			if filepath.IsAbs(path) {
				return path, nil
			}
			return filepath.Abs(path)
		},
		Now:   time.Now,
		NewID: workspace.NewID,
	}
}

// DefaultExecutionDependencies returns the production dependencies for one
// attempt execution episode.
func DefaultExecutionDependencies(probe faultpoint.Probe) ExecutionDependencies {
	return defaultExecutionDependencies(probe)
}

func executionDependenciesFrom(dependencies dependencies) ExecutionDependencies {
	return ExecutionDependencies{
		Probe:               dependencies.probe,
		Client:              dependencies.client,
		ResolveTrampoline:   dependencies.resolveTrampoline,
		Now:                 dependencies.now,
		NewID:               dependencies.newID,
		afterMovementFailed: dependencies.afterMovementFailed,
	}
}

// Run drives one prepared score through the durable success slice.
func Run(
	ctx context.Context,
	preparation *validate.Preparation,
	started StartedObserver,
) Result {
	execution := defaultExecutionDependencies(faultpoint.ProbeFromEnvironment())
	return run(ctx, preparation, started, dependencies{
		probe:             execution.Probe,
		client:            execution.Client,
		resolveTrampoline: execution.ResolveTrampoline,
		now:               execution.Now,
		newID:             execution.NewID,
		workspaceStart:    workspace.Start,
	})
}

func run(
	ctx context.Context,
	preparation *validate.Preparation,
	started StartedObserver,
	dependencies dependencies,
) (result Result) {
	if preparation == nil || preparation.Score == nil ||
		preparation.Cast == nil || started == nil ||
		dependencies.probe == nil || dependencies.client == nil ||
		dependencies.resolveTrampoline == nil ||
		dependencies.now == nil || dependencies.newID == nil ||
		dependencies.workspaceStart == nil {
		return Result{Err: errors.New("driver: incomplete run")}
	}
	startResult, err := dependencies.workspaceStart(preparation, dependencies.probe)
	if err != nil {
		return Result{Err: err}
	}
	result = Result{RunID: startResult.RunID}
	if err := started(startResult.RunID); err != nil {
		return interrupted(result, err)
	}
	if _, err := initialRemainingMS(preparation.Score.EffectivePolicy().ActiveWallClockMin); err != nil {
		return interrupted(result, err)
	}
	seeds := movementSeeds(preparation.Score)
	store, err := runstore.New(preparation.RepositoryRoot, dependencies.probe)
	if err != nil {
		return stopped(result, err)
	}
	// The lease is a canceller's signal target, so install the harmless wake
	// relay before the lease can become durable. A buffered signal received in
	// the acquisition-to-watcher window is delivered after Watch starts.
	wake := make(chan os.Signal, 1)
	signal.Notify(wake, syscall.SIGUSR1)
	defer signal.Stop(wake)
	authority, err := store.AcquireDriver(startResult.RunID, seeds)
	if err != nil {
		return stopped(result, err)
	}
	defer func() {
		if result.prepareAcknowledged {
			return
		}
		if !releasesDriverLease(result.Outcome) {
			return
		}
		if err := authority.Release(); err != nil && result.Err == nil {
			result.Err = err
		}
	}()
	control, err := cancellation.Watch(store, startResult.RunID)
	if err != nil {
		return stopped(result, err)
	}
	defer control.Stop()
	wakeDone := make(chan struct{})
	defer close(wakeDone)
	go func() {
		for {
			select {
			case <-wake:
				control.Wake()
			case <-wakeDone:
				return
			}
		}
	}()
	if cancelled, handled := controlResult(ctx, result, store, authority, control); handled {
		return cancelled
	}
	if err := startResult.Run.BindDriver(authority); err != nil {
		return stopped(result, err)
	}
	if cancelled, handled := controlResult(ctx, result, store, authority, control); handled {
		return cancelled
	}
	return liveRunLoop(ctx, result, startResult.Run, store, authority, control, dependencies)
}

// liveRunLoop executes exactly one selector decision per iteration, then
// reloads through selectLiveBetweenUnit before advancing again.
func liveRunLoop(
	ctx context.Context,
	result Result,
	run *workspace.Run,
	store *runstore.Store,
	authority *runstore.Driver,
	control *cancellation.Watcher,
	dependencies dependencies,
) Result {
	for {
		if cancelled, handled := controlResult(ctx, result, store, authority, control); handled {
			return cancelled
		}
		input, err := store.LoadRunInput(result.RunID)
		if err != nil {
			return interrupted(result, err)
		}
		if input.Projection.State.Run == runstate.RunSucceeded {
			result.Outcome = OutcomeSucceeded
			return result
		}
		decision, err := selectLiveBetweenUnit(store, authority)
		if err != nil {
			return interrupted(result, err)
		}
		if decision.Action == nil {
			return interrupted(result, errors.New("driver: live selector returned no action"))
		}
		switch decision.Action.Kind {
		case recovery.ActionAppendMovementReady, recovery.ActionAppendMovementStarted:
			eventType := runstate.EventMovementReady
			if decision.Action.Kind == recovery.ActionAppendMovementStarted {
				eventType = runstate.EventMovementStarted
			}
			if _, err := authority.Append(runstate.Event{
				RunID: result.RunID, ScoreRevision: input.Projection.State.ScoreHead.Revision,
				MovementID: decision.Action.MovementID, Type: eventType, Payload: json.RawMessage(`{}`),
			}, faultpoint.ReceiptAddress("movement."+string(eventType))); err != nil {
				return stopped(result, err)
			}
		case recovery.ActionSelectInitialPerformer:
			movement, performer, err := liveInitialSelection(input, decision.Action.MovementID)
			if err != nil {
				return interrupted(result, err)
			}
			baseCommit := ""
			baseTree := input.BaseTree
			baseCompositionHash := ""
			if len(movement.Needs) != 0 {
				composed, err := PrepareMovementBase(store, authority, input, decision.Action.MovementID, input.Projection.Scheduler.RemainingTime, dependencies.now, dependencies.newID)
				if err != nil {
					if errors.Is(err, ErrCompositionBudgetExhausted) {
						return liveCompositionBudgetExhaustion(result, store, authority)
					}
					if errors.Is(err, ErrCompositionCancelled) {
						if err := control.Execute(ctx); err != nil {
							if errors.Is(err, runstate.ErrSweepUnverifiable) {
								return halted(result, "sweep_unverifiable", err)
							}
							return stopped(result, err)
						}
						return Result{RunID: result.RunID, Outcome: OutcomeCancelled}
					}
					if errors.Is(err, ErrCompositionTerminalized) {
						return Result{RunID: result.RunID, Outcome: OutcomeFailed, Reason: "composition_terminal"}
					}
					return stopped(result, err)
				}
				baseCommit, baseTree, baseCompositionHash = composed.Commit, composed.Tree, composed.Hash
			}
			var attempt *workspace.AttemptWorkspace
			if baseCommit == "" {
				attempt, err = run.CreateAttempt(movement.ID)
			} else {
				attempt, err = run.CreateAttemptAtBase(movement.ID, baseCommit)
			}
			if err != nil {
				return stopped(result, err)
			}
			attemptResult := ExecuteAttempt(ctx, AttemptExecution{
				RepositoryRoot: store.RepositoryRoot(), Score: input.Score, Cast: input.Cast,
				RunID: result.RunID, Attempt: attempt, BaseTree: baseTree, BaseCompositionHash: baseCompositionHash, CandidateTree: liveCandidateTree(input),
				Authority: authority, PerformerID: performer.ID, SelectionReason: "initial",
				RemainingMS: input.Projection.Scheduler.RemainingTime, Control: control,
			}, executionDependenciesFrom(dependencies))
			if attemptResult.Outcome != OutcomeSucceeded {
				return attemptResult
			}
		case recovery.ActionMaterializeSuccessor:
			attemptResult := liveMaterializeSuccessor(ctx, result, store, authority, control, dependencies, input)
			if attemptResult.Outcome != OutcomeSucceeded {
				return attemptResult
			}
		case recovery.ActionComposeCandidate:
			if err := ComposeCandidate(store, authority, input, input.Projection.Scheduler.RemainingTime, dependencies.now, dependencies.newID); err != nil {
				if errors.Is(err, ErrCompositionBudgetExhausted) {
					return liveCompositionBudgetExhaustion(result, store, authority)
				}
				if errors.Is(err, ErrCompositionCancelled) {
					if err := control.Execute(ctx); err != nil {
						if errors.Is(err, runstate.ErrSweepUnverifiable) {
							return halted(result, "sweep_unverifiable", err)
						}
						return stopped(result, err)
					}
					return Result{RunID: result.RunID, Outcome: OutcomeCancelled}
				}
				if errors.Is(err, ErrCompositionTerminalized) {
					return Result{RunID: result.RunID, Outcome: OutcomeFailed, Reason: "composition_terminal"}
				}
				return stopped(result, err)
			}
		case recovery.ActionAppendBudgetFailure, recovery.ActionAppendRunFailed:
			return liveBudgetExhaustion(result, store, authority, input, decision.Action)
		default:
			return interrupted(result, fmt.Errorf("driver: unsupported live scheduler action %s", decision.Action.Kind))
		}
	}
}

func liveInitialSelection(
	input runstore.RunInput,
	movementID runstate.MovementID,
) (score.MovementView, cast.PerformerView, error) {
	if input.Score == nil {
		return score.MovementView{}, cast.PerformerView{}, errors.New("driver: live selection has no pinned score")
	}
	if input.Cast == nil {
		return score.MovementView{}, cast.PerformerView{}, errors.New("driver: live selection has no resolved cast")
	}
	for _, movement := range input.Score.Movements() {
		if runstate.MovementID(movement.ID) != movementID {
			continue
		}
		binding, ok := input.Cast.Binding(movement.PartID)
		if !ok {
			return score.MovementView{}, cast.PerformerView{}, errors.New("driver: selected movement has no binding")
		}
		performer, ok := input.Cast.Performer(binding.Performer)
		if !ok {
			return score.MovementView{}, cast.PerformerView{}, errors.New("driver: selected performer is absent")
		}
		return movement, performer, nil
	}
	return score.MovementView{}, cast.PerformerView{}, fmt.Errorf("driver: selected movement %q is absent", movementID)
}

func liveCandidateTree(input runstore.RunInput) string {
	if input.Projection.State.ApplicationCandidate != nil {
		return input.Projection.State.ApplicationCandidate.ResultTree
	}
	return input.BaseTree
}

// ExecuteAttempt drives one fresh attempt after its workspace already exists.
// It is shared by the live wrapper and recovery; neither validation nor
// workspace.Start belongs to this execution core.
func ExecuteAttempt(
	ctx context.Context,
	execution AttemptExecution,
	executionDependencies ExecutionDependencies,
) (result Result) {
	if execution.RepositoryRoot == "" || execution.Score == nil || execution.Cast == nil ||
		execution.RunID == "" || execution.Attempt == nil || execution.BaseTree == "" || execution.CandidateTree == "" ||
		execution.Authority == nil || execution.PerformerID == "" || execution.SelectionReason == "" ||
		executionDependencies.Probe == nil || executionDependencies.Client == nil ||
		executionDependencies.ResolveTrampoline == nil || executionDependencies.Now == nil ||
		executionDependencies.NewID == nil {
		return Result{RunID: execution.RunID, Err: errors.New("driver: incomplete attempt execution")}
	}
	movement, part, performer, plan, err := selectAttempt(
		execution.Score, execution.Cast, execution.Attempt.MovementID, execution.PerformerID,
	)
	if err != nil {
		return Result{RunID: execution.RunID, Err: err}
	}
	baseCompositionHash := execution.BaseCompositionHash
	if len(movement.Needs) != 0 {
		if baseCompositionHash == "" {
			baseCompositionHash, err = movementCompositionDependencyHash(movement.ID, execution.BaseTree)
			if err != nil {
				return Result{RunID: execution.RunID, Err: err}
			}
		}
	}
	startResult := workspace.StartResult{RunID: execution.RunID}
	attempt := execution.Attempt
	authority := execution.Authority
	remainingMS := execution.RemainingMS
	dependencies := dependencies{
		probe:               executionDependencies.Probe,
		client:              executionDependencies.Client,
		resolveTrampoline:   executionDependencies.ResolveTrampoline,
		now:                 executionDependencies.Now,
		newID:               executionDependencies.NewID,
		afterMovementFailed: executionDependencies.afterMovementFailed,
	}
	result = Result{RunID: execution.RunID}
	store, err := runstore.New(execution.RepositoryRoot, executionDependencies.Probe)
	if err != nil {
		return stopped(result, err)
	}
	control := execution.Control
	if control == nil {
		control, err = cancellation.Watch(store, execution.RunID)
		if err != nil {
			return stopped(result, err)
		}
		defer control.Stop()
	}
	if cancelled, handled := controlResult(ctx, result, store, authority, control); handled {
		return cancelled
	}
	policy := execution.Score.EffectivePolicy()
	_, ok := execution.Cast.Binding(movement.PartID)
	if !ok {
		return interrupted(result, errors.New("driver: selected movement has no binding"))
	}
	base := runstate.Event{
		RunID:         startResult.RunID,
		ScoreRevision: execution.Score.Revision(),
		MovementID:    runstate.MovementID(movement.ID),
		PartID:        movement.PartID,
		AttemptID:     attempt.AttemptID,
	}
	appendEvent := func(
		eventType runstate.EventType,
		payload any,
		address string,
	) (faultpoint.DurabilityReceipt, error) {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return faultpoint.DurabilityReceipt{}, err
		}
		event := base
		event.Type = eventType
		event.Payload = encoded
		receipt, err := authority.Append(event, faultpoint.ReceiptAddress(address))
		if err != nil {
			return faultpoint.DurabilityReceipt{}, err
		}
		switch eventType {
		case runstate.EventAttemptCompleted:
			dependencies.probe.Reached(faultpoint.PointLifecycleAttemptCompleted)
		case runstate.EventMovementSucceeded:
			dependencies.probe.Reached(faultpoint.PointLifecycleMovementSucceeded)
		case runstate.EventAcceptanceEvaluationCompleted:
			dependencies.probe.Reached(faultpoint.PointAcceptanceEvaluationCompleted)
		}
		return receipt, nil
	}
	if !execution.SelectionDurable {
		initialSelection, err := workspace.PerformerSelectedEvent(
			attempt, execution.Score.Revision(), performer.ID, performer.Adapter, performer.Model,
			execution.SelectionReason, execution.SelectionCausationID,
		)
		if err != nil {
			return interrupted(result, err)
		}
		if _, err := authority.Append(initialSelection, faultpoint.ReceiptAddress("attempt.performer_selected")); err != nil {
			return stopped(result, err)
		}
	}
	if cancelled, handled := controlResult(ctx, result, store, authority, control); handled {
		return cancelled
	}
	adapterPath, err := dependencies.client.Resolve(performer.Adapter)
	if err != nil {
		return interrupted(result, err)
	}
	trampoline, err := dependencies.resolveTrampoline()
	if err != nil {
		return interrupted(result, err)
	}
	if !filepath.IsAbs(trampoline) {
		return interrupted(result, errors.New("trampoline path is not absolute"))
	}
	grants := effectiveGrants(movement, policy)
	brief, globalInvariants, err := executeBrief(
		execution.Score,
		movement,
		grants,
	)
	if err != nil {
		return stopped(result, err)
	}
	var reviewInput *protocol.ArtifactRef
	if len(movement.Acceptance.ReviewCriteria) == 1 {
		reviewSubjectTree := execution.BaseTree
		if movement.ID == execution.Score.Execution().FinalMovementID {
			reviewSubjectTree = execution.CandidateTree
		}
		input, err := publishReviewSubjectInput(authority, execution.RepositoryRoot, execution.RunID, movement, execution.Score.Revision(), reviewSubjectTree)
		if err != nil {
			return stopped(result, err)
		}
		reviewInput = &input
	}
	inputState, err := authority.State()
	if err != nil {
		return stopped(result, err)
	}
	inputs, err := deliveredInputs(
		execution.Score,
		movement,
		inputState,
		execution.RepositoryRoot,
		execution.RunID,
		reviewInput,
	)
	if err != nil {
		return stopped(result, err)
	}
	request := protocol.ExecuteRequest{
		RunID:         string(startResult.RunID),
		MovementID:    movement.ID,
		AttemptID:     string(attempt.AttemptID),
		ScoreRevision: int(execution.Score.Revision()),
		Model:         performer.Model,
		Brief:         brief,
		Workdir:       attempt.Worktree,
		OutputDir:     attempt.OutputDir,
		Grants:        grants,
		Budget: protocol.Budget{
			RemainingMS: remainingMS,
		},
		Inputs:            inputs,
		Feedback:          []protocol.Feedback{},
		ResolvedDecisions: []protocol.ResolvedDecision{},
	}
	if extension, present := performer.Extensions[performer.Adapter]; present {
		encoded, err := json.Marshal(extension)
		if err != nil {
			return interrupted(result, err)
		}
		request.Extensions = map[string]json.RawMessage{
			performer.Adapter: encoded,
		}
	}
	adapterInterval, err := dependencies.newID()
	if err != nil {
		return interrupted(result, err)
	}
	launchID, err := dependencies.newID()
	if err != nil {
		return interrupted(result, err)
	}
	adapterOpened := dependencies.now()
	if _, err := appendEvent(runstate.EventExecutionStarted, map[string]any{
		"interval_id":        adapterInterval,
		"phase":              "adapter",
		"wall_start":         formatTime(adapterOpened),
		"remaining_at_start": remainingMS,
	}, "execution.adapter.started"); err != nil {
		return stopped(result, err)
	}

	attemptDomains := []canonical.Domain(nil)
	if baseCompositionHash != "" {
		attemptDomains = append(attemptDomains, canonical.DomainMovementComposition)
	}
	attemptVersions, err := identityVersions(attemptDomains...)
	if err != nil {
		return interrupted(result, err)
	}
	if baseCompositionHash != "" {
		attemptVersions["composition"] = canonical.CompositionAlgorithmVersion
	}
	var advisory []string
	var executionHash string
	var executionVersions map[string]any
	recordProbe := func(probe protocol.ProbeResult) (faultpoint.DurabilityReceipt, error) {
		advisory, err = admitProbe(
			movement,
			part,
			policy,
			performer,
			probe,
		)
		if err != nil {
			return faultpoint.DurabilityReceipt{}, err
		}
		features := recognizedFeatures(probe.Features)
		resolutions, err := resolutionProjection(request.ResolvedDecisions)
		if err != nil {
			return faultpoint.DurabilityReceipt{}, err
		}
		executionHash, executionVersions, err = executionDependencyIdentity(
			execution.Score,
			movement,
			part,
			performer,
			grants,
			globalInvariants,
			plan.Hash(),
			baseCompositionHash,
			request.Inputs,
			request.ResolvedDecisions,
			request.Feedback,
		)
		if err != nil {
			return faultpoint.DurabilityReceipt{}, err
		}
		return appendEvent(runstate.EventAdapterProbed, map[string]any{
			"adapter_version":           probe.Adapter.Version,
			"capabilities":              capabilityPayload(probe.Capabilities),
			"enforcement":               probe.Enforcement,
			"negotiated_features":       features,
			"truncated_resolutions":     []any{},
			"delivered_resolutions":     resolutions,
			"delivered_feedback":        feedbackProjection(request.Feedback),
			"advisory_dimensions":       advisory,
			"execution_dependency_hash": executionHash,
			"identity_versions":         executionVersions,
		}, "attempt.adapter_probed")
	}
	recordIdentity := func(
		identity runstate.ProcessIdentity,
	) (faultpoint.DurabilityReceipt, error) {
		attemptNumber, err := journalAttemptNumber(store, execution.RunID, attempt.MovementID)
		if err != nil {
			return faultpoint.DurabilityReceipt{}, err
		}
		payload := map[string]any{
			"attempt_number":    attemptNumber,
			"adapter_process":   processPayload(identity),
			"granted_authority": grants,
			"identity_versions": attemptVersions,
		}
		if baseCompositionHash != "" {
			payload["base_composition_hash"] = baseCompositionHash
		}
		if reviewInput != nil {
			payload["review_subject_input"] = map[string]any{"instance_id": reviewInput.InstanceID, "hash": reviewInput.Hash}
		}
		return appendEvent(runstate.EventAttemptStarted, payload, "attempt.started")
	}
	var observationErr error
	adapterChargedMS := int64(0)
	classifyFailureWithRemaining := func(failure successor.FailureCase, remainingOverride int64) (runstate.Disposition, error) {
		input, err := store.LoadRunInput(execution.RunID)
		if err != nil {
			return runstate.Disposition{}, err
		}
		current := input.Projection.CurrentHeadAttempt
		if current == nil || current.AttemptID != execution.Attempt.AttemptID {
			return runstate.Disposition{}, errors.New("driver: classification attempt is not current")
		}
		facts := current.FailureClassification
		visited := make(map[string]bool, len(facts.VisitedPerformers)+1)
		for _, performerID := range facts.VisitedPerformers {
			visited[performerID] = true
		}
		visited[facts.CurrentPerformer] = true
		hasUnvisitedFallback := false
		for _, fallback := range facts.Fallbacks {
			if !visited[fallback] {
				hasUnvisitedFallback = true
				break
			}
		}
		remaining := facts.RemainingTimeMS
		if remainingOverride >= 0 {
			remaining = remainingOverride
		}
		return successor.Classify(successor.ClassificationInput{
			Failure:              failure,
			HasUnvisitedFallback: hasUnvisitedFallback,
			RetriesConsumed:      facts.RetriesConsumed,
			RetriesPerMovement:   facts.RetriesPerMovement,
			RemainingTimeMS:      remaining,
		})
	}
	classifyFailure := func(failure successor.FailureCase) (runstate.Disposition, error) {
		return classifyFailureWithRemaining(failure, -1)
	}
	logCount := 0
	progressCount := 0
	recorder := adapter.ExecuteRecorder{
		RecordProbe: recordProbe,
		RecordArtifact: func(
			observation adapter.ArtifactObservation,
		) (faultpoint.DurabilityReceipt, error) {
			if observationErr != nil {
				return faultpoint.DurabilityReceipt{}, observationErr
			}
			instance, err := attempt.IngestArtifact(
				workspace.ArtifactInput{
					LogicalOutputID: observation.ArtifactID,
					Kind:            observation.Kind,
					Path:            observation.Path,
					SourcePath:      observation.SourcePath,
				},
				faultpoint.ReceiptAddress(
					"artifact."+observation.ArtifactID+".published",
				),
				faultpoint.ReceiptAddress(
					"artifact."+observation.ArtifactID+".recorded",
				),
			)
			return instance.RecordReceipt, err
		},
		RecordExecutionStopped: func(
			stop adapter.ExecutionStop,
		) (faultpoint.DurabilityReceipt, error) {
			if observationErr != nil {
				return faultpoint.DurabilityReceipt{}, observationErr
			}
			receipt, err := appendEvent(runstate.EventExecutionStopped, map[string]any{
				"interval_id":      stop.IntervalID,
				"reason":           stop.Reason,
				"charging":         stop.Charging,
				"charged_duration": stop.ChargedDurationMS,
			}, "execution.adapter.stopped")
			if err == nil {
				adapterChargedMS = stop.ChargedDurationMS
			}
			return receipt, err
		},
		RecordOutcome: func(
			observation adapter.OutcomeObservation,
		) (faultpoint.DurabilityReceipt, error) {
			if observationErr != nil {
				return faultpoint.DurabilityReceipt{}, observationErr
			}
			switch observation.EventType {
			case string(runstate.EventPerformerCompleted):
				return appendEvent(runstate.EventPerformerCompleted, map[string]any{
					"session_hint_stored": false,
				}, "attempt.performer_completed")
			case string(runstate.EventAttemptFailed):
				failure := observation.Result.Failure
				kind := string(protocol.FailureTaskFailed)
				detail := observation.Result.Detail
				if failure != nil {
					kind = string(failure.Kind)
					detail = failure.Detail
				}
				disposition, err := classifyFailure(successor.FailureCase{AttemptKind: kind})
				if err != nil {
					return faultpoint.DurabilityReceipt{}, err
				}
				payload := map[string]any{
					"kind":        kind,
					"disposition": dispositionPayload(disposition),
				}
				if observation.FailureReason != "" {
					payload["reason"] = observation.FailureReason
				}
				if detail != "" {
					payload["detail"] = detail
				}
				return appendEvent(
					runstate.EventAttemptFailed,
					payload,
					"attempt.failed",
				)
			case string(runstate.EventAttemptBlocked):
				for _, decision := range observation.Raised {
					if decision.Kind != protocol.EventProposal || decision.Proposal == nil || !decision.Proposal.RequiresDecision {
						continue
					}
					unit, ok := recovery.UnimplementedActionOwner(recovery.ActionAppendRoutedRequest)
					if !ok {
						return faultpoint.DurabilityReceipt{}, errors.New("waiting_human proposal has no routed-request owner")
					}
					return faultpoint.DurabilityReceipt{}, fmt.Errorf("waiting_human proposal requires unit %s", unit)
				}
				raised := make([]any, 0, len(observation.Raised))
				pending := make([]string, 0, len(observation.Raised))
				for _, decision := range observation.Raised {
					switch decision.Kind {
					case protocol.EventQuestion:
						if decision.Question == nil {
							return faultpoint.DurabilityReceipt{}, errors.New("waiting_human question is absent")
						}
						decisionID, err := adapterDecisionID(attempt.AttemptID, decision.Question.ID)
						if err != nil {
							return faultpoint.DurabilityReceipt{}, err
						}
						raised = append(raised, map[string]any{
							"decision_id": decisionID,
							"emitted_id":  decision.Question.ID,
							"kind":        "question",
							"question":    decision.Question.Question,
							"blocking":    true,
						})
						pending = append(pending, decisionID)
					case protocol.EventProposal:
						if decision.Proposal == nil {
							return faultpoint.DurabilityReceipt{}, errors.New("waiting_human proposal is absent")
						}
						decisionID, err := adapterDecisionID(attempt.AttemptID, decision.Proposal.ID)
						if err != nil {
							return faultpoint.DurabilityReceipt{}, err
						}
						proposalID, err := adapterProposalID(attempt.AttemptID, decision.Proposal.ID)
						if err != nil {
							return faultpoint.DurabilityReceipt{}, err
						}
						raised = append(raised, map[string]any{
							"decision_id": decisionID,
							"emitted_id":  decision.Proposal.ID,
							"kind":        "proposal",
							"proposal_id": proposalID,
							"blocking":    decision.Proposal.RequiresDecision,
						})
						if decision.Proposal.RequiresDecision {
							pending = append(pending, decisionID)
						}
					default:
						return faultpoint.DurabilityReceipt{}, fmt.Errorf("unsupported raised decision %q", decision.Kind)
					}
				}
				slices.Sort(pending)
				return appendEvent(runstate.EventAttemptBlocked, map[string]any{
					"raised":               raised,
					"pending_decision_ids": pending,
				}, "attempt.blocked")
			default:
				return faultpoint.DurabilityReceipt{}, fmt.Errorf(
					"unsupported execute outcome %q",
					observation.EventType,
				)
			}
		},
		ObserveLog: func(event protocol.LogEvent) {
			if observationErr != nil {
				return
			}
			logCount++
			_, observationErr = appendEvent(runstate.EventLog, map[string]any{
				"level": event.Level, "message": event.Message,
			}, fmt.Sprintf("observation.log.%d", logCount))
		},
		ObserveProgress: func(event protocol.ProgressEvent) {
			if observationErr != nil {
				return
			}
			progressCount++
			_, observationErr = appendEvent(
				runstate.EventProgress,
				map[string]any{"message": event.Message},
				fmt.Sprintf("observation.progress.%d", progressCount),
			)
		},
	}
	executeContext, cancel := context.WithTimeout(
		ctx,
		budgetTimeout(remainingMS),
	)
	report, err := dependencies.client.Execute(executeContext, adapter.ExecutePlan{
		AdapterID:      performer.Adapter,
		AdapterPath:    adapterPath,
		TrampolinePath: trampoline,
		AttemptRoot:    filepath.Dir(attempt.Worktree),
		LaunchID:       launchID,
		Directory:      attempt.Worktree,
		Request:        request,
		IntervalID:     runstate.IntervalID(adapterInterval),
		IntervalOpened: adapterOpened,
		MayPropose:     movement.MayPropose,
		Cancel:         control.Interrupt(),
		Probe:          dependencies.probe,
		RecordIdentity: recordIdentity,
		Recorder:       recorder,
	})
	cancel()
	if cancelled, handled := controlResult(ctx, result, store, authority, control); handled {
		return cancelled
	}
	if err != nil {
		var halt *adapter.HaltError
		if errors.As(err, &halt) {
			return halted(result, "sweep_unverifiable", err)
		}
		return stopped(result, err)
	}
	if terminal, handled := realizeRecordedNoneDisposition(ctx, result, store, authority, control, dependencies); handled {
		return terminal
	}
	if report.Result != nil && report.Result.Outcome == protocol.OutcomeWaitingHuman {
		result.Outcome = OutcomeWaitingHuman
		return result
	}
	if report.Result == nil || report.Result.Outcome != protocol.OutcomeCompleted {
		return interrupted(result, errors.New("adapter did not complete"))
	}

	verificationFailed, err := completeAttemptVerification(
		attempt,
		hasGrant(movement.Grants, "repo_write"),
		authority,
		dependencies.probe,
		appendEvent,
		classifyFailure,
	)
	if err != nil {
		if !verificationFailed {
			return stopped(result, err)
		}
		if terminal, handled := realizeRecordedNoneDisposition(ctx, result, store, authority, control, dependencies); handled {
			return terminal
		}
		return interrupted(result, err)
	}
	if cancelled, handled := controlResult(ctx, result, store, authority, control); handled {
		return cancelled
	}
	subjectTree := execution.BaseTree
	if hasGrant(movement.Grants, "repo_write") {
		subject, err := attempt.CaptureAcceptanceSubject()
		if err != nil {
			return stopped(result, err)
		}
		subjectTree = subject.Tree
	} else if movement.ID == execution.Score.Execution().FinalMovementID {
		subjectTree = execution.CandidateTree
	}
	acceptanceInterval, err := dependencies.newID()
	if err != nil {
		return interrupted(result, err)
	}
	acceptanceOpened := dependencies.now()
	remainingMS = remainingAfter(remainingMS, adapterChargedMS)
	if _, err := appendEvent(runstate.EventExecutionStarted, map[string]any{
		"interval_id":        acceptanceInterval,
		"phase":              "acceptance",
		"wall_start":         formatTime(acceptanceOpened),
		"remaining_at_start": remainingMS,
	}, "execution.acceptance.started"); err != nil {
		return stopped(result, err)
	}
	if cancelled, handled := controlResult(ctx, result, store, authority, control); handled {
		return cancelled
	}
	acceptanceDisposition, err := classifyFailure(successor.FailureCase{AcceptanceReason: "acceptance_failed"})
	if err != nil {
		return stopped(result, err)
	}
	evaluationInput := acceptance.Evaluation{
		RunID:              startResult.RunID,
		ScoreRevision:      execution.Score.Revision(),
		MovementID:         base.MovementID,
		PartID:             base.PartID,
		AttemptID:          base.AttemptID,
		SubjectTree:        subjectTree,
		FailureDisposition: acceptanceDisposition,
		LookupArtifact: func(
			id runstate.ArtifactInstanceID,
		) (runstate.ArtifactRecord, bool, error) {
			state, err := authority.State()
			if err != nil {
				return runstate.ArtifactRecord{}, false, err
			}
			record, exists := state.Artifacts[id]
			return record, exists, nil
		},
		ReadArtifact: func(record runstate.ArtifactRecord) ([]byte, error) {
			return os.ReadFile(filepath.Join(execution.RepositoryRoot, ".partitur", "runs", string(execution.RunID), "artifacts", record.LogicalOutputID, string(record.AttemptID)))
		},
		ValidateEvidence: func(path string, line *int64) error {
			return validateReviewEvidence(execution.RepositoryRoot, subjectTree, path, line)
		},
		Append: func(event runstate.Event) (faultpoint.DurabilityReceipt, error) {
			return authority.Append(
				event,
				faultpoint.ReceiptAddress(
					"acceptance."+string(event.Type),
				),
			)
		},
		RunCriterion: func(request acceptance.RunCriterionRequest) acceptance.RunCriterionResult {
			remaining := remainingAfter(remainingMS, dependencies.now().Sub(acceptanceOpened).Milliseconds())
			return criterionexec.Run(criterionexec.Config{
				RunID: execution.RunID, AttemptID: attempt.AttemptID, AttemptRoot: filepath.Dir(attempt.Worktree),
				Worktree: attempt.Worktree, RepositoryRoot: execution.RepositoryRoot, SubjectTree: subjectTree,
				TrampolinePath: trampoline, RemainingMS: remaining, Probe: dependencies.probe,
				Cancel: control.Interrupt(),
			}, request)
		},
		FailureDispositionFor: func(runResult acceptance.RunCriterionResult) (runstate.Disposition, error) {
			remaining := remainingAfter(remainingMS, dependencies.now().Sub(acceptanceOpened).Milliseconds())
			if runResult.DeadlineTied {
				remaining = 0
			}
			return classifyFailureWithRemaining(successor.FailureCase{AcceptanceReason: "acceptance_failed"}, remaining)
		},
	}
	acceptanceStarted, err := plan.StartEvent(base, subjectTree)
	if err != nil {
		return stopped(result, err)
	}
	if _, err := authority.Append(acceptanceStarted, faultpoint.ReceiptAddress("acceptance.acceptance.started")); err != nil {
		return stopped(result, err)
	}
	evaluation, err := acceptance.EvaluateStarted(plan, evaluationInput)
	if err != nil {
		return stopped(result, err)
	}
	if evaluation.EvaluationCompleted {
		dependencies.probe.Reached(faultpoint.PointAcceptanceEvaluationCompleted)
	}
	if cancelled, handled := controlResult(ctx, result, store, authority, control); handled {
		return cancelled
	}
	if evaluation.Cancelled {
		return interrupted(result, errors.New("driver: criterion returned cancellation without durable control"))
	}
	if !evaluation.EvaluationCompleted && !evaluation.BudgetExhausted {
		dependencies.probe.Reached(faultpoint.PointAcceptanceFailureRecorded)
	}
	if evaluation.BudgetExhausted {
		return TerminalizeAcceptanceBudget(ctx, AcceptanceBudgetTerminalization{
			RepositoryRoot: execution.RepositoryRoot,
			RunID:          execution.RunID,
			AttemptID:      attempt.AttemptID,
			Authority:      authority,
			Control:        control,
			Probe:          dependencies.probe,
			Close: func() error {
				acceptanceDuration := dependencies.now().Sub(acceptanceOpened).Milliseconds()
				if acceptanceDuration < 0 {
					acceptanceDuration = 0
				}
				_, err := appendEvent(runstate.EventExecutionStopped, map[string]any{
					"interval_id":      acceptanceInterval,
					"reason":           "budget_exhausted",
					"charging":         "measured",
					"charged_duration": acceptanceDuration,
				}, "execution.acceptance.stopped")
				return err
			},
		})
	}
	acceptanceStopped := dependencies.now()
	acceptanceDuration := acceptanceStopped.Sub(acceptanceOpened).Milliseconds()
	if acceptanceDuration < 0 {
		acceptanceDuration = 0
	}
	if _, err := appendEvent(runstate.EventExecutionStopped, map[string]any{
		"interval_id":      acceptanceInterval,
		"reason":           "normal",
		"charging":         "measured",
		"charged_duration": acceptanceDuration,
	}, "execution.acceptance.stopped"); err != nil {
		return stopped(result, err)
	}
	if cancelled, handled := controlResult(ctx, result, store, authority, control); handled {
		return cancelled
	}
	if terminal, handled := realizeRecordedNoneDisposition(ctx, result, store, authority, control, dependencies); handled {
		return terminal
	}
	if !evaluation.EvaluationCompleted || (!evaluation.Verified && evaluation.ReviewOutcome == "") {
		return interrupted(result, errors.New("attempt did not earn VERIFIED"))
	}
	gateRequired := movement.Acceptance.HumanGate == "always" || (movement.Acceptance.HumanGate == "on_contested" && evaluation.ReviewOutcome == "CONTESTED")
	if gateRequired {
		gateID, err := humanGateID(attempt.AttemptID)
		if err != nil {
			return interrupted(result, err)
		}
		decisionID, err := dependencies.newID()
		if err != nil {
			return interrupted(result, fmt.Errorf("allocate human gate decision id: %w", err))
		}
		blocking := make([]any, len(evaluation.BlockingFindings))
		for index, finding := range evaluation.BlockingFindings {
			blocking[index] = map[string]any{"artifact_instance_id": finding.ArtifactInstanceID, "finding_id": finding.FindingID}
		}
		payload := map[string]any{
			"decision_id": decisionID, "decision_type": "human_gate", "gate_id": gateID,
			"gate_mode": movement.Acceptance.HumanGate, "subject_tree": subjectTree, "blocking_findings": blocking,
		}
		if evaluation.ReviewOutcome != "" {
			payload["review_outcome"] = evaluation.ReviewOutcome
		}
		if _, err := appendEvent(runstate.EventDecisionRequested, payload, "acceptance.decision.requested.human_gate"); err != nil {
			return stopped(result, err)
		}
		dependencies.probe.Reached(faultpoint.PointHumanGateDecisionRequested)
		result.Outcome = OutcomeWaitingHuman
		return result
	}
	if _, err := appendEvent(
		runstate.EventAttemptCompleted,
		map[string]any{},
		"attempt.completed",
	); err != nil {
		return stopped(result, err)
	}
	if cancelled, handled := controlResult(ctx, result, store, authority, control); handled {
		return cancelled
	}
	state, err := authority.State()
	if err != nil {
		return stopped(result, err)
	}
	artifactIDs := artifactIDs(state, attempt.AttemptID)
	movementVersions, err := identityVersions()
	if err != nil {
		return interrupted(result, err)
	}
	payload := map[string]any{
		"approved_artifact_instance_ids": artifactIDs,
		"identity_versions":              movementVersions,
		"run_succeeded":                  state.FinalMovements[attempt.MovementID],
	}
	if state.RepoWriteMovements[attempt.MovementID] {
		changeSet, ok := state.ChangeSets[attempt.AttemptID]
		if !ok {
			return interrupted(result, errors.New("driver: repo-write attempt has no recorded change set"))
		}
		payload["approved_change_set_id"] = changeSet.ChangeSetID
	}
	if _, err := appendEvent(runstate.EventMovementSucceeded, payload, "movement.succeeded"); err != nil {
		return stopped(result, err)
	}
	if cancelled, handled := controlResult(ctx, result, store, authority, control); handled {
		return cancelled
	}
	result.Outcome = OutcomeSucceeded
	return result
}

func adapterDecisionID(attemptID runstate.AttemptID, emittedID string) (string, error) {
	return scopedAdapterID("dec-", "partitur/decision-id", attemptID, emittedID)
}

func adapterProposalID(attemptID runstate.AttemptID, emittedID string) (string, error) {
	return scopedAdapterID("prp-", "partitur/proposal-id", attemptID, emittedID)
}

func humanGateID(attemptID runstate.AttemptID) (string, error) {
	encoded, err := canonical.Encode(map[string]any{"attempt_id": string(attemptID)})
	if err != nil {
		return "", fmt.Errorf("encode human gate id: %w", err)
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("partitur/gate-id"))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(encoded)
	return fmt.Sprintf("gat-%x", digest.Sum(nil)), nil
}

func publishReviewSubjectInput(
	authority *runstore.Driver,
	repositoryRoot string,
	runID runstate.RunID,
	movement score.MovementView,
	revision uint64,
	subjectTree string,
) (protocol.ArtifactRef, error) {
	if authority == nil || len(movement.Acceptance.ReviewCriteria) != 1 || subjectTree == "" {
		return protocol.ArtifactRef{}, errors.New("driver: incomplete review subject input")
	}
	rubrics := movement.Acceptance.ReviewCriteria[0].Rubrics
	entries := make([]any, len(rubrics))
	for index, rubric := range rubrics {
		entries[index] = map[string]any{"id": rubric, "required_coverage": true}
	}
	contents, err := canonical.Encode(map[string]any{
		"schema": "partitur/subject-tree+json;v=1", "subject_tree": subjectTree,
		"findings_schema": "partitur/findings+json;v=1", "rubrics": entries,
	})
	if err != nil {
		return protocol.ArtifactRef{}, err
	}
	digest := sha256.Sum256(contents)
	hash := runstate.Hash(fmt.Sprintf("sha256:%x", digest))
	relative := runstore.Path(filepath.ToSlash(filepath.Join("inputs", movement.ID, fmt.Sprintf("revision-%d", revision), "subject-tree.json")))
	if err := authority.Mutate(func(transaction *runstore.Txn, _ runstate.State) error {
		_, err := transaction.At("review.subject_tree.published").PublishImmutable(relative, contents, hash)
		return err
	}); err != nil {
		return protocol.ArtifactRef{}, err
	}
	path := filepath.Join(repositoryRoot, ".partitur", "runs", string(runID), filepath.FromSlash(string(relative)))
	if err := os.Chmod(path, 0o400); err != nil {
		return protocol.ArtifactRef{}, err
	}
	return protocol.ArtifactRef{
		ArtifactID: "partitur.subject-tree",
		Kind:       "partitur/subject-tree+json;v=1",
		InstanceID: fmt.Sprintf("partitur.subject-tree@%s@%d", movement.ID, revision),
		Path:       path,
		Hash:       string(hash),
	}, nil
}

// deliveredInputs freezes the artifact instances that this attempt will send
// to its performer. It reads only the already-materialized state passed by the
// caller; retries and fallbacks therefore resolve the same successful source
// attempt rather than mutable current state.
func deliveredInputs(
	compiled *score.Score,
	movement score.MovementView,
	state runstate.State,
	repositoryRoot string,
	runID runstate.RunID,
	reviewInput *protocol.ArtifactRef,
) ([]protocol.ArtifactRef, error) {
	outputs := make(map[string]score.OutputView)
	producers := make(map[string]runstate.MovementID)
	for _, candidate := range compiled.Movements() {
		for _, output := range candidate.Outputs {
			outputs[output.ArtifactID] = output
			producers[output.ArtifactID] = runstate.MovementID(candidate.ID)
		}
	}
	inputs := make([]protocol.ArtifactRef, 0, len(movement.Inputs)+1)
	for _, artifactID := range movement.Inputs {
		output, exists := outputs[artifactID]
		if !exists {
			return nil, fmt.Errorf("driver: input %q has no declared output", artifactID)
		}
		if output.Kind == "change_set" {
			continue
		}
		result, exists := state.MovementResults[producers[artifactID]]
		if !exists || result.AttemptID == "" {
			return nil, fmt.Errorf("driver: input %q has no successful producer", artifactID)
		}
		instanceID := runstate.ArtifactInstanceID(artifactID + "@" + string(result.AttemptID))
		record, exists := state.Artifacts[instanceID]
		if !exists || record.LogicalOutputID != artifactID || record.Kind != output.Kind {
			return nil, fmt.Errorf("driver: input %q has no matching delivered artifact", artifactID)
		}
		inputs = append(inputs, protocol.ArtifactRef{
			ArtifactID: artifactID,
			Kind:       output.Kind,
			InstanceID: string(instanceID),
			Path: filepath.Join(
				repositoryRoot, ".partitur", "runs", string(runID), "artifacts", artifactID, string(record.AttemptID),
			),
			Hash: string(record.ContentHash),
		})
	}
	if reviewInput != nil {
		inputs = append(inputs, *reviewInput)
	}
	slices.SortFunc(inputs, func(left, right protocol.ArtifactRef) int {
		return strings.Compare(left.ArtifactID, right.ArtifactID)
	})
	return inputs, nil
}

func validateReviewEvidence(repositoryRoot, subjectTree, path string, line *int64) error {
	object := strings.TrimPrefix(subjectTree, "git-sha1:")
	object = strings.TrimPrefix(object, "git-sha256:")
	listed, err := exec.Command("git", "-C", repositoryRoot, "ls-tree", object, "--", path).Output()
	if err != nil || !strings.Contains(string(listed), " blob ") {
		return errors.New("review evidence does not name a regular subject file")
	}
	if line == nil {
		return nil
	}
	contents, err := exec.Command("git", "-C", repositoryRoot, "show", object+":"+path).Output()
	if err != nil {
		return err
	}
	if !validEvidenceLine(contents, *line) {
		return errors.New("review evidence line is outside subject file")
	}
	return nil
}

func validEvidenceLine(contents []byte, line int64) bool {
	if line < 1 || len(contents) == 0 {
		return false
	}
	lineCount := int64(strings.Count(string(contents), "\n")) + 1
	if contents[len(contents)-1] == '\n' {
		lineCount--
	}
	return line <= lineCount
}

func scopedAdapterID(prefix, domain string, attemptID runstate.AttemptID, emittedID string) (string, error) {
	encoded, err := canonical.Encode(map[string]any{
		"attempt_id": string(attemptID), "emitted_id": emittedID,
	})
	if err != nil {
		return "", fmt.Errorf("encode adapter-scoped id: %w", err)
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(domain))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(encoded)
	return fmt.Sprintf("%s%x", prefix, digest.Sum(nil)), nil
}

func admitProbe(
	movement score.MovementView,
	part score.PartView,
	policy score.PolicyView,
	performer cast.PerformerView,
	probe protocol.ProbeResult,
) ([]string, error) {
	missing := cast.MissingCapabilities(part, probe.Capabilities)
	if len(missing) != 0 {
		return nil, fmt.Errorf("%w: %v", ErrCapability, missing)
	}
	enforcement := cast.EvaluateEnforcement(
		movement,
		policy,
		performer.AllowAdvisoryEnforcement,
		probe.Enforcement,
	)
	if enforcement.Disposition == cast.EnforcementRefused {
		return nil, fmt.Errorf("%w: %v", ErrEnforcement, enforcement.Unmet)
	}
	advisory := make([]string, len(enforcement.Unmet))
	for index, dimension := range enforcement.Unmet {
		advisory[index] = string(dimension)
	}
	return advisory, nil
}

func selectAttempt(
	compiled *score.Score,
	resolvedCast *cast.Cast,
	movementID runstate.MovementID,
	performerID string,
) (score.MovementView, score.PartView, cast.PerformerView, *acceptance.Plan, error) {
	if compiled == nil || resolvedCast == nil || movementID == "" || performerID == "" {
		return score.MovementView{}, score.PartView{}, cast.PerformerView{}, nil, ErrAttemptSelectionInconsistent
	}
	var movement score.MovementView
	foundMovement := false
	for _, candidate := range compiled.Movements() {
		if candidate.ID == string(movementID) {
			movement = candidate
			foundMovement = true
			break
		}
	}
	if !foundMovement {
		return score.MovementView{}, score.PartView{}, cast.PerformerView{}, nil, ErrAttemptSelectionInconsistent
	}
	var part score.PartView
	foundPart := false
	for _, candidate := range compiled.Parts() {
		if candidate.ID == movement.PartID {
			part = candidate
			foundPart = true
			break
		}
	}
	performer, foundPerformer := resolvedCast.Performer(performerID)
	if !foundPart || !foundPerformer {
		return score.MovementView{}, score.PartView{}, cast.PerformerView{}, nil, ErrAttemptSelectionInconsistent
	}
	plan, err := acceptance.Compile(movement)
	return movement, part, performer, plan, err
}

func movementSeeds(compiled *score.Score) []runstate.MovementSeed {
	movements := compiled.Movements()
	execution := compiled.Execution()
	result := make([]runstate.MovementSeed, len(movements))
	for index, movement := range movements {
		result[index] = runstate.MovementSeed{
			ID:              runstate.MovementID(movement.ID),
			Initial:         runstate.MovementPending,
			RepoWrite:       hasGrant(movement.Grants, "repo_write"),
			HasDependencies: len(movement.Needs) != 0,
			Final:           movement.ID == execution.FinalMovementID,
		}
	}
	return result
}

func effectiveGrants(
	movement score.MovementView,
	policy score.PolicyView,
) protocol.Grants {
	grants := protocol.Grants{
		PathsRW: []string{},
		PathsRO: []string{},
		Shell:   hasGrant(movement.Grants, "shell"),
		Network: hasGrant(movement.Grants, "network"),
	}
	if hasGrant(movement.Grants, "repo_write") {
		grants.PathsRW = slices.Clone(policy.AllowedPaths)
	}
	if hasGrant(movement.Grants, "repo_read") {
		grants.PathsRO = slices.Clone(policy.AllowedPaths)
	}
	return grants
}

func executeBrief(
	compiled *score.Score,
	movement score.MovementView,
	grants protocol.Grants,
) (protocol.Brief, map[string]any, error) {
	execution := compiled.Execution()
	outputs := make([]protocol.OutputSpec, len(movement.Outputs))
	for index, output := range movement.Outputs {
		outputs[index] = protocol.OutputSpec{
			ArtifactID: output.ArtifactID,
			Kind:       output.Kind,
		}
	}
	global := map[string]any{
		"resolved_questions": resolvedQuestionProjection(compiled.ResolvedQuestions()),
		"effective_paths": map[string]any{
			"rw": stringsToAny(grants.PathsRW),
			"ro": stringsToAny(grants.PathsRO),
		},
		"side_effects_permitted": []any{},
		"protected_paths": []any{
			".partitur/**",
			"partitur.yaml",
			"refs/partitur/**",
		},
	}
	globalBytes, err := json.Marshal(global)
	if err != nil {
		return protocol.Brief{}, nil, err
	}
	hard := make([]any, 0, len(movement.Acceptance.ArtifactCriteria)+len(movement.Acceptance.RunCriteria))
	for _, criterion := range movement.Acceptance.ArtifactCriteria {
		value := map[string]any{
			"id":       criterion.ID,
			"artifact": criterion.ArtifactID,
		}
		if criterion.ExpectedHash != "" {
			value["expected_hash"] = criterion.ExpectedHash
		}
		hard = append(hard, value)
	}
	for _, criterion := range movement.Acceptance.RunCriteria {
		value := map[string]any{"id": criterion.ID, "run": criterion.Argv}
		if criterion.TimeoutMin != 0 {
			value["timeout_min"] = criterion.TimeoutMin
		}
		hard = append(hard, value)
	}
	review := make([]any, len(movement.Acceptance.ReviewCriteria))
	for index, criterion := range movement.Acceptance.ReviewCriteria {
		review[index] = map[string]any{"id": criterion.ID, "findings": criterion.Findings, "rubric": stringsToAny(criterion.Rubrics)}
	}
	acceptanceBytes, err := json.Marshal(map[string]any{
		"hard":       hard,
		"review":     review,
		"human_gate": movement.Acceptance.HumanGate,
	})
	if err != nil {
		return protocol.Brief{}, nil, err
	}
	brief := protocol.Brief{
		Goal:             execution.Goal,
		Instruction:      movement.Instruction,
		Acceptance:       acceptanceBytes,
		GlobalInvariants: globalBytes,
		Outputs:          outputs,
	}
	if execution.ContextPresent {
		brief.Context = execution.Context
	}
	if execution.VerificationExpectationPresent {
		brief.VerificationExpectation, err = json.Marshal(map[string]any{
			"intent": execution.VerificationExpectation,
		})
		if err != nil {
			return protocol.Brief{}, nil, err
		}
	}
	return brief, global, nil
}

func resolvedQuestionProjection(questions []score.ResolvedQuestionView) []any {
	values := make([]any, len(questions))
	for index, question := range questions {
		value := map[string]any{
			"id":       question.ID,
			"question": question.Question,
		}
		if question.ResolutionPresent {
			value["disposition"] = "resolved"
			value["resolution"] = question.Resolution
		} else {
			value["disposition"] = "waived"
		}
		values[index] = value
	}
	return values
}

func executionDependencyHash(
	compiled *score.Score,
	movement score.MovementView,
	part score.PartView,
	performer cast.PerformerView,
	grants protocol.Grants,
	global map[string]any,
	acceptanceHash runstate.Hash,
	baseCompositionHash string,
	inputs []protocol.ArtifactRef,
	resolutions []protocol.ResolvedDecision,
	feedback []protocol.Feedback,
) (string, error) {
	hash, _, err := executionDependencyIdentity(
		compiled,
		movement,
		part,
		performer,
		grants,
		global,
		acceptanceHash,
		baseCompositionHash,
		inputs,
		resolutions,
		feedback,
	)
	return hash, err
}

// executionDependencyIdentity derives the recorded tuple from the A.5 value
// itself. This keeps adapter.probed tied to the domains the projection reached,
// rather than to a second list maintained beside the producer.
func executionDependencyIdentity(
	compiled *score.Score,
	movement score.MovementView,
	part score.PartView,
	performer cast.PerformerView,
	grants protocol.Grants,
	global map[string]any,
	acceptanceHash runstate.Hash,
	baseCompositionHash string,
	inputs []protocol.ArtifactRef,
	resolutions []protocol.ResolvedDecision,
	feedback []protocol.Feedback,
) (string, map[string]any, error) {
	value, err := executionDependencyProjection(
		compiled,
		movement,
		part,
		performer,
		grants,
		global,
		acceptanceHash,
		baseCompositionHash,
		inputs,
		resolutions,
		feedback,
	)
	if err != nil {
		return "", nil, err
	}
	hash, err := canonical.Hash(canonical.DomainExecutionDependency, value)
	if err != nil {
		return "", nil, err
	}
	domains, err := executiondep.V3ProjectionDomains(value)
	if err != nil {
		return "", nil, err
	}
	versions, err := identityVersions(domains...)
	if err != nil {
		return "", nil, err
	}
	return hash, versions, nil
}

func executionDependencyProjection(
	compiled *score.Score,
	movement score.MovementView,
	part score.PartView,
	performer cast.PerformerView,
	grants protocol.Grants,
	global map[string]any,
	acceptanceHash runstate.Hash,
	baseCompositionHash string,
	inputs []protocol.ArtifactRef,
	resolutions []protocol.ResolvedDecision,
	feedback []protocol.Feedback,
) (map[string]any, error) {
	outputs := make([]any, len(movement.Outputs))
	for index, output := range movement.Outputs {
		outputs[index] = map[string]any{
			"artifact_id": output.ArtifactID,
			"kind":        output.Kind,
		}
	}
	inputValues := make([]any, len(inputs))
	for index, input := range inputs {
		inputValues[index] = map[string]any{
			"artifact_id":  input.ArtifactID,
			"kind":         input.Kind,
			"instance_id":  input.InstanceID,
			"content_hash": input.Hash,
		}
	}
	feedbackValues := feedbackProjection(feedback)
	resolutionValues, err := resolutionProjection(resolutions)
	if err != nil {
		return nil, err
	}
	movementValue := map[string]any{
		"id":          movement.ID,
		"part":        movement.PartID,
		"instruction": movement.Instruction,
		"needs":       stringsToAny(movement.Needs),
		"inputs":      inputValues,
		"outputs":     outputs,
		"grants":      stringsToAny(movement.Grants),
		"may_propose": movement.MayPropose,
		"acceptance":  string(acceptanceHash),
	}
	if movement.MayPropose {
		scoreBaseHash, err := compiled.Hash()
		if err != nil {
			return nil, err
		}
		movementValue["score_base_hash"] = scoreBaseHash
	}
	if movement.Phase == "draft" {
		movementValue["phase"] = movement.Phase
	}
	if baseCompositionHash != "" {
		movementValue["base_composition_hash"] = baseCompositionHash
	}
	execution := compiled.Execution()
	scoreValue := map[string]any{
		"goal":                            execution.Goal,
		"global_invariants":               global,
		"verification_expectation_intent": execution.VerificationExpectation,
	}
	if execution.ContextPresent {
		scoreValue["context"] = execution.Context
	}
	value := map[string]any{
		"actual_adapter_id": performer.Adapter,
		"movement":          movementValue,
		"part": map[string]any{
			"capabilities": stringsToAny(part.Capabilities),
			"read_only":    part.ReadOnly,
		},
		"model": performer.Model,
		"authority": map[string]any{
			"paths_rw":     stringsToAny(grants.PathsRW),
			"paths_ro":     stringsToAny(grants.PathsRO),
			"shell":        grants.Shell,
			"network":      grants.Network,
			"side_effects": stringsToAny(compiled.EffectivePolicy().SideEffects),
		},
		"score":              scoreValue,
		"resolved_decisions": resolutionValues,
		"feedback":           feedbackValues,
	}
	if extension, present := performer.Extensions[performer.Adapter]; present {
		value["extensions"] = extension
	}
	return value, nil
}

func resolutionProjection(resolutions []protocol.ResolvedDecision) ([]any, error) {
	values := make([]any, len(resolutions))
	for index, decision := range resolutions {
		body := map[string]any{"kind": string(decision.Kind)}
		switch decision.Kind {
		case protocol.ResolvedDecisionAnswer:
			body["answer"] = decision.Answer
		case protocol.ResolvedDecisionAmendmentRejected:
			body["reason"] = decision.Reason
		default:
			return nil, fmt.Errorf("unknown resolved decision kind %q", decision.Kind)
		}
		digest, err := canonical.Hash(canonical.DomainResolutionBody, body)
		if err != nil {
			return nil, err
		}
		values[index] = map[string]any{
			"decision_id": decision.DecisionID,
			"kind":        string(decision.Kind),
			"digest":      digest,
		}
	}
	return values, nil
}

func feedbackProjection(feedback []protocol.Feedback) []any {
	values := make([]any, len(feedback))
	for index, item := range feedback {
		values[index] = map[string]any{
			"previous_attempt_id":  item.PreviousAttemptID,
			"kind":                 item.Kind,
			"artifact_instance_id": item.ArtifactInstanceID,
			"content_hash":         item.ContentHash,
		}
	}
	return values
}

func movementCompositionDependencyHash(movementID, baseTree string) (string, error) {
	return workspace.MovementCompositionHash(runstate.MovementID(movementID), baseTree, nil, "")
}

func movementCompositionMergeDependencyHash(
	movementID runstate.MovementID,
	baseTree string,
	contributors []workspace.CompositionContributor,
	environmentHash string,
) (string, error) {
	return workspace.MovementCompositionHash(
		movementID, baseTree, contributors, environmentHash,
	)
}

func recognizedFeatures(_ []string) []string {
	return []string{}
}

func capabilityPayload(value protocol.Capabilities) map[string]any {
	return map[string]any{
		"repo_read":          value.RepoRead,
		"repo_write":         value.RepoWrite,
		"shell":              value.Shell,
		"network":            value.Network,
		"resumable_sessions": value.ResumableSessions,
	}
}

func processPayload(identity runstate.ProcessIdentity) map[string]any {
	return map[string]any{
		"pid":            identity.PID,
		"session_id":     identity.SessionID,
		"start_identity": startIdentityPayload(identity.Start),
	}
}

func startIdentityPayload(identity runstate.StartIdentity) map[string]any {
	switch identity := identity.(type) {
	case runstate.LinuxStartIdentity:
		return map[string]any{
			"platform":    "linux",
			"boot_id":     identity.BootID,
			"start_ticks": identity.StartTicks,
		}
	case runstate.DarwinStartIdentity:
		return map[string]any{
			"platform":     "darwin",
			"start_tvsec":  identity.StartTVSec,
			"start_tvusec": identity.StartTVUsec,
		}
	default:
		panic(fmt.Sprintf("unsupported start identity %T", identity))
	}
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

func artifactIDs(
	state runstate.State,
	attemptID runstate.AttemptID,
) []string {
	var result []string
	for id, artifact := range state.Artifacts {
		if artifact.AttemptID == attemptID {
			result = append(result, string(id))
		}
	}
	slices.Sort(result)
	return result
}

func hasGrant(grants []string, target string) bool {
	return slices.Contains(grants, target)
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func formatTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

func budgetTimeout(remainingMS int64) time.Duration {
	const maxDuration = time.Duration(1<<63 - 1)
	const maxDurationMS = int64(maxDuration / time.Millisecond)
	if remainingMS > maxDurationMS {
		return maxDuration
	}
	return time.Duration(remainingMS) * time.Millisecond
}

func initialRemainingMS(activeWallClockMin int64) (int64, error) {
	const maxWireSafeInteger = int64(1<<53 - 1)
	const millisecondsPerMinute = int64(60_000)
	if activeWallClockMin > maxWireSafeInteger/millisecondsPerMinute {
		return 0, errors.New("active wall-clock budget exceeds remaining_ms wire range")
	}
	return activeWallClockMin * millisecondsPerMinute, nil
}

func remainingAfter(remainingMS, chargedMS int64) int64 {
	if chargedMS >= remainingMS {
		return 0
	}
	return remainingMS - chargedMS
}

func dispositionPayload(disposition runstate.Disposition) map[string]any {
	payload := map[string]any{
		"charged":           disposition.Charged,
		"movement_terminal": disposition.MovementTerminal,
	}
	if disposition.TerminalReason != "" {
		payload["terminal_reason"] = disposition.TerminalReason
	}
	return payload
}

// realizeRecordedNoneDisposition realizes Arm 2 from its recorded durable
// disposition. Charged successors return through the live between-unit
// selector before any new adapter is launched.
func realizeRecordedNoneDisposition(
	ctx context.Context,
	result Result,
	store *runstore.Store,
	authority *runstore.Driver,
	control *cancellation.Watcher,
	dependencies dependencies,
) (Result, bool) {
	input, err := store.LoadRunInput(result.RunID)
	if err != nil {
		return interrupted(result, err), true
	}
	current := input.Projection.CurrentHeadAttempt
	if current == nil || current.RecordedDisposition == nil {
		return Result{}, false
	}
	if input.Projection.State.CancelRequested {
		if err := control.Execute(ctx); err != nil {
			if errors.Is(err, runstate.ErrSweepUnverifiable) {
				return halted(result, "sweep_unverifiable", err), true
			}
			return stopped(result, err), true
		}
		result.Outcome = OutcomeCancelled
		return result, true
	}
	// §4 post-effect boundary: Client.Execute returned without a sweep halt, so
	// this driver's adapter session is verified empty and its interval is
	// durably closed. This failure disposition has no required change set.
	if !liveBetweenUnitEntry(input.Projection, store, authority) {
		return interrupted(result, errors.New("driver: live between-unit entry condition is not established")), true
	}

	realization, err := successor.Realize(successor.RealizationInput{
		Disposition:      *current.RecordedDisposition,
		CurrentPerformer: current.FailureClassification.CurrentPerformer,
		Binding: cast.BindingView{
			Fallbacks: current.FailureClassification.Fallbacks,
		},
		VisitedPerformers: current.FailureClassification.VisitedPerformers,
	})
	if err != nil {
		return interrupted(result, err), true
	}
	if realization.Action == successor.ActionPendingSuccessor {
		return liveMaterializeSuccessor(ctx, result, store, authority, control, dependencies, input), true
	}
	if liveNoneRealizationMismatch(realization) {
		return interrupted(result, errors.New("driver: recorded disposition has no live realization")), true
	}
	state := input.Projection.State
	payload, err := json.Marshal(map[string]any{
		"reason": realization.TerminalReason, "run_failed": false,
	})
	if err != nil {
		return interrupted(result, err), true
	}
	if _, err := authority.Append(runstate.Event{
		RunID: result.RunID, ScoreRevision: state.ScoreHead.Revision,
		MovementID: current.MovementID, AttemptID: current.AttemptID,
		Type: runstate.EventMovementFailed, Payload: payload,
	}, faultpoint.ReceiptAddress("movement.failed")); err != nil {
		return stopped(result, err), true
	}
	dependencies.probe.Reached(faultpoint.PointLifecycleMovementFailed)
	if dependencies.afterMovementFailed != nil {
		dependencies.afterMovementFailed()
	}

	input, err = store.LoadRunInput(result.RunID)
	if err != nil {
		return interrupted(result, err), true
	}
	if input.Projection.State.CancelRequested {
		if err := control.Execute(ctx); err != nil {
			if errors.Is(err, runstate.ErrSweepUnverifiable) {
				return halted(result, "sweep_unverifiable", err), true
			}
			return stopped(result, err), true
		}
		result.Outcome = OutcomeCancelled
		return result, true
	}
	runFailureReason := "movement_failed"
	if realization.TerminalReason == successor.KindBudgetExhausted {
		runFailureReason = successor.KindBudgetExhausted
	}
	payload, err = json.Marshal(map[string]any{"reason": runFailureReason})
	if err != nil {
		return interrupted(result, err), true
	}
	if _, err := authority.Append(runstate.Event{
		RunID: result.RunID, ScoreRevision: input.Projection.State.ScoreHead.Revision,
		Type: runstate.EventRunFailed, Payload: payload,
	}, faultpoint.ReceiptAddress("run.failed")); err != nil {
		return stopped(result, err), true
	}
	dependencies.probe.Reached(faultpoint.PointLifecycleRunFailed)
	input, err = store.LoadRunInput(result.RunID)
	if err != nil {
		return interrupted(result, err), true
	}
	if liveFailedTerminalProjectionAbsent(input.Projection.State.Run) {
		return interrupted(result, errors.New("driver: durable failed terminal projection is absent")), true
	}
	result.Outcome = OutcomeFailed
	result.Reason = runFailureReason
	result.Err = nil
	return result, true
}

// liveMaterializeSuccessor performs exactly one durable live continuation.
// Every invocation appends one performer.selected and reloads the journal.
// Revision restarts and decision resumes have no retry-policy attempt bound;
// progress is instead the durable effect and re-projection at each selection cut.
func liveMaterializeSuccessor(
	ctx context.Context,
	result Result,
	store *runstore.Store,
	authority *runstore.Driver,
	control *cancellation.Watcher,
	dependencies dependencies,
	input runstore.RunInput,
) Result {
	current := input.Projection.CurrentHeadAttempt
	if current == nil {
		return interrupted(result, errors.New("driver: pending successor has no current attempt"))
	}
	pending := input.Projection.Scheduler.PendingSuccessor
	if pending == nil {
		return interrupted(result, errors.New("driver: charged disposition has no pending successor"))
	}
	if !liveBetweenUnitEntry(input.Projection, store, authority) {
		return interrupted(result, errors.New("driver: live between-unit entry condition is not established"))
	}
	decision := recovery.PlanBetweenUnit(input.Projection)
	if decision.CaseID == recovery.CaseBudgetExhausted {
		return liveBudgetExhaustion(result, store, authority, input, decision.Action)
	}
	if liveMaterializationMismatch(decision.Action, pending) {
		return interrupted(result, errors.New("driver: live selector did not materialize recorded successor"))
	}
	if decision.CaseID != recovery.CaseScheduler {
		return interrupted(result, errors.New("driver: successor materialization has unexpected selector case"))
	}
	performer, ok := input.Cast.Performer(pending.Performer)
	if !ok {
		return interrupted(result, fmt.Errorf("driver: pending successor performer %q is absent from resolved cast", pending.Performer))
	}
	causationID, err := latestFailureEventID(store, result.RunID, pending.AttemptID)
	if err != nil {
		return interrupted(result, err)
	}
	base, err := PrepareSuccessorBase(store, authority, input, pending.MovementID, input.Projection.Scheduler.RemainingTime, dependencies.now, dependencies.newID)
	if err != nil {
		if errors.Is(err, ErrCompositionBudgetExhausted) {
			return liveCompositionBudgetExhaustion(result, store, authority)
		}
		if errors.Is(err, ErrCompositionCancelled) {
			if err := control.Execute(ctx); err != nil {
				if errors.Is(err, runstate.ErrSweepUnverifiable) {
					return halted(result, "sweep_unverifiable", err)
				}
				return stopped(result, err)
			}
			return Result{RunID: result.RunID, Outcome: OutcomeCancelled}
		}
		if errors.Is(err, ErrCompositionTerminalized) {
			return Result{RunID: result.RunID, Outcome: OutcomeFailed, Reason: "composition_terminal"}
		}
		return stopped(result, err)
	}
	attempt, err := workspace.CreateRecoveredAttemptAtBase(store, authority, input, string(pending.MovementID), base.Commit)
	if err != nil {
		return stopped(result, err)
	}
	selection, err := workspace.PerformerSelectedEvent(
		attempt, input.Projection.State.ScoreHead.Revision, performer.ID, performer.Adapter, performer.Model,
		pending.Reason, causationID,
	)
	if err != nil {
		return interrupted(result, err)
	}
	before, err := store.ReadJournal(result.RunID)
	if err != nil {
		return stopped(result, err)
	}
	if _, err := authority.Append(selection, faultpoint.ReceiptAddress("attempt.performer_selected")); err != nil {
		return stopped(result, err)
	}
	next, err := store.LoadRunInput(result.RunID)
	if err != nil {
		return interrupted(result, err)
	}
	after, err := store.ReadJournal(result.RunID)
	if err != nil {
		return stopped(result, err)
	}
	if len(after.Events) <= len(before.Events) {
		return interrupted(result, errors.New("driver: live successor made no durable journal progress"))
	}
	if next.Projection.CurrentHeadAttempt == nil {
		return interrupted(result, errors.New("driver: durable successor projection is absent"))
	}
	if next.Projection.CurrentHeadAttempt.AttemptID != attempt.AttemptID {
		return interrupted(result, errors.New("driver: durable successor projection selected another attempt"))
	}
	if next.Projection.Scheduler.PendingSuccessor != nil {
		return interrupted(result, errors.New("driver: durable successor remains pending after selection"))
	}
	return ExecuteAttempt(ctx, AttemptExecution{
		RepositoryRoot:       store.RepositoryRoot(),
		Score:                next.Score,
		Cast:                 next.Cast,
		RunID:                result.RunID,
		Attempt:              attempt,
		BaseTree:             base.Tree,
		BaseCompositionHash:  base.Hash,
		CandidateTree:        liveCandidateTree(next),
		Authority:            authority,
		PerformerID:          performer.ID,
		SelectionReason:      pending.Reason,
		SelectionCausationID: causationID,
		SelectionDurable:     true,
		RemainingMS:          next.Projection.Scheduler.RemainingTime,
		RetriesConsumed:      next.Projection.CurrentHeadAttempt.FailureClassification.RetriesConsumed,
		VisitedPerformers:    append([]string(nil), next.Projection.CurrentHeadAttempt.FailureClassification.VisitedPerformers...),
		Control:              control,
	}, executionDependenciesFrom(dependencies))
}

func liveCompositionBudgetExhaustion(result Result, store *runstore.Store, authority *runstore.Driver) Result {
	input, err := store.LoadRunInput(result.RunID)
	if err != nil {
		return interrupted(result, err)
	}
	decision := recovery.PlanBetweenUnit(input.Projection)
	if decision.CaseID != recovery.CaseBudgetExhausted {
		return interrupted(result, errors.New("driver: composition budget close did not select budget exhaustion"))
	}
	return liveBudgetExhaustion(result, store, authority, input, decision.Action)
}

// liveBudgetExhaustion realizes RC-RESUME-045 at a zero-budget selection cut.
// A RUNNING movement fails before the run; between movements the run fails
// directly, because there is no live movement to terminalize.
func liveBudgetExhaustion(
	result Result,
	store *runstore.Store,
	authority *runstore.Driver,
	input runstore.RunInput,
	action *recovery.Action,
) Result {
	if action == nil {
		return interrupted(result, errors.New("driver: budget exhaustion has no action"))
	}
	if action.Kind == recovery.ActionAppendBudgetFailure {
		if action.MovementID == "" || input.Projection.State.Movements[action.MovementID] != runstate.MovementRunning {
			return interrupted(result, errors.New("driver: budget exhaustion has no running movement action"))
		}
		payload, err := json.Marshal(map[string]any{"reason": "budget_exhausted", "run_failed": false})
		if err != nil {
			return interrupted(result, err)
		}
		if _, err := authority.Append(runstate.Event{
			RunID: result.RunID, ScoreRevision: input.Projection.State.ScoreHead.Revision,
			MovementID: action.MovementID, Type: runstate.EventMovementFailed, Payload: payload,
		}, faultpoint.ReceiptAddress("movement.failed.budget_exhausted")); err != nil {
			return stopped(result, err)
		}
		input, err = store.LoadRunInput(result.RunID)
		if err != nil {
			return interrupted(result, err)
		}
	} else if action.Kind != recovery.ActionAppendRunFailed {
		return interrupted(result, fmt.Errorf("driver: unsupported budget exhaustion action %s", action.Kind))
	}
	payload, err := json.Marshal(map[string]any{"reason": action.FailureReason})
	if err != nil {
		return interrupted(result, err)
	}
	if _, err := authority.Append(runstate.Event{
		RunID: result.RunID, ScoreRevision: input.Projection.State.ScoreHead.Revision,
		Type: runstate.EventRunFailed, Payload: payload,
	}, faultpoint.ReceiptAddress("run.failed.budget_exhausted")); err != nil {
		return stopped(result, err)
	}
	input, err = store.LoadRunInput(result.RunID)
	if err != nil {
		return interrupted(result, err)
	}
	if liveFailedTerminalProjectionAbsent(input.Projection.State.Run) {
		return interrupted(result, errors.New("driver: durable failed terminal projection is absent"))
	}
	result.Outcome = OutcomeFailed
	result.Reason = action.FailureReason
	result.Err = nil
	return result
}

func liveMaterializationMismatch(action *recovery.Action, pending *recovery.PendingSuccessor) bool {
	return action == nil || action.Kind != recovery.ActionMaterializeSuccessor ||
		action.MovementID != pending.MovementID || action.PendingSuccessor == nil
}

func latestFailureEventID(store *runstore.Store, runID runstate.RunID, attemptID runstate.AttemptID) (string, error) {
	journal, err := store.ReadJournal(runID)
	if err != nil {
		return "", err
	}
	for index := len(journal.Events) - 1; index >= 0; index-- {
		event := journal.Events[index]
		if event.AttemptID != attemptID {
			continue
		}
		if event.Type == runstate.EventAttemptFailed || event.Type == runstate.EventAcceptanceFailed {
			if event.EventID == "" {
				return "", errors.New("driver: successor failure causation_id is absent")
			}
			return event.EventID, nil
		}
	}
	return "", errors.New("driver: successor failure event is absent")
}

func liveMovementAttemptCount(state runstate.State, movementID runstate.MovementID) int {
	count := 0
	for _, attempt := range state.Attempts {
		if attempt.MovementID == movementID {
			count++
		}
	}
	return count
}

func journalAttemptNumber(store *runstore.Store, runID runstate.RunID, movementID runstate.MovementID) (int, error) {
	input, err := store.LoadRunInput(runID)
	if err != nil {
		return 0, err
	}
	count := liveMovementAttemptCount(input.Projection.State, movementID)
	if count == 0 {
		return 0, errors.New("driver: attempt.started has no durable performer selection")
	}
	return count, nil
}

func liveFailedTerminalProjectionAbsent(run runstate.RunLifecycle) bool {
	return run != runstate.RunFailed
}

func liveSelectionMismatch(action *recovery.Action, want recovery.ActionKind, movementID runstate.MovementID) bool {
	return action == nil || action.Kind != want || action.MovementID != movementID
}

func liveNoneRealizationMismatch(realization successor.Realization) bool {
	return realization.Action != successor.ActionMovementFailed || realization.Charge != successor.ChargeNone
}

func liveBetweenUnitEntry(
	projection recovery.Projection,
	store *runstore.Store,
	authority *runstore.Driver,
) bool {
	// The journal cannot attest the session and change-set conjuncts of §6's
	// local post-effect boundary. Callers with a completed effect must establish
	// them before this check; the direct failure-disposition call above is after
	// Client.Execute's §4 cleanup. selectLiveBetweenUnit is used only before the
	// first effect, when those conjuncts are vacuous. The test below pins both
	// call sites so a new pre-cleanup entry is rejected in review and CI.
	state := projection.State
	lease, present, err := store.ReadLease(authority.RunID())
	if err != nil || !present || state.Run.Terminal() || state.Authority.Owner == nil ||
		state.Authority.Epoch != lease.Epoch || state.Authority.Owner.PID != lease.PID ||
		!reflect.DeepEqual(state.Authority.Owner.Start, lease.Start) ||
		!authority.MatchesLease(lease.Identity()) || state.OpenExecution != nil {
		return false
	}
	current := projection.CurrentHeadAttempt
	return current == nil || current.ScoreRevision != state.ScoreHead.Revision ||
		(current.State != runstate.AttemptStarting && current.State != runstate.AttemptRunning && current.State != runstate.AttemptVerifying)
}

func selectLiveBetweenUnit(store *runstore.Store, authority *runstore.Driver) (recovery.Decision, error) {
	input, err := store.LoadRunInput(authority.RunID())
	if err != nil {
		return recovery.Decision{}, err
	}
	// This initial-scheduling path has no completed driver effect yet; §6's
	// session and change-set conjuncts are therefore vacuous.
	if !liveBetweenUnitEntry(input.Projection, store, authority) {
		return recovery.Decision{}, errors.New("driver: live between-unit entry condition is not established")
	}
	decision := recovery.PlanBetweenUnit(input.Projection)
	if !decision.Valid() {
		return recovery.Decision{}, errors.New("driver: compiled lifecycle has no between-unit action")
	}
	return decision, nil
}

func stopped(result Result, err error) Result {
	switch {
	case errors.Is(err, workspace.ErrGitUnverifiable):
		return halted(result, "git_unverifiable", err)
	case errors.Is(err, runstore.ErrJournalCorrupt):
		return halted(result, "journal_corrupt", err)
	case errors.Is(err, runstore.ErrJournalIdempotencyConflict):
		return halted(result, "journal_idempotency_conflict", err)
	case errors.Is(err, runstore.ErrLeaseOwnerUnverifiable):
		return halted(result, "owner_unverifiable", err)
	default:
		return interrupted(result, err)
	}
}

func interrupted(result Result, err error) Result {
	result.Outcome = OutcomeInterrupted
	result.Reason = ""
	result.Err = err
	return result
}

func releasesDriverLease(outcome Outcome) bool {
	return outcome != OutcomeHalted && outcome != OutcomeCancelled
}

func cancellationResult(ctx context.Context, result Result, control *cancellation.Watcher) (Result, bool) {
	if err := control.Err(); err != nil {
		return stopped(result, err), true
	}
	select {
	case <-control.Cancelled():
		if err := control.Execute(ctx); err != nil {
			if errors.Is(err, runstate.ErrSweepUnverifiable) {
				return halted(result, "sweep_unverifiable", err), true
			}
			return stopped(result, err), true
		}
		result.Outcome = OutcomeCancelled
		result.Reason = ""
		result.Err = nil
		return result, true
	default:
		return Result{}, false
	}
}

func controlResult(ctx context.Context, result Result, store *runstore.Store, authority *runstore.Driver, control *cancellation.Watcher) (Result, bool) {
	if cancelled, handled := cancellationResult(ctx, result, control); handled {
		return cancelled, true
	}
	select {
	case <-control.Prepared():
		store.Reached(faultpoint.PointPrepareObserved)
		if err := store.AcknowledgePrepare(ctx, authority, control.PrepareID()); err != nil {
			if cancelled, handled := cancellationResult(ctx, result, control); handled {
				return cancelled, true
			}
			return stopped(result, err), true
		}
		result.Outcome = OutcomeWaitingHuman
		result.Reason = ""
		result.Err = nil
		result.prepareAcknowledged = true
		return result, true
	default:
		return Result{}, false
	}
}

func halted(result Result, reason string, err error) Result {
	result.Outcome = OutcomeHalted
	result.Reason = reason
	result.Err = err
	return result
}
