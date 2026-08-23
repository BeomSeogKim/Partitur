package docmarker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
)

type remainingSpecimenExpectation struct {
	language string
	prefix   string
	hash     string
}

type remainingBlockExpectation struct {
	name               string
	start              string
	end                string
	carrierMarker      string
	carrierHash        string
	referenceCount     int
	carrierAssignments []int
	relocatedCarriers  map[int][]string
	specimens          []remainingSpecimenExpectation
}

func TestRemainingFenceDecomposition(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	inventory := remainingOriginalAnnotationInventory(t)

	blocks := []remainingBlockExpectation{
		{
			name:           "repository-layout",
			start:          "**Repository-layout clauses.**",
			end:            "Git refs the core owns — never user-visible branches",
			carrierMarker:  "**Repository-layout clauses.**",
			carrierHash:    "b68fe3023223a0e3245d5e858e9a8d23e3fca87122484cca721edaa6deb909cf",
			referenceCount: 14,
			carrierAssignments: []int{
				2, 1, 2, 1, 3, 2, 1, 1, 0, 2, 1, 1, 1, 1, 0, 2, 1, 0, 1, 0, 0, 2, 1, 0, 1,
			},
			relocatedCarriers: map[int][]string{
				2: {
					"`partitur init` creates `.partitur/`, its `.gitignore` entries for `runs/` and `work/`",
				},
				7: {
					"`manifest.yaml` is a rebuildable projection/checkpoint of the journal.",
					"The manifest records the source revision and both hashes.",
					"The run manifest pins the resolved performer, adapter id, and model for every part",
					"the manifest additionally records, **per attempt**, that observed",
				},
				9: {
					"**The snapshot and the complete approval payload are written *before* the prepare.**",
					"an **auto** proposal has no durable proposal record to rebuild them from.",
					"Recovery then\n  *replays* a plan rather than recomputing one.",
				},
				10: {
					"Decision-time revalidation needs the original operations because §9 re-runs steps 1–9.",
					"Neither `typed_delta` nor `actual_impact` can reconstruct the original operations; both are lossy",
				},
				13: {
					"**Artifact instances.** Score-declared output ids are *logical* ids.",
					"**Artifact recording atomicity.** Recording an artifact follows a fixed order:",
				},
				14: {
					"`runs/<run-id>/inputs/<movement-id>/revision-<score-revision>/subject-tree.json`.",
					"The file is fsynced before rename and then made read-only to the performer; for\nrename durability, see §1's “Rename durability”. Its delivered `instance_id` is",
					"all retries and fallbacks on that movement\nrevision reuse that instance and its exact raw bytes.",
				},
				15: {
					"**Session hints and privacy.** Session continuity across attempts is carried by an",
					"they live in `runs/<id>/session/` with mode\n`0600` and are deleted with the run.",
				},
				17: {
					"`runs/<run-id>/authority.json` is a\n  fsynced checkpoint of that projection, not its authority.",
					"It lives in `driver.lease` and in the owner's memory only.",
					"`authority.json` is never consulted for the\n  token, and never authoritative for the epoch.",
				},
				18: {
					"**Diagnostics privacy.** Vendor and adapter `stderr` may contain a session id the adapter",
					"Adapters MUST buffer `stderr` to a bounded size and sanitize it against",
				},
				20: {
					"the core captures `stdout` and `stderr` separately at\n  `attempts/<attempt-id>/criteria/<criterion-id>/stdout` and `stderr`.",
				},
				21: {
					"the core captures `stdout` and `stderr` separately at\n  `attempts/<attempt-id>/criteria/<criterion-id>/stdout` and `stderr`.",
				},
				23: {
					"The core immediately copies the file announced by an `artifact` notification.",
					"**Artifact recording atomicity.** Recording an artifact follows a fixed order:",
				},
				24: {
					"TMPDIR=<attempt staging directory>/tmp\n  TMP=<attempt staging directory>/tmp\n  TEMP=<attempt staging directory>/tmp",
					"The three temporary-directory spellings are core-set to the attempt staging\n  directory",
				},
			},
			specimens: []remainingSpecimenExpectation{
				{language: "text", prefix: "<repo>/\n", hash: "e9d8317897c69d1389551fa42b898e858be535750437a0afc9f103f007197847"},
				{language: "text", prefix: "~/.config/partitur/cast.yaml", hash: "64a8522db5b9a0a0acbb23479be84849f40b636fef757a0e0d46c745051f5ad3"},
			},
		},
		{
			name:           "score-example",
			start:          "**Score-example field clauses.**",
			end:            "**Path policy semantics.**",
			carrierMarker:  "**Score-example field clauses.**",
			carrierHash:    "475969535298313966debca0bd196a8f3484107a1599f3002c770d5a7060ff9f",
			referenceCount: 31,
			carrierAssignments: []int{
				1, 1, 0, 2, 1, 0, 1, 0, 2, 1, 4, 0, 1, 0, 4, 2, 0, 2, 0,
				0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 1, 1, 0, 0, 1,
			},
			relocatedCarriers: map[int][]string{
				3: {
					"| `status` | required | — | either `draft` or `finalized` |",
				},
				6: {
					"While `status: draft`, only the movement named by\n`draft.interview_movement` may run.",
				},
				8: {
					"1. `status: finalized` requires every open question resolved or waived,",
				},
				12: {
					"**The `apply` judgment** branches explicitly on the gate",
					"| `apply_gate.require` / `.waived` | exactly one, iff `status: finalized` | — | `require` non-empty, duplicate-free |",
				},
				14: {
					"| `apply_gate.predicates` | optional | `[]` | closed enum |",
				},
				17: {
					"A `read_only` part can never receive\n   `repo_write`.",
				},
				19: {
					"While `status: draft`, only the movement named by\n`draft.interview_movement` may run.",
				},
				20: {
					"**`kind: change_set` is a core-synthesized logical output, never an artifact.**",
				},
				21: {
					"performer cannot emit one and must never be asked to.",
					"satisfied only by `change_set.recorded`, which the core appends after capturing the\n    worktree.",
				},
				22: {
					"A movement that requests a `repo_write` grant must **declare** ≥1 `hard` criterion or\n   `human_gate: always`.",
				},
				23: {
					"A movement that requests a `repo_write` grant must **declare** ≥1 `hard` criterion or\n   `human_gate: always`.",
				},
				24: {
					"Every **score-declared** acceptance criterion carries an `id` unique within its movement.",
					"`status` and `apply` always attach the\ncriterion count and ids/spec hashes",
				},
				25: {
					"**The final verification movement** (`verification.final_movement`, required unless the gate\nis waived) is the run's **terminal sink**",
				},
				26: {
					"transitively depend on every non-draft movement via `needs`, and must have no downstream\n    movement",
				},
				27: {
					"otherwise it must be declared, must not hold `repo_write`, must\n    transitively depend",
				},
				29: {
					"**Apply-gate achievability** — a finalized score whose gate can never be satisfied is\n    rejected (§8): `require ∋ verified` ⇒ the final movement **declares** ≥1 hard criterion;\n    `require ∋ reviewed` or any predicate present ⇒ it declares ≥1 review criterion with a\n    typed `findings` output; `require ∋ approved` ⇒ it declares `human_gate: always`.",
				},
				30: {
					"**Apply-gate achievability** — a finalized score whose gate can never be satisfied is\n    rejected (§8): `require ∋ verified` ⇒ the final movement **declares** ≥1 hard criterion;\n    `require ∋ reviewed` or any predicate present ⇒ it declares ≥1 review criterion with a\n    typed `findings` output; `require ∋ approved` ⇒ it declares `human_gate: always`.",
				},
				31: {
					"every\n  declared review criterion is satisfied by a well-formed, subject-bound findings artifact (§7).",
				},
				32: {
					"every\n  declared review criterion is satisfied by a well-formed, subject-bound findings artifact (§7).",
				},
				36: {
					"`active_wall_clock_min` bounds active execution only — adapter runs,\nacceptance, **composition**, retries, fallbacks, revision restarts, and decision resumes —\nexcluding `WAITING_HUMAN` and stopped time.",
					"Active time is delimited by paired, fsynced `execution.started` / `execution.stopped`\n  events.",
					"Each attempt receives the remainder at its start (`request.budget`).",
				},
				37: {
					"`retries_per_movement` is the movement's **quality-retry budget**",
				},
			},
			specimens: []remainingSpecimenExpectation{
				{language: "yaml", prefix: "score: \"0.2\"\n", hash: "5b3f5c65fc2c32177336e28cc38cdc07600a84e3810fe6bf536fa2275ac1fa7c"},
			},
		},
		{
			name:           "adapter-methods",
			start:          "**Adapter-method field clauses.**",
			end:            "**Validation probing.**",
			carrierMarker:  "**Adapter-method field clauses.**",
			carrierHash:    "6b026743bb12d09c9cca538df0bcfb70339bd5d2d9d95ac3f1a04e91d79fbb26",
			referenceCount: 6,
			carrierAssignments: []int{
				7, 3, 1, 1, 1, 2, 1, 1, 1, 0, 2, 1, 3, 1, 1, 3, 1, 1, 1, 2, 2,
				0, 2, 1, 0, 0, 2, 1, 1, 0, 3,
			},
			relocatedCarriers: map[int][]string{
				10: {
					"| `context` | optional | **absent** — omitted from the canonical projection, from `brief`, and from A.5 alike, never sent as `\"\"` | — |",
				},
				22: {
					"Feedback is read-only diagnosis; rejected changes are never applied\nto the base.",
				},
				25: {
					"Each attempt gets a fresh worktree built from the **approved results of its dependency\n  movements** (the clean base)",
				},
				26: {
					"plus an always-writable `output_dir` at\n  `.partitur/work/<run-id>/<attempt-id>/output/`",
				},
				30: {
					"returns `outcome: waiting_human` with\n  `pending_decision_ids`, and exits.",
					"it must equal every\n  emitted question plus precisely those proposals with `requires_decision: true`.",
					"Adapter outcome `waiting_human` maps to terminal `attempt.blocked`, taking the attempt from\n  `RUNNING` to `BLOCKED`.",
				},
			},
			specimens: []remainingSpecimenExpectation{
				{language: "text", prefix: "probe() -> {\n", hash: "6b3a469e2260c0ff61eba49c64e199045008154f49aa6dca160e8dc3c59ac2bb"},
				{language: "text", prefix: "execute(request) -> streams `event` notifications", hash: "211c14a07c39e6f5943491fb634db41d76ed4519383c3e21d7d9d97f75159053"},
				{language: "text", prefix: "cancel(attempt_id) ->", hash: "e0a1ee505422a22289df57eb0662c372c5b1a485f4d96effe5dd4c1c33d2a472"},
			},
		},
		{
			name:               "event-notifications",
			start:              "**Event-notification field clauses.**",
			end:                "For change-set capture and delivery",
			carrierMarker:      "**Event-notification field clauses.**",
			carrierHash:        "a68bbea0d659cbe84c9cddd65d8765cd7b4e68c1953f72e749085cbc4616721f",
			referenceCount:     3,
			carrierAssignments: []int{2, 0, 1, 1},
			relocatedCarriers: map[int][]string{
				2: {
					"**Amendment format v0.2.** A `proposal` carries:",
				},
			},
			specimens: []remainingSpecimenExpectation{
				{language: "text", prefix: "log {", hash: "da6b056b719b96a35d46bdcdf085db33189b705ba6b29d4487983e262e5c03b3"},
				{language: "text", prefix: "progress {", hash: "eed2e976cc47077f22e1f5fe72bd464e57f0652fd59bc9c0026a97394d2c3e81"},
				{language: "text", prefix: "artifact {", hash: "dca9f393ce6a517b5495b51ce1fe074f31f718bb0cfc80721160921d9142b1e3"},
				{language: "text", prefix: "proposal {", hash: "a8cab1df96b74ce4fa52a72986c4120fc3b2431125d6ab49c4386caf6b783a8a"},
				{language: "text", prefix: "question {", hash: "9e1122bc693350906450a36c94559221c3fc5d3cb42592491c416124ca860dc5"},
			},
		},
		{
			name:               "reserved-inputs",
			start:              "**Reserved-input field clauses.**",
			end:                "**Review-subject publication.**",
			carrierMarker:      "**Reserved-input field clauses.**",
			carrierHash:        "ede5de419510cecb787823db9883a86d74a29c77af1051006ea9107e24324af8",
			referenceCount:     3,
			carrierAssignments: []int{1, 1, 1, 2, 1, 1, 0, 2},
			relocatedCarriers: map[int][]string{
				7: {
					"`attempt.started` records `review_subject_input: {instance_id, hash}` iff the movement declares a\nreview criterion.",
					"The location is derived from that event envelope and its score revision; the\nadapter receives the same `instance_id` and path.",
				},
			},
			specimens: []remainingSpecimenExpectation{
				{language: "text", prefix: "artifact_id: partitur.score-base\n", hash: "f7020681f700501152537c9a2fa4ae499ed51a40f44a1e35627c045bb52a608f"},
				{language: "text", prefix: "artifact_id: partitur.subject-tree\n", hash: "e0c33bb7d4e547a49fb27e6962288b5dc1a57c6137dedf0c6a31dacd1e5bd7a5"},
			},
		},
	}
	if len(inventory) != len(blocks) {
		t.Fatalf("original annotation inventory blocks = %d, decomposition blocks = %d", len(inventory), len(blocks))
	}

	for _, block := range blocks {
		t.Run(block.name, func(t *testing.T) {
			section := between(t, string(contents), block.start, block.end)
			originals, found := inventory[block.name]
			if !found {
				t.Fatalf("original annotation inventory has no block %q", block.name)
			}
			if len(originals) != len(block.carrierAssignments) {
				t.Fatalf("original annotation runs = %d, assignments = %d", len(originals), len(block.carrierAssignments))
			}
			seenOriginals := make(map[string]struct{}, len(originals))
			for _, original := range originals {
				if original == "" {
					t.Fatal("original annotation inventory contains an empty run")
				}
				if _, duplicate := seenOriginals[original]; duplicate {
					t.Fatalf("original annotation run assigned twice: %q", original)
				}
				seenOriginals[original] = struct{}{}
			}

			if len(block.specimens) == 0 {
				t.Fatal("block has no expected specimens")
			}
			requireOccurrence(t, section, block.carrierMarker, 1)
			carrierEnd := strings.Index(section, "```"+block.specimens[0].language+"\n")
			if carrierEnd <= 0 {
				t.Fatalf("carrier marker %q has no adjacent specimen", block.carrierMarker)
			}
			carrierText := section[:carrierEnd]
			if got := sha256Text(carrierText); got != block.carrierHash {
				t.Fatalf("carrier bytes hash = %s, want %s", got, block.carrierHash)
			}
			var carriers []string
			references := 0
			for _, bullet := range markdownBullets(carrierText) {
				if strings.HasPrefix(bullet, "- For ") {
					references++
					continue
				}
				carriers = append(carriers, bullet)
			}
			if references != block.referenceCount {
				t.Fatalf("reference-only bullets = %d, want %d", references, block.referenceCount)
			}
			assigned := 0
			for index, count := range block.carrierAssignments {
				if count < 0 || assigned+count > len(carriers) {
					t.Fatalf("original annotation run %d has invalid carrier assignment %d", index+1, count)
				}
				relocated := block.relocatedCarriers[index+1]
				if count == 0 && len(relocated) == 0 {
					t.Fatalf("original annotation run %d has neither a local nor relocated carrier", index+1)
				}
				for _, carrier := range relocated {
					requireOccurrence(t, string(contents), carrier, 1)
				}
				assigned += count
			}
			if assigned != len(carriers) {
				t.Fatalf("assigned carriers = %d, resulting carriers = %d", assigned, len(carriers))
			}
			seenCarriers := make(map[string]struct{}, len(carriers))
			for _, carrier := range carriers {
				if _, duplicate := seenCarriers[carrier]; duplicate {
					t.Fatalf("carrier assigned twice: %q", carrier)
				}
				seenCarriers[carrier] = struct{}{}
			}

			language := block.specimens[0].language
			for _, expected := range block.specimens {
				if expected.language != language {
					t.Fatal("one decomposition block uses multiple fence languages")
				}
			}
			fences := languageFences(t, section, language)
			if len(fences) != len(block.specimens) {
				t.Fatalf("top-level payloads = %d, want %d", len(fences), len(block.specimens))
			}

			seenSpecimens := make(map[string]struct{}, len(block.specimens))
			for _, expected := range block.specimens {
				var matches []string
				for _, specimen := range fences {
					if strings.HasPrefix(specimen, expected.prefix) {
						matches = append(matches, specimen)
					}
				}
				if len(matches) != 1 {
					t.Fatalf("specimens with prefix %q = %d, want 1", expected.prefix, len(matches))
				}
				if _, duplicate := seenSpecimens[expected.prefix]; duplicate {
					t.Fatalf("duplicate specimen prefix %q", expected.prefix)
				}
				seenSpecimens[expected.prefix] = struct{}{}
				if count := strings.Count(matches[0], "#"); count != 0 {
					t.Fatalf("specimen %q comment-marker count = %d, want 0", expected.prefix, count)
				}
				if got := sha256Text(matches[0]); got != expected.hash {
					t.Fatalf("specimen %q bytes hash = %s, want %s", expected.prefix, got, expected.hash)
				}
				if expected.language == "yaml" {
					if _, err := canonical.ParseYAML([]byte(matches[0])); err != nil {
						t.Fatalf("specimen %q is not one safe YAML document: %v", expected.prefix, err)
					}
				}
			}
			if len(seenSpecimens) != len(block.specimens) {
				t.Fatalf("top-level payloads = %d, want %d", len(seenSpecimens), len(block.specimens))
			}
			t.Logf("%s decomposition: %d original annotation runs, %d local prose carriers, %d relocated carrier groups, %d reference-only bullets, %d independently copyable top-level payloads, 0 comment markers",
				block.name, len(originals), len(carriers), len(block.relocatedCarriers), references, len(seenSpecimens))
		})
	}
}

func remainingOriginalAnnotationInventory(t *testing.T) map[string][]string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("testdata", "remaining_original_annotations.txt"))
	if err != nil {
		t.Fatal(err)
	}
	inventory := make(map[string][]string)
	block := ""
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			block = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			if _, duplicate := inventory[block]; duplicate {
				t.Fatalf("duplicate annotation inventory block %q", block)
			}
			inventory[block] = nil
			continue
		}
		if line == "" {
			continue
		}
		if block == "" {
			t.Fatalf("annotation outside an inventory block: %q", line)
		}
		inventory[block] = append(inventory[block], line)
	}
	return inventory
}

func languageFences(t *testing.T, section, language string) []string {
	t.Helper()
	open := "```" + language + "\n"
	const close = "\n```"
	var fences []string
	for {
		start := strings.Index(section, open)
		if start < 0 {
			return fences
		}
		section = section[start+len(open):]
		end := strings.Index(section, close)
		if end < 0 {
			t.Fatalf("unclosed %s fence", language)
		}
		fences = append(fences, section[:end])
		section = section[end+len(close):]
	}
}
