package cast

import (
	"slices"
	"strconv"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
)

// Resolve parses, layers, defaults, and validates cast layers. Every source
// layer is schema-checked. Whole performer and binding objects from the first
// layer that declares an id win; no field is deep-merged.
func Resolve(layers []Layer) (*Cast, []Diagnostic) {
	documents := make([]layerDocument, 0, len(layers))
	var diagnostics []Diagnostic
	graphUsable := true
	for _, layer := range layers {
		value, err := canonical.ParseYAML(layer.Data)
		if err != nil {
			addDiagnostic(&diagnostics, RuleIngress, layer.Origin, "",
				"invalid_restricted_yaml")
			graphUsable = false
			continue
		}
		decoder := layerDecoder{origin: layer.Origin}
		document, usable := decoder.decode(value)
		diagnostics = append(diagnostics, decoder.diagnostics...)
		if usable {
			documents = append(documents, document)
		} else {
			graphUsable = false
		}
	}

	resolved := &Cast{
		performers:         make(map[string]performer),
		bindings:           make(map[string]binding),
		performersComplete: true,
	}
	performersOpen := true
	bindingsOpen := true
	for _, document := range documents {
		if performersOpen {
			if !document.PerformersUsable {
				performersOpen = false
				resolved.performersComplete = false
			} else {
				for id, value := range document.Performers {
					if _, exists := resolved.performers[id]; !exists {
						resolved.performers[id] = clonePerformer(value)
					}
				}
			}
		}
		if bindingsOpen {
			if !document.BindingsUsable {
				bindingsOpen = false
			} else {
				for partID, value := range document.Bindings {
					if _, exists := resolved.bindings[partID]; !exists {
						resolved.bindings[partID] = cloneBinding(value)
					}
				}
			}
		}
	}
	if graphUsable {
		validateStatic(resolved, &diagnostics)
	}
	sortDiagnostics(diagnostics)
	if len(diagnostics) != 0 {
		return nil, diagnostics
	}
	return resolved, nil
}

type layerDecoder struct {
	origin      string
	diagnostics []Diagnostic
}

func (d *layerDecoder) decode(value any) (layerDocument, bool) {
	root, ok := d.object(value, "")
	if !ok {
		return layerDocument{}, false
	}
	d.fields(root, "", "cast", "performers", "bindings")
	version, present := d.requiredString(root, "", "cast")
	if present && version != "0.1" {
		d.schema("/cast", "invalid_value")
	}
	result := layerDocument{
		Performers:       make(map[string]performer),
		Bindings:         make(map[string]binding),
		PerformersUsable: true,
		BindingsUsable:   true,
	}
	if value, present := root["performers"]; present {
		result.Performers, result.PerformersUsable = d.decodePerformers(value)
	}
	if value, present := root["bindings"]; present {
		result.Bindings, result.BindingsUsable = d.decodeBindings(value)
	}
	return result, true
}

func (d *layerDecoder) decodePerformers(value any) (map[string]performer, bool) {
	object, ok := d.object(value, "/performers")
	if !ok {
		return nil, false
	}
	result := make(map[string]performer, len(object))
	for id, value := range object {
		pointer := pointerJoin("/performers", id)
		entry, ok := d.object(value, pointer)
		if !ok {
			result[id] = performer{Origin: d.origin}
			continue
		}
		d.fields(entry, pointer,
			"adapter", "model", "allow_advisory_enforcement", "extensions")
		adapter, _ := d.requiredString(entry, pointer, "adapter")
		model, _ := d.requiredString(entry, pointer, "model")
		advisory := false
		if value := d.optionalBool(entry, pointer, "allow_advisory_enforcement"); value != nil {
			advisory = *value
		}
		var extensions map[string]any
		if value, present := entry["extensions"]; present {
			if decoded, ok := d.object(value, pointer+"/extensions"); ok {
				extensions = cloneMap(decoded)
			}
		}
		result[id] = performer{
			Adapter:                  adapter,
			Model:                    model,
			AllowAdvisoryEnforcement: advisory,
			Extensions:               extensions,
			Origin:                   d.origin,
		}
	}
	return result, true
}

func (d *layerDecoder) decodeBindings(value any) (map[string]binding, bool) {
	object, ok := d.object(value, "/bindings")
	if !ok {
		return nil, false
	}
	result := make(map[string]binding, len(object))
	for partID, value := range object {
		pointer := pointerJoin("/bindings", partID)
		entry, ok := d.object(value, pointer)
		if !ok {
			result[partID] = binding{Origin: d.origin}
			continue
		}
		d.fields(entry, pointer, "performer", "fallbacks")
		performerID, performerValid := d.requiredString(entry, pointer, "performer")
		fallbacks := []string{}
		fallbacksValid := []bool{}
		if value, present := entry["fallbacks"]; present {
			fallbacks, fallbacksValid, _ =
				d.stringArray(value, pointer+"/fallbacks")
		}
		result[partID] = binding{
			Performer:      performerID,
			PerformerValid: performerValid,
			Fallbacks:      fallbacks,
			FallbacksValid: fallbacksValid,
			Origin:         d.origin,
			Valid:          true,
		}
	}
	return result, true
}

func (d *layerDecoder) requiredString(
	object map[string]any,
	base, name string,
) (string, bool) {
	value, present := object[name]
	pointer := pointerJoin(base, name)
	if !present {
		d.schema(pointer, "required")
		return "", false
	}
	return d.string(value, pointer)
}

