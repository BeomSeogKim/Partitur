package adapter

import (
	"bytes"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

func TestEncodeExecuteRequestTagsResolvedDecisions(t *testing.T) {
	request := protocol.ExecuteRequest{
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
	}
	encoded, err := encodeExecuteRequest(request)
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
	frameBytes, err := SerializedExecuteRequestBytes(request)
	if err != nil {
		t.Fatal(err)
	}
	if frameBytes != len(encoded)-1 {
		t.Fatalf("serialized frame bytes = %d, want %d", frameBytes, len(encoded)-1)
	}
}
