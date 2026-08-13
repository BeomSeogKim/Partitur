package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

func TestValidateCriterionErrorEndpointSynthetic(t *testing.T) {
	if err := validateCriterionErrorEndpoint(criterionErrorEvents(false), false); err != nil {
		t.Fatalf("valid left endpoint: %v", err)
	}
	if err := validateCriterionErrorEndpoint(criterionErrorEvents(true), true); err != nil {
		t.Fatalf("valid right endpoint: %v", err)
	}
	tests := []struct {
		name, want string
		events     []runstate.Event
		wantRight  bool
	}{
		{"exactly_one_error", "ERROR count=2", append(criterionErrorEvents(false), criterionCompleted("command-passes", "ERROR", "workspace_verification_failed")), false},
		{"verification_detail", "detail=\"other\"", []runstate.Event{criterionStarted("command-passes"), criterionCompleted("command-passes", "ERROR", "other")}, false},
		{"erroring_criterion", "criterion_id=\"other\"", []runstate.Event{criterionStarted("other"), criterionCompleted("other", "ERROR", "workspace_verification_failed")}, false},
		{"no_post_error_start", "criterion.started appears after", append(criterionErrorEvents(false), criterionStarted("later")), false},
		{"left_has_no_failure", "left endpoint", criterionErrorEvents(true), false},
		{"right_has_one_failure", "right endpoint", append(criterionErrorEvents(true), acceptanceFailed("command-passes", "criterion_errored")), true},
		{"failure_binds_criterion", "want erroring criterion", append(criterionErrorEvents(false), acceptanceFailed("other", "criterion_errored")), true},
		{"failure_reason", "reason=\"other\"", append(criterionErrorEvents(false), acceptanceFailed("command-passes", "other")), true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateCriterionErrorEndpoint(test.events, test.wantRight)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want containing %q", err, test.want)
			}
		})
	}
}

func criterionErrorEvents(right bool) []runstate.Event {
	events := []runstate.Event{criterionStarted("command-passes"), criterionCompleted("command-passes", "ERROR", "workspace_verification_failed")}
	if right {
		events = append(events, acceptanceFailed("command-passes", "criterion_errored"))
	}
	return events
}

func criterionStarted(id string) runstate.Event {
	return criterionEvent(runstate.EventCriterionStarted, map[string]any{"criterion_id": id})
}

func criterionCompleted(id, outcome, detail string) runstate.Event {
	return criterionEvent(runstate.EventCriterionCompleted, map[string]any{"criterion_id": id, "outcome": outcome, "error_detail": detail})
}

func acceptanceFailed(id, reason string) runstate.Event {
	return criterionEvent(runstate.EventAcceptanceFailed, map[string]any{"failed_criterion_id": id, "reason": reason})
}

func criterionEvent(eventType runstate.EventType, payload map[string]any) runstate.Event {
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return runstate.Event{Type: eventType, Payload: encoded}
}
