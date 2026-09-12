package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/docclause"
)

type byteEdit struct {
	oldStart int
	oldEnd   int
	newStart int
	newEnd   int
}

type diffOperation uint8

const (
	diffEqual diffOperation = iota
	diffDelete
	diffInsert
)

func regenerateLedger(gitDirectory, documentPath, registryPath, deltaPath string) ([]byte, string, error) {
	newDocument, err := os.ReadFile(resolvePath(gitDirectory, documentPath))
	if err != nil {
		return nil, "", err
	}
	oldRegistryContents, err := os.ReadFile(resolvePath(gitDirectory, registryPath))
	if err != nil {
		return nil, "", err
	}
	oldRegistry, err := decodeRegistry(oldRegistryContents)
	if err != nil {
		return nil, "", fmt.Errorf("decode registry: %w", err)
	}
	if oldRegistry.InputBlob == "" {
		return nil, "", fmt.Errorf("registry input_blob is empty")
	}

	command := exec.Command("git", "cat-file", "blob", oldRegistry.InputBlob)
	command.Dir = gitDirectory
	oldDocument, err := command.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(oldDocument))
		if detail != "" {
			return nil, "", fmt.Errorf("resolve registry input_blob %q via git: %w: %s", oldRegistry.InputBlob, err, detail)
		}
		return nil, "", fmt.Errorf("resolve registry input_blob %q via git: %w", oldRegistry.InputBlob, err)
	}
	if actual := docclause.GitBlobID(oldDocument); actual != oldRegistry.InputBlob {
		return nil, "", fmt.Errorf("resolved old document blob %q, want %q", actual, oldRegistry.InputBlob)
	}
	oldRegions, err := docclause.GenerateRegions(oldDocument, oldRegistry.InputBlob)
	if err != nil {
		return nil, "", fmt.Errorf("generate old region universe: %w", err)
	}
	if err := docclause.ValidateRegistry(oldRegistry.DocumentPath, oldDocument, oldRegistry.InputBlob, oldRegions, oldRegistry); err != nil {
		return nil, "", fmt.Errorf("validate old registry: %w", err)
	}
	if _, err := docclause.ClassificationDigest(oldRegistry.DocumentPath, oldDocument, oldRegistry.InputBlob, oldRegions, oldRegistry); err != nil {
		return nil, "", fmt.Errorf("validate old classifications: %w", err)
	}

	resolvedDeltaPath := deltaPath
	if deltaPath != "" {
		resolvedDeltaPath = resolvePath(gitDirectory, deltaPath)
	}
	delta, err := readClassificationDelta(resolvedDeltaPath)
	if err != nil {
		return nil, "", err
	}
	edits, err := diffBytes(oldDocument, newDocument)
	if err != nil {
		return nil, "", err
	}
	oldDecisions := mergeAdjacentDecisions(allDecisions(oldRegistry))

	shifted := make([]docclause.Classification, 0, len(oldDecisions))
	type dirtyDecision struct {
		decision docclause.Classification
		start    int
		end      int
	}
	var dirty []dirtyDecision
	for _, decision := range oldDecisions {
		if decisionDirty(decision, edits) {
			start := projectBoundary(decision.StartByte, true, edits)
			end := projectBoundary(decision.EndByte, false, edits)
			if end < start {
				end = start
			}
			dirty = append(dirty, dirtyDecision{decision: decision, start: start, end: end})
			continue
		}
		decision.StartByte = mapBoundary(decision.StartByte, true, edits)
		decision.EndByte = mapBoundary(decision.EndByte, false, edits)
		shifted = append(shifted, decision)
	}

	if err := validateDelta(newDocument, delta, shifted); err != nil {
		return nil, "", err
	}
	for _, item := range dirty {
		supplied := false
		for _, decision := range delta {
			if decision.StartByte < item.end && decision.EndByte > item.start {
				supplied = true
				break
			}
		}
		if !supplied {
			return nil, "", fmt.Errorf("dirty %s must be re-supplied by -classification-delta", describeDecision(item.decision))
		}
	}

	decisions := append(shifted, delta...)
	sortDecisions(decisions)
	if err := validateCompleteCoverage(newDocument, decisions); err != nil {
		return nil, "", err
	}
	decisions = mergeAdjacentDecisions(decisions)

	newBlob := docclause.GitBlobID(newDocument)
	regions, err := docclause.GenerateRegions(newDocument, newBlob)
	if err != nil {
		return nil, "", fmt.Errorf("generate new region universe: %w", err)
	}
	if err := docclause.ValidateUniverse(newDocument, newBlob, regions); err != nil {
		return nil, "", fmt.Errorf("validate new region universe: %w", err)
	}
	registry, err := buildRegistry(documentPath, newBlob, regions, decisions)
	if err != nil {
		return nil, "", err
	}
	if err := docclause.ValidateRegistry(documentPath, newDocument, newBlob, regions, registry); err != nil {
		return nil, "", fmt.Errorf("validate regenerated registry: %w", err)
	}
	marked, err := docclause.Materialize(documentPath, newDocument, newBlob, regions, registry)
	if err != nil {
		return nil, "", fmt.Errorf("materialize regenerated registry: %w", err)
	}
	digest, err := docclause.ClassificationDigest(documentPath, newDocument, newBlob, regions, registry)
	if err != nil {
		return nil, "", fmt.Errorf("digest regenerated classifications: %w", err)
	}
	registry.Activation = &docclause.ActivationPins{
		MarkedBlob:                  docclause.GitBlobID(marked),
		OrderedClassificationSHA256: digest,
	}
	if err := docclause.ValidateRegistry(documentPath, newDocument, newBlob, regions, registry); err != nil {
		return nil, "", fmt.Errorf("validate final registry: %w", err)
	}
	if err := docclause.ValidateUniverse(newDocument, newBlob, regions); err != nil {
		return nil, "", fmt.Errorf("validate final region universe: %w", err)
	}
	if err := docclause.ValidateActivation(documentPath, newDocument, marked, newBlob, regions, registry); err != nil {
		return nil, "", fmt.Errorf("validate final activation: %w", err)
	}

	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(registry); err != nil {
		return nil, "", err
	}
	pins, err := formatPins(registry)
	if err != nil {
		return nil, "", err
	}
	return encoded.Bytes(), pins, nil
}

