package canonical

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Encode serializes a validated JSON AST using RFC 8785 (JCS).
// The accepted values deliberately match the small JSON type system used by
// the spike: nil, bool, string, float64, []any, and map[string]any.
func Encode(value any) ([]byte, error) {
	return appendValue(nil, value)
}

// ParseJSON is the raw-JSON entrance used by the spike. It rejects duplicate
// object names, numbers that cannot be represented as finite binary64 values,
// and negative-zero spellings before returning the AST consumed by Encode.
func ParseJSON(input []byte) (any, error) {
	if err := validateJSONStringEncoding(input); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	value, err := decodeValue(decoder)
	if err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing JSON value")
	}
	return value, nil
}

func validateJSONStringEncoding(input []byte) error {
	for index := 0; index < len(input); index++ {
		if input[index] != '"' {
			if input[index] >= utf8.RuneSelf {
				return errors.New("non-ASCII byte outside JSON string")
			}
			continue
		}
		index++
		for ; index < len(input) && input[index] != '"'; index++ {
			character := input[index]
			if character < 0x20 {
				return errors.New("unescaped control character in JSON string")
			}
			if character >= utf8.RuneSelf {
				_, size := utf8.DecodeRune(input[index:])
				if size == 1 {
					return errors.New("invalid UTF-8 in JSON string")
				}
				index += size - 1
				continue
			}
			if character != '\\' {
				continue
			}
			index++
			if index >= len(input) {
				return errors.New("unterminated JSON escape")
			}
			if input[index] != 'u' {
				if !strings.ContainsRune(`"\/bfnrt`, rune(input[index])) {
					return errors.New("invalid JSON escape")
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
					return errors.New("high surrogate without low surrogate")
				}
				low, afterLow, err := readHexCodeUnit(input, index+3)
				if err != nil || low < 0xdc00 || low > 0xdfff {
					return errors.New("high surrogate without low surrogate")
				}
				index = afterLow - 1
			case codeUnit >= 0xdc00 && codeUnit <= 0xdfff:
				return errors.New("lone low surrogate")
			}
		}
		if index >= len(input) {
			return errors.New("unterminated JSON string")
		}
	}
	return nil
}

func readHexCodeUnit(input []byte, start int) (uint16, int, error) {
	if start+4 > len(input) {
		return 0, start, errors.New("short Unicode escape")
	}
	value, err := strconv.ParseUint(string(input[start:start+4]), 16, 16)
	if err != nil {
		return 0, start, errors.New("invalid Unicode escape")
	}
	return uint16(value), start + 4, nil
}

func decodeValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token := token.(type) {
	case nil, bool, string:
		return token, nil
	case json.Number:
		number, err := strconv.ParseFloat(token.String(), 64)
		if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
			return nil, fmt.Errorf("number %q is not a finite IEEE 754 binary64 value", token)
		}
		if number == 0 && strings.HasPrefix(token.String(), "-") {
			return nil, fmt.Errorf("negative zero %q is rejected", token)
		}
		if number == 0 && !lexicallyZero(token.String()) {
			return nil, fmt.Errorf("number %q underflows IEEE 754 binary64", token)
		}
		return number, nil
	case json.Delim:
		switch token {
		case '[':
			var values []any
			for decoder.More() {
				value, err := decodeValue(decoder)
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
			values := map[string]any{}
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
				value, err := decodeValue(decoder)
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
	return nil, fmt.Errorf("unsupported JSON token %T", token)
}

func lexicallyZero(value string) bool {
	mantissa := value
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
		sortUTF16(names)
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
		return nil, fmt.Errorf("unsupported JSON AST type %T", value)
	}
}

func appendString(output []byte, value string) ([]byte, error) {
	if !utf8.ValidString(value) {
		return nil, errors.New("string is not valid UTF-8")
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
				output = strconv.AppendInt(output, int64(character&0xf), 16)
			} else {
				output = utf8.AppendRune(output, character)
			}
		}
	}
	return append(output, '"'), nil
}

func appendNumber(output []byte, value float64) ([]byte, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, errors.New("NaN and Infinity are not JCS numbers")
	}
	if value == 0 {
		value = 0 // RFC 8785 Number::toString maps both zero signs to "0".
	}
	format := byte('f')
	absolute := math.Abs(value)
	if (absolute != 0 && absolute < 1e-6) || absolute >= 1e21 {
		format = 'e'
	}
	output = strconv.AppendFloat(output, value, format, -1, 64)
	if format == 'e' {
		length := len(output)
		if length >= 4 && output[length-4] == 'e' && output[length-3] == '-' && output[length-2] == '0' {
			output[length-2] = output[length-1]
			output = output[:length-1]
		}
	}
	return output, nil
}

func sortUTF16(values []string) {
	for index := 1; index < len(values); index++ {
		for cursor := index; cursor > 0 && utf16Less(values[cursor], values[cursor-1]); cursor-- {
			values[cursor], values[cursor-1] = values[cursor-1], values[cursor]
		}
	}
}

func utf16Less(left, right string) bool {
	leftUnits := utf16.Encode([]rune(left))
	rightUnits := utf16.Encode([]rune(right))
	limit := min(len(leftUnits), len(rightUnits))
	for index := range limit {
		if leftUnits[index] != rightUnits[index] {
			return leftUnits[index] < rightUnits[index]
		}
	}
	return len(leftUnits) < len(rightUnits)
}
