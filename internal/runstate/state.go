package runstate

import "fmt"

// NewState creates the projection input supplied by the authenticated pinned
// score. Invalid seeds are programmer errors.
func NewState(seed []MovementSeed) State {
	state := State{
		Movements:         make(map[MovementID]MovementState, len(seed)),
		Attempts:          make(map[AttemptID]Attempt),
		AdapterLaunches:   make(map[AttemptID]AdapterLaunch),
		CriterionLaunches: make(map[CriterionLaunchKey]CriterionLaunch),
		Acceptances:       make(map[AttemptID]Acceptance),
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
	}
	return state
}

func cloneState(input State) State {
	output := input
	output.Movements = cloneMap(input.Movements)
	output.Attempts = cloneMap(input.Attempts)
	for id, attempt := range output.Attempts {
		if attempt.Failure != nil {
			failure := *attempt.Failure
			attempt.Failure = &failure
			output.Attempts[id] = attempt
		}
	}
	output.AdapterLaunches = cloneMap(input.AdapterLaunches)
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
