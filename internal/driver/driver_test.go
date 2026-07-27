package driver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/acceptance"
	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
	"github.com/BeomSeogKim/Partitur/internal/validate"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

func TestSelectSliceRejectsEachUnsupportedShape(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   error
	}{
		{
			name: "more than one movement",
			mutate: func(score map[string]any) {
				final := score["movements"].([]any)[0].(map[string]any)
				final["needs"] = []any{"prepare"}
				final["inputs"] = []any{"notes"}
				score["movements"] = append(
					[]any{
						map[string]any{
							"id":          "prepare",
							"part":        "reader",
							"grants":      []any{"repo_read"},
							"instruction": "Prepare notes.",
							"outputs": []any{
								map[string]any{
									"id":   "notes",
									"kind": "artifact",
								},
							},
							"acceptance": map[string]any{
								"hard": []any{
									map[string]any{
										"id":       "notes-present",
										"artifact": "notes",
									},
								},
							},
						},
					},
					map[string]any{
						"id": "sentinel",
					},
				)
				movements := score["movements"].([]any)
				movements[len(movements)-1] = final
			},
			want: ErrUnsupportedSlice,
		},
		{
			name: "external hard criterion",
			mutate: func(score map[string]any) {
				movement := score["movements"].([]any)[0].(map[string]any)
				movement["acceptance"] = map[string]any{
					"hard": []any{
						map[string]any{"id": "external", "run": []any{"true"}},
					},
				}
			},
			want: acceptance.ErrUnsupportedCriteria,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			score := sliceScore()
			test.mutate(score)
			preparation := prepareFixture(t, score)
			_, _, _, _, err := selectSlice(preparation)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestEffectiveAuthorityDoesNotInferReadFromWrite(t *testing.T) {
	score := prepareFixture(t, sliceScore()).Score
	movement := score.Movements()[0]
	movement.Grants = []string{"repo_write"}
	grants := effectiveGrants(movement, score.EffectivePolicy())
	if len(grants.PathsRW) != 1 || len(grants.PathsRO) != 0 {
		t.Fatalf("effective grants = %+v", grants)
	}
}

func TestProbeAdmissionRejectsEachFailClosedBoundary(t *testing.T) {
	movement := score.MovementView{
		Grants: []string{"repo_read"},
	}
	part := score.PartView{
		Capabilities: []string{"repo_read"},
		ReadOnly:     true,
	}
	policy := score.PolicyView{AllowedPaths: []string{"**"}}
	performer := cast.PerformerView{}
	probe := protocol.ProbeResult{
		Capabilities: protocol.Capabilities{RepoRead: true},
		Enforcement: protocol.Enforcement{
			ReadOnly:      true,
			NetworkGrants: true,
			ShellGrants:   true,
		},
	}
	t.Run("capability", func(t *testing.T) {
		probe := probe
		probe.Capabilities.RepoRead = false
		if _, err := admitProbe(
			movement,
			part,
			policy,
			performer,
			probe,
		); !errors.Is(err, ErrCapability) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("strict enforcement", func(t *testing.T) {
		probe := probe
		probe.Enforcement.ReadOnly = false
		if _, err := admitProbe(
			movement,
			part,
			policy,
			performer,
			probe,
		); !errors.Is(err, ErrEnforcement) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("advisory records exact dimension", func(t *testing.T) {
		probe := probe
		probe.Enforcement.ReadOnly = false
		performer := performer
		performer.AllowAdvisoryEnforcement = true
		dimensions, err := admitProbe(
			movement,
			part,
			policy,
			performer,
			probe,
		)
		if err != nil || len(dimensions) != 1 ||
			dimensions[0] != "read_only" {
			t.Fatalf("dimensions=%v error=%v", dimensions, err)
		}
	})
}

func TestRunIDWriteFailureStopsBeforeDriverAuthority(t *testing.T) {
	preparation := prepareRunnableFixture(t, sliceScore(), sliceCast())
	writeErr := errors.New("stdout unavailable")
	result := run(
		context.Background(),
		preparation,
		func(runID runstate.RunID) error {
			if runID == "" {
				t.Fatal("observer received empty run id")
			}
			return writeErr
		},
		testDependencies(),
	)
	if result.RunID == "" || result.Outcome != OutcomeInterrupted ||
		result.Reason != "" || !errors.Is(result.Err, writeErr) {
		t.Fatalf("result = %#v", result)
	}
	state := replayDriverState(t, preparation, result.RunID)
	if state.Run.Terminal() || state.Authority.Epoch != 0 {
		t.Fatalf(
			"run=%s authority_epoch=%d",
			state.Run,
			state.Authority.Epoch,
		)
	}
	assertNoDriverLease(t, preparation.RepositoryRoot, result.RunID)
}

func TestUnrepresentableWireBudgetInterruptsBeforeDriverAuthority(t *testing.T) {
	scoreDocument := sliceScore()
	budget := scoreDocument["policy"].(map[string]any)["budget"].(map[string]any)
	budget["active_wall_clock_min"] = float64((1<<53-1)/60_000 + 1)
	preparation := prepareRunnableFixture(t, scoreDocument, sliceCast())
	result := run(
		context.Background(),
		preparation,
		func(runstate.RunID) error { return nil },
		testDependencies(),
	)
	if result.RunID == "" || result.Outcome != OutcomeInterrupted ||
		result.Reason != "" || result.Err == nil {
		t.Fatalf("result = %#v", result)
	}
	state := replayDriverState(t, preparation, result.RunID)
	if state.Run.Terminal() || state.Authority.Epoch != 0 {
		t.Fatalf(
			"run=%s authority_epoch=%d",
			state.Run,
			state.Authority.Epoch,
		)
	}
	assertNoDriverLease(t, preparation.RepositoryRoot, result.RunID)
}

func TestPostCreationOperationalFailureLeavesResumableRun(t *testing.T) {
	castDocument := sliceCast()
	worker := castDocument["performers"].(map[string]any)["worker"].(map[string]any)
	worker["adapter"] = "driver-interruption-fixture"
	preparation := prepareRunnableFixture(t, sliceScore(), castDocument)
	result := run(
		context.Background(),
		preparation,
		func(runstate.RunID) error { return nil },
		testDependencies(),
	)
	if result.RunID == "" || result.Outcome != OutcomeInterrupted ||
		result.Reason != "" || result.Err == nil {
		t.Fatalf("result = %#v", result)
	}
	state := replayDriverState(t, preparation, result.RunID)
	if state.Run.Terminal() {
		t.Fatalf("operational interruption terminalized run as %s", state.Run)
	}
	if state.Authority.Epoch != 1 {
		t.Fatalf("authority epoch = %d, want 1", state.Authority.Epoch)
	}
	assertNoDriverLease(t, preparation.RepositoryRoot, result.RunID)
}

func TestStoppedClassifiesOnlyAppendixDHalts(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		outcome Outcome
		reason  string
	}{
		{
			name:    "ordinary operational failure",
			err:     errors.New("filesystem unavailable"),
			outcome: OutcomeInterrupted,
		},
		{
			name:    "journal corrupt",
			err:     runstore.ErrJournalCorrupt,
			outcome: OutcomeHalted,
			reason:  "journal_corrupt",
		},
		{
			name:    "journal idempotency conflict",
			err:     runstore.ErrJournalIdempotencyConflict,
			outcome: OutcomeHalted,
			reason:  "journal_idempotency_conflict",
		},
		{
			name:    "owner unverifiable",
			err:     runstore.ErrLeaseOwnerUnverifiable,
			outcome: OutcomeHalted,
			reason:  "owner_unverifiable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := stopped(Result{RunID: "run-1"}, test.err)
			if result.Outcome != test.outcome ||
				result.Reason != test.reason ||
				!errors.Is(result.Err, test.err) {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestRecoveryHaltRetainsDriverLease(t *testing.T) {
	if releasesDriverLease(OutcomeHalted) {
		t.Fatal("recovery halt would release its safety interlock")
	}
	for _, outcome := range []Outcome{
		OutcomeSucceeded,
		OutcomeFailed,
		OutcomeCancelled,
		OutcomeInterrupted,
	} {
		if !releasesDriverLease(outcome) {
			t.Fatalf("outcome %s would strand a live driver's lease", outcome)
		}
	}
}

func TestBudgetTimeoutDoesNotOverflowWireSafeRemainder(t *testing.T) {
	const maxWireSafeMS = int64(1<<53 - 1)
	const maxWholeMinutes = maxWireSafeMS / 60_000
	remaining, err := initialRemainingMS(maxWholeMinutes)
	if err != nil || remaining != maxWholeMinutes*60_000 {
		t.Fatalf("maximum whole-minute remainder=%d error=%v", remaining, err)
	}
	if _, err := initialRemainingMS(maxWholeMinutes + 1); err == nil {
		t.Fatal("unrepresentable minute budget was admitted to the wire")
	}
	if timeout := budgetTimeout(maxWireSafeMS); timeout != time.Duration(1<<63-1) {
		t.Fatalf("timeout = %s", timeout)
	}
	if timeout := budgetTimeout(600_000); timeout != 10*time.Minute {
		t.Fatalf("ordinary timeout = %s", timeout)
	}
}

func testDependencies() dependencies {
	return dependencies{
		probe:             faultpoint.Nop{},
		client:            adapter.NewClient(),
		resolveTrampoline: func() (string, error) { return "/unused/trampoline", nil },
		now:               time.Now,
		newID:             workspace.NewID,
		workspaceStart:    workspace.Start,
	}
}

func prepareRunnableFixture(
	t *testing.T,
	scoreDocument map[string]any,
	castDocument map[string]any,
) *validate.Preparation {
	t.Helper()
	preparation := prepareFixtureWithCast(t, scoreDocument, castDocument)
	for _, arguments := range [][]string{
		{"init"},
		{"config", "user.name", "Partitur Test"},
		{"config", "user.email", "partitur@example.invalid"},
		{"add", "partitur.yaml", ".partitur/cast.yaml"},
		{"commit", "-m", "fixture"},
	} {
		command := exec.Command("git", arguments...)
		command.Dir = preparation.RepositoryRoot
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	return preparation
}

func replayDriverState(
	t *testing.T,
	preparation *validate.Preparation,
	runID runstate.RunID,
) runstate.State {
	t.Helper()
	store, err := runstore.New(preparation.RepositoryRoot, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.Replay(
		runID,
		movementSeeds(preparation.Score),
		"driver.test.replay",
	)
	if err != nil {
		t.Fatal(err)
	}
	return replay.State
}

func assertNoDriverLease(
	t *testing.T,
	repositoryRoot string,
	runID runstate.RunID,
) {
	t.Helper()
	path := filepath.Join(
		repositoryRoot,
		".partitur",
		"runs",
		string(runID),
		"driver.lease",
	)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("driver lease after return: %v", err)
	}
}

func prepareFixture(
	t *testing.T,
	score map[string]any,
) *validate.Preparation {
	return prepareFixtureWithCast(t, score, sliceCast())
}

func prepareFixtureWithCast(
	t *testing.T,
	score map[string]any,
	castDocument map[string]any,
) *validate.Preparation {
	t.Helper()
	root := t.TempDir()
	writeJSON(t, filepath.Join(root, "partitur.yaml"), score)
	if err := os.Mkdir(filepath.Join(root, ".partitur"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(root, ".partitur", "cast.yaml"), castDocument)
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
	t.Setenv("HOME", t.TempDir())
	preparation, result := validate.Prepare()
	if result.Refusal != nil || result.HasDiagnostics() || preparation == nil {
		t.Fatalf("preparation=%#v result=%#v", preparation, result)
	}
	return preparation
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func sliceScore() map[string]any {
	return map[string]any{
		"score":    "0.2",
		"name":     "driver-fixture",
		"revision": float64(1),
		"status":   "finalized",
		"goal":     "Produce one report.",
		"verification": map[string]any{
			"expectation": map[string]any{
				"intent": "pass-existing-tests",
				"apply_gate": map[string]any{
					"require": []any{"verified"},
				},
			},
			"final_movement": "inspect",
		},
		"parts": map[string]any{
			"reader": map[string]any{
				"capabilities": []any{"repo_read"},
				"read_only":    true,
			},
		},
		"movements": []any{
			map[string]any{
				"id":          "inspect",
				"part":        "reader",
				"grants":      []any{"repo_read"},
				"instruction": "Write the report.",
				"outputs": []any{
					map[string]any{"id": "report", "kind": "artifact"},
				},
				"acceptance": map[string]any{
					"hard": []any{
						map[string]any{
							"id":       "report-present",
							"artifact": "report",
						},
					},
				},
			},
		},
		"policy": map[string]any{
			"allowed_paths": []any{"**"},
			"budget": map[string]any{
				"active_wall_clock_min": float64(10),
			},
		},
	}
}

func sliceCast() map[string]any {
	return map[string]any{
		"cast": "0.1",
		"performers": map[string]any{
			"worker": map[string]any{
				"adapter": "codex",
				"model":   "gpt-5.6-sol",
			},
		},
		"bindings": map[string]any{
			"reader": map[string]any{"performer": "worker"},
		},
	}
}
