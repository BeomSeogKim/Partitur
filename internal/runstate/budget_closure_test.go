package runstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type budgetIntervalCloser struct {
	reason string
	closer string
}

func TestBudgetIntervalClosureTableIsClosed(t *testing.T) {
	lines := recoveryDesignLines(t)
	section := recoveryDocumentSection(t, lines,
		"## 6. Run state model v0.2",
		"## 7. Acceptance runner and CLI v0.2")
	for index := range section {
		section[index] = strings.TrimSpace(section[index])
	}

	const header = "| Situation | `reason` | Closed by |"
	const separator = "|---|---|---|"
	headerIndex := uniqueLineIndex(t, section, header)
	if headerIndex+1 >= len(section) || section[headerIndex+1] != separator {
		t.Fatalf("budget interval closure table has missing or malformed separator after %q", header)
	}

	got := map[string]budgetIntervalCloser{}
	for index := headerIndex + 2; index < len(section) && strings.HasPrefix(section[index], "|"); index++ {
		cells := markdownTableCells(t, "budget interval closure table", section[index], 3)
		if _, exists := got[cells[0]]; exists {
			t.Fatalf("budget interval closure table repeats situation %q", cells[0])
		}
		got[cells[0]] = budgetIntervalCloser{
			reason: strings.Trim(cells[1], "`"),
			closer: cells[2],
		}
	}

	want := map[string]budgetIntervalCloser{
		"Recovery finds an `execution.started` with no matching stop": {
			reason: "recovered",
			closer: "the recovering process",
		},
		"A wedged driver is fenced and cancelled (control channel, below)": {
			reason: "cancelled",
			closer: "the canceller",
		},
		"A wedged driver is fenced and **superseded** by an approved revision (§9)": {
			reason: "superseded",
			closer: "the approving command",
		},
		"A **responsive driver cancels itself** through the oracle's `(c)` (control channel, below)": {
			reason: "cancelled",
			closer: "the driver, in the canceller role",
		},
		"A **responsive driver quiesces itself** for a prepared supersession (§6 step 2)": {
			reason: "superseded",
			closer: "the driver, in the quiescing role",
		},
	}
	if len(got) != len(want) {
		t.Fatalf("budget interval closure rows=%d, want %d: %v", len(got), len(want), got)
	}
	for situation, expected := range want {
		if actual, exists := got[situation]; !exists {
			t.Fatalf("budget interval closure table is missing %q", situation)
		} else if actual != expected {
			t.Fatalf("budget interval closure %q=%+v, want %+v", situation, actual, expected)
		}
	}
}

func TestPrepareACKControlDrainReferencesControlChannel(t *testing.T) {
	lines := recoveryDesignLines(t)
	section := recoveryDocumentSection(t, lines,
		"## 6. Run state model v0.2",
		"## 7. Acceptance runner and CLI v0.2")
	contents := strings.Join(strings.Fields(strings.Join(section, "\n")), " ")

	const marker = "**Control drain.**"
	if count := strings.Count(contents, marker); count != 1 {
		t.Fatalf("prepare ACK control-drain marker count=%d, want 1", count)
	}
	controlDrain := strings.Index(contents, marker)
	quiesce := strings.Index(contents, "2. **Quiesce or expire.**")
	commit := strings.Index(contents, "3. **Commit.**")
	if quiesce == -1 || commit == -1 || controlDrain <= quiesce || controlDrain >= commit {
		t.Fatalf("prepare ACK control drain must appear between quiesce and commit: quiesce=%d drain=%d commit=%d", quiesce, controlDrain, commit)
	}

	for _, clause := range []string{
		"This ACK applies step 4's process-control and response-suppression drain **by reference**, substituting the durable `amendment.approval_prepared` for `cancel.requested` as the request and supersession for cancellation as the terminal intent.",
		// The packet 13 restatement repair narrowed this clause. It used to write out the
		// observation boundary and the no-response-derived-outcome consequence, both of which
		// the cancellation drain it applies "by reference" already owns as
		// `cancellation.no-response-derived-outcome-after-observing-request`. This lock exists
		// to hold that the ACK names that boundary rather than inventing its own, so it pins
		// the reference instead. It is a `Contains` check: it catches the clause being dropped
		// or reworded, and does not catch the consequence being re-derived alongside it.
		"It takes effect at the observation boundary that rule fixes, with the durable prepare standing in `cancel.requested`'s place.",
		"This is the drain only; the ACK does not execute the cancellation oracle.",
		"`amendment.approved` is the single producer of the affected attempts' `attempt.superseded` projection (B.5), so the response remains only a completeness marker.",
	} {
		if !strings.Contains(contents, clause) {
			t.Fatalf("prepare ACK control drain is missing clause %q", clause)
		}
	}
}

