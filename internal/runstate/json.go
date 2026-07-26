package runstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
)

func parseJSONExact(input []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if trailing := bytes.TrimSpace(input[decoder.InputOffset():]); len(trailing) != 0 {
		return nil, errors.New("trailing JSON value")
	}
	return value, nil
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
		value, err := strconv.ParseFloat(token.String(), 64)
		if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
			return nil, fmt.Errorf("invalid JSON number %q", token)
		}
		return value, nil
	case json.Delim:
		switch token {
		case '[':
			var values []any
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
			values := map[string]any{}
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				name, ok := nameToken.(string)
				if !ok {
					return nil, errors.New("JSON object name is not a string")
				}
				if _, duplicate := values[name]; duplicate {
					return nil, fmt.Errorf("duplicate JSON object name %q", name)
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
	return nil, fmt.Errorf("unsupported JSON token %T", token)
}

func validUintString(value string) bool {
	if value == "" {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}
