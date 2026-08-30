package docmarker

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendixB2AttemptStartedDecomposition(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	section := between(t, string(contents),
		"## B.2 Attempt lifecycle and performer selection",
		"## B.3 Evidence",
	)

	type annotationCarrier struct {
		original string
		carriers []string
	}
	inventory := []annotationCarrier{
		{
			original: "per-movement display ordinal — never the identifier (§6)",
			carriers: []string{
				"- `attempt.started.attempt_number` is the per-movement display ordinal.",
				"- `attempt.started.attempt_number` is never the attempt identifier (§6).",
			},
		},
		{
			original: "recorded BEFORE the trampoline's gate is released, so an",
			carriers: []string{
				"- `attempt.started.adapter_process` is recorded before the trampoline's gate is released.",
			},
		},
		{
			original: "unrecorded process can never have executed adapter code (§4)",
			carriers: []string{
				"- An adapter process absent from `attempt.started.adapter_process` can never have executed adapter\n  code (§4).",
			},
		},
		{
			original: "== pid; the trampoline is the session leader",
			carriers: []string{
				"- `attempt.started.adapter_process.session_id` equals `attempt.started.adapter_process.pid`.",
				"- The trampoline recorded by `attempt.started.adapter_process` is the session leader.",
			},
		},
		{
			original: "PGID is deliberately absent: §4 enumerates the recorded session and discovers its process groups at sweep time",
			carriers: []string{
				"- `attempt.started.adapter_process` deliberately has no `pgid` field.",
				"- At sweep time, §4 enumerates the recorded session and discovers its process groups.",
			},
		},
		{
			original: "movement_composition_dependency_hash (A.4); present iff the movement has dependencies. Journaled here because §5 records it and §9 checks it — a declared identity that is never persisted protects nothing. A dependency set with zero contributing change sets uses `composition_mode: identity`",
			carriers: []string{
				"- `attempt.started.base_composition_hash` is the `movement_composition_dependency_hash` (A.4).",
				"- `attempt.started.base_composition_hash` is present if and only if the movement has dependencies.",
				"- `attempt.started.base_composition_hash` is journaled because §5 records it and §9 checks it.",
				"- A declared composition identity that is never persisted protects nothing.",
				"- A dependency set with zero contributing change sets uses `composition_mode: identity`.",
			},
		},
		{
			original: "present iff this movement declares a review criterion (§2):",
			carriers: []string{
				"- `attempt.started.review_subject_input` is present if and only if the movement declares a review\n  criterion (§2).",
			},
		},
		{
			original: "the durable, raw-byte commitment to §4's reserved input; its path is derived from this envelope, not trusted input",
			carriers: []string{
				"- When present, `attempt.started.review_subject_input` is `{instance_id, hash}`, the durable raw-byte\n  commitment to §4's reserved input.",
				"- The reserved-input path is derived from the `attempt.started` envelope and is never trusted input.",
			},
		},
		{
			original: "exactly the wire `grants` object of §4",
			carriers: []string{
				"- `attempt.started.granted_authority` is exactly the wire `grants` object of §4.",
			},
		},
	}

	if len(inventory) != 9 {
		t.Fatalf("attempt.started annotation inventory = %d, want 9", len(inventory))
	}
	originals := make(map[string]struct{}, len(inventory))
	carriers := make(map[string]struct{})
	for _, item := range inventory {
		if item.original == "" || len(item.carriers) == 0 {
			t.Fatalf("incomplete annotation inventory item: %+v", item)
		}
		if _, duplicate := originals[item.original]; duplicate {
			t.Fatalf("duplicate original annotation %q", item.original)
		}
		originals[item.original] = struct{}{}
		for _, carrier := range item.carriers {
			if _, duplicate := carriers[carrier]; duplicate {
				t.Fatalf("carrier assigned twice: %q", carrier)
			}
			carriers[carrier] = struct{}{}
			requireOccurrence(t, section, carrier, 1)
		}
	}
	if len(carriers) != 17 {
		t.Fatalf("attempt.started prose carriers = %d, want 17", len(carriers))
	}

	wantSpecimen := `attempt.started {
  attempt_number,
  adapter_process: {
    pid,
    session_id,
    start_identity: (
        {platform: "linux",  boot_id, start_ticks}
      | {platform: "darwin", start_tvsec, start_tvusec}
    )
  },
  base_composition_hash?,
  review_subject_input?: {instance_id, hash},
  granted_authority: {
    paths_rw: [pattern], paths_ro: [pattern], shell: bool, network: bool
  },
  identity_versions
}`
	var specimens []string
	for _, specimen := range textFences(t, section) {
		if strings.HasPrefix(specimen, "attempt.started {\n") {
			specimens = append(specimens, specimen)
		}
	}
	if len(specimens) != 1 {
		t.Fatalf("standalone attempt.started specimens = %d, want 1", len(specimens))
	}
	if specimens[0] != wantSpecimen {
		t.Fatalf("attempt.started specimen =\n%s\nwant:\n%s", specimens[0], wantSpecimen)
	}
	if strings.Contains(specimens[0], "#") {
		t.Fatal("attempt.started specimen still carries an internal annotation")
	}
	topLevelPayloads := 0
	for _, line := range strings.Split(specimens[0], "\n") {
		if !strings.HasPrefix(line, " ") && strings.HasSuffix(line, " {") {
			topLevelPayloads++
		}
	}
	if topLevelPayloads != 1 {
		t.Fatalf("attempt.started top-level payloads = %d, want 1", topLevelPayloads)
	}
	t.Logf("attempt.started decomposition: %d original annotations, %d prose carriers, one standalone comment-free specimen", len(inventory), len(carriers))
}

