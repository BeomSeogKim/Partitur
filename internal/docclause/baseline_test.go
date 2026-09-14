package docclause

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type confirmedPacketPin struct {
	DecisionCount int
	AnchorCount   int
	SourceSHA256  string
	DecisionsHash string
}

const (
	confirmedBaselineMarkedBlob                  = "dc92a3acf129768a7a3979e16b47baae2a6fb6f1"
	confirmedBaselineOrderedClassificationSHA256 = "20b6b17348a5600624705ae70bc3dcc168f8e20dff4a03251b0a05b6cd00f035"
)

func TestDesignStagingLedgerPinsCurrentUniverseAndActivatesBaseline(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository path")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	documentPath := "docs/DESIGN.md"
	document, err := os.ReadFile(filepath.Join(repository, documentPath))
	if err != nil {
		t.Fatal(err)
	}
	ledgerContents, err := os.ReadFile(filepath.Join(repository, "docs", "DESIGN.clause-staging.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"machine_proposals", `"pending"`, `"reviewed"`} {
		if bytes.Contains(ledgerContents, []byte(forbidden)) {
			t.Fatalf("staging ledger contains editable or detector-shaped field %q", forbidden)
		}
	}
	var registry Registry
	decoder := json.NewDecoder(bytes.NewReader(ledgerContents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("git", "hash-object", documentPath)
	command.Dir = repository
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	commandBlob := strings.TrimSpace(string(output))
	if registry.InputBlob != commandBlob {
		t.Fatalf("staging input blob = %q, git hash-object = %q", registry.InputBlob, commandBlob)
	}
	if calculated := GitBlobID(document); calculated != commandBlob {
		t.Fatalf("calculated Git blob = %q, git hash-object = %q", calculated, commandBlob)
	}

	regions, err := GenerateRegions(document, commandBlob)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRegistry(documentPath, document, commandBlob, regions, registry); err != nil {
		t.Fatal(err)
	}
	if err := validateConfirmedActivationPins(registry); err != nil {
		t.Fatal(err)
	}
	if len(regions) == 0 {
		t.Fatal("generated region universe is empty")
	}
	reviewedOrdinals := map[int]confirmedPacketPin{
		1: {
			DecisionCount: 77,
			AnchorCount:   51,
			SourceSHA256:  "916f5c3dab897c61d2ee77cf013c6ec3a246cc5bc6e985c08838b23836420e4b",
			DecisionsHash: "e3d994f50c4373b72b5dd00a7be093fe23216bd86106161d91e6ae39b366c09d",
		},
		2: {
			DecisionCount: 139,
			AnchorCount:   105,
			SourceSHA256:  "8b6cfd7811ea6534b7d0129c5c48121e70849cafe2985e4c7a62f9c2f7a71083",
			DecisionsHash: "ae47eaaa679ee4aac6f827d8de8e3273a27e1f28f12e3105c21107b1bd1cb0fa",
		},
		3: {
			DecisionCount: 91,
			AnchorCount:   40,
			SourceSHA256:  "f003ba74a689cad25f1a600cc12b8dfb7d1e16856f8487c4bc4149816c316ea9",
			DecisionsHash: "cef59390f461f5c9abbcc6cb2b12d850b5f0af5b19e45455ecc2d2793c161213",
		},
		4: {
			DecisionCount: 158,
			AnchorCount:   89,
			SourceSHA256:  "81f0c1a5f263a44425fa24b057b4b285ba3376ba6f00782d922f3dbf0037080c",
			DecisionsHash: "81ac7a6905d2a74693720bfc653ca03263931da31d88d0f96ff8be0a966987bd",
		},
		5: {
			DecisionCount: 206,
			AnchorCount:   136,
			SourceSHA256:  "10bae7eba92be153b2da9e090439359332f0095d8ead71faef211a5e618c0c25",
			DecisionsHash: "b66394c9682bfa5849a9847a6fba026ccc6afbf82ac65876a05c76709c97860c",
		},
		6: {
			DecisionCount: 136,
			AnchorCount:   99,
			SourceSHA256:  "af8a6f7e5ce1f5a2b2ab996dd3c0fb602a6936abf04541f221a3c2f546eaf517",
			DecisionsHash: "ec7746549ab26365de88c60d605a1a2f36b5c200d528e7f17584614a1871ead4",
		},
		7: {
			DecisionCount: 143,
			AnchorCount:   85,
			SourceSHA256:  "e5bbfa264d6d487ed16734675ee14db0b41a2f3ef2334104fa8613c7c38da759",
			DecisionsHash: "1477c278c4e156923a0b1a2d12b8eb0d11275dac5884b54167ee3a7558ed0dd2",
		},
		8: {
			DecisionCount: 165,
			AnchorCount:   101,
			SourceSHA256:  "33869831cb05b76248b311fd4dadfed8afdf34a26919aef8a33b1890cfd2a30a",
			DecisionsHash: "9be1b767da51b6431dfcc347a92cd03a4d2e028dc1d2d1a3353050af99f2a5c1",
		},
		9: {
			DecisionCount: 195,
			AnchorCount:   132,
			SourceSHA256:  "2632dd064b2e0338488dff1eec47ecd2fcfc25afc4a67016df5c2aab5a02c7c4",
			DecisionsHash: "2f9ef9ac2c0f92a0c2bd49c7f25bfe924704f631793cc5f4d987c7c61e313433",
		},
		10: {
			DecisionCount: 161,
			AnchorCount:   110,
			SourceSHA256:  "5db279996c5b21241a7fa9ea6438658ef2d6670edbdae392ffa4e0e20158abf0",
			DecisionsHash: "dcf69e88654df6362d445645720641ce5921a587cd19947b13095388557d21b2",
		},
		11: {
			DecisionCount: 119,
			AnchorCount:   85,
			SourceSHA256:  "0a448a609afcf452b91cd1ce1660801cc55ad58918beec17b15382b391d21a87",
			DecisionsHash: "aa1822aa53b43964d41234765ebc71c148720607c74bb877272e122f0c41de27",
		},
		12: {
			DecisionCount: 105,
			AnchorCount:   82,
			SourceSHA256:  "50c76525cc924446b6061c366b883eb9847f27d7b28641a91f6956a80dcc7288",
			DecisionsHash: "6f1d90300e50f3c923a89b52a73ade0e259de84f04575e7b32f8b435d754eb39",
		},
		13: {
			DecisionCount: 125,
			AnchorCount:   68,
			SourceSHA256:  "b591268708154f0f20d24af6b5c0e1405905e54b6a47d27edb3b53e49a5b5d1c",
			DecisionsHash: "f98b8c450133010312af8b10d391d3aab14290c024613329ec268f92ed21ff31",
		},
		14: {
			DecisionCount: 127,
			AnchorCount:   87,
			SourceSHA256:  "a51bda255041905bd56d5fb21db57bdfc89e91829a797e57740ee0af5488625e",
			DecisionsHash: "7bc769cd0b286cb4d7945eee8493180cca931e1e13d030943e5ffaf36cca1fc3",
		},
		15: {
			DecisionCount: 136,
			AnchorCount:   101,
			SourceSHA256:  "d27a20356092a375297e4d4e21baf2e49e8b26a8b7a2ff222e66bb6c7a22c892",
			DecisionsHash: "e34e1ed36f6f703f05d24509664880eca67d0602c72c769b8e1b69f8bb53f843",
		},
		16: {
			DecisionCount: 119,
			AnchorCount:   92,
			SourceSHA256:  "9170ef7be879bc61c2a67fb52a0f46b85534d330a24cf6e7dd6a81763f47058b",
			DecisionsHash: "b8385826dfa4d9482c4f4f7bd8f722ccd832a5a48c4916f8fa74165cf9723f34",
		},
		17: {
			DecisionCount: 154,
			AnchorCount:   110,
			SourceSHA256:  "70ba3c4460a2d4d85231eb49c145a6dd389abf294564ee2f452474f7e3049f11",
			DecisionsHash: "a4ded734e84d341e52b4e1f145171ec3e7bb65148378609719e94c9dbeb93b8e",
		},
		18: {
			DecisionCount: 122,
			AnchorCount:   90,
			SourceSHA256:  "85c8d469ece3d9d096b55096fcbd83a4f6936deee99f0f957de770a97bbbab28",
			DecisionsHash: "72ee6df5e73103bb20e6ca4b4426503dd9e52824a82b266e1ac748c9bebe88a7",
		},
		19: {
			DecisionCount: 149,
			AnchorCount:   109,
			SourceSHA256:  "77b9471a945ecdbbc368d7d2a6b64d823c64e04455c3e85f8b53ee49fbad0649",
			DecisionsHash: "4e24f3ee4689ef3ca609a0c958484836340b99f93604f8c73bde1348a4c93be4",
		},
		20: {
			DecisionCount: 113,
			AnchorCount:   83,
			SourceSHA256:  "c04bf13b3e96cc4345d7499fd4c07fabf73b81a6febe4b45a9b77fa010d8b973",
			DecisionsHash: "2ed7f0fedf9bdcd61c67e1e8d13999949ac3fce9b36a4c36f3d9453a06f71788",
		},
		21: {
			DecisionCount: 140,
			AnchorCount:   86,
			SourceSHA256:  "80870166d635d47be9b9595ff4158446394d9b1bdbc9b6194f3881b043e17cc3",
			DecisionsHash: "326ed5f4c83b5d52784acbfebe1e705b9256135d291af528b052d1a8fe23ca01",
		},
		22: {
			DecisionCount: 125,
			AnchorCount:   94,
			SourceSHA256:  "22e349d141ac066e84ce8840fbd1ab8a5b2c297f6fbae272203353f6375bd9ca",
			DecisionsHash: "6dd5b67b53d9d32146226ef198f510c02a8e5604234521f9cd82cd63d979c5ad",
		},
		23: {
			DecisionCount: 126,
			AnchorCount:   76,
			SourceSHA256:  "401026141f9ad2baff8217dc4b72f814d31d3ecd37ca93469a901cce971e78d9",
			DecisionsHash: "b4d8e3993a26603b83fee34c7dfae0bc42a886b01bcb0e199107394d6fa917c2",
		},
		24: {
			DecisionCount: 98,
			AnchorCount:   73,
			SourceSHA256:  "9f5d37a1b5f1a213e2163ac856c2311a40f2863d4804c3fe649d9cf5d1406241",
			DecisionsHash: "20a950506cf6c5fb2ca76dd2b41996d61e8f6135a079334ebe8a581c4efaaac7",
		},
		25: {
			DecisionCount: 101,
			AnchorCount:   74,
			SourceSHA256:  "5e80a90026f32b0fff3e7cc8eb9cd72130e612987a255d90cc4c079f1adbd447",
			DecisionsHash: "de58a03bd7bd8d52e0972ffb8f88712481e91fe57a10992df41d1892537b43c9",
		},
		26: {
			DecisionCount: 81,
			AnchorCount:   55,
			SourceSHA256:  "9b1acc082aaa9f4a9af2312e32486d6ab0720e8210652dc474b76faddc82ae7a",
			DecisionsHash: "95a071fc985efac6a91ab57abfe4d47f2141fce911f6443874acd3264f227469",
		},
		27: {
			DecisionCount: 118,
			AnchorCount:   76,
			SourceSHA256:  "fc50743024f40afec26d2993d4caa81198f5d474323e28e88cca3af074459052",
			DecisionsHash: "3d5339dc89da8234825643a567842f36c3650cb114dbde358eb4e2f2e17ae3f5",
		},
		28: {
			DecisionCount: 68,
			AnchorCount:   48,
			SourceSHA256:  "11d528095315780a747da1c9bb86c059c7cafd84e08c6f8eac288452652dd5b6",
			DecisionsHash: "551d1f06e51d4340d69bfdad56682d3e13e5a12ba1b608765e6bda083b81a307",
		},
		29: {
			DecisionCount: 121,
			AnchorCount:   41,
			SourceSHA256:  "c119d2f8cd128123d17f94a173302d573850edb86d93090dac5719b0bb9d5085",
			DecisionsHash: "6afc3735c970c224ea460e05356b8aa48100c15ba81aab0c05cf15379c1c3fb5",
		},
		30: {
			DecisionCount: 172,
			AnchorCount:   78,
			SourceSHA256:  "5eaffdf736e66ea33e9e36b3c726310e47f5aa643abd3a4a62df913bd6c39350",
			DecisionsHash: "a62f730117c044775eae2543fac79080c03e9aec884183ee43b9e6a72eda91c2",
		},
		31: {
			DecisionCount: 201,
			AnchorCount:   44,
			SourceSHA256:  "bfec3f918eb8bde4ec42e09c51b7a4c3062c05c2ac1f47c424fec9732e992482",
			DecisionsHash: "5f003172bd3e1883b84daf4c52e41bbd254f4b599d39ba7dd40fd01dafcd4691",
		},
		32: {
			DecisionCount: 186,
			AnchorCount:   102,
			SourceSHA256:  "4d861c8076a0121bf7fa93bbe53970c0cc1d985d8b049d101a32c97802520e34",
			DecisionsHash: "9925d767277c960812f8463193107a27b004f003fd897dea69dcde60734e9358",
		},
		33: {
			DecisionCount: 64,
			AnchorCount:   21,
			SourceSHA256:  "03917926301189ffb98d8b7c93e4f8fd6ec47b4ed8c845b3a5677ba4aba2efe3",
			DecisionsHash: "6680b96ac9d23165ee1c1ef2a3fd8fa029710941336c9587e0ad7b88233f4b81",
		},
	}
	if err := validateStagingReviewProgress(regions, registry, reviewedOrdinals); err != nil {
		t.Fatal(err)
	}
	if err := validateConfirmedPacketPins(registry, reviewedOrdinals); err != nil {
		t.Fatal(err)
	}
	if len(regions) != len(reviewedOrdinals) {
		t.Fatalf("reviewed ordinal lock has %d entries, want %d", len(reviewedOrdinals), len(regions))
	}
	if pending := Pending(regions, registry); len(pending) != 0 {
		t.Fatalf("complete staging registry pending = %v, want empty", pending)
	}
	marked, err := Materialize(documentPath, document, commandBlob, regions, registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateActivation(documentPath, document, marked, commandBlob, regions, registry); err != nil {
		t.Fatal(err)
	}
	// On the accepting path `marked` is what Materialize just produced, so ValidateActivation's
	// equality against a fresh materialization cannot fail there. The witness below supplies the
	// only state that isolates it: mutated bytes carrying a blob pin that matches them.
	activationMutations := []struct {
		name           string
		mutate         func(*Registry, *[]byte)
		pinWant        string
		activationWant string
	}{
		{
			name: "missing activation",
			mutate: func(got *Registry, _ *[]byte) {
				got.Activation = nil
			},
			pinWant:        "baseline activation is absent",
			activationWant: "baseline activation is absent",
		},
		{
			name: "marked bytes with matching blob pin",
			mutate: func(got *Registry, marked *[]byte) {
				(*marked)[0] ^= 1
				got.Activation.MarkedBlob = GitBlobID(*marked)
			},
			activationWant: "does not equal materialized",
		},
		{
			name: "marked blob pin",
			mutate: func(got *Registry, _ *[]byte) {
				got.Activation.MarkedBlob = strings.Repeat("0", 40)
			},
			pinWant:        "baseline marked blob",
			activationWant: "marked blob",
		},
		{
			name: "ordered classification pin",
			mutate: func(got *Registry, _ *[]byte) {
				got.Activation.OrderedClassificationSHA256 = strings.Repeat("0", 64)
			},
			pinWant:        "baseline ordered classification SHA-256",
			activationWant: "ordered classification digest",
		},
	}
	for _, mutation := range activationMutations {
		t.Run("activation "+mutation.name, func(t *testing.T) {
			got := cloneRegistry(registry)
			gotMarked := append([]byte(nil), marked...)
			mutation.mutate(&got, &gotMarked)
			if mutation.pinWant != "" {
				err := validateConfirmedActivationPins(got)
				if err == nil || !strings.Contains(err.Error(), mutation.pinWant) {
					t.Fatalf("activation pin error = %v, want %q", err, mutation.pinWant)
				}
			}
			err := ValidateActivation(documentPath, document, gotMarked, commandBlob, regions, got)
			if err == nil || !strings.Contains(err.Error(), mutation.activationWant) {
				t.Fatalf("activation error = %v, want %q", err, mutation.activationWant)
			}
		})
	}

	// Every region is reviewed, so a receipt-without-admission and a pending region are no
	// longer reachable by mutating the registry alone: the witness withholds admission instead.
	terminalWitnessOrdinal := regions[len(regions)-1].Key.Ordinal
	mutations := []struct {
		name   string
		mutate func(*testing.T, *Registry, map[int]confirmedPacketPin)
		want   string
	}{
		{
			name: "receipt for unreviewed ordinal",
			mutate: func(_ *testing.T, _ *Registry, reviewed map[int]confirmedPacketPin) {
				delete(reviewed, terminalWitnessOrdinal)
			},
			want: fmt.Sprintf("packet %d has a receipt but is not admitted", terminalWitnessOrdinal),
		},
		{
			name: "reviewed ordinal receipt absent",
			mutate: func(_ *testing.T, got *Registry, _ map[int]confirmedPacketPin) {
				receipts := make([]RegionReceipt, 0, len(got.Receipts))
				for _, receipt := range got.Receipts {
					if receipt.Key.Ordinal != 1 {
						receipts = append(receipts, receipt)
					}
				}
				got.Receipts = receipts
			},
			want: "packet 1 is locked as reviewed but has no receipt",
		},
		{
			name: "reviewed ordinal receipt stale",
			mutate: func(t *testing.T, got *Registry, _ map[int]confirmedPacketPin) {
				for index := range got.Receipts {
					if got.Receipts[index].Key.Ordinal == 1 && got.Receipts[index].Review != nil {
						got.Receipts[index].Review.SourceSHA256 = strings.Repeat("0", 64)
						return
					}
				}
				t.Fatal("packet 1 review receipt not found")
			},
			want: "packet 1 is locked as reviewed but is pending",
		},
		{
			name: "activation while packets pending",
			mutate: func(_ *testing.T, got *Registry, reviewed map[int]confirmedPacketPin) {
				receipts := make([]RegionReceipt, 0, len(got.Receipts))
				for _, receipt := range got.Receipts {
					if receipt.Key.Ordinal != terminalWitnessOrdinal {
						receipts = append(receipts, receipt)
					}
				}
				got.Receipts = receipts
				delete(reviewed, terminalWitnessOrdinal)
			},
			want: "baseline activation must remain absent while staging regions are pending",
		},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			got := cloneRegistry(registry)
			reviewed := make(map[int]confirmedPacketPin, len(reviewedOrdinals))
			for ordinal, pin := range reviewedOrdinals {
				reviewed[ordinal] = pin
			}
			mutation.mutate(t, &got, reviewed)
			if err := ValidateRegistry(documentPath, document, commandBlob, regions, got); err != nil {
				t.Fatalf("injected registry rejected before review-progress check: %v", err)
			}
			err := validateStagingReviewProgress(regions, got, reviewed)
			if err == nil || !strings.Contains(err.Error(), mutation.want) {
				t.Fatalf("review-progress error = %v, want %q", err, mutation.want)
			}
		})
	}

	decisionMutations := []struct {
		name         string
		mutate       func(*testing.T, *Registry, int)
		existingWant func(int) string
		pinWant      string
	}{
		{
			name: "decision end byte moved by one",
			mutate: func(t *testing.T, got *Registry, ordinal int) {
				review := packetReview(t, got, ordinal)
				review.Decisions[0].EndByte++
			},
			pinWant: "decisions digest",
		},
		{
			name: "decision kind flipped",
			mutate: func(t *testing.T, got *Registry, ordinal int) {
				review := packetReview(t, got, ordinal)
				for index := range review.Decisions {
					if review.Decisions[index].Kind == ClassificationAnchor {
						review.Decisions[index].Kind = ClassificationNonNormative
						return
					}
				}
				t.Fatalf("packet %d anchor decision not found", ordinal)
			},
			existingWant: func(int) string { return "non-normative classification carries marker ID" },
			pinWant:      "anchor count",
		},
		{
			name: "decision marker ID renamed",
			mutate: func(t *testing.T, got *Registry, ordinal int) {
				review := packetReview(t, got, ordinal)
				for index := range review.Decisions {
					if review.Decisions[index].Kind == ClassificationAnchor {
						review.Decisions[index].MarkerID += ".renamed"
						return
					}
				}
				t.Fatalf("packet %d anchor decision not found", ordinal)
			},
			pinWant: "decisions digest",
		},
		{
			name: "decision removed",
			mutate: func(t *testing.T, got *Registry, ordinal int) {
				review := packetReview(t, got, ordinal)
				review.Decisions = append(review.Decisions[:0], review.Decisions[1:]...)
			},
			existingWant: func(ordinal int) string {
				return fmt.Sprintf("packet %d is locked as reviewed but is pending", ordinal)
			},
			pinWant: "decision count",
		},
	}
	for _, mutation := range decisionMutations {
		t.Run(mutation.name, func(t *testing.T) {
			for ordinal := range reviewedOrdinals {
				t.Run(fmt.Sprintf("packet %d", ordinal), func(t *testing.T) {
					got := cloneRegistry(registry)
					mutation.mutate(t, &got, ordinal)
					existingErr := ValidateRegistry(documentPath, document, commandBlob, regions, got)
					if existingErr == nil {
						existingErr = validateStagingReviewProgress(regions, got, reviewedOrdinals)
					}
					if mutation.existingWant == nil {
						if existingErr != nil {
							t.Fatalf("existing registry validation rejected mutation: %v", existingErr)
						}
					} else {
						existingWant := mutation.existingWant(ordinal)
						if existingErr == nil || !strings.Contains(existingErr.Error(), existingWant) {
							t.Fatalf("existing registry validation error = %v, want %q", existingErr, existingWant)
						}
					}

					pinErr := validateConfirmedPacketPins(got, reviewedOrdinals)
					if pinErr == nil || !strings.Contains(pinErr.Error(), mutation.pinWant) {
						t.Fatalf("confirmed-packet pin error = %v, want %q", pinErr, mutation.pinWant)
					}
				})
			}
		})
	}
}

func packetReview(t *testing.T, registry *Registry, ordinal int) *ReviewReceipt {
	t.Helper()
	for index := range registry.Receipts {
		if registry.Receipts[index].Key.Ordinal == ordinal && registry.Receipts[index].Review != nil {
			return registry.Receipts[index].Review
		}
	}
	t.Fatalf("packet %d review receipt not found", ordinal)
	return nil
}

func validateStagingReviewProgress(regions []Region, registry Registry, reviewedOrdinals map[int]confirmedPacketPin) error {
	receiptOrdinals := make(map[int]bool, len(registry.Receipts))
	for _, receipt := range registry.Receipts {
		ordinal := receipt.Key.Ordinal
		if _, admitted := reviewedOrdinals[ordinal]; !admitted {
			return fmt.Errorf("packet %d has a receipt but is not admitted by the reviewed ordinal lock", ordinal)
		}
		receiptOrdinals[ordinal] = true
	}
	for ordinal := range reviewedOrdinals {
		if !receiptOrdinals[ordinal] {
			return fmt.Errorf("packet %d is locked as reviewed but has no receipt", ordinal)
		}
	}

	pending := Pending(regions, registry)
	pendingOrdinals := make(map[int]bool, len(pending))
	for _, key := range pending {
		pendingOrdinals[key.Ordinal] = true
	}
	for ordinal := range reviewedOrdinals {
		if pendingOrdinals[ordinal] {
			return fmt.Errorf("packet %d is locked as reviewed but is pending", ordinal)
		}
	}
	if len(pending) != 0 && registry.Activation != nil {
		return fmt.Errorf("baseline activation must remain absent while staging regions are pending")
	}
	return nil
}

func validateConfirmedActivationPins(registry Registry) error {
	if registry.Activation == nil {
		return fmt.Errorf("baseline activation is absent")
	}
	if registry.Activation.MarkedBlob != confirmedBaselineMarkedBlob {
		return fmt.Errorf("baseline marked blob = %q, want %q", registry.Activation.MarkedBlob, confirmedBaselineMarkedBlob)
	}
	if registry.Activation.OrderedClassificationSHA256 != confirmedBaselineOrderedClassificationSHA256 {
		return fmt.Errorf("baseline ordered classification SHA-256 = %q, want %q",
			registry.Activation.OrderedClassificationSHA256, confirmedBaselineOrderedClassificationSHA256)
	}
	return nil
}

func validateConfirmedPacketPins(registry Registry, reviewedOrdinals map[int]confirmedPacketPin) error {
	for ordinal, pin := range reviewedOrdinals {
		var review *ReviewReceipt
		for index := range registry.Receipts {
			if registry.Receipts[index].Key.Ordinal == ordinal {
				review = registry.Receipts[index].Review
				break
			}
		}
		if review == nil {
			return fmt.Errorf("packet %d confirmed review receipt is absent", ordinal)
		}
		if len(review.Decisions) != pin.DecisionCount {
			return fmt.Errorf("packet %d decision count = %d, want %d", ordinal, len(review.Decisions), pin.DecisionCount)
		}
		anchors := 0
		for _, decision := range review.Decisions {
			if decision.Kind == ClassificationAnchor {
				anchors++
			}
		}
		if anchors != pin.AnchorCount {
			return fmt.Errorf("packet %d anchor count = %d, want %d", ordinal, anchors, pin.AnchorCount)
		}
		if review.SourceSHA256 != pin.SourceSHA256 {
			return fmt.Errorf("packet %d source SHA-256 = %q, want %q", ordinal, review.SourceSHA256, pin.SourceSHA256)
		}
		digest, err := confirmedDecisionsDigest(review.Decisions)
		if err != nil {
			return fmt.Errorf("packet %d decisions digest: %w", ordinal, err)
		}
		if digest != pin.DecisionsHash {
			return fmt.Errorf("packet %d decisions digest = %q, want %q", ordinal, digest, pin.DecisionsHash)
		}
	}
	return nil
}

func confirmedDecisionsDigest(decisions []Classification) (string, error) {
	// Lexical field order and json.Marshal's compact encoding define the object
	// form; the slice retains the receipt's stored decision order.
	type canonicalDecision struct {
		EndByte   int                `json:"end_source_byte"`
		Kind      ClassificationKind `json:"kind"`
		MarkerID  string             `json:"marker_id,omitempty"`
		StartByte int                `json:"start_source_byte"`
	}
	canonical := make([]canonicalDecision, len(decisions))
	for index, decision := range decisions {
		canonical[index] = canonicalDecision{
			EndByte:   decision.EndByte,
			Kind:      decision.Kind,
			MarkerID:  decision.MarkerID,
			StartByte: decision.StartByte,
		}
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest), nil
}

func TestP3CompletionRowsAreByteLockedAndCarryNoPendingEnumeration(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository path")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	contents, err := os.ReadFile(filepath.Join(repository, "docs", "COMPLETION.md"))
	if err != nil {
		t.Fatal(err)
	}
	document := string(contents)
	rows := []string{
		"| The atomic P3 activation check accepts only when every region names the same immutable input blob, the ordered region universe covers every physical source line without a gap or overlap, classifications cover every non-whitespace payload byte exactly once, the classifications materialise as inline marker ranges, and the resulting marked blob and ordered classification match their pins; its fixed completion predicate is `unclassified == ∅`. The check validates structure and never proposes or infers a classification. | mechanical |",
		"| A human reviews every region of the pinned input blob and confirms the complete ordered byte-range classification, including each independently violable normative statement and every explicitly non-normative range. This is manual because a detector would reproduce reviewer errors where judgement is hard and introduce errors elsewhere. | manual |",
	}
	for _, row := range rows {
		if count := strings.Count(document, row); count != 1 {
			t.Fatalf("P3 completion row occurrence count = %d, want 1: %s", count, row)
		}
	}
	sectionStart := strings.Index(document, "## 9. P3 baseline classification")
	if sectionStart < 0 {
		t.Fatal("P3 completion section start not found")
	}
	sectionEnd := strings.Index(document[sectionStart:], "\n---\n")
	if sectionEnd < 0 {
		t.Fatal("P3 completion section boundaries not found")
	}
	section := document[sectionStart : sectionStart+sectionEnd]
	if strings.Contains(section, "| pending") || strings.Contains(section, "pending regions:") {
		t.Fatal("P3 completion row carries a pending-region enumeration")
	}
}

func TestP3MarkerInvariantIsByteLocked(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository path")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	contents, err := os.ReadFile(filepath.Join(repository, "docs", "MARKERS.md"))
	if err != nil {
		t.Fatal(err)
	}
	row := "| `baseline-complete-classification` | The enrolled blob has a complete ordered classification with `unclassified == ∅`; every non-whitespace payload byte is classified exactly once as anchored or explicitly non-normative. |"
	if count := strings.Count(string(contents), row); count != 1 {
		t.Fatalf("baseline-complete-classification occurrence count = %d, want 1", count)
	}
}
