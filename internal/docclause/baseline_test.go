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
	confirmedBaselineMarkedBlob                  = "da5ad45389a102dd73317f5821c7cd1d38df00bf"
	confirmedBaselineOrderedClassificationSHA256 = "f9f452ed7122611edb40e3260d6e9c3fea508f43fd7fd457c13c6141d82a25b7"
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
			DecisionCount: 207,
			AnchorCount:   137,
			SourceSHA256:  "53ef24a2aa27e30cc333c1065651a6e55fcc29fecee30578f5901f444f023222",
			DecisionsHash: "0342acbdd7a8e6f54b3ba13ad3e22ce9199dc11c911e7fe4d50fd9d22b071091",
		},
		6: {
			DecisionCount: 137,
			AnchorCount:   100,
			SourceSHA256:  "5fad7254502979261e46c49aeefb07a93fe2b871c95af4a0636275d26b8cc246",
			DecisionsHash: "ec347cfe1b7cd14eb05eb66562c3455c7b6c04b60b5e7eed594a600c2696f400",
		},
		7: {
			DecisionCount: 142,
			AnchorCount:   83,
			SourceSHA256:  "7bf2c10d3762e430e6093143d6b46257ba4513a0549c2c22b1183c6e0087847f",
			DecisionsHash: "78d92406828818d4e22cb6b6a2b9fd1051885deac8a165da9b22e2f2f4afac91",
		},
		8: {
			DecisionCount: 163,
			AnchorCount:   99,
			SourceSHA256:  "1431b9aa196317a5053bae188d09add23a810d215520b33aa6561a1c4f1810be",
			DecisionsHash: "141ac4547412a2f5bc651ea29af440fd95a6a0997965364b756a4155e33ea1b8",
		},
		9: {
			DecisionCount: 195,
			AnchorCount:   133,
			SourceSHA256:  "e51c5ea1e0564fe4f7a0b367b89fa08af27506602bef4a00d0c80f4425631cb7",
			DecisionsHash: "a47d669de0b9622385821f5cc9100452cc9501aea32d3884b140a67eb6c107fe",
		},
		10: {
			DecisionCount: 160,
			AnchorCount:   109,
			SourceSHA256:  "27870e31aa626449bc6996686432b243731d88dc3943837e1520ae0467611d33",
			DecisionsHash: "06d68f8278e92174b4416e22bf74cfabc1cc96426ae10c1cf371f2108872a74e",
		},
		11: {
			DecisionCount: 114,
			AnchorCount:   80,
			SourceSHA256:  "b3e58b70c870126f44b1d064bd3fc5144fdf2ead2acf57612b1f2a0a30e95ab4",
			DecisionsHash: "83490b938e1296fed9110d23b5e19320d443b719dc47e8cc9d3f208ea3c5a1fe",
		},
		12: {
			DecisionCount: 103,
			AnchorCount:   79,
			SourceSHA256:  "38df101d00d27a73f1c9029e26f1dfa4eaee4a0fee926fd8aeb19e5d1186c47e",
			DecisionsHash: "f21c3f3e546437ae96ec35b055ccd597fe0874f7e300d34ed54600d4ee96d4a8",
		},
		13: {
			DecisionCount: 128,
			AnchorCount:   75,
			SourceSHA256:  "cf00d656aa2b2f1251e016f1869d4c0134adfceac0da798ca4a228b7a46fd2b0",
			DecisionsHash: "135356c44047f06fbfb0c5ba9358eda64936c0c1f91c1a6457e565c237b99e03",
		},
		14: {
			DecisionCount: 123,
			AnchorCount:   85,
			SourceSHA256:  "783b10e0aeb3bd367235614b60534f5023239b7224db49a893ea103f0ad25862",
			DecisionsHash: "b65d4d8288c0823e7ae781c4fc88ae4f11962baecf6808675dd3678e5bfd495c",
		},
		15: {
			DecisionCount: 135,
			AnchorCount:   101,
			SourceSHA256:  "d7abffb9536e4cd12dca82c431c2eaddec9f2c3f7b0448024f0079ec3448c098",
			DecisionsHash: "22be6fcbc02d4b030af418fa2d08fff0f924a1519830707c1388d596aadf688e",
		},
		16: {
			DecisionCount: 100,
			AnchorCount:   79,
			SourceSHA256:  "e67522d62bad562a8fc82fb0a4177a5c7c256f5ce3a9804acc3803acebe76947",
			DecisionsHash: "b6535184122d1d483f2f0ea91b542e8ed1604995c79e9330d69997409276f95e",
		},
		17: {
			DecisionCount: 144,
			AnchorCount:   102,
			SourceSHA256:  "ee2bdb6643050d9d312bbd1b50fd7eaffb8606b4cc22a136bd283f6628101dc0",
			DecisionsHash: "9ae8c1aae12f3a99ca5e23d01b18a03df0e8e9ce069d05554798fa7d72d0c1b1",
		},
		18: {
			DecisionCount: 148,
			AnchorCount:   105,
			SourceSHA256:  "dd54c8bc75c294a8d79fa91b414bafb1cbd280a306c77335c9d7f05418b5500e",
			DecisionsHash: "8d4aaef9ca807bff0abf508bffe2a75cf50bc4cc2e62752d95537badfe684f23",
		},
		19: {
			DecisionCount: 141,
			AnchorCount:   104,
			SourceSHA256:  "d344c2e8fe89911a6bd1054aaabfe4726f2025030d8c60e3c3b86f6f58a313b2",
			DecisionsHash: "4fd423503de30c7ddbf4ab5478ea1cfeea03a45d835c7009ff5842c19f00662d",
		},
		20: {
			DecisionCount: 109,
			AnchorCount:   79,
			SourceSHA256:  "a05d5484dffd6550743da4b2daeeead9b880a3fc66c29cf2aa4ead151315b836",
			DecisionsHash: "c14506b7fc97c9e523bb42a06ca73ad5bb3e6d9d33298f8377cb397d33ed582a",
		},
		21: {
			DecisionCount: 137,
			AnchorCount:   91,
			SourceSHA256:  "d3b15ab2ba9062e995dfa594dacca31d6c2ac32d2f674621bbfa68afa1b8de17",
			DecisionsHash: "e364e44da3df66037460a954d9e0f7e8b183aa490437d71849cb236c6ff32210",
		},
		22: {
			DecisionCount: 144,
			AnchorCount:   100,
			SourceSHA256:  "2c05b9c8f80185af47c1096f682b21c647228f64f3820e0a1bf6b9631e4732ae",
			DecisionsHash: "631415676e17b0c418a1809db7bbc0343a73f509b1e004fd0c293fd7d856fbc9",
		},
		23: {
			DecisionCount: 117,
			AnchorCount:   75,
			SourceSHA256:  "7dba23e35b4464b143c6f250010e8a20185691cb318473f8909bae2f7ff57919",
			DecisionsHash: "5599151fd70beac65cc14c095af129a33c03005ede672da56a9129e4e4810344",
		},
		24: {
			DecisionCount: 109,
			AnchorCount:   77,
			SourceSHA256:  "daa409bcf4748889d37056c44149cf792f24673e655097100d7f5b695dd57e7c",
			DecisionsHash: "8770a33c23d78ea7c1c1fa96007516ea8ca16084f60aa04d63b439c3cb0edd45",
		},
		25: {
			DecisionCount: 96,
			AnchorCount:   74,
			SourceSHA256:  "346e144710efb470726c329fac16f07b2f3f67d6ff1f7f54879f987f6560cefe",
			DecisionsHash: "bbe69aad18d14c1a2cf544d5dee28898307cc10b26934f5bbbe87adc69ea9863",
		},
		26: {
			DecisionCount: 103,
			AnchorCount:   68,
			SourceSHA256:  "925dfb66700848e05e65d6656b5ba770350edb9ede2c593c8674b7034af01776",
			DecisionsHash: "01e50d26a07505bf2454ab749bcc65141002d9f47274335fe194ce0f80f19e04",
		},
		27: {
			DecisionCount: 84,
			AnchorCount:   54,
			SourceSHA256:  "b348c3e52962614e28c6e205a093b1c42171843f71b0cb2d0cafa3d7459d711c",
			DecisionsHash: "e12634d02a0cfd3f7348d01925575ac190d2e64fde4a0cb01edb004fac264e23",
		},
		28: {
			DecisionCount: 73,
			AnchorCount:   52,
			SourceSHA256:  "bb74efc2dc2c09357903170aea6d9180de140fbbc7ad119018e41da7f24cbe73",
			DecisionsHash: "067a082ab961c8da7120dcd53417bdc82563838b6256fd60f8d23ff704eb822d",
		},
		29: {
			DecisionCount: 101,
			AnchorCount:   63,
			SourceSHA256:  "6b939d5a21fd5675d0a328a62dd7f6b357be5ef13593b539ac18282d330fd595",
			DecisionsHash: "98f796483a5c269359f309d9b1413ad6a42976de0d02e8fd5aafd913e32ed163",
		},
		30: {
			DecisionCount: 171,
			AnchorCount:   52,
			SourceSHA256:  "23ab49d50891df86255ac919a9a30db922b666f6157f2ee809baa9aabe6a851a",
			DecisionsHash: "58100f450ae76133ec7ca93a138e9a74ede95ca648fa44de335df56af6a4342c",
		},
		31: {
			DecisionCount: 187,
			AnchorCount:   44,
			SourceSHA256:  "dd924e8c6ab28569e25fc81e79a1da8f4f2e9120735a55f48184abb7ebd6121d",
			DecisionsHash: "8fed7ed5d1cfd295327bfff5cd0a9f9a3d5b4c54135d1c4e380db34157041a7d",
		},
		32: {
			DecisionCount: 192,
			AnchorCount:   92,
			SourceSHA256:  "41df25b4329a456bf8fe5fa98818202449e0fc3fb3304f4a28ad57cd84207310",
			DecisionsHash: "ab1893c4cf2bd4dc4433518b5632e6951f5e8466def0ee0bd404ba7ed6043be5",
		},
		33: {
			DecisionCount: 128,
			AnchorCount:   58,
			SourceSHA256:  "26faa3352a76c7158f1886e9953bdbdf0af9a8c7996c4b540caa6aa212a0cd41",
			DecisionsHash: "5ab8fc16aaeafbc6b464d659c6697ffd333a3d95323422397f667ec43b17b1cf",
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
