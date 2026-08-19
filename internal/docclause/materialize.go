package docclause

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/docmarker"
)

var materializedMarkerPattern = regexp.MustCompile(docmarker.Production)

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
	var output bytes.Buffer
	cursor := 0
	for _, classification := range classifications {
		label := string(classification.Kind)
		if classification.Kind == ClassificationAnchor {
			label = "anchor=" + classification.MarkerID
		}
		output.Write(document[cursor:classification.StartByte])
		fmt.Fprintf(&output, "<!-- partitur:mark begin %s -->", label)
		output.Write(document[classification.StartByte:classification.EndByte])
		fmt.Fprintf(&output, "<!-- partitur:mark end %s -->", label)
		cursor = classification.EndByte
	}
	output.Write(document[cursor:])
	materialized := output.Bytes()
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
	for index, classification := range classifications {
		label := string(classification.Kind)
		if classification.Kind == ClassificationAnchor {
			label = "anchor=" + classification.MarkerID
		}
		if ranges[index].Classification != label {
			return fmt.Errorf("materialized range %d classification %q does not match registry %q", index+1, ranges[index].Classification, label)
		}
		wantContents := string(source[classification.StartByte:classification.EndByte])
		if ranges[index].Contents != wantContents {
			return fmt.Errorf("materialized range %d source bytes do not match registry", index+1)
		}
	}
	if unmarked := materializedMarkerPattern.ReplaceAll(materialized, nil); !bytes.Equal(unmarked, source) {
		return fmt.Errorf("materialized payload bytes do not match source")
	}
	return nil
}