func (d *layerDecoder) optionalBool(
	object map[string]any,
	base, name string,
) *bool {
	value, present := object[name]
	if !present {
		return nil
	}
	pointer := pointerJoin(base, name)
	if value == nil {
		d.schema(pointer, "must_not_be_null")
		return nil
	}
	decoded, ok := value.(bool)
	if !ok {
		d.schema(pointer, "expected_boolean")
		return nil
	}
	return &decoded
}

func (d *layerDecoder) string(value any, pointer string) (string, bool) {
	if value == nil {
		d.schema(pointer, "must_not_be_null")
		return "", false
	}
	decoded, ok := value.(string)
	if !ok {
		d.schema(pointer, "expected_string")
		return "", false
	}
	return decoded, true
}

func (d *layerDecoder) stringArray(
	value any,
	pointer string,
) ([]string, []bool, bool) {
	values, ok := d.array(value, pointer)
	if !ok {
		return nil, nil, false
	}
	result := make([]string, len(values))
	valid := make([]bool, len(values))
	for index, value := range values {
		decoded, ok := d.string(value, indexPointer(pointer, index))
		if ok {
			result[index] = decoded
			valid[index] = true
		}
	}
	return result, valid, true
}

func (d *layerDecoder) object(value any, pointer string) (map[string]any, bool) {
	if value == nil {
		d.schema(pointer, "must_not_be_null")
		return nil, false
	}
	decoded, ok := value.(map[string]any)
	if !ok {
		d.schema(pointer, "expected_object")
		return nil, false
	}
	return decoded, true
}

func (d *layerDecoder) array(value any, pointer string) ([]any, bool) {
	if value == nil {
		d.schema(pointer, "must_not_be_null")
		return nil, false
	}
	decoded, ok := value.([]any)
	if !ok {
		d.schema(pointer, "expected_array")
		return nil, false
	}
	return decoded, true
}

func (d *layerDecoder) fields(
	object map[string]any,
	pointer string,
	allowed ...string,
) {
	set := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		set[name] = struct{}{}
	}
	for name := range object {
		if _, ok := set[name]; !ok {
			d.schema(pointerJoin(pointer, name), "unknown_field")
		}
	}
}

func (d *layerDecoder) schema(pointer, detail string) {
	addDiagnostic(&d.diagnostics, RuleSchema, d.origin, pointer, detail)
}

func validateStatic(resolved *Cast, diagnostics *[]Diagnostic) {
	for partID, binding := range resolved.bindings {
		base := pointerJoin("/bindings", partID)
		if !binding.Valid {
			continue
		}
		if binding.PerformerValid {
			_, exists := resolved.performers[binding.Performer]
			if !exists && resolved.performersComplete {
				addDiagnostic(diagnostics, RuleStatic, binding.Origin,
					base+"/performer", "performer_missing")
			}
		}
		seen := make(map[string]struct{}, len(binding.Fallbacks))
		for index, fallback := range binding.Fallbacks {
			if index >= len(binding.FallbacksValid) ||
				!binding.FallbacksValid[index] {
				continue
			}
			pointer := indexPointer(base+"/fallbacks", index)
			if binding.PerformerValid && fallback == binding.Performer {
				addDiagnostic(diagnostics, RuleStatic, binding.Origin,
					pointer, "fallback_is_primary")
			}
			if _, duplicate := seen[fallback]; duplicate {
				addDiagnostic(diagnostics, RuleStatic, binding.Origin,
					pointer, "duplicate_fallback")
			}
			seen[fallback] = struct{}{}
			_, exists := resolved.performers[fallback]
			if !exists && resolved.performersComplete {
				addDiagnostic(diagnostics, RuleStatic, binding.Origin,
					pointer, "performer_missing")
			}
		}
	}
}

func addDiagnostic(
	diagnostics *[]Diagnostic,
	rule RuleID,
	origin, pointer, detail string,
) {
	*diagnostics = append(*diagnostics, Diagnostic{
		Rule:    rule,
		Origin:  origin,
		Pointer: pointer,
		Detail:  detail,
	})
}

func sortDiagnostics(diagnostics []Diagnostic) {
	slices.SortFunc(diagnostics, func(left, right Diagnostic) int {
		if order := diagnosticRank(left.Rule) - diagnosticRank(right.Rule); order != 0 {
			return order
		}
		if order := strings.Compare(left.Origin, right.Origin); order != 0 {
			return order
		}
		if order := strings.Compare(left.Pointer, right.Pointer); order != 0 {
			return order
		}
		return strings.Compare(left.Detail, right.Detail)
	})
}

func diagnosticRank(rule RuleID) int {
	switch rule {
	case RuleIngress:
		return 0
	case RuleSchema:
		return 1
	case RuleStatic:
		return 2
	case RuleScore:
		return 3
	default:
		return 100
	}
}

func pointerJoin(base, token string) string {
	escaped := strings.ReplaceAll(token, "~", "~0")
	escaped = strings.ReplaceAll(escaped, "/", "~1")
	return base + "/" + escaped
}

func indexPointer(base string, index int) string {
	return base + "/" + strconv.Itoa(index)
}

func clonePerformer(value performer) performer {
	value.Extensions = cloneMap(value.Extensions)
	return value
}

func cloneBinding(value binding) binding {
	value.Fallbacks = slices.Clone(value.Fallbacks)
	value.FallbacksValid = slices.Clone(value.FallbacksValid)
	return value
}

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = cloneJSON(item)
	}
	return result
}

func cloneJSON(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneMap(value)
	case []any:
		result := make([]any, len(value))
		for index, item := range value {
			result[index] = cloneJSON(item)
		}
		return result
	default:
		return value
	}
}
