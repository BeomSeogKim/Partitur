package score

import "testing"

func TestCompareRecordsItemContentAndSequenceSeparately(t *testing.T) {
	base := finalizedFixture()
	movementAt(base, 0)["outputs"] = append(arrayAt(movementAt(base, 0), "outputs"),
		map[string]any{"id": "plan-summary", "kind": "document"})
	changed := cloneFixture(base)
	outputs := arrayAt(movementAt(changed, 0), "outputs")
	outputs[0], outputs[1] = outputs[1], outputs[0]
	outputs[0].(map[string]any)["kind"] = "directory"
	movements := arrayAt(changed, "movements")
	movements[0], movements[1] = movements[1], movements[0]

	impact, err := Compare(assertCompiles(t, base), assertCompiles(t, changed))
	if err != nil {
		t.Fatal(err)
	}
	assertChange(t, impact, "/movements", "replace")
	assertChange(t, impact, "/movements[id=plan]/outputs", "replace")
	assertChange(t, impact, "/movements[id=plan]/outputs[id=plan-summary]", "replace")
	for _, change := range impact.ScoreChanges {
		if stringsHasNumericPointer(change.Selector) {
			t.Fatalf("numeric selector %q", change.Selector)
		}
	}
}

func TestCompareDerivesAuthorityBudgetAndContainment(t *testing.T) {
	base := finalizedFixture()
	changed := cloneFixture(base)
	policy := objectAt(changed["policy"])
	policy["allowed_paths"] = []any{"internal/**"}
	budget := objectAt(policy["budget"])
	budget["active_wall_clock_min"] = float64(50)
	movementAt(changed, 1)["grants"] = []any{"repo_read", "repo_write"}

	impact, err := Compare(assertCompiles(t, base), assertCompiles(t, changed))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := impact.Authority.AllowedPaths.Removed, []string{"cmd/**"}; !equalStrings(got, want) {
		t.Fatalf("removed paths = %#v, want %#v", got, want)
	}
	if len(impact.Authority.Grants) != 1 || impact.Authority.Grants[0].MovementID != "build" || len(impact.Authority.Grants[0].Removed) != 1 {
		t.Fatalf("grant impact = %#v", impact.Authority.Grants)
	}
	if impact.Budget.ActiveWallClockMin == nil || impact.Budget.ActiveWallClockMin.From != 90 || impact.Budget.ActiveWallClockMin.To != 50 {
		t.Fatalf("budget impact = %#v", impact.Budget.ActiveWallClockMin)
	}
	if !impact.Contains(impact) {
		t.Fatal("an impact does not contain itself")
	}
	narrow := impact
	narrow.ScoreChanges = narrow.ScoreChanges[:len(narrow.ScoreChanges)-1]
	if narrow.Contains(impact) {
		t.Fatal("missing score change contained actual impact")
	}
	narrow = impact
	narrow.Authority.AllowedPaths.Removed = nil
	if narrow.Contains(impact) {
		t.Fatal("missing path removal contained actual impact")
	}
	narrow = impact
	narrow.Authority.Grants = nil
	if narrow.Contains(impact) {
		t.Fatal("missing grant removal contained actual impact")
	}
	narrow = impact
	narrow.Budget.ActiveWallClockMin = &BudgetChange{From: 90, To: 60}
	if narrow.Contains(impact) {
		t.Fatal("smaller budget decrease contained actual impact")
	}
}

func assertChange(t *testing.T, impact Impact, selector, operation string) {
	t.Helper()
	for _, change := range impact.ScoreChanges {
		if change.Selector == selector && change.Operation == operation {
			if change.BeforeHash == "" || change.AfterHash == "" {
				t.Fatalf("change %#v has incomplete hashes", change)
			}
			return
		}
	}
	t.Fatalf("missing %s %s in %#v", operation, selector, impact.ScoreChanges)
}

func stringsHasNumericPointer(selector string) bool {
	for index := 0; index < len(selector); index++ {
		if selector[index] == '/' && index+1 < len(selector) && selector[index+1] >= '0' && selector[index+1] <= '9' {
			return true
		}
	}
	return false
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
