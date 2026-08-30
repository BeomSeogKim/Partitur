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

func TestDesignStagingLedgerPinsCurrentUniverseAndRemainsUnactivated(t *testing.T) {
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
			DecisionCount: 129,
			AnchorCount:   88,
			SourceSHA256:  "2d1c1e7ec2ca8b7c75383c9773f8c3cfb643a13166a5a945a1af1818e3d7d0a5",
			DecisionsHash: "9e0162dfca95a452c35892bf81c88e6333705f7af02a77ad53655543a36ecaa4",
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
			DecisionCount: 129,
			AnchorCount:   95,
			SourceSHA256:  "872ceaf618cf00e7b1ac09af840a07c664528477311bdd6749046792755c0533",
			DecisionsHash: "8405a2bccf501982ae8871db7decc3b248a56d32316fac7b3496d078f1e7683c",
		},
		19: {
			DecisionCount: 141,
			AnchorCount:   103,
			SourceSHA256:  "1bcc351ef64500de9070a95a537b60829efbad2ce06748f876571c2672886981",
			DecisionsHash: "02073409fc07e747d019e59f51bb9133c760e3db20d739e8e84e086504baf5fc",
		},
		20: {
			DecisionCount: 112,
			AnchorCount:   81,
			SourceSHA256:  "df720226b9728d3dbe14b3239e2790c21723dc2909bff11169810ed982aeb48f",
			DecisionsHash: "e36030f71e4daecf05e89ec88f3b5113e5ad87ab469add29bf648d085b531f23",
		},
		21: {
			DecisionCount: 151,
			AnchorCount:   100,
			SourceSHA256:  "010781ed13d0f0fa138e8d2faee0a1275bd6e034ab32d783460673c682f4554f",
			DecisionsHash: "b7e41e13b145e13a2f9048ac56454e20f10b0cb83068c6150b1991c204f93287",
		},
		22: {
			DecisionCount: 113,
			AnchorCount:   80,
			SourceSHA256:  "ee6b1ef305efcee886c19b229879dc249fda40e53a2ee7ebcda0bd9ebe607c76",
			DecisionsHash: "7f24c9085bf8e09166e1c68c00d0acc11cdd2e64c50189d6228ad2d1c0b8aed1",
		},
		23: {
			DecisionCount: 139,
			AnchorCount:   90,
			SourceSHA256:  "b8ec3aa302a48cb5a896d6a09baff7c36f61a5511f8a80e9cdf3c6ffa9e487ad",
			DecisionsHash: "7407095dde03ae42093a3d4f7b3202504157071d7d07cd98b9920fbdb38c11ee",
		},
		24: {
			DecisionCount: 92,
			AnchorCount:   68,
			SourceSHA256:  "eca0143ea2ab1f72723adb3b064173717bcfc1fde501b309e0a7291e983d6b98",
			DecisionsHash: "54f89c1adce2d23aa5d37682e36dbfbc3abcf83893a617fdd1a07c6751756af7",
		},
		25: {
			DecisionCount: 108,
			AnchorCount:   77,
			SourceSHA256:  "4bd08b02201cee12af40a61673582b329b85c69090f5c1935a1562bc52b97f93",
			DecisionsHash: "3a93514b66ea891750426296ab922e0e4162275889e40f7427b787cb9ad40b75",
		},
		26: {
			DecisionCount: 79,
			AnchorCount:   48,
			SourceSHA256:  "ff4b0557f6bcc3f2f96db75bc4702b801a925255382254824696a7e53f5ace4d",
			DecisionsHash: "25b8778ee16e2673cfae6c26e1806a9fc37ddf0109f59af3c7bef98611bb3f08",
		},
	}
	if err := validateStagingReviewProgress(regions, registry, reviewedOrdinals); err != nil {
		t.Fatal(err)
	}
	if err := validateConfirmedPacketPins(registry, reviewedOrdinals); err != nil {
		t.Fatal(err)
	}
	if len(regions) <= len(reviewedOrdinals) {
		t.Fatal("review-progress mutation requires an unreviewed region")
	}

	var unreviewedRegion Region
	unreviewedRegionStart := 0
	for _, region := range regions {
		if _, admitted := reviewedOrdinals[region.Key.Ordinal]; !admitted {
			unreviewedRegion = region
			break
		}
		unreviewedRegionStart += len(regionBytes(region))
	}
	mutations := []struct {
		name   string
		mutate func(*testing.T, *Registry)
		want   string
	}{
		{
			name: "receipt for unreviewed ordinal",
			mutate: func(_ *testing.T, got *Registry) {
				got.Receipts = append(got.Receipts, RegionReceipt{
					Key: unreviewedRegion.Key,
					Review: &ReviewReceipt{
						SourceSHA256: SourceDigest(unreviewedRegion.Lines),
						Decisions: []Classification{{
							StartByte: unreviewedRegionStart,
							EndByte:   unreviewedRegionStart + len(regionBytes(unreviewedRegion)),
							Kind:      ClassificationNonNormative,
						}},
					},
				})
			},
			want: fmt.Sprintf("packet %d has a receipt but is not admitted", unreviewedRegion.Key.Ordinal),
		},
		{
			name: "reviewed ordinal receipt absent",
			mutate: func(_ *testing.T, got *Registry) {
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
			mutate: func(t *testing.T, got *Registry) {
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
			mutate: func(_ *testing.T, got *Registry) {
				got.Activation = &ActivationPins{}
			},
			want: "baseline activation must remain absent while staging regions are pending",
		},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			got := cloneRegistry(registry)
			mutation.mutate(t, &got)
			if err := ValidateRegistry(documentPath, document, commandBlob, regions, got); err != nil {
				t.Fatalf("injected registry rejected before review-progress check: %v", err)
			}
			err := validateStagingReviewProgress(regions, got, reviewedOrdinals)
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
