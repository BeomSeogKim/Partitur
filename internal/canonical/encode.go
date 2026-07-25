// Package canonical implements Partitur's canonical JSON encoding and
// versioned identity substrate from DESIGN Appendix A.1-A.3.
package canonical

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// Encode serializes a JSON AST using RFC 8785 (JCS).
//
// A JSON AST consists of nil, bool, string, float64, []any, and
// map[string]any. Numbers are binary64 values because numeric ingress has
// already discarded decimal lexical precision.
func Encode(value any) ([]byte, error) {
	return appendValue(nil, value)
}

func appendValue(output []byte, value any) ([]byte, error) {
	switch value := value.(type) {
	case nil:
		return append(output, "null"...), nil
	case bool:
		return strconv.AppendBool(output, value), nil
	case string:
		return appendString(output, value)
	case float64:
		return appendNumber(output, value)
	case []any:
		output = append(output, '[')
		for index, element := range value {
			if index != 0 {
				output = append(output, ',')
			}
			var err error
			output, err = appendValue(output, element)
			if err != nil {
				return nil, err
			}
		}
		return append(output, ']'), nil
	case map[string]any:
		names := make([]string, 0, len(value))
		for name := range value {
			names = append(names, name)
		}
		slices.SortFunc(names, compareUTF16)

		output = append(output, '{')
		for index, name := range names {
			if index != 0 {
				output = append(output, ',')
			}
			var err error
			output, err = appendString(output, name)
			if err != nil {
				return nil, err
			}
			output = append(output, ':')
			output, err = appendValue(output, value[name])
			if err != nil {
				return nil, err
			}
		}
		return append(output, '}'), nil
	default:
		return nil, fmt.Errorf("canonical JSON: unsupported AST type %T", value)
	}
}

func appendString(output []byte, value string) ([]byte, error) {
	if !utf8.ValidString(value) {
		return nil, errors.New("canonical JSON: string is not valid UTF-8")
	}

	output = append(output, '"')
	for _, character := range value {
		switch character {
		case '"', '\\':
			output = append(output, '\\', byte(character))
		case '\b':
			output = append(output, `\b`...)
		case '\t':
			output = append(output, `\t`...)
		case '\n':
			output = append(output, `\n`...)
		case '\f':
			output = append(output, `\f`...)
		case '\r':
			output = append(output, `\r`...)
		default:
			if character < 0x20 {
				output = append(output, `\u00`...)
				output = strconv.AppendInt(output, int64(character>>4), 16)
				output = strconv.AppendInt(output, int64(character&0x0f), 16)
			} else {
				output = utf8.AppendRune(output, character)
			}
		}
	}
	return append(output, '"'), nil
}

func appendNumber(output []byte, value float64) ([]byte, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, errors.New("canonical JSON: NaN and Infinity are not JCS numbers")
	}
	if value == 0 {
		value = 0 // ECMAScript Number::toString maps both zero signs to "0".
	}

	format := byte('f')
	absolute := math.Abs(value)
	if (absolute != 0 && absolute < 1e-6) || absolute >= 1e21 {
		format = 'e'
	}

	start := len(output)
	output = strconv.AppendFloat(output, value, format, -1, 64)
	if format == 'e' {
		output = trimExponentZero(output, start)
	}
	return output, nil
}

func trimExponentZero(output []byte, start int) []byte {
	exponent := slices.Index(output[start:], byte('e'))
	if exponent < 0 {
		return output
	}
	exponent += start + 1
	if exponent < len(output) && (output[exponent] == '+' || output[exponent] == '-') {
		exponent++
	}
	for exponent+1 < len(output) && output[exponent] == '0' {
		copy(output[exponent:], output[exponent+1:])
		output = output[:len(output)-1]
	}
	return output
}

func compareUTF16(left, right string) int {
	leftUnits := utf16.Encode([]rune(left))
	rightUnits := utf16.Encode([]rune(right))
	for index := 0; index < min(len(leftUnits), len(rightUnits)); index++ {
		if leftUnits[index] < rightUnits[index] {
			return -1
		}
		if leftUnits[index] > rightUnits[index] {
			return 1
		}
	}
	return len(leftUnits) - len(rightUnits)
}