func decodeRegistry(contents []byte) (docclause.Registry, error) {
	var registry docclause.Registry
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		return docclause.Registry{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return docclause.Registry{}, fmt.Errorf("registry contains trailing JSON values")
		}
		return docclause.Registry{}, err
	}
	return registry, nil
}

func readClassificationDelta(path string) ([]docclause.Classification, error) {
	if path == "" {
		return nil, nil
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read classification delta: %w", err)
	}
	var delta []docclause.Classification
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&delta); err != nil {
		return nil, fmt.Errorf("decode classification delta: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("classification delta contains trailing JSON values")
		}
		return nil, fmt.Errorf("decode classification delta trailing data: %w", err)
	}
	if delta == nil {
		return nil, fmt.Errorf("classification delta must be a JSON array")
	}
	return delta, nil
}

func resolvePath(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

func allDecisions(registry docclause.Registry) []docclause.Classification {
	var decisions []docclause.Classification
	for _, receipt := range registry.Receipts {
		if receipt.Review != nil {
			decisions = append(decisions, receipt.Review.Decisions...)
		}
	}
	sortDecisions(decisions)
	return decisions
}

func decisionDirty(decision docclause.Classification, edits []byteEdit) bool {
	for _, edit := range edits {
		if edit.oldStart < decision.EndByte && edit.oldEnd > decision.StartByte {
			return true
		}
		if edit.oldStart == edit.oldEnd && decision.StartByte < edit.oldStart && edit.oldStart < decision.EndByte {
			return true
		}
	}
	return false
}

func mapBoundary(position int, includeInsertion bool, edits []byteEdit) int {
	shift := 0
	for _, edit := range edits {
		if position < edit.oldStart {
			break
		}
		if position == edit.oldStart && edit.oldStart == edit.oldEnd {
			if includeInsertion {
				return edit.newEnd
			}
			return edit.newStart
		}
		if position == edit.oldStart {
			if includeInsertion {
				return edit.newEnd
			}
			return edit.newStart
		}
		if position < edit.oldEnd {
			return edit.newStart
		}
		if position == edit.oldEnd {
			return edit.newEnd
		}
		shift = edit.newEnd - edit.oldEnd
	}
	return position + shift
}

func projectBoundary(position int, includeInsertion bool, edits []byteEdit) int {
	for _, edit := range edits {
		if edit.oldStart < position && position < edit.oldEnd {
			if includeInsertion {
				return edit.newStart
			}
			return edit.newEnd
		}
	}
	return mapBoundary(position, includeInsertion, edits)
}

func validateDelta(document []byte, delta, shifted []docclause.Classification) error {
	seenMarkers := make(map[string]bool)
	for _, decision := range shifted {
		if decision.Kind == docclause.ClassificationAnchor {
			seenMarkers[decision.MarkerID] = true
		}
	}
	ordered := append([]docclause.Classification(nil), delta...)
	sortDecisions(ordered)
	for index, decision := range ordered {
		if decision.StartByte < 0 || decision.EndByte <= decision.StartByte || decision.EndByte > len(document) {
			return fmt.Errorf("classification delta has invalid byte range [%d,%d)", decision.StartByte, decision.EndByte)
		}
		if len(bytes.Trim(document[decision.StartByte:decision.EndByte], " \t\r\n")) == 0 {
			return fmt.Errorf("classification delta byte range [%d,%d) has no payload", decision.StartByte, decision.EndByte)
		}
		switch decision.Kind {
		case docclause.ClassificationAnchor:
			if decision.MarkerID == "" {
				return fmt.Errorf("anchor classification delta [%d,%d) requires marker_id", decision.StartByte, decision.EndByte)
			}
			if seenMarkers[decision.MarkerID] {
				return fmt.Errorf("duplicate marker_id %q", decision.MarkerID)
			}
			seenMarkers[decision.MarkerID] = true
		case docclause.ClassificationNonNormative:
			if decision.MarkerID != "" {
				return fmt.Errorf("non-normative classification delta carries marker_id %q", decision.MarkerID)
			}
		default:
			return fmt.Errorf("classification delta has unknown kind %q", decision.Kind)
		}
		if index != 0 && ordered[index-1].EndByte > decision.StartByte {
			return fmt.Errorf("classification delta ranges overlap at source byte %d", decision.StartByte)
		}
	}
	combined := append(append([]docclause.Classification(nil), shifted...), ordered...)
	sortDecisions(combined)
	for index := 1; index < len(combined); index++ {
		if combined[index-1].EndByte > combined[index].StartByte {
			return fmt.Errorf("classification delta overlaps an existing decision at source byte %d", combined[index].StartByte)
		}
	}
	return nil
}

func validateCompleteCoverage(document []byte, decisions []docclause.Classification) error {
	coverage := make([]bool, len(document))
	for _, decision := range decisions {
		if decision.StartByte < 0 || decision.EndByte <= decision.StartByte || decision.EndByte > len(document) {
			return fmt.Errorf("classification has invalid byte range [%d,%d)", decision.StartByte, decision.EndByte)
		}
		for position := decision.StartByte; position < decision.EndByte; position++ {
			if coverage[position] {
				return fmt.Errorf("classification overlap at source byte %d", position)
			}
			coverage[position] = true
		}
	}
	var uncovered [][2]int
	for position := 0; position < len(document); {
		if coverage[position] || asciiWhitespace(document[position]) {
			position++
			continue
		}
		start := position
		end := position + 1
		position++
		for position < len(document) && !coverage[position] {
			if !asciiWhitespace(document[position]) {
				end = position + 1
			}
			position++
		}
		uncovered = append(uncovered, [2]int{start, end})
	}
	if len(uncovered) == 0 {
		return nil
	}
	parts := make([]string, len(uncovered))
	for index, span := range uncovered {
		parts[index] = fmt.Sprintf("[%d,%d)", span[0], span[1])
	}
	return fmt.Errorf("new document has uncovered non-whitespace byte ranges: %s; supply -classification-delta", strings.Join(parts, ", "))
}

func mergeAdjacentDecisions(decisions []docclause.Classification) []docclause.Classification {
	if len(decisions) == 0 {
		return nil
	}
	sortDecisions(decisions)
	merged := make([]docclause.Classification, 0, len(decisions))
	for _, decision := range decisions {
		if len(merged) != 0 {
			previous := &merged[len(merged)-1]
			if previous.EndByte == decision.StartByte && previous.Kind == decision.Kind && previous.MarkerID == decision.MarkerID {
				previous.EndByte = decision.EndByte
				continue
			}
		}
		merged = append(merged, decision)
	}
	return merged
}

func sortDecisions(decisions []docclause.Classification) {
	sort.SliceStable(decisions, func(left, right int) bool {
		if decisions[left].StartByte == decisions[right].StartByte {
			return decisions[left].EndByte < decisions[right].EndByte
		}
		return decisions[left].StartByte < decisions[right].StartByte
	})
}

func buildRegistry(documentPath, blob string, regions []docclause.Region, decisions []docclause.Classification) (docclause.Registry, error) {
	registry := docclause.Registry{
		DocumentPath:           documentPath,
		InputBlob:              blob,
		NonblankLinesPerRegion: docclause.NonblankLinesPerRegion,
		RegionUniverse:         make([]docclause.RegionKey, len(regions)),
		Receipts:               make([]docclause.RegionReceipt, len(regions)),
	}
	decisionIndex := 0
	regionStart := 0
	for index, region := range regions {
		regionEnd := regionStart + len([]byte(strings.Join(region.Lines, "\n")+"\n"))
		var bucket []docclause.Classification
		for decisionIndex < len(decisions) && decisions[decisionIndex].StartByte < regionEnd {
			decision := decisions[decisionIndex]
			if decision.EndByte <= regionStart {
				decisionIndex++
				continue
			}
			fragment := decision
			if fragment.StartByte < regionStart {
				fragment.StartByte = regionStart
			}
			if fragment.EndByte > regionEnd {
				fragment.EndByte = regionEnd
			}
			if fragment.EndByte > fragment.StartByte && len(bytes.Trim([]byte(strings.Join(region.Lines, "\n") + "\n")[fragment.StartByte-regionStart:fragment.EndByte-regionStart], " \t\r\n")) != 0 {
				bucket = append(bucket, fragment)
			}
			if decision.EndByte <= regionEnd {
				decisionIndex++
			} else {
				break
			}
		}
		registry.RegionUniverse[index] = region.Key
		registry.Receipts[index] = docclause.RegionReceipt{
			Key: region.Key,
			Review: &docclause.ReviewReceipt{
				SourceSHA256: docclause.SourceDigest(region.Lines),
				Decisions:    bucket,
			},
		}
		regionStart = regionEnd
	}
	if decisionIndex != len(decisions) {
		return docclause.Registry{}, fmt.Errorf("classification [%d,%d) was not assigned to a region", decisions[decisionIndex].StartByte, decisions[decisionIndex].EndByte)
	}
	return registry, nil
}

func describeDecision(decision docclause.Classification) string {
	if decision.MarkerID != "" {
		return fmt.Sprintf("marker %q", decision.MarkerID)
	}
	return fmt.Sprintf("non-normative decision [%d,%d)", decision.StartByte, decision.EndByte)
}

func formatPins(registry docclause.Registry) (string, error) {
	if registry.Activation == nil {
		return "", fmt.Errorf("cannot emit absent activation pins")
	}
	var output strings.Builder
	fmt.Fprintf(&output, "const (\n\tconfirmedBaselineMarkedBlob = %q\n", registry.Activation.MarkedBlob)
	fmt.Fprintf(&output, "\tconfirmedBaselineOrderedClassificationSHA256 = %q\n)\n\n", registry.Activation.OrderedClassificationSHA256)
	output.WriteString("reviewedOrdinals := map[int]confirmedPacketPin{\n")
	for _, receipt := range registry.Receipts {
		type canonicalDecision struct {
			EndByte   int                          `json:"end_source_byte"`
			Kind      docclause.ClassificationKind `json:"kind"`
			MarkerID  string                       `json:"marker_id,omitempty"`
			StartByte int                          `json:"start_source_byte"`
		}
		canonical := make([]canonicalDecision, len(receipt.Review.Decisions))
		for index, decision := range receipt.Review.Decisions {
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
		sum := sha256.Sum256(encoded)
		anchors := 0
		for _, decision := range receipt.Review.Decisions {
			if decision.Kind == docclause.ClassificationAnchor {
				anchors++
			}
		}
		fmt.Fprintf(&output, "\t%d: {\n", receipt.Key.Ordinal)
		fmt.Fprintf(&output, "\t\tDecisionCount: %d,\n\t\tAnchorCount: %d,\n", len(receipt.Review.Decisions), anchors)
		fmt.Fprintf(&output, "\t\tSourceSHA256: %q,\n\t\tDecisionsHash: %q,\n\t},\n", receipt.Review.SourceSHA256, hex.EncodeToString(sum[:]))
	}
	output.WriteString("}\n")
	return output.String(), nil
}

func asciiWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func diffBytes(oldDocument, newDocument []byte) ([]byteEdit, error) {
	oldLines := splitLines(oldDocument)
	newLines := splitLines(newDocument)
	lineOperations, err := myersOperations(len(oldLines), len(newLines), func(oldIndex, newIndex int) bool {
		return bytes.Equal(oldLines[oldIndex], newLines[newIndex])
	})
	if err != nil {
		return nil, err
	}
	var edits []byteEdit
	oldLine, newLine := 0, 0
	oldByte, newByte := 0, 0
	for operationIndex := 0; operationIndex < len(lineOperations); {
		if lineOperations[operationIndex] == diffEqual {
			oldByte += len(oldLines[oldLine])
			newByte += len(newLines[newLine])
			oldLine++
			newLine++
			operationIndex++
			continue
		}
		oldStart, newStart := oldByte, newByte
		for operationIndex < len(lineOperations) && lineOperations[operationIndex] != diffEqual {
			if lineOperations[operationIndex] == diffDelete {
				oldByte += len(oldLines[oldLine])
				oldLine++
			} else {
				newByte += len(newLines[newLine])
				newLine++
			}
			operationIndex++
		}
		hunk, err := rawByteDiff(oldDocument[oldStart:oldByte], newDocument[newStart:newByte])
		if err != nil {
			return nil, err
		}
		for _, edit := range hunk {
			edit.oldStart += oldStart
			edit.oldEnd += oldStart
			edit.newStart += newStart
			edit.newEnd += newStart
			edits = append(edits, edit)
		}
	}
	return edits, nil
}

func splitLines(document []byte) [][]byte {
	lines := bytes.SplitAfter(document, []byte{'\n'})
	if len(lines) != 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func rawByteDiff(oldDocument, newDocument []byte) ([]byteEdit, error) {
	operations, err := myersOperations(len(oldDocument), len(newDocument), func(oldIndex, newIndex int) bool {
		return oldDocument[oldIndex] == newDocument[newIndex]
	})
	if err != nil {
		return nil, err
	}
	return editsFromOperations(operations), nil
}

func myersOperations(n, m int, equal func(int, int) bool) ([]diffOperation, error) {
	maximum := n + m
	frontier := map[int]int{1: 0}
	trace := make([][]int32, 0)
	for distance := 0; distance <= maximum; distance++ {
		if distance > 8192 {
			return nil, fmt.Errorf("diff edit distance exceeds fail-closed limit 8192")
		}
		snapshot := make([]int32, 2*distance+3)
		for diagonal := -distance - 1; diagonal <= distance+1; diagonal++ {
			snapshot[diagonal+distance+1] = int32(frontier[diagonal])
		}
		trace = append(trace, snapshot)
		for diagonal := -distance; diagonal <= distance; diagonal += 2 {
			var x int
			if diagonal == -distance || (diagonal != distance && frontier[diagonal-1] < frontier[diagonal+1]) {
				x = frontier[diagonal+1]
			} else {
				x = frontier[diagonal-1] + 1
			}
			y := x - diagonal
			for x < n && y < m && equal(x, y) {
				x++
				y++
			}
			frontier[diagonal] = x
			if x >= n && y >= m {
				return backtrackDiff(trace, x, y), nil
			}
		}
	}
	return nil, fmt.Errorf("byte diff did not converge")
}

func backtrackDiff(trace [][]int32, x, y int) []diffOperation {
	operations := make([]diffOperation, 0, x+y)
	for distance := len(trace) - 1; distance >= 0; distance-- {
		diagonal := x - y
		var previousDiagonal int
		if diagonal == -distance || (diagonal != distance && traceValue(trace[distance], distance, diagonal-1) < traceValue(trace[distance], distance, diagonal+1)) {
			previousDiagonal = diagonal + 1
		} else {
			previousDiagonal = diagonal - 1
		}
		previousX := traceValue(trace[distance], distance, previousDiagonal)
		previousY := previousX - previousDiagonal
		for x > previousX && y > previousY {
			operations = append(operations, diffEqual)
			x--
			y--
		}
		if distance == 0 {
			break
		}
		if x == previousX {
			operations = append(operations, diffInsert)
			y--
		} else {
			operations = append(operations, diffDelete)
			x--
		}
	}
	for left, right := 0, len(operations)-1; left < right; left, right = left+1, right-1 {
		operations[left], operations[right] = operations[right], operations[left]
	}
	return operations
}

func traceValue(snapshot []int32, distance, diagonal int) int {
	return int(snapshot[diagonal+distance+1])
}

func editsFromOperations(operations []diffOperation) []byteEdit {
	var edits []byteEdit
	oldPosition, newPosition := 0, 0
	active := false
	var edit byteEdit
	flush := func() {
		if active {
			edits = append(edits, edit)
			active = false
		}
	}
	for _, operation := range operations {
		if operation == diffEqual {
			flush()
			oldPosition++
			newPosition++
			continue
		}
		if !active {
			edit = byteEdit{oldStart: oldPosition, oldEnd: oldPosition, newStart: newPosition, newEnd: newPosition}
			active = true
		}
		if operation == diffDelete {
			oldPosition++
			edit.oldEnd = oldPosition
		} else {
			newPosition++
			edit.newEnd = newPosition
		}
	}
	flush()
	return edits
}
