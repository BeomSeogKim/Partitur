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
	confirmedBaselineMarkedBlob                  = "cff3cce23748a40243617374796fe1724b88919f"
	confirmedBaselineOrderedClassificationSHA256 = "9a63a30b34170434e53471896994c51fc76638bada8ce07c3c406af6ba0e5441"
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
			DecisionCount: 195,
			AnchorCount:   133,
			SourceSHA256:  "bcf4b1edd3e697f63bb6888de4fbd3d9bd2b8fe0a1c3d2b8f0385a49cab8502c",
			DecisionsHash: "168a8a63f5dc545be513910ca756f59e2fe7b4ed835e5e49c721d1f6fd1cefad",
		},
		10: {
			DecisionCount: 162,
			AnchorCount:   115,
			SourceSHA256:  "3430ea5bbfc9cc661b46dc528b8e82a3ad9ce65dcb6347a4d6ab7237b54e0d84",
			DecisionsHash: "adf3eb53e2de1abc38f55893d9bdcb2186500230a1c12a66c65953a4e863dc3d",
		},
		11: {
			DecisionCount: 117,
			AnchorCount:   82,
			SourceSHA256:  "b7f7cf66ed3643db387756a79468f720e4f7672afef52600c0dc1609eeb47d44",
			DecisionsHash: "7692305bf19febf641f130d580604783ee7cac849ebed933e11ee082534adf0b",
		},
		12: {
			DecisionCount: 102,
			AnchorCount:   80,
			SourceSHA256:  "cf98d572017c536ecbe5ad4bfa187ca44ca25626fb871ff9172a96ec34aa0e80",
			DecisionsHash: "0705bddfc18f5fd0ef064da5d5497a4444672086c8310a48c81976cd0c66f63c",
		},
		13: {
			DecisionCount: 128,
			AnchorCount:   69,
			SourceSHA256:  "9447aefda52a71f689f60ffa4f468de9b1d0bfcdde5ca459b050c8fb32af76bf",
			DecisionsHash: "3efe40c1dc1aeaacf9256ef9d5414dffceecaba0f00bb9eed805928a7ef23b92",
		},
		14: {
			DecisionCount: 130,
			AnchorCount:   89,
			SourceSHA256:  "f75165ba4df18dc21462bde678c9a20212c7d8a48f3e4584ea322f817301c46d",
			DecisionsHash: "7be602cd417289191b80a0a5e9d2d10a863e2e3028b7f038e0f5137a370098a9",
		},
		15: {
			DecisionCount: 137,
			AnchorCount:   103,
			SourceSHA256:  "30c471510d85adf6be2259914e08b9a2a2ebd74d639deac36a058727ee7714c3",
			DecisionsHash: "6c686bc4644e5fad6fbd5f44cc4fc0a04699fca19fd003cfd4536a25d82793b8",
		},
		16: {
			DecisionCount: 111,
			AnchorCount:   88,
			SourceSHA256:  "36ef053fe452f3b20620be58b6ce26e56ef8b8f9e1bc7c02a056b609b1b54962",
			DecisionsHash: "00d3dbb2beef36caba523b0d786a16d4d54ecf8893711b91ab2f12407eec273c",
		},
		17: {
			DecisionCount: 156,
			AnchorCount:   108,
			SourceSHA256:  "496be404f45024a99ec3a4c87b5de7d0c8977615130290ec7200fef6f4b81514",
			DecisionsHash: "57a23377d4551bd0b8a648f87230d002b6fbc42c23fb1e04898a3760701751b2",
		},
		18: {
			DecisionCount: 125,
			AnchorCount:   91,
			SourceSHA256:  "d7d729d8625d9f67a9d543d7aa443c9650f406b150f3e98534e7796bcb333769",
			DecisionsHash: "bc541babccdb3e36f2f55725c470641b0b02c200193dc31bfe032a05004f4a46",
		},
		19: {
			DecisionCount: 142,
			AnchorCount:   107,
			SourceSHA256:  "2013e5aad0b3e3f61d8c22e194d25a70f72e9880c2c09409764de4bbe1adffaa",
			DecisionsHash: "32e85dca5061fe8ba3899df522e83351e0004a9ea48f318301b1874a1a35e068",
		},
		20: {
			DecisionCount: 113,
			AnchorCount:   80,
			SourceSHA256:  "6239c6e1e00064bf90ec452c06c24b333c2c22b9ae3281625cb1b46cb00e9406",
			DecisionsHash: "97fc18ba1603f8e1794551c633af84344b0e4aaa5b72e3b0c554e62007146155",
		},
		21: {
			DecisionCount: 150,
			AnchorCount:   98,
			SourceSHA256:  "8e37d13f2284124675e235b7dd215ef481a74e723c2bf8b0163374aba348ecd2",
			DecisionsHash: "0dbaa7490905111f1443edb1d0ee69b3883d07741112ab3f61381d48ecb61395",
		},
		22: {
			DecisionCount: 114,
			AnchorCount:   81,
			SourceSHA256:  "fb5974f67be2bfcabb6f013b304eb1e4eada6db52e98f4222d2f73a0e838135c",
			DecisionsHash: "0c03d242fdf8bd6f7f8e03c7f77e57cf471b4d109aea800c0a34b0f6a6c20d58",
		},
		23: {
			DecisionCount: 138,
			AnchorCount:   89,
			SourceSHA256:  "367d8e04c003872d3a59c2b23d91a1d7ec8691f7da612c5d1524dd32fdb4f551",
			DecisionsHash: "0bf1b5ee3151731ec3c92c8da95e3d09a7261a2edc7187f15e9ce5232c6ad70f",
		},
		24: {
			DecisionCount: 95,
			AnchorCount:   71,
			SourceSHA256:  "02cfcb5b8c551772334e711aa85b4e9f582aa942d73572aa9e10472f31b6d54f",
			DecisionsHash: "04478b0e548d83cc950d4fa504176c32774eb37c40a65e645aa8253f98040d5d",
		},
		25: {
			DecisionCount: 104,
			AnchorCount:   73,
			SourceSHA256:  "880ff428879bf52643988c17bc7aa5bf0db05f75be157d4f35a775a3cd7b2444",
			DecisionsHash: "f4f512bb528418c6ead187fe7457baf79bc96bb2bb6c796544ea33054736255c",
		},
		26: {
			DecisionCount: 80,
			AnchorCount:   52,
			SourceSHA256:  "7b1e748c478c19de1ea5a35f67c57797174490ac45ed83c77f627d0801495ab4",
			DecisionsHash: "690512e93f0b46632671d476f507a55d288559cd8172f1e9956a1ad972b3bbbe",
		},
		27: {
			DecisionCount: 118,
			AnchorCount:   76,
			SourceSHA256:  "9bd4224aed475a2724b936aded7d247f4bba762b3f1d9ceeb2dc341be313703f",
			DecisionsHash: "86c9154ff7af895e9ff5ceed270dc018ac4bf478cb854b379545b9724dd6be9e",
		},
		28: {
			DecisionCount: 47,
			AnchorCount:   36,
			SourceSHA256:  "92d86bac58b776d5d49d12538bc5daa5ddb9f7e69146a517a68ed207efb2dcbf",
			DecisionsHash: "d32ecf9cdb737d116259f2c11b5dbef65c3806802f44afc9db86764cc277c8bf",
		},
		29: {
			DecisionCount: 153,
			AnchorCount:   37,
			SourceSHA256:  "4e9a2009e58fff56cec276ed9f7863803a91a0f2cb6ddd348c0ff4fe7df0e456",
			DecisionsHash: "13df5a94a3af48139b47070a844cdca9e0d9a3a203cdbe3354c52ed330d84e95",
		},
		30: {
			DecisionCount: 177,
			AnchorCount:   76,
			SourceSHA256:  "b72e8dfba5bf851e9684d0d5e12ed85167c259f6c24b304f0513066936148675",
			DecisionsHash: "c487158864ed846b0e008de8629b845730ef235755276cc05a76cb51052e4dea",
		},
		31: {
			DecisionCount: 205,
			AnchorCount:   66,
			SourceSHA256:  "0c6da2f031c9db9278eedaf2c3cf33c860b735d24f1e625d3071db7ba0e4fc06",
			DecisionsHash: "c7490b87c5ddb90f23ddecadaece96e0efa9d6e8659dd3cbf7fa24537a94803c",
		},
		32: {
			DecisionCount: 175,
			AnchorCount:   84,
			SourceSHA256:  "2f62c89a70c980f7519ef22d3c26a45517187066f1d3dcb93933670cc46f2cbf",
			DecisionsHash: "5693323663fe78619375216879af07e51f34dd4a09eafacd0b34a475b151f9b3",
		},
		33: {
			DecisionCount: 26,
			AnchorCount:   15,
			SourceSHA256:  "03d2df068b1c239503e958b783718727fd6c93a4f2bae0e3d0ab1aaeede22fdc",
			DecisionsHash: "f62e29f180b0a59ba5422846b853967891ffd71bdf41a26750de021a52a8eda2",
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
