package docclause

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

type AnchorProposal struct {
	MarkerID  string `json:"marker_id"`
	StartLine int    `json:"start_source_line"`
	EndLine   int    `json:"end_source_line"`
	Basis     string `json:"basis"`
}

type BoundarySuggestion struct {
	StartLine int    `json:"start_source_line"`
	EndLine   int    `json:"end_source_line"`
	Basis     string `json:"basis"`
}

type NameSuggestion struct {
	MarkerID  string `json:"marker_id"`
	StartLine int    `json:"start_source_line"`
	EndLine   int    `json:"end_source_line"`
	Basis     string `json:"basis"`
}

type Proposal struct {
	Anchors    []AnchorProposal     `json:"anchor_proposals"`
	Boundaries []BoundarySuggestion `json:"boundary_suggestions"`
	Names      []NameSuggestion     `json:"name_suggestions"`
	Covered    int                  `json:"lines_with_any_proposal"`
	Total      int                  `json:"physical_lines"`
}

func Propose(region Region) Proposal {
	proposal := Proposal{
		Anchors: []AnchorProposal{}, Boundaries: []BoundarySuggestion{}, Names: []NameSuggestion{},
		Total: len(region.Lines),
	}
	covered := make(map[int]bool)
	for offset, line := range region.Lines {
		lineNumber := region.Key.StartLine + offset
		for _, id := range uniqueStrings(catalogIDPattern.FindAllString(line, -1)) {
			proposal.Anchors = append(proposal.Anchors, AnchorProposal{
				MarkerID: id, StartLine: lineNumber, EndLine: lineNumber,
				Basis: "exact existing catalog ID token",
			})
			covered[lineNumber] = true
		}
	}

	for offset := 0; offset < len(region.Lines); {
		if strings.TrimSpace(region.Lines[offset]) == "" {
			offset++
			continue
		}
		start := offset
		for offset+1 < len(region.Lines) && strings.TrimSpace(region.Lines[offset+1]) != "" {
			offset++
		}
		end := offset
		startLine := region.Key.StartLine + start
		endLine := region.Key.StartLine + end
		proposal.Boundaries = append(proposal.Boundaries, BoundarySuggestion{
			StartLine: startLine, EndLine: endLine,
			Basis: "contiguous nonblank byte run; no normativity inferred",
		})
		for line := startLine; line <= endLine; line++ {
			covered[line] = true
		}
		if !hasCatalogProposal(proposal.Anchors, startLine, endLine) {
			proposal.Names = append(proposal.Names, NameSuggestion{
				MarkerID: suggestedName(region.Lines[start], startLine), StartLine: startLine, EndLine: endLine,
				Basis: "lexical slug of first line; proposed name only",
			})
		}
		offset++
	}
	proposal.Covered = len(covered)
	return proposal
}

func hasCatalogProposal(proposals []AnchorProposal, start, end int) bool {
	for _, proposal := range proposals {
		if proposal.StartLine >= start && proposal.EndLine <= end {
			return true
		}
	}
	return false
}

func suggestedName(line string, lineNumber int) string {
	var words []string
	var word strings.Builder
	flush := func() {
		if word.Len() == 0 {
			return
		}
		words = append(words, strings.ToLower(word.String()))
		word.Reset()
	}
	for _, r := range line {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word.WriteRune(r)
		} else {
			flush()
		}
		if len(words) == 5 {
			break
		}
	}
	flush()
	if len(words) == 0 {
		return fmt.Sprintf("proposed/line-%d", lineNumber)
	}
	return fmt.Sprintf("proposed/%s-%d", strings.Join(words, "-"), lineNumber)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool)
	var unique []string
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			unique = append(unique, value)
		}
	}
	sort.Strings(unique)
	return unique
}
