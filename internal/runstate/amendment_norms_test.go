package runstate

import (
	"strings"
	"testing"
)

func TestAmendmentRejectionPatchHashFormsAreSpecified(t *testing.T) {
	lines := recoveryDesignLines(t)
	section := recoveryDocumentSection(t, lines,
		"## B.5 Amendments",
		"## B.6 Shipping")
	contents := strings.Join(strings.Fields(strings.Join(section, "\n")), " ")

	for _, clause := range []string{
		"patch_operations_hash?, # partitur/patch-operations",
		"patch_operations_hash_form?, # required iff patch_operations_hash: `partitur/patch-operations`",
		"for the canonical identity, or `raw-byte-sha256`",
		"submitted operations' raw-byte sha256",
	} {
		if !strings.Contains(contents, clause) {
			t.Fatalf("amendment.rejected patch hash form is missing clause %q", clause)
		}
	}
}

func TestAmendmentEvaluatorReadinessIsNotProved(t *testing.T) {
	lines := recoveryDesignLines(t)
	contents := strings.Join(lines, "\n")

	const row = "| The amendment evaluator and routed-proposal recovery (§9) | evaluator, recovery, and fault-injection tests (Appendix E) | **not proved** — no amendment evaluator, routed-path executor, or routed-path crash oracle exists |"
	if count := strings.Count(contents, row); count != 1 {
		t.Fatalf("§9 implementation-readiness row count=%d, want 1", count)
	}
}

func TestRoutedProposalE2EdgesAreSpecified(t *testing.T) {
	lines := recoveryDesignLines(t)
	section := recoveryDocumentSection(t, lines,
		"## E.2 The catalog",
		"## E.3 `cancel.fence_decided_to_terminal` is not a lost-write boundary")
	contents := strings.Join(section, "\n")

	for _, row := range []string{
		"| `proposal.published_to_routed` | proposal record published `R` | `amendment.routed_human` appended `R` | §1 routed-proposal records; C.1 `RC-RESUME-035` | A proposal record with no routing event is unreferenced and quarantined; recovery never manufactures a route from an orphan record |",
		"| `proposal.routed_to_decision_requested` | `amendment.routed_human` appended `R` | matching `decision.requested` appended `R` | §1 routed-proposal records; C.1 `RC-RESUME-037` | Recovery appends the request idempotently from the routed event; it never re-runs routing or infers the request from `attempt.blocked` |",
	} {
		if count := strings.Count(contents, row); count != 1 {
			t.Fatalf("routed-proposal E.2 row count=%d, want 1: %q", count, row)
		}
	}
}
