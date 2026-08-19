package docclause

import (
	"fmt"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/docmarker"
)

func Materialize(document []byte, inputBlob string, regions []Region, registry Registry) ([]byte, error) {
	if strings.Contains(string(document), "<!-- partitur:mark") {
		if _, err := docmarker.Parse(string(document)); err != nil {
			return nil, fmt.Errorf("unmatched source marker token: %w", err)
		}
		return nil, fmt.Errorf("source already contains marker tokens")
	}
	if _, err := ClassificationDigest(document, inputBlob, regions, registry); err != nil {
		return nil, err
	}
	classifications, err := orderedClassifications(regions, registry)
	if err != nil {
		return nil, err
	}
	lines, err := physicalLines(document)
	if err != nil {
		return nil, err
	}
	var output strings.Builder
	for _, classification := range classifications {
		label := string(classification.Kind)
		if classification.Kind == ClassificationAnchor {
			label = "anchor=" + classification.MarkerID
		}
		fmt.Fprintf(&output, "<!-- partitur:mark begin %s -->\n", label)
		for _, line := range lines[classification.StartLine-1 : classification.EndLine] {
			output.WriteString(line)
			output.WriteByte('\n')
		}
		fmt.Fprintf(&output, "<!-- partitur:mark end %s -->\n", label)
	}
	materialized := []byte(output.String())
	if err := ValidateMaterialized(materialized, document, inputBlob, regions, registry); err != nil {
		return nil, err
	}
	return materialized, nil
}

func ValidateMaterialized(materialized, source []byte, inputBlob string, regions []Region, registry Registry) error {
	if _, err := ClassificationDigest(source, inputBlob, regions, registry); err != nil {
		return err
	}
	classifications, err := orderedClassifications(regions, registry)
	if err != nil {
		return err
	}
	ranges, err := docmarker.Parse(string(materialized))
	if err != nil {
		return fmt.Errorf("unmatched materialized marker token: %w", err)
	}
	if len(ranges) != len(classifications) {
		return fmt.Errorf("materialized ranges = %d, registry classifications = %d", len(ranges), len(classifications))
	}
	lines, err := physicalLines(source)
	if err != nil {
		return err
	}
	for index, classification := range classifications {
		label := string(classification.Kind)
		if classification.Kind == ClassificationAnchor {
			label = "anchor=" + classification.MarkerID
		}
		if ranges[index].Classification != label {
			return fmt.Errorf("materialized range %d classification %q does not match registry %q", index+1, ranges[index].Classification, label)
		}
		wantContents := strings.Join(lines[classification.StartLine-1:classification.EndLine], "\n")
		if ranges[index].Contents != "\n"+wantContents+"\n" {
			return fmt.Errorf("materialized range %d source bytes do not match registry", index+1)
		}
	}
	return nil
}
