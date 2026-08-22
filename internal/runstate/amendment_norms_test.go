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
		"patch_operations_hash?, patch_operations_hash_form?,",
		"- `amendment.rejected.patch_operations_hash` uses `partitur/patch-operations`.",
		"- `amendment.rejected.patch_operations_hash_form` is required if and only if `patch_operations_hash` is present.",
		"- `amendment.rejected.patch_operations_hash_form` is `partitur/patch-operations` for the canonical identity or `raw-byte-sha256` for the submitted operations' raw-byte sha256.",
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
		"- For `claimed_impact` optionality and containment, see §9's “Admissibility pipeline”.",
		"- For `claimed_impact`'s shape, see §9's “`actual_impact`”; for its optionality, see §9's “Admissibility pipeline”, step 7.",
		"7. **Impact computation and claim containment** — when `claimed_impact` is present, a claim narrower than the actual impact on any component rejects with `claim_narrower`; when it is absent, no containment check applies. The optional claim is a proposer-supplied scope assertion, not authority over the core-computed `actual_impact`.",
	} {
		if count := strings.Count(contents, clause); count != 1 {
			t.Fatalf("claimed-impact optionality clause count=%d, want 1: %q", count, clause)
		}
	}
}

func TestApprovalDecisionTypeSyntaxIsSpecified(t *testing.T) {
	lines := recoveryDesignLines(t)
	section := approvalSyntaxSection(t, lines)
	forms := approvalRejectForms(t, section)

	humanGate := approvalRejectFormForTypes(t, forms, "human_gate")
	if humanGate.reason != "optional" {
		t.Fatalf("human_gate rejection reason = %q, want optional", humanGate.reason)
	}

	amendmentFinalization := approvalRejectFormForTypes(t, forms, "amendment", "finalization")
	if amendmentFinalization.reason != "required" {
		t.Fatalf("amendment/finalization rejection reason = %q, want required", amendmentFinalization.reason)
	}

	amendmentPolicy := amendmentFinalizationApprovalPolicy(t, section)
	if amendmentFinalization.reason != amendmentPolicy.reason {
		t.Fatalf("amendment/finalization grammar reason = %q, want policy %q", amendmentFinalization.reason, amendmentPolicy.reason)
	}
	if !amendmentPolicy.overrideInvalid {
		t.Fatal("amendment/finalization approval policy permits --override")
	}
	if amendmentFinalization.reasonField != amendmentPolicy.reasonField {
		t.Fatalf("amendment/finalization grammar field = %q, want policy field %q", amendmentFinalization.reasonField, amendmentPolicy.reasonField)
	}

	humanReason := amendmentHumanRejectionReason(t, recoveryDocumentSection(t, lines,
		"## B.5 Amendments",
		"## B.6 Shipping"))
	if amendmentFinalization.reasonField != humanReason {
		t.Fatalf("amendment/finalization rejection field = %q, want B.5 field %q", amendmentFinalization.reasonField, humanReason)
	}
}

func approvalSyntaxSection(t *testing.T, lines []string) []string {
	t.Helper()

	start := uniqueLineIndex(t, lines, "**Operands and options.** Enough to be a contract rather than a sketch; anything not listed is not")
	end := uniqueLinePrefixIndex(t, lines, "**`partitur status` observable surface.**")
	if end <= start {
		t.Fatal("partitur status section must follow operands and options")
	}
	return lines[start+1 : end]
}

