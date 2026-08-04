package score

import (
	"fmt"
	"slices"
	"sort"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
)

// Change is one typed score-AST change. Hashes bind a value to its semantic
// selector; they are absent on the side that did not exist.
type Change struct {
	Selector   string
	Operation  string
	BeforeHash string
	AfterHash  string
}

// SetChange describes exact-string set membership changes.
type SetChange struct {
	Added   []string
	Removed []string
}

// GrantChange describes a movement's grant-set change.
type GrantChange struct {
	MovementID string
	Added      []string
	Removed    []string
}

// BudgetChange describes one numeric budget change.
type BudgetChange struct {
	From int64
	To   int64
}

// Impact is the normative actual_impact / claimed_impact shape from §9.
type Impact struct {
	ScoreChanges []Change
	Authority    AuthorityImpact
	Budget       BudgetImpact
}

type AuthorityImpact struct {
	AllowedPaths SetChange
	Grants       []GrantChange
	SideEffects  SetChange
}

type BudgetImpact struct {
	ActiveWallClockMin *BudgetChange
	RetriesPerMovement *BudgetChange
}

// Compare returns the typed impact between two validated score ASTs.
func Compare(before, after *Score) (Impact, error) {
	if before == nil || after == nil {
		return Impact{}, fmt.Errorf("score: compare requires two scores")
	}
	if !before.document.DefaultsApplied || !after.document.DefaultsApplied {
		return Impact{}, fmt.Errorf("score: compare requires defaulted scores")
	}
	left, right := before.projectionValue(), after.projectionValue()
	impact := Impact{}
	for _, key := range []string{"score", "name", "revision", "status", "goal", "context", "draft", "open_questions", "verification", "extensions"} {
		compareField(&impact, "/"+key, left, right, key)
	}
	compareParts(&impact, left["parts"].(map[string]any), right["parts"].(map[string]any))
	compareMovements(&impact, left["movements"].([]any), right["movements"].([]any))
	comparePolicy(&impact, left["policy"].(map[string]any), right["policy"].(map[string]any))
	sortImpact(&impact)
	return impact, nil
}

// Contains reports whether claim is at least as broad as actual under §9's
// component-wise containment rules.
func (claim Impact) Contains(actual Impact) bool {
	claimChanges := make(map[string]struct{}, len(claim.ScoreChanges))
	for _, change := range claim.ScoreChanges {
		claimChanges[change.Selector+"\x00"+change.Operation] = struct{}{}
	}
	for _, change := range actual.ScoreChanges {
		if _, ok := claimChanges[change.Selector+"\x00"+change.Operation]; !ok {
			return false
		}
	}
	if !setContains(claim.Authority.AllowedPaths, actual.Authority.AllowedPaths) ||
		!setContains(claim.Authority.SideEffects, actual.Authority.SideEffects) ||
		!grantsContain(claim.Authority.Grants, actual.Authority.Grants) {
		return false
	}
	return budgetContains(claim.Budget.ActiveWallClockMin, actual.Budget.ActiveWallClockMin) &&
		budgetContains(claim.Budget.RetriesPerMovement, actual.Budget.RetriesPerMovement)
}

// TypedDelta returns the score_changes representation accepted by runstate.
func (impact Impact) TypedDelta() []any {
	result := make([]any, 0, len(impact.ScoreChanges))
	for _, change := range impact.ScoreChanges {
		value := map[string]any{"selector": change.Selector, "operation": change.Operation}
		if change.BeforeHash != "" {
			value["before_hash"] = change.BeforeHash
		}
		if change.AfterHash != "" {
			value["after_hash"] = change.AfterHash
		}
		result = append(result, value)
	}
	return result
}

// Value returns the canonical-JSON shape used in amendment event payloads.
func (impact Impact) Value() map[string]any {
	budget := map[string]any{}
	if impact.Budget.ActiveWallClockMin != nil {
		budget["active_wall_clock_min"] = budgetValue(*impact.Budget.ActiveWallClockMin)
	}
	if impact.Budget.RetriesPerMovement != nil {
		budget["retries_per_movement"] = budgetValue(*impact.Budget.RetriesPerMovement)
	}
	grants := make([]any, 0, len(impact.Authority.Grants))
	for _, grant := range impact.Authority.Grants {
		grants = append(grants, map[string]any{"movement_id": grant.MovementID, "added": stringsValue(grant.Added), "removed": stringsValue(grant.Removed)})
	}
	return map[string]any{
		"score_changes": impact.TypedDelta(),
		"authority": map[string]any{
			"allowed_paths": setValue(impact.Authority.AllowedPaths),
			"grants":        grants,
			"side_effects":  setValue(impact.Authority.SideEffects),
		},
		"budget": budget,
	}
}

