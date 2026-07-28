package successor

import (
	"errors"
	"reflect"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

func TestClassifyInfrastructure(t *testing.T) {
	cases := []struct {
		name  string
		input ClassificationInput
		want  runstate.Disposition
	}{
		{
			name:  "unvisited fallback with time charges fallback",
			input: ClassificationInput{Failure: attemptFailure(KindAdapterUnavailable), HasUnvisitedFallback: true, RemainingTimeMS: 1},
			want:  retryDisposition(ChargeFallback),
		},
		{
			name:  "no fallback with time terminalizes fallbacks exhausted",
			input: ClassificationInput{Failure: attemptFailure(KindModelUnavailable), RemainingTimeMS: 1},
			want:  terminalDisposition("fallbacks_exhausted"),
		},
		{
			name:  "zero time terminalizes budget exhausted even with fallback",
			input: ClassificationInput{Failure: attemptFailure(KindProviderTimeout), HasUnvisitedFallback: true},
			want:  terminalDisposition(KindBudgetExhausted),
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := Classify(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("Classify() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestClassifyQuality(t *testing.T) {
	cases := []struct {
		name  string
		input ClassificationInput
		want  runstate.Disposition
	}{
		{
			name:  "retry remains with time charges quality retry",
			input: ClassificationInput{Failure: attemptFailure(KindTaskFailed), RetriesConsumed: 1, RetriesPerMovement: 2, RemainingTimeMS: 1},
			want:  retryDisposition(ChargeQualityRetry),
		},
		{
			name:  "retry cap terminalizes retries exhausted",
			input: ClassificationInput{Failure: acceptanceFailure("criterion_failed"), RetriesConsumed: 2, RetriesPerMovement: 2, RemainingTimeMS: 1},
			want:  terminalDisposition("retries_exhausted"),
		},
		{
			name:  "zero time terminalizes budget exhausted despite retry",
			input: ClassificationInput{Failure: acceptanceFailure("criterion_errored"), RetriesPerMovement: 1},
			want:  terminalDisposition(KindBudgetExhausted),
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := Classify(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("Classify() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestClassifyImmediatelyTerminalNeverConsultsBudget(t *testing.T) {
	for _, kind := range []string{KindGrantDenied, KindProtocolError, KindBudgetExhausted} {
		t.Run(kind, func(t *testing.T) {
			got, err := Classify(ClassificationInput{
				Failure:              attemptFailure(kind),
				HasUnvisitedFallback: true,
				RetriesPerMovement:   99,
			})
			if err != nil {
				t.Fatal(err)
			}
			want := terminalDisposition(kind)
			if got != want {
				t.Fatalf("Classify() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestClassifyQualityNeverFallsBack(t *testing.T) {
	got, err := Classify(ClassificationInput{
		Failure:              attemptFailure(KindTaskFailed),
		HasUnvisitedFallback: true,
		RetriesConsumed:      1,
		RetriesPerMovement:   1,
		RemainingTimeMS:      1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := terminalDisposition("retries_exhausted"); got != want {
		t.Fatalf("Classify() = %+v, want %+v", got, want)
	}
}

func TestClassifyRejectsCasesOutsideAppendixD(t *testing.T) {
	for _, kind := range []string{"human_gate_rejected", "composition_failed", "composition_unresolvable"} {
		t.Run(kind, func(t *testing.T) {
			_, err := Classify(ClassificationInput{Failure: attemptFailure(kind)})
			if !errors.Is(err, ErrUnknownFailureCase) {
				t.Fatalf("Classify() error = %v, want unknown failure case", err)
			}
		})
	}
}

func TestRealizeRecordedDisposition(t *testing.T) {
	binding := cast.BindingView{Performer: "primary", Fallbacks: []string{"fallback-1", "fallback-2"}}
	cases := []struct {
		name  string
		input RealizationInput
		want  Realization
	}{
		{
			name: "quality retry keeps current performer",
			input: RealizationInput{
				Disposition:       retryDisposition(ChargeQualityRetry),
				CurrentPerformer:  "fallback-1",
				Binding:           binding,
				VisitedPerformers: []string{"primary", "fallback-1"},
			},
			want: Realization{Action: ActionPendingSuccessor, Performer: "fallback-1", Charge: ChargeQualityRetry},
		},
		{
			name: "fallback chooses immediate next unvisited performer",
			input: RealizationInput{
				Disposition:       retryDisposition(ChargeFallback),
				CurrentPerformer:  "primary",
				Binding:           binding,
				VisitedPerformers: []string{"primary"},
			},
			want: Realization{Action: ActionPendingSuccessor, Performer: "fallback-1", Charge: ChargeFallback},
		},
		{
			name:  "none carries terminal reason verbatim",
			input: RealizationInput{Disposition: terminalDisposition("grant_denied")},
			want:  Realization{Action: ActionMovementFailed, Charge: ChargeNone, TerminalReason: "grant_denied"},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := Realize(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("Realize() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestRealizeFallbackNeverRevisitsEarlierPerformer(t *testing.T) {
	input := RealizationInput{
		Disposition:       retryDisposition(ChargeFallback),
		CurrentPerformer:  "fallback-1",
		Binding:           cast.BindingView{Performer: "primary", Fallbacks: []string{"fallback-1", "fallback-2"}},
		VisitedPerformers: []string{"primary"},
	}
	got, err := Realize(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Performer != "fallback-2" {
		t.Fatalf("fallback successor = %q, want fallback-2", got.Performer)
	}
}

func TestRealizeIsIdempotentForRecordedDisposition(t *testing.T) {
	input := RealizationInput{
		Disposition:       retryDisposition(ChargeFallback),
		CurrentPerformer:  "primary",
		Binding:           cast.BindingView{Performer: "primary", Fallbacks: []string{"fallback-1", "fallback-2"}},
		VisitedPerformers: []string{"primary"},
	}
	first, err := Realize(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Realize(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || first.Performer != "fallback-1" {
		t.Fatalf("repeated realization changed successor: first=%+v second=%+v", first, second)
	}
}

func TestRealizeRejectsUnavailableRecordedFallback(t *testing.T) {
	_, err := Realize(RealizationInput{
		Disposition:       retryDisposition(ChargeFallback),
		Binding:           cast.BindingView{Fallbacks: []string{"fallback-1"}},
		VisitedPerformers: []string{"fallback-1"},
	})
	if !errors.Is(err, ErrFallbackUnavailable) {
		t.Fatalf("Realize() error = %v, want unavailable fallback", err)
	}
}

func attemptFailure(kind string) FailureCase {
	return FailureCase{AttemptKind: kind}
}

func acceptanceFailure(reason string) FailureCase {
	return FailureCase{AcceptanceReason: reason}
}
