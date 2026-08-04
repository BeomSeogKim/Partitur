package score

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
)

// ApplyPatch applies RFC 6902 operations to a canonical JSON value. It never
// mutates document or an operation value supplied by its caller.
func ApplyPatch(document any, operations []any) (any, error) {
	result := cloneJSON(document)
	for index, raw := range operations {
		operation, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("patch operation %d must be an object", index)
		}
		op, ok := operation["op"].(string)
		if !ok || op == "" {
			return nil, fmt.Errorf("patch operation %d has invalid op", index)
		}
		path, ok := operation["path"].(string)
		if !ok {
			return nil, fmt.Errorf("patch operation %d has invalid path", index)
		}
		if _, err := patchPointer(path); err != nil {
			return nil, fmt.Errorf("patch operation %d path: %w", index, err)
		}

		var err error
		switch op {
		case "add":
			value, present := operation["value"]
			if !present {
				err = errors.New("add requires value")
			} else {
				result, err = patchAdd(result, path, cloneJSON(value))
			}
		case "remove":
			result, err = patchRemove(result, path)
		case "replace":
			value, present := operation["value"]
			if !present {
				err = errors.New("replace requires value")
			} else {
				result, err = patchReplace(result, path, cloneJSON(value))
			}
		case "move", "copy":
			from, present := operation["from"].(string)
			if !present {
				err = fmt.Errorf("%s requires from", op)
				break
			}
			if _, pointerErr := patchPointer(from); pointerErr != nil {
				err = fmt.Errorf("from: %w", pointerErr)
				break
			}
			if op == "move" && pointerIsChild(path, from) {
				err = errors.New("move destination is within source")
				break
			}
			if op == "move" && path == from {
				break
			}
			var value any
			value, err = patchValue(result, from)
			if err == nil && op == "move" {
				result, err = patchRemove(result, from)
			}
			if err == nil {
				result, err = patchAdd(result, path, cloneJSON(value))
			}
		case "test":
			value, present := operation["value"]
			if !present {
				err = errors.New("test requires value")
				break
			}
			var actual any
			actual, err = patchValue(result, path)
			if err == nil && !canonicalEqual(actual, value) {
				err = errors.New("test value does not match")
			}
		default:
			err = fmt.Errorf("unsupported operation %q", op)
		}
		if err != nil {
			return nil, fmt.Errorf("patch operation %d (%s): %w", index, op, err)
		}
	}
	return result, nil
}

func patchAdd(document any, path string, value any) (any, error) {
	parts, _ := patchPointer(path)
	if len(parts) == 0 {
		return value, nil
	}
	parent, token, err := patchParent(document, parts)
	if err != nil {
		return nil, err
	}
	switch parent := parent.(type) {
	case map[string]any:
		parent[token] = value
	case []any:
		index, err := patchArrayIndex(token, len(parent), true)
		if err != nil {
			return nil, err
		}
		parent = append(parent, nil)
		copy(parent[index+1:], parent[index:])
		parent[index] = value
		if len(parts) == 1 {
			return parent, nil
		}
		if err := patchSetParent(document, parts[:len(parts)-1], parent); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("parent is not an object or array")
	}
	return document, nil
}

func patchReplace(document any, path string, value any) (any, error) {
	parts, _ := patchPointer(path)
	if len(parts) == 0 {
		return value, nil
	}
	parent, token, err := patchParent(document, parts)
	if err != nil {
		return nil, err
	}
	switch parent := parent.(type) {
	case map[string]any:
		if _, ok := parent[token]; !ok {
			return nil, errors.New("object member does not exist")
		}
		parent[token] = value
	case []any:
		index, err := patchArrayIndex(token, len(parent), false)
		if err != nil {
			return nil, err
		}
		parent[index] = value
	default:
		return nil, errors.New("parent is not an object or array")
	}
	return document, nil
}

func patchRemove(document any, path string) (any, error) {
	parts, _ := patchPointer(path)
	if len(parts) == 0 {
		return nil, nil
	}
	parent, token, err := patchParent(document, parts)
	if err != nil {
		return nil, err
	}
	switch parent := parent.(type) {
	case map[string]any:
		if _, ok := parent[token]; !ok {
			return nil, errors.New("object member does not exist")
		}
		delete(parent, token)
	case []any:
		index, err := patchArrayIndex(token, len(parent), false)
		if err != nil {
			return nil, err
		}
		copy(parent[index:], parent[index+1:])
		parent[len(parent)-1] = nil
		parent = parent[:len(parent)-1]
		if len(parts) == 1 {
			return parent, nil
		}
		if err := patchSetParent(document, parts[:len(parts)-1], parent); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("parent is not an object or array")
	}
	return document, nil
}

func patchValue(document any, path string) (any, error) {
	parts, _ := patchPointer(path)
	current := document
	for _, token := range parts {
		switch value := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = value[token]
			if !ok {
				return nil, errors.New("object member does not exist")
			}
		case []any:
			index, err := patchArrayIndex(token, len(value), false)
			if err != nil {
				return nil, err
			}
			current = value[index]
		default:
			return nil, errors.New("path traverses scalar")
		}
	}
	return current, nil
}

func patchParent(document any, parts []string) (any, string, error) {
	if len(parts) == 0 {
		return nil, "", errors.New("root has no parent")
	}
	parentPath := ""
	for _, part := range parts[:len(parts)-1] {
		parentPath += "/" + escapePointerToken(part)
	}
	parent, err := patchValue(document, parentPath)
	return parent, parts[len(parts)-1], err
}

func patchSetParent(document any, parts []string, replacement any) error {
	if len(parts) == 0 {
		return errors.New("array root replacement is unsupported")
	}
	parent, token, err := patchParent(document, parts)
	if err != nil {
		return err
	}
	switch parent := parent.(type) {
	case map[string]any:
		parent[token] = replacement
	case []any:
		index, err := patchArrayIndex(token, len(parent), false)
		if err != nil {
			return err
		}
		parent[index] = replacement
	default:
		return errors.New("parent is not an object or array")
	}
	return nil
}

func patchPointer(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, errors.New("must be an RFC 6901 pointer")
	}
	parts := strings.Split(path[1:], "/")
	for index, part := range parts {
		var builder strings.Builder
		for offset := 0; offset < len(part); offset++ {
			if part[offset] != '~' {
				builder.WriteByte(part[offset])
				continue
			}
			if offset+1 == len(part) || (part[offset+1] != '0' && part[offset+1] != '1') {
				return nil, errors.New("invalid ~ escape")
			}
			offset++
			if part[offset] == '0' {
				builder.WriteByte('~')
			} else {
				builder.WriteByte('/')
			}
		}
		parts[index] = builder.String()
	}
	return parts, nil
}

func escapePointerToken(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

func patchArrayIndex(token string, length int, add bool) (int, error) {
	if add && token == "-" {
		return length, nil
	}
	if token == "" || token == "-" || (len(token) > 1 && token[0] == '0') {
		return 0, errors.New("invalid array index")
	}
	for _, character := range token {
		if character < '0' || character > '9' {
			return 0, errors.New("invalid array index")
		}
	}
	index, err := strconv.Atoi(token)
	if err != nil || index < 0 || index > length || (!add && index == length) {
		return 0, errors.New("array index is out of bounds")
	}
	return index, nil
}

func pointerIsChild(path, parent string) bool {
	return parent != "" && strings.HasPrefix(path, parent+"/")
}

func canonicalEqual(left, right any) bool {
	leftBytes, leftErr := canonical.Encode(left)
	rightBytes, rightErr := canonical.Encode(right)
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
}
