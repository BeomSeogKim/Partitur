package docmarker

import (
	"fmt"
	"regexp"
	"strings"
)

const Production = `<!-- partitur:mark (begin|end) (anchor=([A-Za-z0-9]+(?:[._/-][A-Za-z0-9]+)*)|non-normative) -->`

var pattern = regexp.MustCompile(Production)

type Range struct {
	Classification string
	Contents       string
}

func Parse(document string) ([]Range, error) {
	matches := pattern.FindAllStringSubmatchIndex(document, -1)
	remainder := pattern.ReplaceAllString(document, "")
	if strings.Contains(remainder, "<!-- partitur:mark") {
		return nil, fmt.Errorf("malformed marker token")
	}

	seenAnchors := make(map[string]bool)
	var ranges []Range
	var openClassification string
	contentStart := 0
	for _, match := range matches {
		boundary := document[match[2]:match[3]]
		classification := document[match[4]:match[5]]
		switch boundary {
		case "begin":
			if openClassification != "" {
				return nil, fmt.Errorf("nested marker range")
			}
			if strings.HasPrefix(classification, "anchor=") {
				id := strings.TrimPrefix(classification, "anchor=")
				if seenAnchors[id] {
					return nil, fmt.Errorf("duplicate marker ID")
				}
				seenAnchors[id] = true
			}
			openClassification = classification
			contentStart = match[1]
		case "end":
			if openClassification == "" || classification != openClassification {
				return nil, fmt.Errorf("marker end does not match open range")
			}
			contents := document[contentStart:match[0]]
			if strings.Trim(contents, " \t\r\n") == "" {
				return nil, fmt.Errorf("empty marker range")
			}
			ranges = append(ranges, Range{Classification: classification, Contents: contents})
			openClassification = ""
		}
	}
	if openClassification != "" {
		return nil, fmt.Errorf("unclosed marker range")
	}
	return ranges, nil
}
