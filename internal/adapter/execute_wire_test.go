package adapter

import (
	"bytes"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

func TestEncodeExecuteRequestTagsResolvedDecisions(t *testing.T) {
	encoded, err := encodeExecuteRequest(protocol.ExecuteRequest{
		ResolvedDecisions: []protocol.ResolvedDecision{
			{
				DecisionID: "question-1",
				Kind:       protocol.ResolvedDecisionAnswer,
			},
			{
				DecisionID: "proposal-1",
				Kind:       protocol.ResolvedDecisionAmendmentRejected,
				Reason:     "human_rejected",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(
		`"resolved_decisions":[` +
			`{"decision_id":"question-1","kind":"answer","answer":""},` +
			`{"decision_id":"proposal-1","kind":"amendment_rejected","reason":"human_rejected"}` +
			`]`,
	)
	if !bytes.Contains(encoded, want) {
		t.Fatalf("encoded request missing tagged resolutions:\n%s", encoded)
	}
}