func compareParts(impact *Impact, before, after map[string]any) {
	left, right := make(map[string]map[string]any, len(before)), make(map[string]map[string]any, len(after))
	for id, value := range before {
		left[id] = value.(map[string]any)
	}
	for id, value := range after {
		right[id] = value.(map[string]any)
	}
	compareIDObjects(impact, "/parts", left, right)
}

func compareMovements(impact *Impact, before, after []any) {
	left, leftOrder := objectsByID(before)
	right, rightOrder := objectsByID(after)
	for id, value := range left {
		selector := "/movements[id=" + id + "]"
		afterValue, ok := right[id]
		if !ok {
			addChange(impact, selector, "remove", value, nil)
			impact.Authority.Grants = appendGrantChange(impact.Authority.Grants, id, stringSet(value["grants"].([]any)), nil)
			continue
		}
		for _, key := range []string{"id", "phase", "part", "needs", "grants", "instruction", "inputs", "may_propose"} {
			compareField(impact, selector+"/"+key, value, afterValue, key)
		}
		compareIDArray(impact, selector+"/outputs", value["outputs"].([]any), afterValue["outputs"].([]any))
		beforeAcceptance, afterAcceptance := value["acceptance"].(map[string]any), afterValue["acceptance"].(map[string]any)
		compareField(impact, selector+"/acceptance/human_gate", beforeAcceptance, afterAcceptance, "human_gate")
		compareIDArray(impact, selector+"/acceptance/hard", beforeAcceptance["hard"].([]any), afterAcceptance["hard"].([]any))
		compareIDArray(impact, selector+"/acceptance/review", beforeAcceptance["review"].([]any), afterAcceptance["review"].([]any))
		impact.Authority.Grants = appendGrantChange(impact.Authority.Grants, id,
			stringSet(value["grants"].([]any)), stringSet(afterValue["grants"].([]any)))
	}
	for id, value := range right {
		if _, ok := left[id]; !ok {
			addChange(impact, "/movements[id="+id+"]", "add", nil, value)
			impact.Authority.Grants = appendGrantChange(impact.Authority.Grants, id, nil, stringSet(value["grants"].([]any)))
		}
	}
	if !slices.Equal(leftOrder, rightOrder) {
		addChange(impact, "/movements", "replace", before, after)
	}
}

func comparePolicy(impact *Impact, before, after map[string]any) {
	compareField(impact, "/policy/amendment", before, after, "amendment")
	leftPaths, rightPaths := stringSet(before["allowed_paths"].([]any)), stringSet(after["allowed_paths"].([]any))
	compareField(impact, "/policy/allowed_paths", before, after, "allowed_paths")
	impact.Authority.AllowedPaths = setDifference(leftPaths, rightPaths)
	leftEffects, rightEffects := stringSet(before["side_effects"].([]any)), stringSet(after["side_effects"].([]any))
	compareField(impact, "/policy/side_effects", before, after, "side_effects")
	impact.Authority.SideEffects = setDifference(leftEffects, rightEffects)
	leftBudget, rightBudget := before["budget"].(map[string]any), after["budget"].(map[string]any)
	for _, field := range []string{"active_wall_clock_min", "retries_per_movement"} {
		compareField(impact, "/policy/budget/"+field, leftBudget, rightBudget, field)
	}
	if !canonicalEqual(leftBudget["active_wall_clock_min"], rightBudget["active_wall_clock_min"]) {
		impact.Budget.ActiveWallClockMin = &BudgetChange{From: int64(leftBudget["active_wall_clock_min"].(float64)), To: int64(rightBudget["active_wall_clock_min"].(float64))}
	}
	if !canonicalEqual(leftBudget["retries_per_movement"], rightBudget["retries_per_movement"]) {
		impact.Budget.RetriesPerMovement = &BudgetChange{From: int64(leftBudget["retries_per_movement"].(float64)), To: int64(rightBudget["retries_per_movement"].(float64))}
	}
}

func compareIDObjects(impact *Impact, base string, before, after map[string]map[string]any) {
	for id, value := range before {
		selector := base + "[id=" + id + "]"
		if other, ok := after[id]; ok {
			if !canonicalEqual(value, other) {
				addChange(impact, selector, "replace", value, other)
			}
		} else {
			addChange(impact, selector, "remove", value, nil)
		}
	}
	for id, value := range after {
		if _, ok := before[id]; !ok {
			addChange(impact, base+"[id="+id+"]", "add", nil, value)
		}
	}
}

func compareIDArray(impact *Impact, base string, before, after []any) {
	left, leftOrder := objectsByID(before)
	right, rightOrder := objectsByID(after)
	compareIDObjects(impact, base, left, right)
	if !slices.Equal(leftOrder, rightOrder) {
		addChange(impact, base, "replace", before, after)
	}
}