func uniqueLinePrefixIndex(t *testing.T, lines []string, prefix string) int {
	t.Helper()

	index := -1
	count := 0
	for lineIndex, line := range lines {
		if strings.HasPrefix(line, prefix) {
			index = lineIndex
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%q prefix count = %d, want 1", prefix, count)
	}
	return index
}

type approvalRejectForm struct {
	decisionTypes map[string]struct{}
	reason        string
	reasonField   string
}

type approvalDecisionPolicy struct {
	reason          string
	reasonField     string
	overrideInvalid bool
}

func amendmentFinalizationApprovalPolicy(t *testing.T, lines []string) approvalDecisionPolicy {
	t.Helper()

	narrative := strings.Join(strings.Fields(strings.Join(lines, "\n")), " ")
	const prefix = "For an `amendment` or `finalization` decision,"
	if count := strings.Count(narrative, prefix); count != 1 {
		t.Fatalf("amendment/finalization approval policy prefix count = %d, want 1", count)
	}
	policyText := strings.TrimSpace(strings.TrimPrefix(narrative[strings.Index(narrative, prefix):], prefix))
	policy := approvalDecisionPolicy{}
	if strings.Contains(policyText, "rejection requires `--reason <text>`") {
		policy.reason = "required"
	}
	const mappingPrefix = "which becomes B.5's `"
	_, mapping, found := strings.Cut(policyText, mappingPrefix)
	if found {
		field, _, closed := strings.Cut(mapping, "`")
		if !closed || field == "" {
			t.Fatalf("unparseable amendment/finalization B.5 mapping %q", policyText)
		}
		policy.reasonField = field
	}
	policy.overrideInvalid = strings.Contains(policyText, "`--override` is invalid")
	if policy.reason == "" || policy.reasonField == "" || !policy.overrideInvalid {
		t.Fatalf("unparseable amendment/finalization approval policy %q", policyText)
	}
	return policy
}

func approvalRejectForms(t *testing.T, lines []string) []approvalRejectForm {
	t.Helper()

	narrative := strings.Join(strings.Fields(strings.Join(lines, "\n")), " ")
	const humanGateCarrier = "- `--reject [--reason <text>]` is valid only for `human_gate`."
	const amendmentCarrier = "- Amendment and finalization rejection requires `--reason <text>` under B.5's `human_reason` rule."
	for _, carrier := range []string{humanGateCarrier, amendmentCarrier} {
		if count := strings.Count(narrative, carrier); count != 1 {
			t.Fatalf("approve reject carrier count = %d, want 1: %q", count, carrier)
		}
	}

	var forms []approvalRejectForm
	for _, line := range lines {
		command := strings.TrimSpace(line)
		if !strings.HasPrefix(command, "| --reject") {
			continue
		}

		form := approvalRejectForm{decisionTypes: make(map[string]struct{})}
		switch command {
		case "| --reject [--reason <text>]":
			form.reason = "optional"
			form.decisionTypes["human_gate"] = struct{}{}
		case "| --reject --reason <text>":
			form.reason = "required"
			form.decisionTypes["amendment"] = struct{}{}
			form.decisionTypes["finalization"] = struct{}{}
			form.reasonField = "human_reason"
		default:
			t.Fatalf("unparseable approve reject grammar %q", line)
		}
		forms = append(forms, form)
	}
	if len(forms) != 2 {
		t.Fatalf("approve reject grammar forms = %d, want 2", len(forms))
	}
	return forms
}

func approvalRejectFormForTypes(t *testing.T, forms []approvalRejectForm, want ...string) approvalRejectForm {
	t.Helper()

	for _, form := range forms {
		if len(form.decisionTypes) != len(want) {
			continue
		}
		matches := true
		for _, decisionType := range want {
			if _, ok := form.decisionTypes[decisionType]; !ok {
				matches = false
				break
			}
		}
		if matches {
			return form
		}
	}
	t.Fatalf("approve reject grammar has no form for decision types %s", strings.Join(want, ", "))
	return approvalRejectForm{}
}

func amendmentHumanRejectionReason(t *testing.T, lines []string) string {
	t.Helper()

	inPayload := false
	field := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "amendment.human_rejected {" {
			if inPayload {
				t.Fatal("B.5 has nested amendment.human_rejected payload")
			}
			inPayload = true
			continue
		}
		if !inPayload {
			continue
		}
		if trimmed == "}" {
			break
		}
		if !strings.HasPrefix(trimmed, "human_reason,") {
			continue
		}
		candidate := strings.TrimSpace(strings.SplitN(trimmed, ",", 2)[0])
		if candidate == "" || field != "" {
			t.Fatalf("unparseable amendment.human_rejected reason field %q", line)
		}
		field = candidate
	}
	if !inPayload || field == "" {
		t.Fatal("B.5 has no amendment.human_rejected reason field")
	}
	carrier := "- `amendment.human_rejected." + field + "` is non-empty."
	if count := strings.Count(strings.Join(lines, "\n"), carrier); count != 1 {
		t.Fatalf("B.5 amendment.human_rejected reason carrier count = %d, want 1: %q", count, carrier)
	}
	return field
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
		"## B.2 Attempt lifecycle and performer selection",
		"## B.3 Evidence")
	contents := strings.Join(strings.Fields(strings.Join(section, "\n")), " ")
	for _, clause := range []string{
		"delivered_feedback: [ {previous_attempt_id, kind, artifact_instance_id, content_hash} ],",
		"- `adapter.probed.delivered_feedback` is sorted by `(previous_attempt_id, artifact_instance_id)` and is always present, including as `[]`.",
		"- `adapter.probed.delivered_feedback` makes the exact request reconstructible without leaving “none delivered” ambiguous.",
	} {
		if !strings.Contains(contents, clause) {
			t.Fatalf("adapter.probed delivered feedback is missing clause %q", clause)
		}
	}
}

func TestAdapterProbedRecordsDeliveredResolutions(t *testing.T) {
	lines := recoveryDesignLines(t)
	section := recoveryDocumentSection(t, lines,
		"## B.2 Attempt lifecycle and performer selection",
		"## B.3 Evidence")
	contents := strings.Join(strings.Fields(strings.Join(section, "\n")), " ")
	for _, clause := range []string{
		"delivered_resolutions: [ {decision_id, kind, digest} ],",
		"- `adapter.probed.delivered_resolutions` is in delivered order and is always present, including as `[]`.",
		"- `adapter.probed.delivered_resolutions` makes the exact request reconstructible without inferring delivery from eligibility and truncation.",
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
		"| `proposal.published_to_routed` | proposal record published `R` | `amendment.routed_human` appended `R` | §1 routed-proposal records; C.1 `RC-RESUME-035`, `RC-RESUME-049` | An ordinary adapter- or CLI-origin routed record has no durable reference other than its route. A CLI publisher retains the state lock across this edge; an adapter publisher is protected by its matching live driver lease. A live publisher is not an orphan. A crash after publication releases that protection, so recovery quarantines the record when the route and blocking descriptor are absent |",
		"| `proposal.core_finalization_published_to_routed` | core-finalization proposal record published `R` | matching `amendment.routed_human` appended `R` | §2 finalization; C.1 `RC-RESUME-038` | The core-finalization publisher retains the state lock across this edge. A crash after publication releases that protection: §1 quarantines the route-absent original record, then `RC-RESUME-038` re-evaluates §2 under the lock and constructs and routes one fresh record. Once the route raw-hash-binds the record, recovery retains it; after the ordinary route-to-request consequence, the second `resume` appends neither another record nor another route |",
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
