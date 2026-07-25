package canonical

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v4"
)

const maxSafeInteger = float64(1<<53 - 1)

// ParseJSON parses exactly one JSON value into the AST accepted by Encode.
// Duplicate names, invalid Unicode, and numbers forbidden by Appendix A.1 are
// rejected before their lexical form is lost.
func ParseJSON(input []byte) (any, error) {
	if err := validateJSONStrings(input); err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder)
	if err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("parse JSON: trailing value")
		}
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	return value, nil
}

// ParseYAML parses exactly one YAML 1.2 representation graph into the AST
// accepted by Encode. It applies the YAML-to-JSON ingress restrictions from
// Appendix A.1 at this boundary.
func ParseYAML(input []byte) (any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(input))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	if len(document.Content) != 1 {
		return nil, errors.New("parse YAML: expected one non-empty document")
	}

	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("parse YAML: multiple documents")
		}
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	return yamlValue(document.Content[0])
}

// ValidateSafeInteger applies the schema-controlled numeric rule from
// Appendix A.1. Opaque values do not call this function.
func ValidateSafeInteger(value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) ||
		math.Trunc(value) != value ||
		value < -maxSafeInteger || value > maxSafeInteger {
		return fmt.Errorf("%v is not an integer in the I-JSON safe range", value)
	}
	return nil
}

func validateJSONStrings(input []byte) error {
	if !utf8.Valid(input) {
		return errors.New("parse JSON: input is not valid UTF-8")
	}

	for index := 0; index < len(input); index++ {
		if input[index] != '"' {
			continue
		}
		index++
		for ; index < len(input) && input[index] != '"'; index++ {
			if input[index] < 0x20 {
				return errors.New("parse JSON: unescaped control character")
			}
			if input[index] >= utf8.RuneSelf {
				_, size := utf8.DecodeRune(input[index:])
				index += size - 1
				continue
			}
			if input[index] != '\\' {
				continue
			}

			index++
			if index >= len(input) {
				return errors.New("parse JSON: unterminated escape")
			}
			if input[index] != 'u' {
				if !strings.ContainsRune(`"\/bfnrt`, rune(input[index])) {
					return errors.New("parse JSON: invalid escape")
				}
				continue
			}

			codeUnit, next, err := readHexCodeUnit(input, index+1)
			if err != nil {
				return err
			}
			index = next - 1
			switch {
			case codeUnit >= 0xd800 && codeUnit <= 0xdbff:
				if index+6 >= len(input) || input[index+1] != '\\' || input[index+2] != 'u' {
					return errors.New("parse JSON: high surrogate without low surrogate")
				}
				low, afterLow, err := readHexCodeUnit(input, index+3)
				if err != nil || low < 0xdc00 || low > 0xdfff {
					return errors.New("parse JSON: high surrogate without low surrogate")
				}
				index = afterLow - 1
			case codeUnit >= 0xdc00 && codeUnit <= 0xdfff:
				return errors.New("parse JSON: lone low surrogate")
			}
		}
		if index >= len(input) {
			return errors.New("parse JSON: unterminated string")
		}
	}
	return nil
}

func readHexCodeUnit(input []byte, start int) (uint16, int, error) {
	if start+4 > len(input) {
		return 0, start, errors.New("parse JSON: short Unicode escape")
	}
	value, err := strconv.ParseUint(string(input[start:start+4]), 16, 16)
	if err != nil {
		return 0, start, errors.New("parse JSON: invalid Unicode escape")
	}
	return uint16(value), start + 4, nil
}

func decodeJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}

	switch token := token.(type) {
	case nil, bool, string:
		return token, nil
	case json.Number:
		return parseNumber(token.String())
	case json.Delim:
		switch token {
		case '[':
			values := make([]any, 0)
			for decoder.More() {
				value, err := decodeJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				values = append(values, value)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return values, nil
		case '{':
			values := make(map[string]any)
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				name, ok := nameToken.(string)
				if !ok {
					return nil, errors.New("object name is not a string")
				}
				if _, duplicate := values[name]; duplicate {
					return nil, fmt.Errorf("duplicate object name %q", name)
				}
				value, err := decodeJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				values[name] = value
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return values, nil
		}
	}
	return nil, fmt.Errorf("unsupported token %T", token)
}

func parseNumber(literal string) (float64, error) {
	value, err := strconv.ParseFloat(literal, 64)
	if value == 0 {
		switch {
		case lexicallyZero(literal) && strings.HasPrefix(literal, "-"):
			return 0, fmt.Errorf("negative-zero spelling %q is forbidden", literal)
		case !lexicallyZero(literal):
			return 0, fmt.Errorf("number %q underflows binary64 to zero", literal)
		}
	}
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("number %q is not a finite binary64 value", literal)
	}
	return value, nil
}

func lexicallyZero(literal string) bool {
	mantissa := literal
	if exponent := strings.IndexAny(mantissa, "eE"); exponent >= 0 {
		mantissa = mantissa[:exponent]
	}
	for _, character := range mantissa {
		if character >= '1' && character <= '9' {
			return false
		}
	}
	return true
}

