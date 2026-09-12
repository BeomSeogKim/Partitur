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
	confirmedBaselineMarkedBlob                  = "8b64c256e301ec97c1e29da19feb62c13ff2592d"
	confirmedBaselineOrderedClassificationSHA256 = "c3de2e8d5548a06a2fbf36d2bbc6b9447697f54d83b8f3d11903a9be126225aa"
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
			DecisionCount: 118,
			AnchorCount:   82,
			SourceSHA256:  "cc64878c6a1b261d832d1646032f570c4d4999a679183a0b4461ab35977b93ca",
			DecisionsHash: "0a57ff6fb8bacb198cbdea8afb8b934d773b8ea3c594d903c4d9ae50f7aa89ca",
		},
		12: {
			DecisionCount: 103,
			AnchorCount:   82,
			SourceSHA256:  "f146d682f6be08986249c8f2e740a5e1dfea62bff136d25334cf994972abc5fc",
			DecisionsHash: "c330ca4a2997dd6bda7a0a2e377dfca9d4bf8eaed5d3bbf5e4e567c15786d2c9",
		},
		13: {
			DecisionCount: 127,
			AnchorCount:   69,
			SourceSHA256:  "cd0c7db125b2decdb4765e506daaf20ab0b015f19c4aa60af5a5744cb8f99475",
			DecisionsHash: "0614422cc02c995595ef4dba710b440f148fe0f6d53f92ffdd5207560c79613a",
		},
		14: {
			DecisionCount: 133,
			AnchorCount:   91,
			SourceSHA256:  "8bc40e46d27dae5e53d698dfa37daa43a8a80010795377a64c0270dc5e9f3072",
			DecisionsHash: "b6e50a860c7963466f943434e25aaab0911d3848ff7a58f0be6ededd92fd253d",
		},
		15: {
			DecisionCount: 135,
			AnchorCount:   101,
			SourceSHA256:  "491ad7c485f46a2dc6ab1fb29cbbc9879c1b26da11887126a9555a3ff8be7dbf",
			DecisionsHash: "701550513b9cd389443de0f0a486e918cc172e4b386299612f8a4e7db21b1c73",
		},
		16: {
			DecisionCount: 112,
			AnchorCount:   88,
			SourceSHA256:  "bab9f8945fce7118cd37dcd02a96bc6f619770e8bf6e92fead70990e5d3cda46",
			DecisionsHash: "26fcaa5ed36aace893e2b6c9bd245eed6164f441c75478beca7429a7f90fc641",
		},
		17: {
			DecisionCount: 156,
			AnchorCount:   108,
			SourceSHA256:  "186575acbd5be65a8cc2ed2a1b0419ab9261c49f351bbefd63c4aeff633c39e9",
			DecisionsHash: "38f38887dea876197d7ad1efbb2daea0d1e998861e36bb4dbc58570325fcab13",
		},
		18: {
			DecisionCount: 124,
			AnchorCount:   90,
			SourceSHA256:  "43298e238b16234b55a21c84f83ed3445e8e868bdb95a509c3eeadf7012acfc2",
			DecisionsHash: "0cf21172035a2fab7e3971ab240ab9406efc3f1ca8d7dc51eb590163d52ae105",
		},
		19: {
			DecisionCount: 145,
			AnchorCount:   110,
			SourceSHA256:  "c50c2badf651c02993a0e889bb31e8cf2de72210f7ef9d5c9a741e851207f3b5",
			DecisionsHash: "ad3a1b230628405316ac56c755056b3a8ab281c0568034ba0697012f9d468fc1",
		},
		20: {
			DecisionCount: 114,
			AnchorCount:   81,
			SourceSHA256:  "a0016b9d2da41359686bf22ecf87291daf73724b6d23174f7104547f3768c107",
			DecisionsHash: "51870414f4c0a10ca29d23ac474cdd038ca5bafe79af114c9850fec74e756eb1",
		},
		21: {
			DecisionCount: 147,
			AnchorCount:   95,
			SourceSHA256:  "c9573a10d5a86648bd02f863e4e0b724502527683632bcf9af28ae4f65e5247d",
			DecisionsHash: "c2f018dc8ee532ca83e6224e4cdefe3cceca3021116d4ed8d43ff7e66c5c2470",
		},
		22: {
			DecisionCount: 114,
			AnchorCount:   82,
			SourceSHA256:  "7800cc656c444fec15dec9197d9d1edb55e1edc1d6f8ec8d8c2855cce5dd3bdf",
			DecisionsHash: "801231352eaafc31a14e74e77ed3d7d0c1211d81658a244f75398355c0a2c6f7",
		},
		23: {
			DecisionCount: 139,
			AnchorCount:   88,
			SourceSHA256:  "4fd4112681c80ffae1a3b980dc6b8a874eb6a64e92c0d373b49f0176f2a1c8a3",
			DecisionsHash: "99c0486b9f88b47bcc47889feb7c7b0694e21c58634ce41d1f54bc4194b8ae0a",
		},
		24: {
			DecisionCount: 95,
			AnchorCount:   71,
			SourceSHA256:  "c7bdd5f7e25cb8e60e9cb75ee68f1801722b176fe0313b5dce8c5f7483b33daa",
			DecisionsHash: "25f05ed87a525c8bfb5dd5baf5d973905f9e83a3a71a5a9a34ee6a812b25a9f8",
		},
		25: {
			DecisionCount: 103,
			AnchorCount:   72,
			SourceSHA256:  "fb9d0c59cdc1f9e6d082af62a635921d244d4ebea325b17c3b1ab5a231c1c2d5",
			DecisionsHash: "97cabb68eb19d8f353f9f618a4a28df18dab08ea61dc54325652c18cea067cb7",
		},
		26: {
			DecisionCount: 80,
			AnchorCount:   53,
			SourceSHA256:  "a5a9de1e1d680644ac187c13a1d5b41c2379b5d4eaabdb224b60b3a54535fb22",
			DecisionsHash: "4f1bc46f102333c41d8da44f1a6c0900a4b8d6aaa6f1d9bc6bd4a1250775fb0a",
		},
		27: {
			DecisionCount: 117,
			AnchorCount:   77,
			SourceSHA256:  "0b5af6fda4815af9ddb3978ff9e9b70b7c05427910f2bc262e93b6ded9e3c8d7",
			DecisionsHash: "fc8d7be11b4792e78d07939fad344feeaf3d167785e148c527418b939aa69bf0",
		},
		28: {
			DecisionCount: 50,
			AnchorCount:   36,
			SourceSHA256:  "da76ac231669a0cb90b8beebf010b3a4022e7dfcac2a8252bb310c4a11c28d9d",
			DecisionsHash: "93b3546849b3cdc23506c055fd1311d68fe17b1669dbb79e1886783c8b66db63",
		},
		29: {
			DecisionCount: 151,
			AnchorCount:   38,
			SourceSHA256:  "901d0d3e020d815db19d6b07997fc518e50700184ca31d2f8ae4663170f39b4c",
			DecisionsHash: "f4b6ff24786cd8e16662895770410b9612369128a5158d7cd7ddab6d3ddb09d4",
		},
		30: {
			DecisionCount: 176,
			AnchorCount:   76,
			SourceSHA256:  "66a6f169b9ff77cf55bfdb6d49699ad39f85fa2e195aa273ed5da10ffeed3714",
			DecisionsHash: "a2bf935eb0bf6da9f860095d50b72285971d6e386f6d244e769cecb3c7df5a9e",
		},
		31: {
			DecisionCount: 207,
			AnchorCount:   65,
			SourceSHA256:  "7e384515350ed1f11fd8a4612284dfc28147c1b425c91a2cb69a565229d4604c",
			DecisionsHash: "512abb39d868a8ff8f6927094ad47bcaeb306151e0f8ace385554e27e1ebd9d2",
		},
		32: {
			DecisionCount: 174,
			AnchorCount:   85,
			SourceSHA256:  "44fef9fbe90f7ab46b707327200f6d2a1ddddeadc72394fd0627eccf974c8d9c",
			DecisionsHash: "867dc546caf56561821d2986d7d81690edeb04bc3d3c4ef7f24f4e351a5b5730",
		},
		33: {
			DecisionCount: 28,
			AnchorCount:   15,
			SourceSHA256:  "a209d0c8d64dc0eed7445e9ebc7b2b83361df8b56bcad5ceb069ba0aa4a587b9",
			DecisionsHash: "19669d007be0996e5174c488a7244723b6b07d0bcfc44193789e57cfb5a7c1de",
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
