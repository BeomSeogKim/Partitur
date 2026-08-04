package runstate

import (
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
		"The substitution begins at the observation boundary: from the moment the driver observes the durable prepare — not from the moment it writes a protocol frame — it records no response-derived attempt outcome.",
		"This is the drain only; the ACK does not execute the cancellation oracle.",
		"`amendment.approved` is the single producer of the affected attempts' `attempt.superseded` projection (B.5), so the response remains only a completeness marker.",
	} {
		if !strings.Contains(contents, clause) {
			t.Fatalf("prepare ACK control drain is missing clause %q", clause)
		}
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