func yamlValue(node *yaml.Node) (any, error) {
	if node.Anchor != "" || node.Alias != nil {
		return nil, errors.New("parse YAML: anchors and aliases are forbidden")
	}
	if !utf8.ValidString(node.Value) {
		return nil, errors.New("parse YAML: scalar is not valid UTF-8")
	}

	switch node.Kind {
	case yaml.ScalarNode:
		return yamlScalar(node)
	case yaml.SequenceNode:
		if node.Style&yaml.TaggedStyle != 0 && node.Tag != "!!seq" {
			return nil, fmt.Errorf("parse YAML: tag %q is forbidden on a sequence", node.Tag)
		}
		values := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			value, err := yamlValue(child)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	case yaml.MappingNode:
		if node.Style&yaml.TaggedStyle != 0 && node.Tag != "!!map" {
			return nil, fmt.Errorf("parse YAML: tag %q is forbidden on a mapping", node.Tag)
		}
		if len(node.Content)%2 != 0 {
			return nil, errors.New("parse YAML: malformed mapping")
		}
		values := make(map[string]any, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			keyNode := node.Content[index]
			if keyNode.Kind != yaml.ScalarNode || keyNode.Tag != "!!str" {
				return nil, errors.New("parse YAML: mapping keys must be strings")
			}
			keyValue, err := yamlValue(keyNode)
			if err != nil {
				return nil, err
			}
			key := keyValue.(string)
			if _, duplicate := values[key]; duplicate {
				return nil, fmt.Errorf("parse YAML: duplicate mapping key %q", key)
			}
			value, err := yamlValue(node.Content[index+1])
			if err != nil {
				return nil, err
			}
			values[key] = value
		}
		return values, nil
	default:
		return nil, fmt.Errorf("parse YAML: node kind %d is forbidden", node.Kind)
	}
}

func yamlScalar(node *yaml.Node) (any, error) {
	if node.Style == 0 && looksRestrictedTimestamp(node.Value) {
		return nil, fmt.Errorf("parse YAML: plain timestamp %q is forbidden", node.Value)
	}

	switch node.Tag {
	case "!!str":
		if node.Style == 0 {
			if _, err := strconv.ParseFloat(strings.ReplaceAll(node.Value, "_", ""), 64); errors.Is(err, strconv.ErrRange) {
				return nil, fmt.Errorf("parse YAML: number %q is not a finite binary64 value", node.Value)
			}
			if looksSexagesimal(node.Value) {
				return nil, fmt.Errorf("parse YAML: sexagesimal-looking scalar %q is forbidden", node.Value)
			}
		}
		return node.Value, nil
	case "!!null":
		return nil, nil
	case "!!bool":
		switch {
		case strings.EqualFold(node.Value, "true"):
			return true, nil
		case strings.EqualFold(node.Value, "false"):
			return false, nil
		default:
			return nil, fmt.Errorf("parse YAML: invalid boolean %q", node.Value)
		}
	case "!!int":
		literal := strings.ReplaceAll(node.Value, "_", "")
		integer, err := parseYAMLInteger(literal)
		if err != nil {
			return nil, fmt.Errorf("parse YAML: invalid integer %q", node.Value)
		}
		if integer.Sign() == 0 && strings.HasPrefix(literal, "-") {
			return nil, fmt.Errorf("parse YAML: negative-zero spelling %q is forbidden", node.Value)
		}
		value, _ := new(big.Float).SetInt(integer).Float64()
		if math.IsInf(value, 0) {
			return nil, fmt.Errorf("parse YAML: number %q is not a finite binary64 value", node.Value)
		}
		return value, nil
	case "!!float":
		literal := strings.ReplaceAll(node.Value, "_", "")
		value, err := parseNumber(literal)
		if err != nil {
			return nil, fmt.Errorf("parse YAML: %w", err)
		}
		return value, nil
	default:
		return nil, fmt.Errorf("parse YAML: resolved scalar tag %q is forbidden", node.Tag)
	}
}

func parseYAMLInteger(literal string) (*big.Int, error) {
	sign := ""
	unsigned := literal
	if strings.HasPrefix(unsigned, "+") || strings.HasPrefix(unsigned, "-") {
		sign = unsigned[:1]
		unsigned = unsigned[1:]
	}

	base := 10
	switch {
	case strings.HasPrefix(unsigned, "0b"):
		base, unsigned = 2, unsigned[2:]
	case strings.HasPrefix(unsigned, "0o"):
		base, unsigned = 8, unsigned[2:]
	case strings.HasPrefix(unsigned, "0x"):
		base, unsigned = 16, unsigned[2:]
	}
	if unsigned == "" {
		return nil, errors.New("missing integer digits")
	}
	integer, ok := new(big.Int).SetString(sign+unsigned, base)
	if !ok {
		return nil, errors.New("invalid integer")
	}
	return integer, nil
}

func looksSexagesimal(value string) bool {
	// Match YAML 1.1's integer base-60 shape. YAML 1.2 resolves it as a
	// string, but Appendix A.1 rejects this legacy numeric spelling.
	unsigned := value
	if strings.HasPrefix(unsigned, "+") || strings.HasPrefix(unsigned, "-") {
		unsigned = unsigned[1:]
	}
	parts := strings.Split(unsigned, ":")
	if len(parts) < 2 || parts[0] == "" || parts[0][0] < '1' || parts[0][0] > '9' {
		return false
	}
	for _, character := range parts[0][1:] {
		if (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	for _, part := range parts[1:] {
		if part == "" || len(part) > 2 {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
		group, _ := strconv.Atoi(part)
		if group > 59 {
			return false
		}
	}
	return true
}

var restrictedTimestampLayouts = []string{
	"2006-1-2T15:4:5.999999999Z07:00",
	"2006-1-2t15:4:5.999999999Z07:00",
	"2006-1-2 15:4:5.999999999",
	"2006-1-2",
}

func looksRestrictedTimestamp(value string) bool {
	if len(value) < len("0000-0-0") || value[4] != '-' {
		return false
	}
	for index := range 4 {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	for _, layout := range restrictedTimestampLayouts {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}
