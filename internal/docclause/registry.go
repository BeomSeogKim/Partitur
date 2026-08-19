package docclause

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
)

var markerIDPattern = regexp.MustCompile(`^[A-Za-z0-9]+(?:[._/-][A-Za-z0-9]+)*$`)

type ClassificationKind string

const (
	ClassificationAnchor       ClassificationKind = "anchor"
	ClassificationNonNormative ClassificationKind = "non-normative"
)

type Classification struct {
	StartLine int                `json:"start_source_line"`
	EndLine   int                `json:"end_source_line"`
	Kind      ClassificationKind `json:"kind"`
	MarkerID  string             `json:"marker_id,omitempty"`
}

type ReviewReceipt struct {
	SourceSHA256 string           `json:"source_sha256"`
	Decisions    []Classification `json:"confirmed_decisions"`
}

type RegionReceipt struct {
	Key      RegionKey      `json:"key"`
	Proposed Proposal       `json:"machine_proposals"`
	Review   *ReviewReceipt `json:"review_receipt,omitempty"`
}

type Registry struct {
	Receipts []RegionReceipt `json:"region_receipts"`
}

// StagingRegistry is deliberately empty until the human classification pass.
var StagingRegistry = Registry{}

func ValidateRegistry(document []byte, inputBlob string, regions []Region, registry Registry) error {
	if err := ValidateUniverse(document, inputBlob, regions); err != nil {
		return err
	}
	want := make(map[string]Region, len(regions))
	for _, region := range regions {
		want[regionKey(region.Key)] = region
	}
	seen := make(map[string]bool)
	for _, receipt := range registry.Receipts {
		key := regionKey(receipt.Key)
		if seen[key] {
			return fmt.Errorf("duplicate region receipt %s", key)
		}
		seen[key] = true
		region, ok := want[key]
		if !ok {
			return fmt.Errorf("region receipt %s does not match immutable universe", key)
		}
		if receipt.Review == nil {
			continue
		}
		if receipt.Review.SourceSHA256 != SourceDigest(region.Lines) {
			continue
		}
		if err := validateRegionDecisions(region, receipt.Review.Decisions); err != nil {
			return fmt.Errorf("region %d review: %w", region.Key.Ordinal, err)
		}
	}
	return nil
}

func Pending(regions []Region, registry Registry) []RegionKey {
	receipts := make(map[string]RegionReceipt, len(registry.Receipts))
	for _, receipt := range registry.Receipts {
		receipts[regionKey(receipt.Key)] = receipt
	}
	var pending []RegionKey
	for _, region := range regions {
		receipt, ok := receipts[regionKey(region.Key)]
		if !ok || receipt.Review == nil || receipt.Review.SourceSHA256 != SourceDigest(region.Lines) || validateRegionDecisions(region, receipt.Review.Decisions) != nil {
			pending = append(pending, region.Key)
		}
	}
	return pending
}

func ClassificationDigest(document []byte, inputBlob string, regions []Region, registry Registry) (string, error) {
	if err := ValidateRegistry(document, inputBlob, regions, registry); err != nil {
		return "", err
	}
	if pending := Pending(regions, registry); len(pending) != 0 {
		return "", fmt.Errorf("classification has %d pending regions", len(pending))
	}
	classifications, err := orderedClassifications(regions, registry)
	if err != nil {
		return "", err
	}
	canonical := struct {
		InputBlob       string           `json:"input_blob"`
		Classifications []Classification `json:"ordered_classifications"`
	}{InputBlob: inputBlob, Classifications: classifications}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func orderedClassifications(regions []Region, registry Registry) ([]Classification, error) {
	receipts := make(map[string]RegionReceipt, len(registry.Receipts))
	for _, receipt := range registry.Receipts {
		receipts[regionKey(receipt.Key)] = receipt
	}
	var all []Classification
	for _, region := range regions {
		all = append(all, receipts[regionKey(region.Key)].Review.Decisions...)
	}
	sort.Slice(all, func(left, right int) bool {
		if all[left].StartLine == all[right].StartLine {
			return all[left].EndLine < all[right].EndLine
		}
		return all[left].StartLine < all[right].StartLine
	})
	merged := make([]Classification, 0, len(all))
	for _, classification := range all {
		if len(merged) != 0 {
			last := &merged[len(merged)-1]
			if last.EndLine+1 == classification.StartLine && last.Kind == classification.Kind && last.MarkerID == classification.MarkerID {
				last.EndLine = classification.EndLine
				continue
			}
		}
		merged = append(merged, classification)
	}
	seenAnchors := make(map[string]bool)
	for _, classification := range merged {
		if classification.Kind == ClassificationAnchor {
			if seenAnchors[classification.MarkerID] {
				return nil, fmt.Errorf("duplicate anchor classification %q", classification.MarkerID)
			}
			seenAnchors[classification.MarkerID] = true
		}
	}
	return merged, nil
}

func validateRegionDecisions(region Region, decisions []Classification) error {
	if len(decisions) == 0 {
		return fmt.Errorf("confirmed decisions are empty")
	}
	ordered := append([]Classification(nil), decisions...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].StartLine < ordered[right].StartLine })
	want := region.Key.StartLine
	for _, decision := range ordered {
		if decision.StartLine != want {
			return fmt.Errorf("classification starts at %d, want %d (gap or overlap)", decision.StartLine, want)
		}
		if decision.EndLine < decision.StartLine || decision.EndLine > region.Key.EndLine {
			return fmt.Errorf("classification has invalid range %d-%d", decision.StartLine, decision.EndLine)
		}
		switch decision.Kind {
		case ClassificationAnchor:
			if !markerID(decision.MarkerID) {
				return fmt.Errorf("anchor classification has invalid marker ID %q", decision.MarkerID)
			}
		case ClassificationNonNormative:
			if decision.MarkerID != "" {
				return fmt.Errorf("non-normative classification carries marker ID %q", decision.MarkerID)
			}
		default:
			return fmt.Errorf("unknown classification kind %q", decision.Kind)
		}
		want = decision.EndLine + 1
	}
	if want != region.Key.EndLine+1 {
		return fmt.Errorf("classification ends at %d, want %d", want-1, region.Key.EndLine)
	}
	return nil
}

func regionKey(key RegionKey) string {
	return fmt.Sprintf("%s/%d/%d-%d", key.InputBlob, key.Ordinal, key.StartLine, key.EndLine)
}

func markerID(value string) bool {
	return markerIDPattern.MatchString(value)
}