func TestAppendixBRemainingDecomposition(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	inventory := appendixBOriginalAnnotationInventory(t)

	type payloadExpectation struct {
		name string
		hash string
	}
	type blockExpectation struct {
		name               string
		start              string
		end                string
		carrierMarkers     []string
		carrierHash        string
		carrierAssignments []int
		payloads           []payloadExpectation
		skipPayload        string
	}
	blocks := []blockExpectation{
		{
			name: "B1", start: "## B.1 Run and movement lifecycle", end: "## B.2 Attempt lifecycle and performer selection",
			carrierMarkers:     []string{"**B.1 field clauses.**"},
			carrierHash:        "66e707d9714f88b484e639b820c252c121e12be4ac79b43043e0b5ce5e608874",
			carrierAssignments: []int{1, 1, 1, 2, 1, 3, 1, 2, 2, 2, 1, 1, 1, 1, 4, 1, 1, 1, 1, 1, 1, 1, 1, 1, 3, 1},
			payloads: []payloadExpectation{
				{name: "run.started", hash: "df882ebbb086f4df7bcc8a735b8d6a69fe178922013f594a810bdfab3e51cc97"},
				{name: "run.succeeded", hash: "58967e254a0797a56f533dcb8cf21b9f5f99048271bd7756a52ef51ace52456b"},
				{name: "run.failed", hash: "08514ac45996a05e923580e2ead83efee85c87fa319250e51df768ee79d78bea"},
				{name: "run.cancelled", hash: "fca7880d592428c8efab00c8e277b39bfa8fd9ac19187c89016531fb204c889c"},
				{name: "movement.ready", hash: "14d7c3f89bbe82c9e6a41f3dcaced13cc8c843b5519c66f7c698b34781d01f9e"},
				{name: "movement.started", hash: "29c49f9d5fdddbf240d017fc38c789bdde57b161234af0b0e7ea6df6767b2368"},
				{name: "movement.succeeded", hash: "6346e0a88431165f82fc1ed74777287c0622910d59f0975e591aa7e752346f13"},
				{name: "movement.failed", hash: "67f5e209d30ba41e1cdf2b98e542b3446c806e0046a56416102f74df6ccb3f62"},
				{name: "movement.cancelled", hash: "84082ef0da52a456ed667c0fa8afe16ed9e91dd615c148ca28769cab3b57a922"},
			},
		},
		{
			name: "B2", start: "## B.2 Attempt lifecycle and performer selection", end: "## B.3 Evidence",
			carrierMarkers:     []string{"**`performer.selected` and `adapter.probed` field clauses.**", "**Remaining B.2 field clauses.**"},
			carrierHash:        "683d80f393078cb0fd421aec140aa80e9606ce7cf617598f8f6038b33dcaccd5",
			carrierAssignments: []int{1, 2, 1, 2, 1, 1, 3, 1, 2, 2, 1, 1, 1, 2, 10, 1, 3, 3, 7, 3, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2},
			skipPayload:        "attempt.started",
			payloads: []payloadExpectation{
				{name: "performer.selected", hash: "b4ee5a3e746190d356b8868591f8e5c6058a433e605530ba2fc66dd8f1b3d349"},
				{name: "adapter.probed", hash: "df96c702d3e14ad4d8dfa6477be12d7d8b0a2da3adbbe17c10d14452ce5bcb7c"},
				{name: "performer.completed", hash: "c258d143811d8a8d83397c61aafbe46caa79f59fa77be9062f2399b783a958a2"},
				{name: "attempt.completed", hash: "9984a7192f28cf55bc380439f8a6a3ef7fe99ebf5428c54f6bd8956c2fcc6903"},
				{name: "attempt.blocked", hash: "b0ccf2c59e9578d6f59654ebda4544b87fa1509889bf555251193fd596d4d539"},
				{name: "attempt.failed", hash: "7ce52c744771ea9b82fb51b71806faf373dfa79bffdaabd8f505129f859f1677"},
				{name: "attempt.cancelled", hash: "4978d730b312c52d2449aebc595bc46b465bc9dc4335c5652aeb513de0c59577"},
				{name: "attempt.superseded", hash: "1123d3739c73fb9167d8d0ef652eeac5286374f4877c55d0575261d56b250ef3"},
				{name: "execution.started", hash: "ac411f565664ba8861e21b4245490d06d8b76c56961fe3242b83613a550f897b"},
				{name: "execution.stopped", hash: "1791bb4da3e9b5201686f0c949118e52464059a19ab79376d49cccd3fe2e9f85"},
			},
		},
		{
			name: "B3", start: "## B.3 Evidence", end: "## B.4 Decisions",
			carrierMarkers:     []string{"**B.3 field clauses.**"},
			carrierHash:        "4b559489d713a1bb8582a0a76de21525a4749c38c1881165c9fefaa00b7902d0",
			carrierAssignments: []int{1, 2, 1, 1, 2, 1, 3, 1, 1, 2, 3, 1, 1, 1, 1, 1, 2, 1, 3, 2, 1, 1, 1, 1, 2, 4, 1, 1, 2, 1, 2, 1, 1, 1, 1, 1, 2, 3, 1, 1, 1, 3, 1, 2, 1, 1, 1, 2, 2},
			payloads: []payloadExpectation{
				{name: "artifact.recorded", hash: "0d682b3501197c4a049cec9fc22e066e25709c6db707e1b99443e38d9d0680df"},
				{name: "change_set.recorded", hash: "85da1433fc9fd230c726b6fd8cf942b6b111e6d467f8bb6c594e7999969c0e34"},
				{name: "verification.passed", hash: "800fd0d734506372bb91a33c8567ce646ba3b493684d1d9d743269c3e33986d6"},
				{name: "composition.conflicted", hash: "f16b0d16a6f9bc45dd2576df9c672e10e2c5b030440d979d095a6a20b8646e6c"},
				{name: "composition.failed", hash: "f5ceb3e22ceeb7adaff9c342594b32838540cc7ca86c042cee41b38b3ef34196"},
				{name: "application_candidate.recorded", hash: "8263d1d13cbe9c0553e765559a9640f1dcda3fb85c58f899886f9997fcfdb3ca"},
				{name: "acceptance.started", hash: "518de36bea3bd60d00b9a896dd2e84ec61c60611a9b7b845fd401e9472929658"},
				{name: "criterion.started", hash: "0a79d7db6544d2b50f8a580adfcb289e1fb99ddc04907b69fe034d9e853f85c4"},
				{name: "criterion.completed", hash: "199599ed681dcf81a70868043d428e780eae6c2dba8d6248e2d73b0949ff0777"},
				{name: "acceptance.failed", hash: "2780ad1b62a761364a5c255d8ba33e747268489a7e1d51b5f4ee4a2513ab00a1"},
				{name: "acceptance.evaluation_completed", hash: "e38ff9b85c39d425ef49ca69d5d342a6996806481cf8189a8a2abd541c424ece"},
			},
		},
		{
			name: "B4", start: "## B.4 Decisions", end: "## B.5 Amendments",
			carrierMarkers:     []string{"**B.4 field clauses.**"},
			carrierHash:        "3fa248ecbe65458b851e4b7023edc385368f8c704c8194aad546d504012d3292",
			carrierAssignments: []int{2, 1, 3, 1, 1, 2, 3, 1, 2, 2, 1, 4, 3, 2, 1, 2},
			payloads: []payloadExpectation{
				{name: "decision.requested", hash: "61aec8d2a47d61c79df4aea9afd0614b457f598028ad9cd3e8a11104110034c1"},
				{name: "decision.resolved", hash: "5784717e370bbc56167135cb86c9e35e99c79f4e09b9034e9c40dee2476d7bbf"},
				{name: "decision.obsoleted", hash: "3fd5f0607912bda2b8936be5231efb0d9b492814b663c138e093e2ebe10e3ffd"},
			},
		},
		{
			name: "B5", start: "## B.5 Amendments", end: "## B.6 Shipping",
			carrierMarkers:     []string{"**B.5 field clauses.**"},
			carrierHash:        "9d0f920c0fcbadc6a877cf53c1b18a9b4aa4aba823600ae83acf6cab6fcff2bc",
			carrierAssignments: []int{2, 1, 2, 2, 2, 1, 2, 1, 5, 1, 1, 1, 3, 3, 3, 1, 1, 1, 1, 1, 1, 1, 3, 3, 1, 1, 2, 2, 1, 2, 1, 1, 1, 1, 1, 4, 3, 1, 1, 1, 1, 2, 1, 1, 1, 1, 1, 1, 3, 1, 1, 2, 1, 1, 1, 2, 1},
			payloads: []payloadExpectation{
				{name: "amendment.rejected", hash: "0b34bd25e9c7a4b1ab4c3051016a95031852c8f1aac51743e2fff7dba7688a36"},
				{name: "amendment.routed_human", hash: "4f1f059e7d96328cdd640f4f2b9c87d53082b95a1da9ee5e36f356834755bba2"},
				{name: "amendment.approved", hash: "cbcbf5afab7095913e451df814c402cfb6c14791c5cb76fd48584f6dc7fad623"},
				{name: "amendment.approval_abandoned", hash: "d16991e9f0c69525679cbf626d2dc91036c5ccac41410052658a0ba7835528f2"},
				{name: "amendment.approval_prepared", hash: "2c87271ac7843a9db7058bce4d9d62b13e641df81aa3e0878adf08f43eec0f00"},
				{name: "amendment.quiesce_observed", hash: "906441391ac830d89fd5fe2e26f3c20cd5a555853760fe5c320c783729269785"},
				{name: "amendment.human_rejected", hash: "17a53d62168d4b078671d60e7aee0fcb79707622df4f2e3b047d09f291e3401f"},
			},
		},
		{
			name: "B6", start: "## B.6 Shipping", end: "## B.7 Control and diagnostics",
			carrierMarkers:     []string{"**B.6 field clauses.**"},
			carrierHash:        "9a66520f0a4c1a0509145486e74fdc9f91109f83f52b27dd2773d91019e7d55c",
			carrierAssignments: []int{1, 1, 1, 1, 2, 3, 1, 2, 1, 1, 2},
			payloads: []payloadExpectation{
				{name: "apply.started", hash: "a70ba4dfe3ee0fdbff43989ed6827591188878cefcc4ccce834342a9295d166f"},
				{name: "apply.completed", hash: "ad1b7bb1fa8edc1869350479bbae29ff0fdfa2631f62815cbff215ffabd61f88"},
				{name: "apply.failed", hash: "3ce075f595826a9720fd58079c7e0e60eb1e02e297cf03006443929531da45ac"},
				{name: "apply.recovery_required", hash: "e3b2484ea6a7f9ca77dbd59d43d4c374a65a523ea9d93e021045b6440c58fe46"},
				{name: "apply.recovery_resolved", hash: "e9086dc5cc71f0d9480b51da564b614acf7ebd1932654f5177453bbbbd5f20a2"},
				{name: "score.promotion_started", hash: "9ecaeefde7207a9acc7de163aa14da5d801efb32b4781d23f68aa6521fa47fd2"},
				{name: "score.promoted", hash: "dc6e5b3d0bcf3109ac1f190a7f3bcf6218000312bb31c98a0b80e9f65bbb8aff"},
				{name: "score.promotion_recovery_required", hash: "35e676ecf27699468bcf304ae31d89d846d34a83dd5b0387665904c19d34eef2"},
			},
		},
		{
			name: "B7", start: "## B.7 Control and diagnostics", end: "# Appendix C — Recovery",
			carrierMarkers:     []string{"**B.7 field clauses.**"},
			carrierHash:        "ccd531ca37f87c655d03673bdf527b29e97c437bf38d3a8bf213c27189dd8a4a",
			carrierAssignments: []int{2, 2, 1, 1, 1, 2, 3, 1, 2, 1, 1},
			payloads: []payloadExpectation{
				{name: "authority.granted", hash: "7693b856070815ea2795f76588b6040dcb1314c6a60eb2f960a55d8c235a5c4d"},
				{name: "cancel.requested", hash: "9ac5a0a94d87c41a8e42df1c7f78401bea5ee11bb948dcc24a53527c770e247e"},
				{name: "journal.tail_truncated", hash: "638e8334c7828de5b1aec3c0be25feb49463c175d5dcc24e594ccdde7f9cb7b5"},
				{name: "log", hash: "da6b056b719b96a35d46bdcdf085db33189b705ba6b29d4487983e262e5c03b3"},
				{name: "progress", hash: "eed2e976cc47077f22e1f5fe72bd464e57f0652fd59bc9c0026a97394d2c3e81"},
			},
		},
	}

	for _, block := range blocks {
		t.Run(block.name, func(t *testing.T) {
			section := between(t, string(contents), block.start, block.end)
			originals := inventory[block.name]
			if len(originals) != len(block.carrierAssignments) {
				t.Fatalf("original annotations = %d, assignments = %d", len(originals), len(block.carrierAssignments))
			}

			var carrierRegion strings.Builder
			for _, marker := range block.carrierMarkers {
				requireOccurrence(t, section, marker, 1)
				from := strings.Index(section, marker)
				to := strings.Index(section[from:], "```text\n")
				if to <= 0 {
					t.Fatalf("carrier marker %q has no adjacent payload specimen", marker)
				}
				carrierRegion.WriteString(section[from : from+to])
			}
			carrierText := carrierRegion.String()
			if got := sha256Text(carrierText); got != block.carrierHash {
				t.Fatalf("carrier bytes hash = %s, want %s", got, block.carrierHash)
			}
			carriers := markdownBullets(carrierText)
			assigned := 0
			for index, count := range block.carrierAssignments {
				if count <= 0 || assigned+count > len(carriers) {
					t.Fatalf("original annotation %d has invalid carrier assignment %d", index+1, count)
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

			expectedPayloads := make(map[string]string, len(block.payloads))
			for _, payload := range block.payloads {
				expectedPayloads[payload.name] = payload.hash
			}
			seenPayloads := make(map[string]struct{}, len(block.payloads))
			for _, specimen := range textFences(t, section) {
				name := topLevelPayloadName(specimen)
				if name == "" || name == block.skipPayload {
					continue
				}
				wantHash, expected := expectedPayloads[name]
				if !expected {
					t.Fatalf("unexpected top-level payload %q", name)
				}
				if _, duplicate := seenPayloads[name]; duplicate {
					t.Fatalf("duplicate top-level payload %q", name)
				}
				seenPayloads[name] = struct{}{}
				if count := strings.Count(specimen, "#"); count != 0 {
					t.Fatalf("%s comment-marker count = %d, want 0", name, count)
				}
				if count := topLevelPayloadCount(specimen); count != 1 {
					t.Fatalf("%s top-level payloads = %d, want 1", name, count)
				}
				if got := sha256Text(specimen); got != wantHash {
					t.Fatalf("%s specimen bytes hash = %s, want %s", name, got, wantHash)
				}
			}
			if len(seenPayloads) != len(block.payloads) {
				t.Fatalf("top-level payloads = %d, want %d", len(seenPayloads), len(block.payloads))
			}
			t.Logf("%s decomposition: %d original annotation runs, %d prose carriers, %d independently copyable payloads, 0 comment markers",
				block.name, len(originals), len(carriers), len(seenPayloads))
		})
	}
}

func appendixBOriginalAnnotationInventory(t *testing.T) map[string][]string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("testdata", "appendix_b_original_annotations.txt"))
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

func markdownBullets(section string) []string {
	var bullets []string
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "- ") {
			bullets = append(bullets, line)
			continue
		}
		if len(bullets) != 0 && strings.HasPrefix(line, "  ") {
			bullets[len(bullets)-1] += "\n" + line
		}
	}
	return bullets
}

func topLevelPayloadName(specimen string) string {
	first, _, _ := strings.Cut(specimen, "\n")
	separator := strings.Index(first, " {")
	if separator <= 0 || first[0] < 'a' || first[0] > 'z' {
		return ""
	}
	return strings.TrimSpace(first[:separator])
}

func topLevelPayloadCount(specimen string) int {
	count := 0
	for _, line := range strings.Split(specimen, "\n") {
		if !strings.HasPrefix(line, " ") && strings.Contains(line, " {") {
			count++
		}
	}
	return count
}

func sha256Text(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

func textFences(t *testing.T, section string) []string {
	t.Helper()
	const open = "```text\n"
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
			t.Fatal("unclosed text fence")
		}
		fences = append(fences, section[:end])
		section = section[end+len(close):]
	}
}

func between(t *testing.T, document, start, end string) string {
	t.Helper()
	if strings.Count(document, start) != 1 || strings.Count(document, end) != 1 {
		t.Fatalf("section boundaries %q and %q must each occur once", start, end)
	}
	from := strings.Index(document, start)
	to := strings.Index(document[from:], end)
	if to <= 0 {
		t.Fatalf("section end %q does not follow %q", end, start)
	}
	return document[from : from+to]
}
