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
	confirmedBaselineMarkedBlob                  = "277b6b2a3b62ab3f3e9513b930e49e706086b5a9"
	confirmedBaselineOrderedClassificationSHA256 = "28aa6e1095ee35115616be45cd4bf7fe852f0160160fc26a62f240b1b0855e52"
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
			DecisionCount: 100,
			AnchorCount:   79,
			SourceSHA256:  "a664af3565a224342433aa9db2d35dfe06b915d752c7fa9560c75e3b2b60dce5",
			DecisionsHash: "97096e0f6bc61c634d9210b2222ceba9dd8157c1b19ac6b855ed4acc5539e840",
		},
		17: {
			DecisionCount: 147,
			AnchorCount:   107,
			SourceSHA256:  "89e014250341b8def45f6443d2051599df0ad28467848fc2d4f7725382c5fefa",
			DecisionsHash: "1c8f220e757e92059f095fd8d33a34aa87b73e658a69e7514764006a9096786e",
		},
		18: {
			DecisionCount: 141,
			AnchorCount:   102,
			SourceSHA256:  "05a57e5674755c059f3861ee8696a3f5723eb34b01680b1a50b1d14776db0e89",
			DecisionsHash: "9fbe63dc8c01993ca4a9bd998696b37146a230652baf19af8de1ab2aff919b0b",
		},
		19: {
			DecisionCount: 142,
			AnchorCount:   104,
			SourceSHA256:  "69bce930f17602ed228506dfe0b947d10834ce18cde1d890b7081cee174b05ca",
			DecisionsHash: "a01c5ca198f6883ec0147ea9de103135f915fd2370bd76f07584d7396d551008",
		},
		20: {
			DecisionCount: 112,
			AnchorCount:   81,
			SourceSHA256:  "5309ee3229b967135ade44a6e46bfb39a6631d548e10082d92e7afa200c06d96",
			DecisionsHash: "3410ab9bb7733a3bbd0a7229db6649ce16e4453fd5797472b555309ce1161b34",
		},
		21: {
			DecisionCount: 138,
			AnchorCount:   93,
			SourceSHA256:  "3e1830b55af61e47c77c501ee37f685b0baf16d0d63675011caccebea111bdb7",
			DecisionsHash: "a043f2c5650fc9223a9a720cbf6ade0800424393f80b972cedc712febc5fac60",
		},
		22: {
			DecisionCount: 146,
			AnchorCount:   102,
			SourceSHA256:  "b954c13d65f8f82baae4a661326f964162b4accd73c358b12108a6fb50dc9800",
			DecisionsHash: "e5902fb44e500185d54b720e87a1cb9f066bda9e04071d180fff8978c7032cfe",
		},
		23: {
			DecisionCount: 110,
			AnchorCount:   68,
			SourceSHA256:  "39b9f49fa088327291ca2e564a0922e0b1c6415f0de4a3234eae401dfeb362bd",
			DecisionsHash: "e9ef9ad7b1863ea2a66fa9753946c95a311e8dfe68d6e8275c7bbe9f76b3a6e6",
		},
		24: {
			DecisionCount: 113,
			AnchorCount:   81,
			SourceSHA256:  "3bdc85282cb8905208e4e63fd21c6dd6aec6de9b1b34bec8792622a196eefebf",
			DecisionsHash: "56f5bd2a1a1ff493bb36c63df3468df4b0dc998985d8075548be413a5d105da4",
		},
		25: {
			DecisionCount: 87,
			AnchorCount:   65,
			SourceSHA256:  "fa8766954953382aa5dcfb6f2d50aa1f9108a7f89d9511c1ce26f4a6ec64722c",
			DecisionsHash: "f99a15d5d18ead06b816cf8179fcb33bcfbe5dfdc974a12d12bccf0e3cdb3ff4",
		},
		26: {
			DecisionCount: 100,
			AnchorCount:   67,
			SourceSHA256:  "7301bb53c651708b21cc607383a1f0e3b1b46a8f0fa7bc9906ac477b208ac584",
			DecisionsHash: "0f860ba8fbe5cd41eded89d354c24848dfda25ad21bc0e419241168ae9ff2ef8",
		},
		27: {
			DecisionCount: 97,
			AnchorCount:   62,
			SourceSHA256:  "1d80ee4d37c190a2ab3c5560ca186e048ef4959fb2e81e06da0342d2cbb7228a",
			DecisionsHash: "cb7fe9e699ca2f23a1dfc49011cbc91af76aca7a6581f5201f9573834cceed5f",
		},
		28: {
			DecisionCount: 67,
			AnchorCount:   48,
			SourceSHA256:  "334d8d4748fcc5bf6fe2ff82850e33ea1316902c39848f5b4aebd315b76b5e00",
			DecisionsHash: "ab69ea17b5ef2ff77d134dd3ba5fd01ce38ce7254ed2d54d7840de6c4be9faa9",
		},
		29: {
			DecisionCount: 108,
			AnchorCount:   58,
			SourceSHA256:  "c43f0bdc413fdfcf411358a0e17f2088b3b078c795195208d04e1ac8f8f3d332",
			DecisionsHash: "17cda9b8a13b60f677d24a02f7889ff2ea8da4bd8b121ba2bdc9d229a88bc420",
		},
		30: {
			DecisionCount: 172,
			AnchorCount:   62,
			SourceSHA256:  "2b48d0442c178520db5ee4d639cd9fb7e466e5e76c8bda059d9a6d0b1eef8847",
			DecisionsHash: "b050546e95d7ada09dfa1fc152ee37f4ccc28fa4351e989578f6b0f38b4de36a",
		},
		31: {
			DecisionCount: 186,
			AnchorCount:   39,
			SourceSHA256:  "f84a4703be2565193c8a720b2b17006ad8230e2dec5131b8dee64e5663a64a0c",
			DecisionsHash: "bb3fb0930daee177c095fde09350786164bdce0fdcf2ef8613e1d346748a3b4c",
		},
		32: {
			DecisionCount: 198,
			AnchorCount:   101,
			SourceSHA256:  "5e4a7e9dca62efe3611d90915b1a9c422d0118ed99a0d6b33db9438db79d1d84",
			DecisionsHash: "09e9a3b9e3231198437e5064bbc02557e0cc73c408431f06547f715a247a5406",
		},
		33: {
			DecisionCount: 106,
			AnchorCount:   43,
			SourceSHA256:  "7d9a368ae6e87c36558b8a86c5d485c07c535042797a176e7f22d4e355947899",
			DecisionsHash: "88d1e0ba67f97ecf0570cba16bb517d7f2e81e067994cc2a60562221726b4a03",
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
