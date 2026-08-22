package docmarker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
)

type sevenBlockExpectation struct {
	name                 string
	start                string
	end                  string
	language             string
	prefix               string
	carrierHash          string
	referenceCount       int
	carrierAssignments   []int
	duplicateCarrierRuns [][]string
	relocatedCarriers    map[int][]string
	specimenHash         string
}

func TestSevenFenceDecomposition(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	inventory := sevenOriginalAnnotationInventory(t)

	blocks := []sevenBlockExpectation{
		{
			name: "routed-proposal", start: "**Routed-proposal-record field clauses.**", end: "Strictly decoded like every other core file:",
			language: "text", prefix: "{\n  schema: \"partitur/proposal-record+json;v=1\",\n",
			carrierHash:        "05e1d12a30f78f239061eeaddf4c20d5f60c4a4a9a38cf013b6e493faad031a6",
			referenceCount:     1,
			carrierAssignments: []int{1, 1, 1, 1, 0},
			relocatedCarriers: map[int][]string{
				5: {
					"when `claimed_impact` is present, a claim\n   narrower than the actual impact",
					"when it\n   is absent, no containment check applies. The optional claim is a proposer-supplied scope",
				},
			},
			specimenHash: "40e2b3e14b712ac7b2face9b1384112c686a4b53622a6a11829b513563453ecf",
		},
		{
			name: "amendment-proposal", start: "**Amendment-proposal field clauses.**", end: "**Rules enforced by `partitur validate` (the score compiler).**",
			language: "text", prefix: "{\n  base_revision, base_hash,\n",
			carrierHash:        "d279afa0510362fe2bc6f3c9d4e5831de37bb75fd0ba972e56c1d9b89276b9a3",
			referenceCount:     3,
			carrierAssignments: []int{0, 0, 0},
			relocatedCarriers: map[int][]string{
				1: {
					"2. **Stale re-check** — `base_revision` / `base_hash` must match the snapshot head.",
				},
				2: {
					"4. **Patch application** to the canonical JSON; any RFC 6902 error rejects.",
				},
				3: {
					"**`actual_impact`** — the normative shape, which `claimed_impact` also uses (§2):",
					"when `claimed_impact` is present, a claim\n   narrower than the actual impact",
					"when it\n   is absent, no containment check applies. The optional claim is a proposer-supplied scope",
				},
			},
			specimenHash: "9d31fa6f92bc828e7eb4d0db2a60323574f975b9f4d5134ff4883c82d877d1b3",
		},
		{
			name: "cast-schema", start: "## 3. Cast schema v0.1 (`cast.yaml`)", end: "> **This example is illustrative, not runnable.**",
			language: "yaml", prefix: "cast: \"0.1\"\n",
			carrierAssignments: []int{0, 0, 0},
			duplicateCarrierRuns: [][]string{
				{"`allow_advisory_enforcement`\ndefaults to `false`;"},
				{
					"adapter-specific data lives only\n   under `extensions.<adapter-id>`.",
					"**`extensions.<adapter-id>` payloads** are authored in the score or cast — by the user,\n    for a vendor to consume — are opaque to the core",
				},
				{"`execute.request.extensions`, when present, contains only the namespace matching this adapter's id."},
			},
			specimenHash: "4740506bd36738a1f683b1854b53562d3ccb40b32d96d74d1a74c6bb80ee71a3",
		},
		{
			name: "application-candidate", start: "**Application-candidate identity clauses.**", end: "It is a **content** identity",
			language: "text", prefix: "candidate_id = H(\"partitur/candidate\",\n",
			carrierHash:        "945512db6b1e609f1c4e68d2719139dc84fe5b50597b10806efae84271f39146",
			carrierAssignments: []int{4, 1},
			specimenHash:       "cb9b4235e9e035d470a3f0eacad5cc1710cdee4d8c8cd5c8407d7c61603088e6",
		},
		{
			name: "actual-impact", start: "**`actual_impact` field clauses.**", end: "`selector` is a **stable semantic selector**",
			language: "text", prefix: "actual_impact = {\n",
			carrierHash:        "bfb2bc9e0bfe890c8ad70a31fe4babb9b254e8d18037b7255bd43caa9fd39a03",
			carrierAssignments: []int{1, 1},
			specimenHash:       "32c344f3c312c071f60a9430d3f5f4a3ba8c672aa5906399cd61bb90653cbcb0",
		},
		{
			name: "execution-dependency", start: "**Execution-dependency field clauses.**", end: "**Effective authority, not raw `policy`**",
			language: "text", prefix: "{\n  actual_adapter_id,\n",
			carrierHash: "32e4750d240a47ab53aa39dc71e1c11d7331fdefab4c069433d9d9c5bc784a11",
			carrierAssignments: []int{
				3, 1, 2, 4, 2, 2, 1, 5, 1, 5, 1, 2, 1, 1, 2, 2, 2, 1, 1, 5, 4,
			},
			specimenHash: "24c2811e7219facb44deb19e2122f2d9317339b1a21cddcd8210849d90f67f73",
		},
		{
			name: "global-invariants", start: "**`global_invariants` field clauses.**", end: "Excluded from `global_invariants`",
			language: "text", prefix: "{\n  resolved_questions: [\n",
			carrierHash:        "7a898d8821d9c502371b859f5c44a329255337e93498fd3d6e7b5348cff47718",
			carrierAssignments: []int{3, 1, 1, 1, 2, 1, 2, 2},
			specimenHash:       "9f92e723baecbeeb4de190782be565ad9d96741f89db3e55ab9aa98f4e32a207",
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

			open := "```" + block.language + "\n"
			fenceStart := strings.Index(section, open)
			if fenceStart < 0 {
				t.Fatalf("%s specimen is absent", block.language)
			}
			var carriers []string
			references := 0
			for _, bullet := range markdownBullets(section[:fenceStart]) {
				if strings.HasPrefix(bullet, "- For ") {
					references++
					continue
				}
				carriers = append(carriers, bullet)
			}
			if references != block.referenceCount {
				t.Fatalf("reference-only bullets = %d, want %d", references, block.referenceCount)
			}
			if block.carrierHash == "" {
				if len(carriers) != 0 {
					t.Fatalf("prose carriers = %d, want 0 duplicate-only carriers", len(carriers))
				}
			} else if got := sha256Text(section[:fenceStart]); got != block.carrierHash {
				t.Fatalf("carrier bytes hash = %s, want %s", got, block.carrierHash)
			}

			assigned := 0
			for index, count := range block.carrierAssignments {
				if count < 0 || assigned+count > len(carriers) {
					t.Fatalf("original annotation run %d has invalid carrier assignment %d", index+1, count)
				}
				var duplicates []string
				if count == 0 {
					if index < len(block.duplicateCarrierRuns) {
						duplicates = block.duplicateCarrierRuns[index]
					}
					if len(duplicates) == 0 && len(block.relocatedCarriers[index+1]) == 0 {
						t.Fatalf("original annotation run %d has neither a local, duplicate, nor relocated carrier", index+1)
					}
				}
				for _, duplicate := range duplicates {
					requireOccurrence(t, string(contents), duplicate, 1)
				}
				for _, relocated := range block.relocatedCarriers[index+1] {
					requireOccurrence(t, string(contents), relocated, 1)
				}
				assigned += count
			}
			if assigned != len(carriers) {
				t.Fatalf("assigned carriers = %d, resulting carriers = %d", assigned, len(carriers))
			}

			fences := languageFences(t, section, block.language)
			if len(fences) != 1 {
				t.Fatalf("top-level payloads = %d, want 1", len(fences))
			}
			specimen := fences[0]
			if !strings.HasPrefix(specimen, block.prefix) {
				t.Fatalf("specimen prefix = %q, want %q", specimen, block.prefix)
			}
			if count := strings.Count(specimen, "#"); count != 0 {
				t.Fatalf("comment markers = %d, want 0", count)
			}
			if got := sha256Text(specimen); got != block.specimenHash {
				t.Fatalf("specimen bytes hash = %s, want %s", got, block.specimenHash)
			}
			if block.language == "yaml" {
				if _, err := canonical.ParseYAML([]byte(specimen)); err != nil {
					t.Fatalf("specimen is not one safe YAML document: %v", err)
				}
			}
			t.Logf("%s decomposition: %d original annotation runs, %d prose carriers, %d relocated carrier groups, %d reference-only bullets, 1 independently copyable top-level payload, 0 comment markers",
				block.name, len(originals), len(carriers), len(block.relocatedCarriers), references)
		})
	}
}

func sevenOriginalAnnotationInventory(t *testing.T) map[string][]string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("testdata", "seven_original_annotations.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sha256Text(string(contents)), "0b9be109fe0e9942313ee7dcb39a44ddb39e97eff8a55e405b19fdbdd5444bcf"; got != want {
		t.Fatalf("original annotation inventory bytes hash = %s, want %s", got, want)
	}
	var inventory map[string][]string
	if err := json.Unmarshal(contents, &inventory); err != nil {
		t.Fatal(err)
	}
	for name, runs := range inventory {
		if len(runs) == 0 {
			t.Fatalf("annotation inventory block %q is empty", name)
		}
		for index, run := range runs {
			if run == "" || !strings.Contains(run, "#") {
				t.Fatalf("annotation inventory block %q run %d is not an exact comment-bearing run", name, index+1)
			}
		}
	}
	return inventory
}
