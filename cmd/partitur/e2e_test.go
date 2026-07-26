package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const fakeAdapterEnvironment = "PARTITUR_VALIDATE_FAKE_ADAPTER"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("partitur-test 9.8.7")
		os.Exit(0)
	}
	if os.Getenv(fakeAdapterEnvironment) == "1" &&
		strings.HasPrefix(
			filepath.Base(os.Args[0]),
			"partitur-adapter-",
		) {
		runValidateFakeAdapter()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestValidateEndToEnd(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-claude")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, adapterID := range []string{
		"capability",
		"enforcement",
		"advisory",
	} {
		path := filepath.Join(bin, "partitur-adapter-"+adapterID)
		if err := os.Symlink(testExecutable, path); err != nil {
			t.Fatal(err)
		}
	}
	baseEnvironment := replaceEnvironment(os.Environ(), map[string]string{
		"PATH":                 bin,
		"PARTITUR_CLAUDE_BIN":  testExecutable,
		"PARTITUR_CODEX_BIN":   testExecutable,
		fakeAdapterEnvironment: "1",
	})

	t.Run("working_directory_is_the_only_repository_root", func(t *testing.T) {
		parent := t.TempDir()
		child := filepath.Join(parent, "nested")
		if err := os.Mkdir(child, 0o700); err != nil {
			t.Fatal(err)
		}
		writeValidateInputs(
			t,
			parent,
			e2eScore("plan"),
			e2eCast(map[string]string{"plan": "performer"}),
		)
		code, stdout, stderr := runValidateBinary(
			t,
			partitur,
			child,
			replaceEnvironment(
				baseEnvironment,
				map[string]string{"HOME": t.TempDir()},
			),
		)
		if code != 2 || stdout != "" ||
			!strings.Contains(
				stderr,
				filepath.Join(child, "partitur.yaml"),
			) {
			t.Fatalf(
				"exit=%d stdout=%q stderr=%q",
				code,
				stdout,
				stderr,
			)
		}
	})

	t.Run("real_first_party_adapters", func(t *testing.T) {
		repository := t.TempDir()
		home := t.TempDir()
		scoreDocument := strictRealAdapterScore()
		castDocument := map[string]any{
			"cast": "0.1",
			"performers": map[string]any{
				"claude-primary": map[string]any{
					"adapter": "claude",
					"model":   "claude-fable-5",
				},
				"codex-fallback": map[string]any{
					"adapter": "codex",
					"model":   "gpt-5.6-sol",
				},
			},
			"bindings": map[string]any{
				"implement": map[string]any{
					"performer": "claude-primary",
				},
				"verify": map[string]any{
					"performer": "codex-fallback",
				},
			},
		}
		writeValidateInputs(t, repository, scoreDocument, castDocument)
		before := repositoryTree(t, repository)
		code, stdout, stderr := runValidateBinary(
			t,
			partitur,
			repository,
			replaceEnvironment(baseEnvironment, map[string]string{"HOME": home}),
		)
		if code != 0 || stdout != "" || stderr != "" {
			t.Fatalf(
				"exit=%d stdout=%q stderr=%q",
				code,
				stdout,
				stderr,
			)
		}
		after := repositoryTree(t, repository)
		if !slicesEqual(before, after) {
			t.Fatalf("repository tree changed\nbefore=%#v\nafter=%#v", before, after)
		}
	})

	t.Run("one_defect_per_fatal_block", func(t *testing.T) {
		tests := []struct {
			name  string
			score map[string]any
			cast  map[string]any
			want  string
		}{
			{
				name: "score",
				score: func() map[string]any {
					value := e2eScore("plan")
					delete(value, "goal")
					return value
				}(),
				cast: e2eCast(map[string]string{"plan": "performer"}),
				want: "score: rule=\"score.schema\" pointer=\"/goal\" detail=\"required\"\n",
			},
			{
				name:  "cast",
				score: e2eScore("plan"),
				cast: func() map[string]any {
					value := e2eCast(map[string]string{"plan": "performer"})
					delete(
						value["performers"].(map[string]any)["performer"].(map[string]any),
						"model",
					)
					return value
				}(),
				want: "cast: rule=\"cast.schema\" origin=\"project\" pointer=\"/performers/performer/model\" detail=\"required\"\n",
			},
			{
				name:  "adapter_environment",
				score: e2eScore("plan"),
				cast: func() map[string]any {
					value := e2eCast(map[string]string{"plan": "performer"})
					value["performers"].(map[string]any)["performer"].(map[string]any)["adapter"] = "missing"
					return value
				}(),
				want: "adapter-environment: adapter=\"missing\" kind=\"executable_absent\" detail=\"partitur-adapter-missing is absent from PATH\" stderr=\"\"\n",
			},
			{
				name:  "capability",
				score: e2eScore("plan"),
				cast: func() map[string]any {
					value := e2eCast(map[string]string{"plan": "performer"})
					value["performers"].(map[string]any)["performer"].(map[string]any)["adapter"] = "capability"
					return value
				}(),
				want: "capability: part=\"plan\" performer=\"performer\" missing=[\"network\"]\n",
			},
			{
				name:  "enforcement",
				score: e2eScore("plan"),
				cast: func() map[string]any {
					value := e2eCast(map[string]string{"plan": "performer"})
					value["performers"].(map[string]any)["performer"].(map[string]any)["adapter"] = "enforcement"
					return value
				}(),
				want: "enforcement: movement=\"plan-movement\" part=\"plan\" performer=\"performer\" unmet=[\"read_only\"]\n",
			},
		}
		for _, test := range tests {
			test := test
			t.Run(test.name, func(t *testing.T) {
				repository := t.TempDir()
				home := t.TempDir()
				writeValidateInputs(t, repository, test.score, test.cast)
				code, stdout, stderr := runValidateBinary(
					t,
					partitur,
					repository,
					replaceEnvironment(
						baseEnvironment,
						map[string]string{"HOME": home},
					),
				)
				if code != 3 || stdout != "" || stderr != test.want {
					t.Fatalf(
						"exit=%d stdout=%q\nstderr=%q\nwant=%q",
						code,
						stdout,
						stderr,
						test.want,
					)
				}
			})
		}
	})

	t.Run("ordered_blocks_and_suppression", func(t *testing.T) {
		t.Run("score_then_cast", func(t *testing.T) {
			repository := t.TempDir()
			home := t.TempDir()
			scoreDocument := e2eScore("plan")
			delete(scoreDocument, "goal")
			castDocument := e2eCast(map[string]string{"plan": "performer"})
			delete(
				castDocument["performers"].(map[string]any)["performer"].(map[string]any),
				"model",
			)
			writeValidateInputs(
				t,
				repository,
				scoreDocument,
				castDocument,
			)
			code, stdout, stderr := runValidateBinary(
				t,
				partitur,
				repository,
				replaceEnvironment(
					baseEnvironment,
					map[string]string{"HOME": home},
				),
			)
			want := "" +
				"score: rule=\"score.schema\" pointer=\"/goal\" detail=\"required\"\n" +
				"cast: rule=\"cast.schema\" origin=\"project\" pointer=\"/performers/performer/model\" detail=\"required\"\n"
			if code != 3 || stdout != "" || stderr != want {
				t.Fatalf(
					"exit=%d stdout=%q\nstderr=%q\nwant=%q",
					code,
					stdout,
					stderr,
					want,
				)
			}
		})

		t.Run("cast_then_adapter_capability_enforcement", func(t *testing.T) {
			repository := t.TempDir()
			home := t.TempDir()
			scoreDocument := e2eScore(
				"bad",
				"capability",
				"enforcement",
				"missing",
			)
			castDocument := e2eCast(map[string]string{
				"bad":         "bad-performer",
				"capability":  "capability-performer",
				"enforcement": "enforcement-performer",
			})
			performers := castDocument["performers"].(map[string]any)
			performers["bad-performer"].(map[string]any)["adapter"] = "bad"
			performers["capability-performer"].(map[string]any)["adapter"] = "capability"
			performers["enforcement-performer"].(map[string]any)["adapter"] = "enforcement"
			writeValidateInputs(
				t,
				repository,
				scoreDocument,
				castDocument,
			)
			code, stdout, stderr := runValidateBinary(
				t,
				partitur,
				repository,
				replaceEnvironment(
					baseEnvironment,
					map[string]string{"HOME": home},
				),
			)
			want := "" +
				"cast: rule=\"cast.score\" origin=\"\" pointer=\"/bindings/missing\" detail=\"binding_missing\"\n" +
				"adapter-environment: adapter=\"bad\" kind=\"executable_absent\" detail=\"partitur-adapter-bad is absent from PATH\" stderr=\"\"\n" +
				"capability: part=\"capability\" performer=\"capability-performer\" missing=[\"network\"]\n" +
				"enforcement: movement=\"enforcement-movement\" part=\"enforcement\" performer=\"enforcement-performer\" unmet=[\"read_only\"]\n"
			if code != 3 || stdout != "" || stderr != want {
				t.Fatalf(
					"exit=%d stdout=%q\nstderr=%q\nwant=%q",
					code,
					stdout,
					stderr,
					want,
				)
			}
		})
	})

	t.Run("advisory_report_is_nonfatal", func(t *testing.T) {
		repository := t.TempDir()
		home := t.TempDir()
		scoreDocument := e2eScore("plan")
		castDocument := e2eCast(map[string]string{"plan": "performer"})
		performer := castDocument["performers"].(map[string]any)["performer"].(map[string]any)
		performer["adapter"] = "advisory"
		performer["allow_advisory_enforcement"] = true
		writeValidateInputs(t, repository, scoreDocument, castDocument)
		code, stdout, stderr := runValidateBinary(
			t,
			partitur,
			repository,
			replaceEnvironment(baseEnvironment, map[string]string{"HOME": home}),
		)
		want := "enforcement advisory: movement=\"plan-movement\" part=\"plan\" " +
			"performer=\"performer\" unmet=[\"read_only\"]\n"
		if code != 0 || stdout != "" || stderr != want {
			t.Fatalf(
				"exit=%d stdout=%q stderr=%q want=%q",
				code,
				stdout,
				stderr,
				want,
			)
		}
	})
}

