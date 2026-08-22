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
			AnchorCount:   41,
			SourceSHA256:  "5d54f0e45bf29eadb66223707c20ba8189ec3772b2b053bc0c7963048abfeaff",
			DecisionsHash: "b22ebf65cf0bad642f7c1837ffaa43c61741afbe5f5050aea32404e0f7a56ef7",
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
