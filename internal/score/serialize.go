package score

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
)

// CanonicalYAML emits the fixed snapshot representation used for amended scores.
func (s *Score) CanonicalYAML() ([]byte, error) {
	projection, err := s.ProjectionBytes()
	if err != nil {
		return nil, err
	}
	value, err := canonical.ParseJSON(projection)
	if err != nil {
		return nil, fmt.Errorf("decode score projection: %w", err)
	}
	var out bytes.Buffer
	if err := writeYAML(&out, value, 0, false); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

func writeYAML(out *bytes.Buffer, value any, indent int, listItem bool) error {
	prefix := strings.Repeat("  ", indent)
	if listItem {
		out.WriteString(prefix)
		out.WriteString("- ")
	}
	switch value := value.(type) {
	case map[string]any:
		bodyIndent := indent
		if len(value) == 0 {
			out.WriteString("{}\n")
			return nil
		}
		if listItem {
			out.WriteByte('\n')
			bodyIndent++
			prefix = strings.Repeat("  ", bodyIndent)
		}
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			out.WriteString(prefix)
			out.WriteString(yamlScalar(key))
			out.WriteString(":")
			switch child := value[key].(type) {
			case map[string]any, []any:
				if emptyYAML(child) {
					out.WriteByte(' ')
					if err := writeYAML(out, child, bodyIndent+1, false); err != nil {
						return err
					}
					continue
				}
				out.WriteByte('\n')
				if err := writeYAML(out, child, bodyIndent+1, false); err != nil {
					return err
				}
			default:
				out.WriteByte(' ')
				writeYAMLScalar(out, child, bodyIndent+1)
			}
		}
	case []any:
		if len(value) == 0 {
			out.WriteString("[]\n")
			return nil
		}
		if listItem {
			out.WriteByte('\n')
		}
		for _, child := range value {
			if err := writeYAML(out, child, indent, true); err != nil {
				return err
			}
		}
	default:
		writeYAMLScalar(out, value, indent)
	}
	return nil
}

func emptyYAML(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		return len(value) == 0
	case []any:
		return len(value) == 0
	default:
		return false
	}
}

func writeYAMLScalar(out *bytes.Buffer, value any, indent int) {
	if text, ok := value.(string); ok && strings.Contains(text, "\n") {
		out.WriteString("|-\n")
		for _, line := range strings.Split(text, "\n") {
			out.WriteString(strings.Repeat("  ", indent))
			out.WriteString(line)
			out.WriteByte('\n')
		}
		return
	}
	out.WriteString(yamlScalarValue(value))
	out.WriteByte('\n')
}

func yamlScalarValue(value any) string {
	if text, ok := value.(string); ok {
		return yamlScalar(text)
	}
	if value == nil {
		return "null"
	}
	encoded, _ := canonical.Encode(value)
	return string(encoded)
}
func yamlScalar(value string) string {
	if yamlPlain(value) {
		return value
	}
	return strconv.Quote(value)
}
func yamlPlain(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\n\r\t") || strings.Contains(value, ": ") || strings.Contains(value, " #") || strings.ContainsAny(value, "[]{}#,&*!|>\\\"'%@`") {
		return false
	}
	first, _ := utf8Rune(value)
	if strings.ContainsRune("-?:", first) || first == ' ' {
		return false
	}
	switch strings.ToLower(value) {
	case "null", "~", "true", "false", ".nan", ".inf", "-.inf", "+.inf":
		return false
	}
	_, err := strconv.ParseFloat(value, 64)
	return err != nil
}
func utf8Rune(value string) (rune, int) {
	for _, value := range value {
		return value, len(string(value))
	}
	return 0, 0
}
