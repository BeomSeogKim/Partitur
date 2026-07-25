package canonical

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"
)

// ParseYAML demonstrates the wrapper that a yamlsafe package needs around a
// general YAML 1.2 parser. The library still parses a representation graph;
// this traversal rejects forbidden YAML features before producing a JSON AST.
func ParseYAML(input []byte) (any, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(input, &document); err != nil {
		return nil, err
	}
	if len(document.Content) != 1 {
		return nil, errors.New("expected one YAML document")
	}
	return yamlNode(document.Content[0])
}

func yamlNode(node *yaml.Node) (any, error) {
	if node.Anchor != "" || node.Alias != nil {
		return nil, errors.New("anchors and aliases are forbidden")
	}
	switch node.Kind {
	case yaml.ScalarNode:
		return yamlScalar(node)
	case yaml.SequenceNode:
		values := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			value, err := yamlNode(child)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	case yaml.MappingNode:
		values := make(map[string]any, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			keyNode := node.Content[index]
			if keyNode.Kind != yaml.ScalarNode || keyNode.Tag != "!!str" {
				return nil, errors.New("mapping keys must be strings")
			}
			key := keyNode.Value
			if key == "<<" {
				return nil, errors.New("merge keys are forbidden")
			}
			if _, duplicate := values[key]; duplicate {
				return nil, fmt.Errorf("duplicate mapping key %q", key)
			}
			value, err := yamlNode(node.Content[index+1])
			if err != nil {
				return nil, err
			}
			values[key] = value
		}
		return values, nil
	default:
		return nil, fmt.Errorf("YAML node kind %d is forbidden", node.Kind)
	}
}

func yamlScalar(node *yaml.Node) (any, error) {
	if node.Style&yaml.TaggedStyle != 0 && node.Tag != "!!str" {
		return nil, fmt.Errorf("explicit tag %q is forbidden", node.Tag)
	}
	switch node.Tag {
	case "!!str":
		// YAML 1.2 treats sexagesimal-looking plain scalars as strings. The
		// design asks to reject them, so this is an explicit Partitur policy.
		if node.Style == 0 && looksSexagesimal(node.Value) {
			return nil, fmt.Errorf("sexagesimal-looking scalar %q is forbidden", node.Value)
		}
		return node.Value, nil
	case "!!null":
		return nil, nil
	case "!!bool":
		return strings.EqualFold(node.Value, "true"), nil
	case "!!int":
		text := normalizeYAMLNumber(node.Value)
		integer, ok := new(big.Int).SetString(text, 0)
		if !ok {
			return nil, fmt.Errorf("invalid YAML integer %q", node.Value)
		}
		value, _ := new(big.Float).SetInt(integer).Float64()
		if math.IsInf(value, 0) {
			return nil, fmt.Errorf("number %q is not a finite IEEE 754 binary64 value", node.Value)
		}
		if value == 0 && strings.HasPrefix(text, "-") {
			return nil, fmt.Errorf("negative zero %q is rejected", node.Value)
		}
		return value, nil
	case "!!float":
		value, err := strconv.ParseFloat(normalizeYAMLNumber(node.Value), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("number %q is not a finite IEEE 754 binary64 value", node.Value)
		}
		if value == 0 && strings.HasPrefix(node.Value, "-") {
			return nil, fmt.Errorf("negative zero %q is rejected", node.Value)
		}
		if value == 0 && !lexicallyZero(node.Value) {
			return nil, fmt.Errorf("number %q underflows IEEE 754 binary64", node.Value)
		}
		return value, nil
	default:
		return nil, fmt.Errorf("implicit or custom tag %q is forbidden", node.Tag)
	}
}

func normalizeYAMLNumber(value string) string {
	return strings.ReplaceAll(value, "_", "")
}

func looksSexagesimal(value string) bool {
	parts := strings.Split(value, ":")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

func ValidateSafeInteger(value any) error {
	number, ok := value.(float64)
	if !ok {
		return errors.New("schema number must be numeric")
	}
	const maximum = float64(1<<53 - 1)
	if math.Trunc(number) != number || number < -maximum || number > maximum {
		return fmt.Errorf("%v is not an integer in the I-JSON safe range", number)
	}
	return nil
}
