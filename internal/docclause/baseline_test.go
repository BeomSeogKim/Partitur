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
	confirmedBaselineMarkedBlob                  = "be6318dc2f1b4f18af465300e39a29cdbeb4ca24"
	confirmedBaselineOrderedClassificationSHA256 = "2f5cc60d473726b1ea9499283bd68f6da3f595a58c25e6ab5273c9bcc9a7224a"
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
			DecisionCount: 138,
			AnchorCount:   104,
			SourceSHA256:  "c259e9f46d95b8b6d355813810e7fcc868a0e4a431bc5cecf299c094b85e5406",
			DecisionsHash: "6bfef272a4b68a9fcaa7c91e5e4e055d3f47582e54973ea6e93d1eef6010a814",
		},
		3: {
			DecisionCount: 91,
			AnchorCount:   39,
			SourceSHA256:  "abdcfaf23bd5b422db07c499fafe519e1386ae855f08c66cf451ad57acf818a3",
			DecisionsHash: "9c0f8f654352befe3f0595553d3715ed454f0e85ac1d98e5afafb8366f8a8d6d",
		},
		4: {
			DecisionCount: 180,
			AnchorCount:   109,
			SourceSHA256:  "aea7342b946eb940ad50c204785446232b77606684d7ce4063690e44700dc0b3",
			DecisionsHash: "365a0759045ea11557b85e04309e27dffc426c592e9c69b6c144e4c8ee143988",
		},
		5: {
			DecisionCount: 181,
			AnchorCount:   115,
			SourceSHA256:  "b489201a53f9c76223884b39ec3b7bf3eeb5cd4676b7452e18a2187830e5d114",
			DecisionsHash: "673a3092fac73933a4d2f3019f94aacdf534390600e2bbcbcf705c30a3d55eec",
		},
		6: {
			DecisionCount: 133,
			AnchorCount:   96,
			SourceSHA256:  "8f7790eabbfb4caa23713e58d7fd395a555250d76085bc16feb2ccc5b2cd0c29",
			DecisionsHash: "5d3c0fab589cce4cf15a15123d9ac9ca44808226f1a5123619df2549f6b0ed35",
		},
		7: {
			DecisionCount: 147,
			AnchorCount:   87,
			SourceSHA256:  "33663b27002200970bbf47e1d5e0173addc181a8967baaca63b140141a59b353",
			DecisionsHash: "20ad928f9ba5503a11d34527dd9e43b4fecae5ac20af0199772c2e8ee643e32f",
		},
		8: {
			DecisionCount: 170,
			AnchorCount:   103,
			SourceSHA256:  "a980dba9f5ff9016c67465362c1b7129120a1dab946546303b5a2cde711adc0c",
			DecisionsHash: "7e19c8ac004ab5534410041b8902194b13a4029e859c910d08b69f8d768f3541",
		},
		9: {
			DecisionCount: 192,
			AnchorCount:   131,
			SourceSHA256:  "b7f0118405d940e5bf4dc02e6ef79014bff0db4fc7d14f8f4857bca3cf34dd95",
			DecisionsHash: "3e82efdcfa295ba507b7062deeddd061c3014dba61368616be18f9d4d508bcac",
		},
		10: {
			DecisionCount: 163,
			AnchorCount:   115,
			SourceSHA256:  "d8f1dc87ac1154af5426988bfb3810ee0058ac4731f3cdb494ed4c60763dd860",
			DecisionsHash: "922da94ded2b8851a61d36904e88cef17f943dfc343365566e00bf72c367d9e1",
		},
		11: {
			DecisionCount: 116,
			AnchorCount:   80,
			SourceSHA256:  "b1b881b83a07d2a3b8e6b69a203e544db90be72736c74a0a7121d596ec85265c",
			DecisionsHash: "fee8a69e308853036290ed69282562a56088268d8c2b25b0f61bbed8279ea1fa",
		},
		12: {
			DecisionCount: 102,
			AnchorCount:   80,
			SourceSHA256:  "eb0ce3d75373607c777388f1919b2c69b36eea4238bd4d29af9404e4eb18c2d2",
			DecisionsHash: "8d35676bfa8b64036aac92c6657e1715b30926a230928a531fe854f91340f121",
		},
		13: {
			DecisionCount: 126,
			AnchorCount:   70,
			SourceSHA256:  "fa5e7146e151d87fd6c1c866031bad6887075e764a9408984ecac5a5b53bcc7a",
			DecisionsHash: "961bd913c1d46aeb694dbbb0c60ac8d6187f8af28e3cac867b5c4075508d9d01",
		},
		14: {
			DecisionCount: 130,
			AnchorCount:   88,
			SourceSHA256:  "7fa84ba055bd33036de9e7d9368a5c8ea022a401cf0de2a615cb4e70092caa86",
			DecisionsHash: "0f349f5a747ed4f39ac57223c248859bfedd783f1cd7bbcc6d4d53ed976f4268",
		},
		15: {
			DecisionCount: 135,
			AnchorCount:   99,
			SourceSHA256:  "20133dee89c4990b8cb45e49447d71e7b82fe3bd7993c1d29851ef0d450ec8af",
			DecisionsHash: "a73e1936f227465372700c42bdbb84f605b8df83d4083d47857e2efdaffaee86",
		},
		16: {
			DecisionCount: 114,
			AnchorCount:   90,
			SourceSHA256:  "8c91291d658ab5e60605e5480f18ca8624327e4591159fba399974f9827e897e",
			DecisionsHash: "35675473d190a449d58674fabfafa2265f3418bab49b4c6a928f0cdab41883dd",
		},
		17: {
			DecisionCount: 156,
			AnchorCount:   109,
			SourceSHA256:  "0733bc9b6918bd7afdd6a3f4d3055b17161c862a4bb8b1403b83f8e34272ec40",
			DecisionsHash: "b81a46068642d45bb1515066cf5236e0f83425e7aed35c2e2645bd1cbade3274",
		},
		18: {
			DecisionCount: 122,
			AnchorCount:   89,
			SourceSHA256:  "c6a4b5a06ae4f57e1e100d15ff75b1019ce0d8f5396016f02e89e0d260f8e24c",
			DecisionsHash: "8f0735df59a8740f0167a351bd7c2902c8cd3b6e5ff2dfb2e3eae4fff89046e3",
		},
		19: {
			DecisionCount: 147,
			AnchorCount:   110,
			SourceSHA256:  "c8ce0e995e59276535c4848a1d656839d2edcf5e3341e0cf7b6b25e69f3bb766",
			DecisionsHash: "c817db35739cbb6fcb4f55500748cc94c100b9df190ca13ac83deb5c9bcba4fe",
		},
		20: {
			DecisionCount: 115,
			AnchorCount:   82,
			SourceSHA256:  "6017117656d794cd25fd134b2bc39e8bbdb832e233da6131237467cc2a7ef964",
			DecisionsHash: "772cd9c5e02244d5cb53bd2d85e67ee7db8c52cd83aee369f596d5d1a27e9103",
		},
		21: {
			DecisionCount: 143,
			AnchorCount:   91,
			SourceSHA256:  "e085cbe464912a8c2f86c7dabb0443e10b14bd345f3b3eb031bd7bc11e254ab1",
			DecisionsHash: "18a703a42128200dd1fbdaa932d4ef07757f5c91182ae9e61c2ddd6a7be401cf",
		},
		22: {
			DecisionCount: 117,
			AnchorCount:   86,
			SourceSHA256:  "8dc9986d66d0428c0d725799f8a8eec1702c626a6c62488d71c9aa045863ad8d",
			DecisionsHash: "d7304053a7ba3d2a1ebc982af8d1694e71dc37c385d657036db0e1c2eeed90a8",
		},
		23: {
			DecisionCount: 136,
			AnchorCount:   85,
			SourceSHA256:  "19e8ad630ee4f081f424bd5e08d4a5328f77478a4ebefa2cb7e2588b9d6e16b2",
			DecisionsHash: "d54e1041cc37765c705eb2dfc1d26b0a39997beb02031a8b50054cad48fe1e78",
		},
		24: {
			DecisionCount: 97,
			AnchorCount:   73,
			SourceSHA256:  "a9b661a23b6f7704de5822c4cba87c4ea597758a86829e1e31907c5e9d7c7a49",
			DecisionsHash: "8789865104de722f1240b8f350d470e473431e50991100c372af7c588289d76b",
		},
		25: {
			DecisionCount: 98,
			AnchorCount:   71,
			SourceSHA256:  "a831ae6ffab0070d9c1a7cb7b74bd7f14295a67f45ca21f6a5ee5671aea255f5",
			DecisionsHash: "ae4f1faf733126968a4eb106e95c44a0a57d7c3b453a41e86cf5271837721560",
		},
		26: {
			DecisionCount: 74,
			AnchorCount:   49,
			SourceSHA256:  "f426fb741d9036ee9c231d6f9eac9321b384892e7ed095c8aeaef01beeb33bb4",
			DecisionsHash: "843f446ad4abcabf1dd6bebbfec116139da3e3df9544455c65196c7516c3c6a3",
		},
		27: {
			DecisionCount: 125,
			AnchorCount:   84,
			SourceSHA256:  "86b3b0bd010143b62b8b31ca0c93f3e975c4eb84c16f10e67c2467c9fb0e7a28",
			DecisionsHash: "080f82f65514cd4dee1ec54b167fef49979ce8a80c0b354e02705a23bd680412",
		},
		28: {
			DecisionCount: 60,
			AnchorCount:   42,
			SourceSHA256:  "55060844ff15cfd6607631d15592036ea999411910f03bb860448cd579312c49",
			DecisionsHash: "b43b0fc1da8c2fdbbf5fb3a46fc37e89545eb706d37c2d6717d8d800ce01b0f6",
		},
		29: {
			DecisionCount: 134,
			AnchorCount:   38,
			SourceSHA256:  "07f1abff2f83060330ac69e8968568f9cb33d5b9957d8cb9b1b571433cfab2e7",
			DecisionsHash: "8d904326f7364218ce53d367efe3df4bcb33d1fa00a786b74c1f19ce3ca9c1bb",
		},
		30: {
			DecisionCount: 172,
			AnchorCount:   78,
			SourceSHA256:  "7609f40a7b4fab82111824e57f0f09b9b7bf38cad752f4b0a2385cb2f4d421f3",
			DecisionsHash: "8a5509e2d81ae09174d49fb503c5d1c461e5ddb1033e01a194a4853cb34d84f0",
		},
		31: {
			DecisionCount: 208,
			AnchorCount:   52,
			SourceSHA256:  "223b253592fdcd2e25394b5179787e81237744394cca2363b4f2f2fc4c23e457",
			DecisionsHash: "f8f7aba4942a0d3d2f242bd7bae607c3ff18e6ac9aff26f7e30a95e8c19b9a53",
		},
		32: {
			DecisionCount: 178,
			AnchorCount:   98,
			SourceSHA256:  "b4cd24a219e5398171d1e24733a440f99d03760d84fa44ed28e496882e77a05d",
			DecisionsHash: "7ff9b3ef293db3e1759cffc34bc67b33cd9cd8a35cf8bb3542768055b19a5123",
		},
		33: {
			DecisionCount: 48,
			AnchorCount:   16,
			SourceSHA256:  "1023d23da8abaa33e9223e0457d9b20309b0eb77c5e8ef8b963217572bba1b2e",
			DecisionsHash: "d981e10b50d9d8f4cbd6ed52b190231a2dddb8512dc3070bf4015b3aac5671e7",
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
