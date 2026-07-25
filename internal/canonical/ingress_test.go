package canonical

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

func TestParseJSONIngressRejections(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
	}{
		{name: "NaN", input: []byte(`NaN`)},
		{name: "positive infinity", input: []byte(`Infinity`)},
		{name: "negative infinity", input: []byte(`-Infinity`)},
		{name: "overflow", input: []byte(`1e9999`)},
		{name: "positive underflow to zero", input: []byte(`1e-9999`)},
		{name: "negative underflow to zero", input: []byte(`-1e-9999`)},
		{name: "negative zero integer", input: []byte(`-0`)},
		{name: "negative zero decimal", input: []byte(`-0.0`)},
		{name: "negative zero exponent", input: []byte(`-0e100`)},
		{name: "negative zero fractional exponent", input: []byte(`-0.000e-100`)},
		{name: "lone high surrogate", input: []byte(`"\ud800"`)},
		{name: "lone low surrogate", input: []byte(`"\udc00"`)},
		{name: "duplicate object name", input: []byte(`{"a":1,"a":2}`)},
		{name: "invalid UTF-8", input: []byte{'"', 0xff, '"'}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseJSON(test.input); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestParseYAMLIngressRejections(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "NaN", input: "value: .nan\n"},
		{name: "positive infinity", input: "value: .inf\n"},
		{name: "negative infinity", input: "value: -.inf\n"},
		{name: "overflow", input: "value: 1e9999\n"},
		{name: "underflow to zero", input: "value: 1e-9999\n"},
		{name: "negative underflow to zero", input: "value: -1e-9999\n"},
		{name: "negative zero integer", input: "value: -0\n"},
		{name: "negative zero float", input: "value: -0.0\n"},
		{name: "negative zero exponent", input: "value: -0e10\n"},
		{name: "negative zero hexadecimal", input: "value: -0x0\n"},
		{name: "negative zero octal", input: "value: -0o0\n"},
		{name: "negative zero binary", input: "value: -0b0\n"},
		{name: "lone high surrogate", input: "value: \"\\ud800\"\n"},
		{name: "lone low surrogate", input: "value: \"\\udc00\"\n"},
		{name: "duplicate mapping key", input: "value: 1\nvalue: 2\n"},
		{name: "anchor", input: "value: &held 1\n"},
		{name: "alias", input: "first: &held 1\nvalue: *held\n"},
		{name: "merge key", input: "value: {<<: {nested: true}}\n"},
		{name: "custom tag", input: "value: !money 12\n"},
		{name: "non-JSON collection tag", input: "value: !!set {one, two}\n"},
		{name: "disallowed resolved tag", input: "value: 2026-07-25T10:20:30Z\n"},
		{name: "sexagesimal spelling", input: "value: 12:34:56\n"},
		{name: "sexagesimal first-component underscore", input: "value: 1_2:34\n"},
		{name: "non-string mapping key", input: "1: value\n"},
		{name: "multiple documents", input: "value: 1\n---\nvalue: 2\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseYAML([]byte(test.input)); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestParseJSONAllowsValidSurrogatePair(t *testing.T) {
	value, err := ParseJSON([]byte(`"\ud800\udc00"`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `"𐀀"` {
		t.Fatalf("encoded = %q", encoded)
	}
}

func TestParseYAMLAllowsNonSexagesimalColonStrings(t *testing.T) {
	value, err := ParseYAML([]byte("values: [12:99, 0:00, 12:3_4]\n"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"values":["12:99","0:00","12:3_4"]}` {
		t.Fatalf("encoded = %q", encoded)
	}
}

func TestParseYAMLAllowsStringMergeNames(t *testing.T) {
	value, err := ParseYAML([]byte("\"<<\": 1\nnested:\n  !!str <<: 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"<<":1,"nested":{"<<":2}}` {
		t.Fatalf("encoded = %q", encoded)
	}
}

func TestPlainTimestampRestrictionDoesNotDependOnResolvedTag(t *testing.T) {
	node := &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   "!!str",
		Value: "2026-07-25T10:20:30Z",
	}
	if _, err := yamlScalar(node); err == nil {
		t.Fatal("plain timestamp resolved as !!str was accepted")
	}

	value, err := ParseYAML([]byte("quoted: \"2026-07-25\"\ntagged: !!str 2026-07-25\n"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	const expected = `{"quoted":"2026-07-25","tagged":"2026-07-25"}`
	if string(encoded) != expected {
		t.Fatalf("encoded = %q, want %q", encoded, expected)
	}
}

func TestJSONAndYAMLLargeIntegersProduceIdenticalASTs(t *testing.T) {
	tests := []struct {
		literal string
		value   float64
		encoded string
	}{
		{
			literal: "18446744073709551616",
			value:   18446744073709551616,
			encoded: `{"n":18446744073709552000}`,
		},
		{
			literal: "9223372036854775808",
			value:   9223372036854775808,
			encoded: `{"n":9223372036854776000}`,
		},
		{
			literal: "99999999999999999999999",
			value:   1e23,
			encoded: `{"n":1e+23}`,
		},
	}
	for _, test := range tests {
		t.Run(test.literal, func(t *testing.T) {
			jsonValue, err := ParseJSON([]byte(`{"n":` + test.literal + `}`))
			if err != nil {
				t.Fatal(err)
			}
			yamlValue, err := ParseYAML([]byte("n: " + test.literal + "\n"))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(yamlValue, jsonValue) {
				t.Fatalf("YAML AST = %#v, JSON AST = %#v", yamlValue, jsonValue)
			}
			if actual := yamlValue.(map[string]any)["n"]; actual != test.value {
				t.Fatalf("value = %#v, want %#v", actual, test.value)
			}

			jsonEncoding, err := Encode(jsonValue)
			if err != nil {
				t.Fatal(err)
			}
			yamlEncoding, err := Encode(yamlValue)
			if err != nil {
				t.Fatal(err)
			}
			if string(jsonEncoding) != test.encoded || string(yamlEncoding) != test.encoded {
				t.Fatalf("JSON = %q, YAML = %q, want %q", jsonEncoding, yamlEncoding, test.encoded)
			}
		})
	}
}

func TestParseYAMLRejectsInvalidUTF8(t *testing.T) {
	if _, err := ParseYAML([]byte{'v', 'a', 'l', 'u', 'e', ':', ' ', 0xff, '\n'}); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestParseYAMLAllowsSpecifiedScalarTags(t *testing.T) {
	value, err := ParseYAML([]byte(`
values:
  - !!str text
  - !!bool true
  - !!null null
  - !!int 7
  - !!float 1.5
sequence: !!seq [one, two]
mapping: !!map {key: value}
`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	const expected = `{"mapping":{"key":"value"},"sequence":["one","two"],"values":["text",true,null,7,1.5]}`
	if string(encoded) != expected {
		t.Fatalf("encoded = %q, want %q", encoded, expected)
	}
}

func TestParseYAMLPreservesBlockScalarChomping(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "clip", input: "value: |\n  line\n", expected: `{"value":"line\n"}`},
		{name: "strip", input: "value: |-\n  line\n", expected: `{"value":"line"}`},
		{name: "keep", input: "value: |+\n  line\n\n", expected: `{"value":"line\n\n"}`},
		{name: "folded clip", input: "value: >\n  one\n  two\n", expected: `{"value":"one two\n"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, err := ParseYAML([]byte(test.input))
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := Encode(value)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != test.expected {
				t.Fatalf("encoded = %q, want %q", encoded, test.expected)
			}
		})
	}
}

func TestParseYAMLUsesYAML12NumberForms(t *testing.T) {
	value, err := ParseYAML([]byte("values: [012, 0x10, 0o20, 0b10000, 1_000]\n"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"values":[12,16,16,16,1000]}` {
		t.Fatalf("encoded = %q", encoded)
	}
}

func TestValidateSafeInteger(t *testing.T) {
	for _, value := range []float64{-maxSafeInteger, 0, maxSafeInteger} {
		if err := ValidateSafeInteger(value); err != nil {
			t.Errorf("%v: %v", value, err)
		}
	}

	tests := []struct {
		name  string
		value float64
	}{
		{name: "fraction", value: 1.5},
		{name: "positive out of range", value: 1 << 53},
		{name: "negative out of range", value: -(1 << 53)},
		{name: "NaN", value: math.NaN()},
		{name: "positive infinity", value: math.Inf(1)},
		{name: "negative infinity", value: math.Inf(-1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateSafeInteger(test.value); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestOpaqueNumbersRemainFullFiniteBinary64(t *testing.T) {
	value, err := ParseJSON([]byte(`{"fraction":1.25,"outside_safe_integer":9007199254740992,"huge":1e300}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Encode(value); err != nil {
		t.Fatal(err)
	}

	numbers := value.(map[string]any)
	if err := ValidateSafeInteger(numbers["fraction"].(float64)); err == nil {
		t.Fatal("schema validation accepted a fraction")
	}
	if err := ValidateSafeInteger(numbers["outside_safe_integer"].(float64)); err == nil {
		t.Fatal("schema validation accepted an out-of-range integer")
	}
}

func TestParseJSONNegativeZeroErrorPreservesLexicalReason(t *testing.T) {
	_, err := ParseJSON([]byte(`-0.000e9999`))
	if err == nil || !strings.Contains(err.Error(), "negative-zero spelling") {
		t.Fatalf("error = %v", err)
	}
}
