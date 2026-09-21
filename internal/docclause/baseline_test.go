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
	confirmedBaselineMarkedBlob                  = "991c39db45bc2994727147e6368784b781bd2798"
	confirmedBaselineOrderedClassificationSHA256 = "76891e312813e63e78f670f33d88ce1ba53ad8fbc3e78b1a0418bcadb2935e18"
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
			SourceSHA256:  "1e40fa6b2798e9291f46b658b4f7675ed9b211fe9ef82c340907e1d25715c899",
			DecisionsHash: "a1194355328a68e5bba0ce2042bd421c1b60c0ed5ab3176b5249a302cd08370f",
		},
		7: {
			DecisionCount: 143,
			AnchorCount:   85,
			SourceSHA256:  "e5bbfa264d6d487ed16734675ee14db0b41a2f3ef2334104fa8613c7c38da759",
			DecisionsHash: "4dc5fae7575f73f3614df5e39d210287bcd894a7f979c47592d305326601f839",
		},
		8: {
			DecisionCount: 165,
			AnchorCount:   101,
			SourceSHA256:  "33869831cb05b76248b311fd4dadfed8afdf34a26919aef8a33b1890cfd2a30a",
			DecisionsHash: "3a2c0c8848e531e7f3cb2c9c391c9cebafd8a27f26718ba06ebc0a1fb5addd07",
		},
		9: {
			DecisionCount: 195,
			AnchorCount:   132,
			SourceSHA256:  "2632dd064b2e0338488dff1eec47ecd2fcfc25afc4a67016df5c2aab5a02c7c4",
			DecisionsHash: "c32f3cca4a416b1a37e9d0e739d904b5416bc254812d1c862ca9e33bcc06bcfd",
		},
		10: {
			DecisionCount: 161,
			AnchorCount:   110,
			SourceSHA256:  "5db279996c5b21241a7fa9ea6438658ef2d6670edbdae392ffa4e0e20158abf0",
			DecisionsHash: "9c003cc1f7adcf6e9ab0785760ec48c99ad1b8f736e39d2cf13967b39b8335c4",
		},
		11: {
			DecisionCount: 113,
			AnchorCount:   80,
			SourceSHA256:  "a235b06647f8d380df13fa1e00495f93277a6d13d71410ca61e109312ad93da4",
			DecisionsHash: "65739f46c73fd2ebf609b0c9a6ac39ce94592b2048db351795ebda9e9b0decec",
		},
		12: {
			DecisionCount: 103,
			AnchorCount:   79,
			SourceSHA256:  "6638e5bd1b65761f97236dd28eb6cc860f314b26c408fdbbb4b9e416e1434cfc",
			DecisionsHash: "72ae9ad21655241cb83b97aaa209b0986403870806be8fb80016f0ee89f07f11",
		},
		13: {
			DecisionCount: 125,
			AnchorCount:   72,
			SourceSHA256:  "1a35dbc1dd8a37698eac5476deba9e3e65506bb0d8704e3e0048c1c1064da7cc",
			DecisionsHash: "52e43d23d90101ad5f8ae238eb0f18cc3de5bc4a3055ca6f937d5584c4be51e9",
		},
		14: {
			DecisionCount: 126,
			AnchorCount:   88,
			SourceSHA256:  "ce25b97cdeafecc8e4112d3ede463b6409c5255b7258b6a58074019102108a23",
			DecisionsHash: "a35eb6958676546ff014959ade4e39c47b67d6b32e61fcac7352edcb7cfc2892",
		},
		15: {
			DecisionCount: 135,
			AnchorCount:   99,
			SourceSHA256:  "97ffeec2bf3a2437d8d90316a6fc49c5141e58e069ad2eccc16d65b1f0f26553",
			DecisionsHash: "8981fe7d5135182897ca6b569eff3dfb6c6f78a2bc0cb83e0f90a2e1f9755652",
		},
		16: {
			DecisionCount: 101,
			AnchorCount:   79,
			SourceSHA256:  "926d87fe7c9adefefcb50ffaa66960e346b012fe2479461d0ad0927906269b51",
			DecisionsHash: "4fa4cced3ab0a89602390d624c969362dd42df2c7bba01a6c76f4df050ee77d2",
		},
		17: {
			DecisionCount: 145,
			AnchorCount:   103,
			SourceSHA256:  "2101bf6f087a231472ed623d6f9ce1bf87db72e9684e4913a242e472176e6e86",
			DecisionsHash: "ef02c5175719b01bd82dd3510b5cba0ee872a728b90043ce1ffe6a92195eb93c",
		},
		18: {
			DecisionCount: 149,
			AnchorCount:   106,
			SourceSHA256:  "8e94477145553173a221d3c4d95002a0f2e4d275fc5d06bf5a7a5778901e75b4",
			DecisionsHash: "614b5c68d7b9a8dea746009f9b54a95a0d0e4419eeb10c7830cc046814d01673",
		},
		19: {
			DecisionCount: 139,
			AnchorCount:   102,
			SourceSHA256:  "ed82e7eb21f8bb7266b6283a2cf3fd934f5c0185e543102c782a1b10f257e468",
			DecisionsHash: "890e4cfab58439304727809ad83a13005f2e525e09a58cc26b07aa64822754da",
		},
		20: {
			DecisionCount: 111,
			AnchorCount:   81,
			SourceSHA256:  "b46c30565afe97367ba1a043aee1fc371aab87b553b037a047eacf101dbf6465",
			DecisionsHash: "c0e8c6d15653548c4f7215d167c3459140cef41e2d24830ce4850ef00de06aba",
		},
		21: {
			DecisionCount: 137,
			AnchorCount:   93,
			SourceSHA256:  "eb40b5d56e5947cc3c11728c82b5244c6ee97232a1e03fc811194f7ea8f5fbab",
			DecisionsHash: "f1d766a2e70d7c7b98c0d3f8ae78a1530161ecc4d6455c9ea3f1b04cbe81f0ad",
		},
		22: {
			DecisionCount: 143,
			AnchorCount:   99,
			SourceSHA256:  "fd99529197a1468846b3ee2c966dbdf6fcfd26659d355b5dda04630be3605ae1",
			DecisionsHash: "f5af9a51f7a10a02a74adc4846955fcde6bc9b7dd1d6a6ef5ec6aba9b6c20a8e",
		},
		23: {
			DecisionCount: 116,
			AnchorCount:   73,
			SourceSHA256:  "2f9e088ac24e933312919afb31ee2af01af24635caee359c4bcb77e9e0e4b34c",
			DecisionsHash: "51bd033285d21921d0b73d6cf25742b723ce5f0cd76e5a690ca1b032fff0e8e3",
		},
		24: {
			DecisionCount: 110,
			AnchorCount:   78,
			SourceSHA256:  "567989e6230055f231620eb2bd6073ea195a6759dcbc43e9cd706a582bce74fd",
			DecisionsHash: "ddbab90fe7fbe24b9c7393893c2c3ec4cab94d24c6a170cb5d858d18365ddde5",
		},
		25: {
			DecisionCount: 94,
			AnchorCount:   72,
			SourceSHA256:  "573cb0b2ad8481a51e944415c694a50573df67ab0f1acff73b3846805c530b1e",
			DecisionsHash: "6fb0bb3d00005e979d4908010dd7bdff329f1fa09ffe5640b2ed8ab57614edc7",
		},
		26: {
			DecisionCount: 102,
			AnchorCount:   67,
			SourceSHA256:  "9c2cbefc570dd0354455bc6c079f223e3a05114fb5779b6b9bc4a4d74ee1876c",
			DecisionsHash: "7add7690795f920b28e7489c9b56d473f8af63005baab30e98d628a81d2925b9",
		},
		27: {
			DecisionCount: 89,
			AnchorCount:   58,
			SourceSHA256:  "e721a41d6ffcb80433fff8a85f97d1fcfdb326dc9e2bc35c0c3c6aa09d3ff7bf",
			DecisionsHash: "d7ac9ba3dce58ccbdf0633291cc59b18d1e7133321f47c2946a6d44d2e957170",
		},
		28: {
			DecisionCount: 70,
			AnchorCount:   50,
			SourceSHA256:  "a36cd524808fe4d5b8a0e949e58bbb4632149e5f51bb55d04d0f709a0fc953d0",
			DecisionsHash: "603e68c3db3c273792526bff0851da9a683fb825d0a3d60447660e5a91a5b632",
		},
		29: {
			DecisionCount: 105,
			AnchorCount:   61,
			SourceSHA256:  "20692da3f94a329f6b6733b9b74c65f3ce85bbfb905d0aac9b45e30d1b50ce25",
			DecisionsHash: "a66802a9c90205046418d8420063aecb9680472c2ee3d37e3ed568aab435a1f2",
		},
		30: {
			DecisionCount: 171,
			AnchorCount:   58,
			SourceSHA256:  "940171e80deb832e35a5b89978d7689143659739f7ab7e36c2e3b29b83361d9e",
			DecisionsHash: "a6a5b10e238d3e5b8d2515c1e7bb1034064aba43095b36b1d78f4f54eb98920f",
		},
		31: {
			DecisionCount: 184,
			AnchorCount:   40,
			SourceSHA256:  "bd7176faa4eb123fac70cc12da0e25a5ae247881116942e6421b99f5144c5d2a",
			DecisionsHash: "84bff41df7c8e1ac06509b2df04e2474a19928507dee209d4e21a7e8ec345f46",
		},
		32: {
			DecisionCount: 195,
			AnchorCount:   95,
			SourceSHA256:  "e285bbf9cd4ba296e1a6da8ff1c0c94f03c0904c9c8f2fa6f35763f35c7787a8",
			DecisionsHash: "3f12a77668e325b25916ec670f2abf7a92ca1c323f482c22b7cb1d65bd981e1a",
		},
		33: {
			DecisionCount: 121,
			AnchorCount:   53,
			SourceSHA256:  "26f5202c6f6dc0f2dbeac60938caae26979e13cbacbd354b63777fe3cd6e7c84",
			DecisionsHash: "e803b854b5d8393137b03690748179d70e9b58ddc6b67b4a9d84ac0078b6e900",
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
