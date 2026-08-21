package docclause

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"
)

// PacketPreview is a view-only rendering. Its contents are intentionally not
// exposed as document bytes, so it cannot be passed to activation by mistake.
type PacketPreview struct {
	contents string
}

func (preview PacketPreview) WriteTo(writer io.Writer) (int64, error) {
	written, err := io.WriteString(writer, preview.contents)
	return int64(written), err
}

func RenderPacketPreview(regions []Region, registry Registry, ordinal int) (PacketPreview, error) {
	if ordinal < 1 || ordinal > len(regions) {
		return PacketPreview{}, fmt.Errorf("region ordinal %d is out of range", ordinal)
	}
	region := regions[ordinal-1]
	regionStart := 0
	for index := 0; index < ordinal-1; index++ {
		regionStart += len(regionBytes(regions[index]))
	}

	var decisions []Classification
	for _, receipt := range registry.Receipts {
		if receipt.Key != region.Key || receipt.Review == nil || receipt.Review.SourceSHA256 != SourceDigest(region.Lines) {
			continue
		}
		decisions = append(decisions, receipt.Review.Decisions...)
		break
	}
	sort.Slice(decisions, func(left, right int) bool {
		if decisions[left].StartByte == decisions[right].StartByte {
			return decisions[left].EndByte < decisions[right].EndByte
		}
		return decisions[left].StartByte < decisions[right].StartByte
	})

	contents := regionBytes(region)
	var output strings.Builder
	fmt.Fprintln(&output, "PARTITUR PACKET PREVIEW — VIEW ONLY; NOT AN ACTIVATION DOCUMENT")
	fmt.Fprintf(&output, "packet %02d lines %d-%d\n", ordinal, region.Key.StartLine, region.Key.EndLine)
	output.WriteString("legend: [[CONFIRMED ...]]...[[/CONFIRMED]]  [[UNCLASSIFIED]]...[[/UNCLASSIFIED]]\n\n")
	cursor := 0
	for _, decision := range decisions {
		start := decision.StartByte - regionStart
		end := decision.EndByte - regionStart
		writeUnclassified(&output, contents[cursor:start])
		label := string(decision.Kind)
		if decision.Kind == ClassificationAnchor {
			label = "anchor=" + decision.MarkerID
		}
		fmt.Fprintf(&output, "[[CONFIRMED %s]]", label)
		output.Write(contents[start:end])
		output.WriteString("[[/CONFIRMED]]")
		cursor = end
	}
	writeUnclassified(&output, contents[cursor:])
	return PacketPreview{contents: output.String()}, nil
}

func writeUnclassified(output *strings.Builder, contents []byte) {
	for len(contents) != 0 {
		lineEnd := bytes.IndexByte(contents, '\n')
		if lineEnd < 0 {
			writeUnclassifiedLine(output, contents)
			return
		}
		writeUnclassifiedLine(output, contents[:lineEnd])
		output.WriteByte('\n')
		contents = contents[lineEnd+1:]
	}
}

func writeUnclassifiedLine(output *strings.Builder, contents []byte) {
	first := 0
	for first < len(contents) && asciiWhitespace(contents[first]) {
		first++
	}
	last := len(contents)
	for last > first && asciiWhitespace(contents[last-1]) {
		last--
	}
	output.Write(contents[:first])
	if first != last {
		output.WriteString("[[UNCLASSIFIED]]")
		output.Write(contents[first:last])
		output.WriteString("[[/UNCLASSIFIED]]")
	}
	output.Write(contents[last:])
}
