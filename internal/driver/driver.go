package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/acceptance"
	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
	"github.com/BeomSeogKim/Partitur/internal/validate"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

var (
	ErrUnsupportedSlice = errors.New("score is outside the one-movement artifact slice")
	ErrCapability       = errors.New("capability_unavailable")
	ErrEnforcement      = errors.New("enforcement_unavailable")
)

type dependencies struct {
	probe             faultpoint.Probe
	client            *adapter.Client
	resolveTrampoline func() (string, error)
	now               func() time.Time
	newID             func() (string, error)
	workspaceStart    func(*validate.Preparation, faultpoint.Probe) (workspace.StartResult, error)
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
		Probe:             dependencies.probe,
		Client:            dependencies.client,
		ResolveTrampoline: dependencies.resolveTrampoline,
		Now:               dependencies.now,
		NewID:             dependencies.newID,
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
	movement, _, performer, _, err := selectSlice(preparation)
	if err != nil {
		return Result{Err: err}
	}
	startResult, err := dependencies.workspaceStart(preparation, dependencies.probe)
	if err != nil {
		return Result{Err: err}
	}
	result = Result{RunID: startResult.RunID}
	if err := started(startResult.RunID); err != nil {
		return interrupted(result, err)
	}
	policy := preparation.Score.EffectivePolicy()
	remainingMS, err := initialRemainingMS(policy.ActiveWallClockMin)
	if err != nil {
		return interrupted(result, err)
	}

	seeds := movementSeeds(preparation.Score)
	store, err := runstore.New(preparation.RepositoryRoot, dependencies.probe)
	if err != nil {
		return stopped(result, err)
	}
	authority, err := store.AcquireDriver(startResult.RunID, seeds)
	if err != nil {
		return stopped(result, err)
	}
	defer func() {
		if !releasesDriverLease(result.Outcome) {
			return
		}
		if err := authority.Release(); err != nil && result.Err == nil {
			result.Err = err
		}
	}()
	if err := startResult.Run.BindDriver(authority); err != nil {
		return stopped(result, err)
	}
	candidate, err := startResult.Run.RecordZeroWriterCandidate()
	if err != nil {
		return stopped(result, err)
	}
	attempt, err := startResult.Run.CreateAttempt(movement.ID)
	if err != nil {
		return stopped(result, err)
	}
	for _, transition := range []runstate.EventType{
		runstate.EventMovementReady,
		runstate.EventMovementStarted,
	} {
		event := runstate.Event{
			RunID:         startResult.RunID,
			ScoreRevision: preparation.Score.Revision(),
			MovementID:    runstate.MovementID(movement.ID),
			Type:          transition,
			Payload:       json.RawMessage(`{}`),
		}
		if _, err := authority.Append(
			event,
			faultpoint.ReceiptAddress("movement."+string(transition)),
		); err != nil {
			return stopped(result, err)
		}
	}
	return ExecuteAttempt(ctx, AttemptExecution{
		RepositoryRoot:  preparation.RepositoryRoot,
		Score:           preparation.Score,
		Cast:            preparation.Cast,
		RunID:           startResult.RunID,
		Attempt:         attempt,
		CandidateTree:   candidate.ResultTree,
		Authority:       authority,
		PerformerID:     performer.ID,
		SelectionReason: "initial",
		RemainingMS:     remainingMS,
	}, executionDependenciesFrom(dependencies))
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
		execution.RunID == "" || execution.Attempt == nil || execution.CandidateTree == "" ||
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
	startResult := workspace.StartResult{RunID: execution.RunID}
	attempt := execution.Attempt
	candidate := workspace.Candidate{ResultTree: execution.CandidateTree}
	authority := execution.Authority
	remainingMS := execution.RemainingMS
	dependencies := dependencies{
		probe:             executionDependencies.Probe,
		client:            executionDependencies.Client,
		resolveTrampoline: executionDependencies.ResolveTrampoline,
		now:               executionDependencies.Now,
		newID:             executionDependencies.NewID,
	}
	result = Result{RunID: execution.RunID}
	policy := execution.Score.EffectivePolicy()
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
		}
		return receipt, nil
	}
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
		execution.Score.Execution(),
		movement,
		grants,
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

	executionVersions, err := identityVersions(canonical.DomainExecutionDependency)
	if err != nil {
		return interrupted(result, err)
	}
	attemptVersions, err := identityVersions()
	if err != nil {
		return interrupted(result, err)
	}
	var advisory []string
	var executionHash string
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
		executionHash, err = executionDependencyHash(
			execution.Score,
			movement,
			part,
			performer,
			grants,
			globalInvariants,
			plan.Hash(),
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
			"advisory_dimensions":       advisory,
			"execution_dependency_hash": executionHash,
			"identity_versions":         executionVersions,
		}, "attempt.adapter_probed")
	}
	recordIdentity := func(
		identity runstate.ProcessIdentity,
	) (faultpoint.DurabilityReceipt, error) {
		return appendEvent(runstate.EventAttemptStarted, map[string]any{
			"attempt_number":    1,
			"adapter_process":   processPayload(identity),
			"granted_authority": grants,
			"identity_versions": attemptVersions,
		}, "attempt.started")
	}
	var observationErr error
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
			return appendEvent(runstate.EventExecutionStopped, map[string]any{
				"interval_id":      stop.IntervalID,
				"reason":           stop.Reason,
				"charging":         stop.Charging,
				"charged_duration": stop.ChargedDurationMS,
			}, "execution.adapter.stopped")
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
				payload := map[string]any{
					"kind": kind,
					"disposition": map[string]any{
						"charged":           "none",
						"movement_terminal": true,
					},
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
		Probe:          dependencies.probe,
		RecordIdentity: recordIdentity,
		Recorder:       recorder,
	})
	cancel()
	if err != nil {
		var halt *adapter.HaltError
		if errors.As(err, &halt) {
			return halted(result, "sweep_unverifiable", err)
		}
		return stopped(result, err)
	}
	if report.Result == nil || report.Result.Outcome != protocol.OutcomeCompleted {
		return interrupted(result, errors.New("adapter did not complete"))
	}

	if _, err := attempt.VerifyReadOnlyAndRecord(); err != nil {
		return stopped(result, err)
	}
	acceptanceInterval, err := dependencies.newID()
	if err != nil {
		return interrupted(result, err)
	}
	state, err := authority.State()
	if err != nil {
		return stopped(result, err)
	}
	acceptanceOpened := dependencies.now()
	remainingMS -= state.ConsumedBudgetMS
	if remainingMS < 0 {
		remainingMS = 0
	}
	if _, err := appendEvent(runstate.EventExecutionStarted, map[string]any{
		"interval_id":        acceptanceInterval,
		"phase":              "acceptance",
		"wall_start":         formatTime(acceptanceOpened),
		"remaining_at_start": remainingMS,
	}, "execution.acceptance.started"); err != nil {
		return stopped(result, err)
	}
	evaluation, err := acceptance.Evaluate(plan, acceptance.Evaluation{
		RunID:              startResult.RunID,
		ScoreRevision:      execution.Score.Revision(),
		MovementID:         base.MovementID,
		PartID:             base.PartID,
		AttemptID:          base.AttemptID,
		SubjectTree:        candidate.ResultTree,
		FailureDisposition: runstate.Disposition{Charged: "none", MovementTerminal: true},
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
		Append: func(event runstate.Event) (faultpoint.DurabilityReceipt, error) {
			return authority.Append(
				event,
				faultpoint.ReceiptAddress(
					"acceptance."+string(event.Type),
				),
			)
		},
	})
	if err != nil {
		return stopped(result, err)
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
	if !evaluation.EvaluationCompleted || !evaluation.Verified {
		return interrupted(result, errors.New("attempt did not earn VERIFIED"))
	}
	if _, err := appendEvent(
		runstate.EventAttemptCompleted,
		map[string]any{},
		"attempt.completed",
	); err != nil {
		return stopped(result, err)
	}
	state, err = authority.State()
	if err != nil {
		return stopped(result, err)
	}
	artifactIDs := artifactIDs(state, attempt.AttemptID)
	movementVersions, err := identityVersions()
	if err != nil {
		return interrupted(result, err)
	}
	if _, err := appendEvent(runstate.EventMovementSucceeded, map[string]any{
		"approved_artifact_instance_ids": artifactIDs,
		"identity_versions":              movementVersions,
		"run_succeeded":                  true,
	}, "movement.succeeded"); err != nil {
		return stopped(result, err)
	}
	result.Outcome = OutcomeSucceeded
	return result
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

func selectSlice(
	preparation *validate.Preparation,
) (
	score.MovementView,
	score.PartView,
	cast.PerformerView,
	*acceptance.Plan,
	error,
) {
	movements := preparation.Score.Movements()
	execution := preparation.Score.Execution()
	if len(movements) != 1 || execution.FinalMovementID != movements[0].ID {
		return score.MovementView{}, score.PartView{}, cast.PerformerView{},
			nil, ErrUnsupportedSlice
	}
	var part score.PartView
	found := false
	for _, candidate := range preparation.Score.Parts() {
		if candidate.ID == movements[0].PartID {
			part, found = candidate, true
			break
		}
	}
	binding, bound := preparation.Cast.Binding(movements[0].PartID)
	performer, exists := preparation.Cast.Performer(binding.Performer)
	if !found || !bound || !exists {
		return score.MovementView{}, score.PartView{}, cast.PerformerView{},
			nil, ErrUnsupportedSlice
	}
	plan, err := acceptance.Compile(movements[0])
	return movements[0], part, performer, plan, err
}

func selectAttempt(
	compiled *score.Score,
	resolvedCast *cast.Cast,
	movementID runstate.MovementID,
	performerID string,
) (score.MovementView, score.PartView, cast.PerformerView, *acceptance.Plan, error) {
	if compiled == nil || resolvedCast == nil || movementID == "" || performerID == "" {
		return score.MovementView{}, score.PartView{}, cast.PerformerView{}, nil, ErrUnsupportedSlice
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
		return score.MovementView{}, score.PartView{}, cast.PerformerView{}, nil, ErrUnsupportedSlice
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
		return score.MovementView{}, score.PartView{}, cast.PerformerView{}, nil, ErrUnsupportedSlice
	}
	plan, err := acceptance.Compile(movement)
	return movement, part, performer, plan, err
}

func movementSeeds(compiled *score.Score) []runstate.MovementSeed {
	movements := compiled.Movements()
	result := make([]runstate.MovementSeed, len(movements))
	for index, movement := range movements {
		result[index] = runstate.MovementSeed{
			ID:        runstate.MovementID(movement.ID),
			Initial:   runstate.MovementPending,
			RepoWrite: hasGrant(movement.Grants, "repo_write"),
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
	execution score.ExecutionView,
	movement score.MovementView,
	grants protocol.Grants,
) (protocol.Brief, map[string]any, error) {
	outputs := make([]protocol.OutputSpec, len(movement.Outputs))
	for index, output := range movement.Outputs {
		outputs[index] = protocol.OutputSpec{
			ArtifactID: output.ArtifactID,
			Kind:       output.Kind,
		}
	}
	global := map[string]any{
		"resolved_questions": []any{},
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
	hard := make([]any, len(movement.Acceptance.ArtifactCriteria))
	for index, criterion := range movement.Acceptance.ArtifactCriteria {
		value := map[string]any{
			"id":       criterion.ID,
			"artifact": criterion.ArtifactID,
		}
		if criterion.ExpectedHash != "" {
			value["expected_hash"] = criterion.ExpectedHash
		}
		hard[index] = value
	}
	acceptanceBytes, err := json.Marshal(map[string]any{
		"hard":       hard,
		"review":     []any{},
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

func executionDependencyHash(
	compiled *score.Score,
	movement score.MovementView,
	part score.PartView,
	performer cast.PerformerView,
	grants protocol.Grants,
	global map[string]any,
	acceptanceHash runstate.Hash,
) (string, error) {
	outputs := make([]any, len(movement.Outputs))
	for index, output := range movement.Outputs {
		outputs[index] = map[string]any{
			"artifact_id": output.ArtifactID,
			"kind":        output.Kind,
		}
	}
	movementValue := map[string]any{
		"id":          movement.ID,
		"part":        movement.PartID,
		"instruction": movement.Instruction,
		"needs":       []any{},
		"inputs":      []any{},
		"outputs":     outputs,
		"grants":      stringsToAny(movement.Grants),
		"may_propose": movement.MayPropose,
		"acceptance":  string(acceptanceHash),
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
		"resolved_decisions": []any{},
		"feedback":           []any{},
	}
	if extension, present := performer.Extensions[performer.Adapter]; present {
		value["extensions"] = extension
	}
	return canonical.Hash(canonical.DomainExecutionDependency, value)
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

func stopped(result Result, err error) Result {
	switch {
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
	return outcome != OutcomeHalted
}

func halted(result Result, reason string, err error) Result {
	result.Outcome = OutcomeHalted
	result.Reason = reason
	result.Err = err
	return result
}