func compareField(impact *Impact, selector string, before, after map[string]any, key string) {
	left, leftOK := before[key]
	right, rightOK := after[key]
	if leftOK == rightOK && (!leftOK || canonicalEqual(left, right)) {
		return
	}
	operation := "replace"
	if !leftOK {
		operation = "add"
	}
	if !rightOK {
		operation = "remove"
	}
	addChange(impact, selector, operation, left, right)
}

func addChange(impact *Impact, selector, operation string, before, after any) {
	change := Change{Selector: selector, Operation: operation}
	if before != nil {
		change.BeforeHash, _ = canonical.Hash(canonical.DomainScoreSubtree, map[string]any{"selector": selector, "value": before})
	}
	if after != nil {
		change.AfterHash, _ = canonical.Hash(canonical.DomainScoreSubtree, map[string]any{"selector": selector, "value": after})
	}
	impact.ScoreChanges = append(impact.ScoreChanges, change)
}

func objectsByID(values []any) (map[string]map[string]any, []string) {
	result, order := make(map[string]map[string]any, len(values)), make([]string, 0, len(values))
	for _, raw := range values {
		value := raw.(map[string]any)
		id := value["id"].(string)
		result[id] = value
		order = append(order, id)
	}
	return result, order
}

func stringSet(values []any) []string {
	result := make([]string, len(values))
	for i, raw := range values {
		result[i] = raw.(string)
	}
	return result
}
func setDifference(before, after []string) SetChange {
	return SetChange{Added: difference(after, before), Removed: difference(before, after)}
}
func difference(left, right []string) []string {
	present := make(map[string]struct{}, len(right))
	for _, v := range right {
		present[v] = struct{}{}
	}
	result := []string{}
	for _, v := range left {
		if _, ok := present[v]; !ok {
			result = append(result, v)
		}
	}
	return result
}
func appendGrantChange(changes []GrantChange, id string, before, after []string) []GrantChange {
	change := setDifference(before, after)
	if len(change.Added) == 0 && len(change.Removed) == 0 {
		return changes
	}
	return append(changes, GrantChange{MovementID: id, Added: change.Added, Removed: change.Removed})
}
func setContains(claim, actual SetChange) bool {
	return isSubset(actual.Added, claim.Added) && isSubset(actual.Removed, claim.Removed)
}
func isSubset(values, containing []string) bool {
	set := make(map[string]struct{}, len(containing))
	for _, v := range containing {
		set[v] = struct{}{}
	}
	for _, v := range values {
		if _, ok := set[v]; !ok {
			return false
		}
	}
	return true
}
func grantsContain(claim, actual []GrantChange) bool {
	byID := make(map[string]GrantChange, len(claim))
	for _, v := range claim {
		byID[v.MovementID] = v
	}
	for _, v := range actual {
		candidate, ok := byID[v.MovementID]
		if !ok || !setContains(SetChange{candidate.Added, candidate.Removed}, SetChange{v.Added, v.Removed}) {
			return false
		}
	}
	return true
}
func budgetContains(claim, actual *BudgetChange) bool {
	if actual == nil {
		return true
	}
	if claim == nil {
		return false
	}
	actualDirection := actual.To - actual.From
	claimDirection := claim.To - claim.From
	if actualDirection == 0 || claimDirection == 0 || (actualDirection < 0) != (claimDirection < 0) {
		return false
	}
	return abs(claimDirection) >= abs(actualDirection)
}
func abs(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
func sortImpact(impact *Impact) {
	sort.Slice(impact.ScoreChanges, func(i, j int) bool {
		if impact.ScoreChanges[i].Selector != impact.ScoreChanges[j].Selector {
			return impact.ScoreChanges[i].Selector < impact.ScoreChanges[j].Selector
		}
		return operationRank(impact.ScoreChanges[i].Operation) < operationRank(impact.ScoreChanges[j].Operation)
	})
	sort.Slice(impact.Authority.Grants, func(i, j int) bool {
		return impact.Authority.Grants[i].MovementID < impact.Authority.Grants[j].MovementID
	})
}
func operationRank(operation string) int {
	switch operation {
	case "add":
		return 0
	case "remove":
		return 1
	default:
		return 2
	}
}
func stringsValue(values []string) []any {
	result := make([]any, len(values))
	for i, v := range values {
		result[i] = v
	}
	return result
}
func setValue(change SetChange) map[string]any {
	return map[string]any{"added": stringsValue(change.Added), "removed": stringsValue(change.Removed)}
}
func budgetValue(change BudgetChange) map[string]any {
	return map[string]any{"from": float64(change.From), "to": float64(change.To)}
}
