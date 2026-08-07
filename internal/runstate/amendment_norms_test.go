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

func TestClaimedImpactOptionalityIsSpecified(t *testing.T) {
	lines := recoveryDesignLines(t)
	contents := strings.Join(strings.Fields(strings.Join(lines, "\n")), " ")

	for _, clause := range []string{
		"claimed_impact?, # optional scope claim; §9 checks containment only when present",
		"claimed_impact?: { ... } # same shape as actual_impact; optional scope claim (§9)",
		"7. **Impact computation and claim containment** — when `claimed_impact` is present, a claim narrower than the actual impact on any component rejects with `claim_narrower`; when it is absent, no containment check applies. The optional claim is a proposer-supplied scope assertion, not authority over the core-computed `actual_impact`.",
	} {
		if count := strings.Count(contents, clause); count != 1 {
			t.Fatalf("claimed-impact optionality clause count=%d, want 1: %q", count, clause)
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

func TestAdapterProbedRecordsDeliveredFeedback(t *testing.T) {
	lines := recoveryDesignLines(t)
	section := recoveryDocumentSection(t, lines,
		"adapter.probed {",
		"attempt.started {")
	contents := strings.Join(strings.Fields(strings.Join(section, "\n")), " ")
	for _, clause := range []string{
		"delivered_feedback: [ {previous_attempt_id, kind, artifact_instance_id, content_hash} ],",
		"# sorted by (previous_attempt_id, artifact_instance_id); always # present, including [], so the exact request remains # reconstructible rather than leaving \"none delivered\" ambiguous",
	} {
		if !strings.Contains(contents, clause) {
			t.Fatalf("adapter.probed delivered feedback is missing clause %q", clause)
		}
	}
}

func TestAdapterProbedRecordsDeliveredResolutions(t *testing.T) {
	lines := recoveryDesignLines(t)
	section := recoveryDocumentSection(t, lines,
		"adapter.probed {",
		"attempt.started {")
	contents := strings.Join(strings.Fields(strings.Join(section, "\n")), " ")
	for _, clause := range []string{
		"delivered_resolutions: [ {decision_id, kind, digest} ],",
		"# in delivered order; always present, including [], so the # exact request remains reconstructible rather than inferring # delivery from eligibility and truncation",
	} {
		if !strings.Contains(contents, clause) {
			t.Fatalf("adapter.probed delivered resolutions is missing clause %q", clause)
		}
	}
}

func TestRoutedProposalE2EdgesAreSpecified(t *testing.T) {
	lines := recoveryDesignLines(t)
	section := recoveryDocumentSection(t, lines,
		"## E.2 The catalog",
		"## E.3 `cancel.fence_decided_to_terminal` is not a lost-write boundary")
	contents := strings.Join(section, "\n")

	for _, row := range []string{
		"| `proposal.published_to_blocked_route` | blocking-proposal record published `R` | matching `attempt.blocked` route descriptor appended `R` | §1 routed-proposal records; §4 blocking handshake; C.1 `RC-RESUME-035` | A record without either a routed event or its matching blocked descriptor is unreferenced and quarantined; a descriptor names and raw-hash-binds the only record from which `RC-RESUME-049` may recover a route |",
		"| `proposal.blocked_route_to_routed` | blocking `attempt.blocked` route descriptor appended `R` | matching `amendment.routed_human` appended `R` | §4 blocking handshake; C.1 `RC-RESUME-049` | Recovery verifies the descriptor-named immutable record and appends the frozen route idempotently; it never re-runs §9 or derives a route from an unreferenced record |",
		"| `proposal.published_to_routed` | proposal record published `R` | `amendment.routed_human` appended `R` | §1 routed-proposal records; C.1 `RC-RESUME-035`, `RC-RESUME-049` | An ordinary routed record has no durable reference other than its route. A command-origin publisher retains the state lock across this edge; an adapter-origin publisher is protected by its matching live driver lease. A live publisher is not an orphan. A crash after publication releases that protection, so recovery quarantines the record when the route and blocking descriptor are absent |",
		"| `proposal.routed_to_decision_requested` | `amendment.routed_human` appended `R` | matching `decision.requested` appended `R` | §1 routed-proposal records; C.1 `RC-RESUME-037` | Recovery appends the request idempotently from the routed event; it never re-runs routing or infers the request from `attempt.blocked` |",
	} {
		if count := strings.Count(contents, row); count != 1 {
			t.Fatalf("routed-proposal E.2 row count=%d, want 1: %q", count, row)
		}
	}
}

func TestCommandOriginProposalPublicationIntervalIsSpecified(t *testing.T) {
	contents := strings.Join(recoveryDesignLines(t), "\n")

	for _, clause := range []string{
		"It is held for one bounded mutation or, only where a\n  named protocol rule says so, a bounded sequence of adjacent local durable mutations. It is never\n  held across a human wait, an adapter execution, or any other external wait.",
		"**Command-origin publication interval.** For an ordinary command-origin route, the repository\n  state lock is retained from the successful proposal-record rename through the fsynced\n  `amendment.routed_human` append. These are distinct durable operations, not one atomic write:\n  Appendix E may inject a crash after the record receipt and before the route append. A `resume`\n  waits for that interval; it must not classify a route-absent record as an orphan while its live\n  publisher holds the lock. If the publisher crashes, the lock is released and replay quarantines\n  the route-absent record under the rule below.",
		"On recovery, after any command-origin publication interval has ended and journal replay has\n  reached an `owner = clear` selection cut, a proposal file with neither\n  `amendment.routed_human` nor a matching blocking\n  `attempt.blocked` route descriptor is quarantined: nothing authoritative referred to it.",
	} {
		if count := strings.Count(contents, clause); count != 1 {
			t.Fatalf("command-origin publication interval clause count=%d, want 1: %q", count, clause)
		}
	}
}

func TestReviewSubjectInputRecoveryCleanupTimingIsSpecified(t *testing.T) {
	contents := strings.Join(recoveryDesignLines(t), "\n")

	const clause = "`RC-RESUME-035` removes an unreferenced pre-`attempt.started` copy only after journal replay has\nreached an `owner = clear` selection cut, and before that cut's selected continuation. A stale or\norphan lease first takes its own cleanup and re-evaluates; a verified live owner yields, and an\nunverifiable owner halts, without this review-subject-input cleanup. On an eligible `resume`, this\ncleanup runs exactly once. Terminal cleanup uses this same recovery step after any lease cleanup\nand performs no independent review-subject-input sweep."
	if count := strings.Count(contents, clause); count != 1 {
		t.Fatalf("review-subject-input recovery cleanup timing clause count=%d, want 1: %q", count, clause)
	}
}