func runValidateFakeAdapter() {
	adapterID := strings.TrimPrefix(
		filepath.Base(os.Args[0]),
		"partitur-adapter-",
	)
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	capabilities := map[string]any{
		"repo_read":          true,
		"repo_write":         true,
		"shell":              true,
		"network":            true,
		"resumable_sessions": true,
		"models":             []any{},
	}
	enforcement := map[string]any{
		"path_grants":    true,
		"read_only":      true,
		"network_grants": true,
		"shell_grants":   true,
		"read_grants":    true,
	}
	switch adapterID {
	case "capability":
		capabilities["network"] = false
	case "enforcement", "advisory":
		enforcement["read_only"] = false
	default:
		os.Exit(9)
	}
	response := map[string]any{
		"jsonrpc": "2.0",
		"id":      "probe",
		"result": map[string]any{
			"protocol": 2,
			"adapter": map[string]any{
				"id":      adapterID,
				"version": "1.2.3",
			},
			"capabilities": capabilities,
			"enforcement":  enforcement,
		},
	}
	_ = json.NewEncoder(os.Stdout).Encode(response)
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func buildE2EBinary(
	t *testing.T,
	root, outputDirectory, name string,
) string {
	t.Helper()
	output := filepath.Join(outputDirectory, name)
	command := exec.Command("go", "build", "-o", output, "./cmd/"+name)
	command.Dir = root
	command.Env = os.Environ()
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, data)
	}
	return output
}

