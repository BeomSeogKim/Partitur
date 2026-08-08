package runstate

import (
	"strings"
	"testing"
)

func TestDraftNoBlockingOutputRecoveryEdgeIsSpecified(t *testing.T) {
	lines := recoveryDesignLines(t)
	section := recoveryDocumentSection(t, lines,
		"## E.2 The catalog",
		"## E.3 `cancel.fence_decided_to_terminal` is not a lost-write boundary")
	contents := strings.Join(section, "\n")

	const row = "| `lifecycle.draft_performer_completed_to_no_blocking_failure` | current draft interview `performer.completed` appended `R` | `attempt.failed {kind: task_failed, reason: draft_no_blocking_output, disposition}` appended `R` | §2 draft result boundary; C.2 `RC-RESUME-050`, `RC-RESUME-039` | A crash after the completed event leaves the draft interview in `VERIFYING`. Recovery selects `RC-RESUME-050` before `RC-RESUME-016` and `RC-RESUME-017`, appends the exact classified failure, and re-evaluates through `RC-RESUME-039`; **acceptance never begins**. A genuinely blocking draft result makes `attempt.blocked`, not `performer.completed`, durable, so it cannot match this edge |"
	if count := strings.Count(contents, row); count != 1 {
		t.Fatalf("draft no-blocking-output E.2 row count=%d, want 1: %q", count, row)
	}
}