func TestPrepareCommitNamesLeaseEpochMismatchHalt(t *testing.T) {
	lines := recoveryDesignLines(t)
	section := recoveryDocumentSection(t, lines,
		"## 6. Run state model v0.2",
		"## 7. Acceptance runner and CLI v0.2")
	contents := strings.Join(strings.Fields(strings.Join(section, "\n")), " ")

	const clause = "the right response is to halt `prepare_lease_epoch_mismatch`, not to guess which incarnation is authoritative."
	if count := strings.Count(contents, clause); count != 1 {
		t.Fatalf("prepare commit lease-epoch mismatch halt clause count=%d, want 1", count)
	}
	commit := strings.Index(contents, "3. **Commit.**")
	planValidation := strings.Index(contents, "**Plan validation is a closed predicate**")
	halt := strings.Index(contents, clause)
	if commit == -1 || planValidation == -1 || halt <= commit || halt >= planValidation {
		t.Fatalf("prepare commit lease-epoch mismatch halt must appear in the commit closing paragraph: commit=%d halt=%d plan_validation=%d", commit, halt, planValidation)
	}
}

func TestExecutionStoppedControlReasonsAreAlwaysClamped(t *testing.T) {
	lines := recoveryDesignLines(t)
	section := recoveryDocumentSection(t, lines,
		"## B.2 Attempt lifecycle and performer selection",
		"## B.3 Evidence")

	const header = "| Type | sync | idem key | Legal from | Projection effect |"
	const separator = "|---|---|---|---|---|"
	headerIndex := uniqueLineIndex(t, section, header)
	if headerIndex+1 >= len(section) || section[headerIndex+1] != separator {
		t.Fatalf("Appendix B event table has missing or malformed separator after %q", header)
	}

	var effect string
	for index := headerIndex + 2; index < len(section) && strings.HasPrefix(section[index], "|"); index++ {
		cells := markdownTableCells(t, "Appendix B event table", section[index], 5)
		if cells[0] == "`execution.stopped`" {
			if effect != "" {
				t.Fatal("Appendix B repeats execution.stopped")
			}
			effect = cells[4]
		}
	}
	if effect == "" {
		t.Fatal("Appendix B has no execution.stopped row")
	}
	for _, clause := range []string{
		"**`reason: cancelled` and `reason: superseded` are always `clamped`**",
		"whether a fence is taken and whether the closer is the opener",
		"this is a property of the reason rather than of the closer",
	} {
		if !strings.Contains(effect, clause) {
			t.Fatalf("execution.stopped projection effect is missing clause %q", clause)
		}
	}
}

func TestCandidateIncompatibleConditionsExcludeExecutedDependencyChanges(t *testing.T) {
	lines := recoveryDesignLines(t)
	section := recoveryDocumentSection(t, lines,
		"## 9. Amendments",
		"# Appendix A — Canonical encoding and identity domains")
	contents := strings.Join(strings.Fields(strings.Join(section, "\n")), " ")
	if strings.Contains(contents, "succeeded_dependency_changed") {
		t.Fatal("§9 retains unreachable candidate_incompatible condition succeeded_dependency_changed")
	}

	for _, clause := range []string{
		"Because the A.5 projection carries `base_composition_hash`, this same check catches a change to how a succeeded movement's clean base was assembled",
		"failure reasons: composition_changed | verification_episode_finished | verification_mode_changed",
	} {
		if !strings.Contains(contents, clause) {
			t.Fatalf("§9 is missing candidate compatibility clause %q", clause)
		}
	}

	appendixB := recoveryDocumentSection(t, lines,
		"## B.5 Amendments",
		"## B.6 Shipping")
	appendixD := recoveryDocumentSection(t, lines,
		"# Appendix D — Closed enums",
		"# Appendix E — Ordered-step boundaries")
	conditions := []string{
		"composition_changed",
		"verification_episode_finished",
		"verification_mode_changed",
	}
	for name, text := range map[string]string{
		"Appendix B": strings.Join(appendixB, "\n"),
		"Appendix D": strings.Join(appendixD, "\n"),
	} {
		for _, condition := range conditions {
			if !strings.Contains(text, condition) {
				t.Fatalf("%s omits candidate_incompatible condition %q", name, condition)
			}
		}
		if strings.Contains(text, "succeeded_dependency_changed") {
			t.Fatalf("%s retains unreachable candidate_incompatible condition succeeded_dependency_changed", name)
		}
	}

	decision, err := os.ReadFile(filepath.Join("..", "..", "docs", "decisions", "0002-verification-semantics.md"))
	if err != nil {
		t.Fatal(err)
	}
	decisionSection := recoveryDocumentSection(t, strings.Split(string(decision), "\n"),
		"### 7. Candidate compatibility — the shared judgment",
		"### 8. `apply`, `promote-score`, and the checkout CAS")
	decisionContents := strings.Join(strings.Fields(strings.Join(decisionSection, "\n")), " ")
	if !strings.Contains(decisionContents, "composition_changed | verification_episode_finished | verification_mode_changed") {
		t.Fatal("Decision 0002 does not retain the closed candidate_incompatible condition enum")
	}
	for _, condition := range conditions {
		if !strings.Contains(decisionContents, condition) {
			t.Fatalf("Decision 0002 omits candidate_incompatible condition %q", condition)
		}
	}
	if strings.Contains(decisionContents, "succeeded_dependency_changed") {
		t.Fatal("Decision 0002 retains unreachable candidate_incompatible condition succeeded_dependency_changed")
	}
}