func runValidateBinary(
	t *testing.T,
	binary, repository string,
	environment []string,
) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := exec.Command(binary, "validate")
	command.Dir = repository
	command.Env = environment
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	exitError, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatal(err)
	}
	return exitError.ExitCode(), stdout.String(), stderr.String()
}

func writeValidateInputs(
	t *testing.T,
	repository string,
	scoreDocument, castDocument map[string]any,
) {
	t.Helper()
	scoreData, err := json.Marshal(scoreDocument)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, "partitur.yaml"),
		scoreData,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	castDirectory := filepath.Join(repository, ".partitur")
	if err := os.MkdirAll(castDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	castData, err := json.Marshal(castDocument)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(castDirectory, "cast.yaml"),
		castData,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
}

func strictRealAdapterScore() map[string]any {
	return map[string]any{
		"score":    "0.2",
		"name":     "real-adapter-fixture",
		"revision": float64(1),
		"status":   "finalized",
		"goal":     "Validate real adapters.",
		"verification": map[string]any{
			"expectation": map[string]any{
				"intent": "pass-existing-tests",
				"apply_gate": map[string]any{
					"waived": true,
					"reason": "end-to-end adapter compatibility fixture",
				},
			},
		},
		"parts": map[string]any{
			"implement": map[string]any{
				"capabilities": []any{
					"repo_read",
					"repo_write",
					"shell",
					"network",
					"resumable_sessions",
				},
			},
			"verify": map[string]any{
				"capabilities": []any{
					"repo_read",
					"shell",
					"network",
				},
			},
		},
		"movements": []any{
			map[string]any{
				"id":          "implement",
				"part":        "implement",
				"grants":      []any{"repo_read", "repo_write", "shell", "network"},
				"instruction": "Validate the adapters.",
				"outputs": []any{
					map[string]any{
						"id":   "change-set",
						"kind": "change_set",
					},
				},
				"acceptance": map[string]any{
					"hard": []any{
						map[string]any{
							"id":  "complete",
							"run": []any{"true"},
						},
					},
				},
			},
			map[string]any{
				"id":          "verify",
				"part":        "verify",
				"needs":       []any{"implement"},
				"grants":      []any{"repo_read", "shell", "network"},
				"instruction": "Verify the adapters.",
				"inputs":      []any{"change-set"},
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

func e2eScore(parts ...string) map[string]any {
	partValues := make(map[string]any, len(parts))
	movements := make([]any, 0, len(parts))
	for index, partID := range parts {
		partValues[partID] = map[string]any{
			"capabilities": []any{
				"repo_read",
				"shell",
				"network",
			},
		}
		movement := map[string]any{
			"id":          partID + "-movement",
			"part":        partID,
			"grants":      []any{"repo_read", "shell", "network"},
			"instruction": "Perform " + partID + ".",
		}
		if index == 0 {
			movement["phase"] = "draft"
		}
		movements = append(movements, movement)
	}
	return map[string]any{
		"score":    "0.2",
		"name":     "validate-e2e",
		"revision": float64(1),
		"status":   "draft",
		"goal":     "Validate the fixture.",
		"draft": map[string]any{
			"interview_movement": parts[0] + "-movement",
		},
		"parts":     partValues,
		"movements": movements,
		"policy": map[string]any{
			"allowed_paths": []any{"**"},
			"budget": map[string]any{
				"active_wall_clock_min": float64(10),
			},
		},
	}
}

func e2eCast(bindings map[string]string) map[string]any {
	performers := make(map[string]any, len(bindings))
	bindingValues := make(map[string]any, len(bindings))
	for partID, performerID := range bindings {
		performers[performerID] = map[string]any{
			"adapter": performerID + "-adapter",
			"model":   "model",
		}
		bindingValues[partID] = map[string]any{
			"performer": performerID,
		}
	}
	return map[string]any{
		"cast":       "0.1",
		"performers": performers,
		"bindings":   bindingValues,
	}
}

func replaceEnvironment(
	environment []string,
	replacements map[string]string,
) []string {
	result := make([]string, 0, len(environment)+len(replacements))
	seen := make(map[string]bool, len(replacements))
	for _, entry := range environment {
		name := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			name = entry[:index]
		}
		if value, exists := replacements[name]; exists {
			if !seen[name] {
				result = append(result, name+"="+value)
				seen[name] = true
			}
			continue
		}
		result = append(result, entry)
	}
	for name, value := range replacements {
		if !seen[name] {
			result = append(result, name+"="+value)
		}
	}
	return result
}

func repositoryTree(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(
		root,
		func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if path == root {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			paths = append(paths, relative)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	return paths
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
