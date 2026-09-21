package claude

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/adapterkit"
)

func TestProgressKeepsMultiLineAssistantText(t *testing.T) {
	t.Parallel()

	report := "# Review: `### Solo: one part, no gate`\nPASS one item: \"the gate is named\"\nFAIL another item: \"no run store is there\""
	sink := &recordingSink{}
	state := streamState{sink: sink}
	if err := state.consume(assistantTextLine(t, report)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sink.progress, []string{"assistant: " + report}) {
		t.Fatalf("progress = %#v", sink.progress)
	}

	oversized := strings.Repeat("a line of the verifier's report\n", 400)
	boundedSink := &recordingSink{}
	boundedState := streamState{sink: boundedSink}
	if err := boundedState.consume(assistantTextLine(t, oversized)); err != nil {
		t.Fatal(err)
	}
	if len(boundedSink.progress) != 1 {
		t.Fatalf("progress = %#v", boundedSink.progress)
	}
	summary := strings.TrimPrefix(boundedSink.progress[0], "assistant: ")
	if len(summary) > adapterkit.MaxEventMessageBytes {
		t.Fatalf("summary of %d bytes exceeds the %d byte cap", len(summary), adapterkit.MaxEventMessageBytes)
	}
	if !strings.Contains(summary, "\n") {
		t.Fatalf("summary kept only the first line: %q", summary)
	}
}

func assistantTextLine(t *testing.T, text string) []byte {
	t.Helper()
	encoded, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":` + string(encoded) + `}]}}`)
}
