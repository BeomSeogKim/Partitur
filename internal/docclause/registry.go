package docclause

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var markerIDPattern = regexp.MustCompile(`^[A-Za-z0-9]+(?:[._/-][A-Za-z0-9]+)*$`)

type ClassificationKind string

const (
	ClassificationAnchor       ClassificationKind = "anchor"
	ClassificationNonNormative ClassificationKind = "non-normative"
)

type Classification struct {
	// Source byte offsets are zero-based and half-open: [StartByte, EndByte).
	StartByte int                `json:"start_source_byte"`
	EndByte   int                `json:"end_source_byte"`
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
	starts := make(map[string]int, len(regions))
	startByte := 0
	for _, region := range regions {
		key := regionKey(region.Key)
		want[key] = region
		starts[key] = startByte
		startByte += len(regionBytes(region))
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
		if err := validateRegionDecisions(region, starts[key], receipt.Review.Decisions); err != nil {
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
	startByte := 0
	for _, region := range regions {
		receipt, ok := receipts[regionKey(region.Key)]
		if !ok || receipt.Review == nil || receipt.Review.SourceSHA256 != SourceDigest(region.Lines) || validateRegionDecisions(region, startByte, receipt.Review.Decisions) != nil {
			pending = append(pending, region.Key)
		}
		startByte += len(regionBytes(region))
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
		if all[left].StartByte == all[right].StartByte {
			return all[left].EndByte < all[right].EndByte
		}
		return all[left].StartByte < all[right].StartByte
	})
	merged := make([]Classification, 0, len(all))
	for _, classification := range all {
		if len(merged) != 0 {
			last := &merged[len(merged)-1]
			if last.EndByte == classification.StartByte && last.Kind == classification.Kind && last.MarkerID == classification.MarkerID {
				last.EndByte = classification.EndByte
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

func validateRegionDecisions(region Region, regionStart int, decisions []Classification) error {
	if len(decisions) == 0 {
		return fmt.Errorf("confirmed decisions are empty")
	}
	ordered := append([]Classification(nil), decisions...)
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].StartByte == ordered[right].StartByte {
			return ordered[left].EndByte < ordered[right].EndByte
		}
		return ordered[left].StartByte < ordered[right].StartByte
	})
	contents := regionBytes(region)
	regionEnd := regionStart + len(contents)
	coverage := make([]uint8, len(contents))
	for _, decision := range ordered {
		if decision.StartByte < regionStart || decision.EndByte <= decision.StartByte || decision.EndByte > regionEnd {
			return fmt.Errorf("classification has invalid byte range [%d,%d)", decision.StartByte, decision.EndByte)
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
		selected := contents[decision.StartByte-regionStart : decision.EndByte-regionStart]
		if len(bytes.Trim(selected, " \t\r\n")) == 0 {
			return fmt.Errorf("classification byte range [%d,%d) has no payload", decision.StartByte, decision.EndByte)
		}
		for sourceByte := decision.StartByte; sourceByte < decision.EndByte; sourceByte++ {
			offset := sourceByte - regionStart
			if coverage[offset] != 0 {
				return fmt.Errorf("classification overlap at source byte %d", sourceByte)
			}
			coverage[offset]++
		}
	}
	for offset, value := range contents {
		if !asciiWhitespace(value) && coverage[offset] == 0 {
			return fmt.Errorf("classification leaves payload source byte %d uncovered", regionStart+offset)
		}
	}
	return nil
}

func regionBytes(region Region) []byte {
	return []byte(strings.Join(region.Lines, "\n") + "\n")
}

func asciiWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func regionKey(key RegionKey) string {
	return fmt.Sprintf("%s/%d/%d-%d", key.InputBlob, key.Ordinal, key.StartLine, key.EndLine)
}

func markerID(value string) bool {
	return markerIDPattern.MatchString(value)
}
