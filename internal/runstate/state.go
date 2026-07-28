package runstate

import "fmt"

// NewState creates the projection input supplied by the authenticated pinned
// score. Invalid seeds are programmer errors.
func NewState(seed []MovementSeed) State {
	state := State{
		Movements:           make(map[MovementID]MovementState, len(seed)),
		RepoWriteMovements:  make(map[MovementID]bool),
		Attempts:            make(map[AttemptID]Attempt),
		AdapterLaunches:     make(map[AttemptID]AdapterLaunch),
		AdapterObservations: make(map[AttemptID]AdapterObservation),
		Artifacts:           make(map[ArtifactInstanceID]ArtifactRecord),
		VerifiedAttempts:    make(map[AttemptID]bool),
		MovementResults:     make(map[MovementID]MovementResult),
		CriterionLaunches:   make(map[CriterionLaunchKey]CriterionLaunch),
		Acceptances:         make(map[AttemptID]Acceptance),
		Application:         ApplicationProjection{State: ApplicationNotApplied},
		Promotion:           PromotionProjection{State: PromotionNotPromoted},
	}
	for _, movement := range seed {
		if movement.ID == "" {
			panic("runstate: empty movement id in seed")
		}
		if movement.Initial != MovementPending && movement.Initial != MovementInapplicable {
			panic(fmt.Sprintf("runstate: illegal initial state %q for movement %q", movement.Initial, movement.ID))
		}
		if _, duplicate := state.Movements[movement.ID]; duplicate {
			panic(fmt.Sprintf("runstate: duplicate movement %q in seed", movement.ID))
		}
		state.Movements[movement.ID] = movement.Initial
		if movement.RepoWrite {
			state.RepoWriteMovements[movement.ID] = true
		}
	}
	return state
}

func cloneState(input State) State {
	output := input
	output.Movements = cloneMap(input.Movements)
	output.RepoWriteMovements = cloneMap(input.RepoWriteMovements)
	output.Attempts = cloneMap(input.Attempts)
	for id, attempt := range output.Attempts {
		if attempt.Failure != nil {
			failure := *attempt.Failure
			attempt.Failure = &failure
			output.Attempts[id] = attempt
		}
	}
	output.AdapterLaunches = cloneMap(input.AdapterLaunches)
	output.AdapterObservations = cloneMap(input.AdapterObservations)
	for id, observation := range output.AdapterObservations {
		observation.Capabilities = cloneMap(observation.Capabilities)
		observation.Enforcement = cloneMap(observation.Enforcement)
		observation.NegotiatedFeatures = append([]string(nil), observation.NegotiatedFeatures...)
		observation.TruncatedResolutions = append([]string(nil), observation.TruncatedResolutions...)
		observation.AdvisoryDimensions = append([]string(nil), observation.AdvisoryDimensions...)
		observation.IdentityVersions = append([]byte(nil), observation.IdentityVersions...)
		output.AdapterObservations[id] = observation
	}
	output.Artifacts = cloneMap(input.Artifacts)
	output.VerifiedAttempts = cloneMap(input.VerifiedAttempts)
	output.MovementResults = cloneMap(input.MovementResults)
	for id, result := range output.MovementResults {
		result.ApprovedArtifactInstanceIDs = append(
			[]ArtifactInstanceID(nil),
			result.ApprovedArtifactInstanceIDs...,
		)
		output.MovementResults[id] = result
	}
	if input.ApplicationCandidate != nil {
		candidate := *input.ApplicationCandidate
		candidate.OrderedChangeSets = append([]string(nil), input.ApplicationCandidate.OrderedChangeSets...)
		candidate.Contributors = append(
			[]CandidateContributor(nil),
			input.ApplicationCandidate.Contributors...,
		)
		candidate.IdentityVersions = append([]byte(nil), input.ApplicationCandidate.IdentityVersions...)
		output.ApplicationCandidate = &candidate
	}
	output.CriterionLaunches = cloneMap(input.CriterionLaunches)
	output.Acceptances = make(map[AttemptID]Acceptance, len(input.Acceptances))
	for id, acceptance := range input.Acceptances {
		acceptance.PlannedCriterionIDs = append([]CriterionID(nil), acceptance.PlannedCriterionIDs...)
		acceptance.Criteria = cloneMap(acceptance.Criteria)
		output.Acceptances[id] = acceptance
	}
	if input.Authority.Owner != nil {
		owner := *input.Authority.Owner
		output.Authority.Owner = &owner
	}
	if input.PendingPrepare != nil {
		prepare := *input.PendingPrepare
		prepare.TargetAttemptIDs = append([]AttemptID(nil), input.PendingPrepare.TargetAttemptIDs...)
		prepare.IdentityVersions = append([]byte(nil), input.PendingPrepare.IdentityVersions...)
		output.PendingPrepare = &prepare
	}
	if input.OpenExecution != nil {
		interval := *input.OpenExecution
		output.OpenExecution = &interval
	}
	return output
}

func cloneMap[K comparable, V any](input map[K]V) map[K]V {
	output := make(map[K]V, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
