package docclause

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const NonblankLinesPerRegion = 200

type RegionKey struct {
	InputBlob string `json:"input_blob"`
	Ordinal   int    `json:"ordinal"`
	StartLine int    `json:"start_source_line"`
	EndLine   int    `json:"end_source_line"`
}

type Region struct {
	Key   RegionKey
	Lines []string
}

func GenerateRegions(document []byte, inputBlob string) ([]Region, error) {
	if inputBlob == "" {
		return nil, fmt.Errorf("input blob is empty")
	}
	lines, err := physicalLines(document)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("document has no physical lines")
	}

	var regions []Region
	start := 0
	nonblank := 0
	for index, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonblank++
		}
		if nonblank == NonblankLinesPerRegion {
			regions = append(regions, makeRegion(inputBlob, len(regions)+1, start, index, lines))
			start = index + 1
			nonblank = 0
		}
	}
	if start < len(lines) {
		regions = append(regions, makeRegion(inputBlob, len(regions)+1, start, len(lines)-1, lines))
	}
	if err := ValidateUniverse(document, inputBlob, regions); err != nil {
		return nil, err
	}
	return regions, nil
}

func ValidateUniverse(document []byte, inputBlob string, regions []Region) error {
	lines, err := physicalLines(document)
	if err != nil {
		return err
	}
	if len(regions) == 0 {
		return fmt.Errorf("region universe is empty")
	}
	expectedStart := 1
	for index, region := range regions {
		if region.Key.InputBlob != inputBlob {
			return fmt.Errorf("region %d input blob %q does not match %q", index+1, region.Key.InputBlob, inputBlob)
		}
		if region.Key.Ordinal != index+1 {
			return fmt.Errorf("region ordinal %d, want %d", region.Key.Ordinal, index+1)
		}
		if region.Key.StartLine != expectedStart {
			return fmt.Errorf("region %d starts at %d, want %d (gap or overlap)", index+1, region.Key.StartLine, expectedStart)
		}
		if region.Key.EndLine < region.Key.StartLine || region.Key.EndLine > len(lines) {
			return fmt.Errorf("region %d has invalid line range %d-%d", index+1, region.Key.StartLine, region.Key.EndLine)
		}
		wantLines := lines[region.Key.StartLine-1 : region.Key.EndLine]
		if strings.Join(region.Lines, "\n") != strings.Join(wantLines, "\n") {
			return fmt.Errorf("region %d source bytes do not match its line range", index+1)
		}
		nonblank := 0
		for _, line := range region.Lines {
			if strings.TrimSpace(line) != "" {
				nonblank++
			}
		}
		if index < len(regions)-1 && nonblank != NonblankLinesPerRegion {
			return fmt.Errorf("region %d has %d nonblank lines, want %d", index+1, nonblank, NonblankLinesPerRegion)
		}
		if index == len(regions)-1 && (nonblank == 0 || nonblank > NonblankLinesPerRegion) {
			return fmt.Errorf("final region has invalid nonblank line count %d", nonblank)
		}
		expectedStart = region.Key.EndLine + 1
	}
	if expectedStart != len(lines)+1 {
		return fmt.Errorf("region universe ends at %d, document ends at %d", expectedStart-1, len(lines))
	}
	return nil
}

func RenderRegion(regions []Region, ordinal, context int) (string, error) {
	if ordinal < 1 || ordinal > len(regions) {
		return "", fmt.Errorf("region ordinal %d is out of range", ordinal)
	}
	if context < 0 {
		return "", fmt.Errorf("context must be non-negative")
	}
	region := regions[ordinal-1]
	start := region.Key.StartLine - context
	if start < 1 {
		start = 1
	}
	end := region.Key.EndLine + context
	all := flattenRegions(regions)
	if end > len(all) {
		end = len(all)
	}
	var output strings.Builder
	for line := start; line <= end; line++ {
		fmt.Fprintf(&output, "%6d %s\n", line, all[line-1])
	}
	return output.String(), nil
}

func SourceDigest(lines []string) string {
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n") + "\n"))
	return hex.EncodeToString(sum[:])
}

func makeRegion(blob string, ordinal, start, end int, lines []string) Region {
	region := Region{Key: RegionKey{InputBlob: blob, Ordinal: ordinal, StartLine: start + 1, EndLine: end + 1}}
	region.Lines = append([]string(nil), lines[start:end+1]...)
	return region
}

func physicalLines(document []byte) ([]string, error) {
	if len(document) == 0 {
		return nil, nil
	}
	if document[len(document)-1] != '\n' {
		return nil, fmt.Errorf("document must end with a newline")
	}
	parts := bytes.Split(document[:len(document)-1], []byte{'\n'})
	lines := make([]string, len(parts))
	for index, part := range parts {
		lines[index] = string(part)
	}
	return lines, nil
}

func flattenRegions(regions []Region) []string {
	var lines []string
	for _, region := range regions {
		lines = append(lines, region.Lines...)
	}
	return lines
}
