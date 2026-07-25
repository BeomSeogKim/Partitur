package canonical

import (
	"math"
	"testing"
)

func TestPublishedCanonicalizationVector(t *testing.T) {
	value, err := ParseJSON([]byte(`{
	  "numbers": [333333333.33333329, 1E30, 4.50, 2e-3, 0.000000000000000000000000001],
	  "string": "\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/",
	  "literals": [null, true, false]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	encoded, err := Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	const expected = `{"literals":[null,true,false],"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],"string":"€$\u000f\nA'B\"\\\\\"/"}`
	if string(encoded) != expected {
		t.Fatalf("encoded = %q, want %q", encoded, expected)
	}
}

func TestUTF16ObjectKeyOrdering(t *testing.T) {
	encoded, err := Encode(map[string]any{
		"\ue000":     "BMP",
		"\U00010000": "surrogate pair",
	})
	if err != nil {
		t.Fatal(err)
	}
	const expected = `{"𐀀":"surrogate pair","":"BMP"}`
	if string(encoded) != expected {
		t.Fatalf("encoded = %q, want %q", encoded, expected)
	}
}

func TestNoUnicodeNormalization(t *testing.T) {
	composed, err := Encode(map[string]any{"value": "\u00c5"})
	if err != nil {
		t.Fatal(err)
	}
	decomposed, err := Encode(map[string]any{"value": "A\u030a"})
	if err != nil {
		t.Fatal(err)
	}
	if string(composed) == string(decomposed) {
		t.Fatalf("composed and decomposed encodings are equal: %q", composed)
	}
}

func TestEncodeLeavesLineAndParagraphSeparatorsUnescaped(t *testing.T) {
	encoded, err := Encode("line\u2028paragraph\u2029end")
	if err != nil {
		t.Fatal(err)
	}
	const expected = "\"line\u2028paragraph\u2029end\""
	if string(encoded) != expected {
		t.Fatalf("encoded = %q, want %q", encoded, expected)
	}
}

func TestProgrammaticNegativeZeroEncodesAsZero(t *testing.T) {
	encoded, err := Encode(math.Copysign(0, -1))
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "0" {
		t.Fatalf("encoded = %q, want 0", encoded)
	}
}

func TestEncodeRejectsNonFiniteNumbers(t *testing.T) {
	tests := []struct {
		name  string
		value float64
	}{
		{name: "NaN", value: math.NaN()},
		{name: "positive infinity", value: math.Inf(1)},
		{name: "negative infinity", value: math.Inf(-1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Encode(test.value); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestEncodeRejectsInvalidASTValues(t *testing.T) {
	if _, err := Encode(1); err == nil {
		t.Fatal("integer bypassed binary64 AST")
	}
	if _, err := Encode(string([]byte{0xff})); err == nil {
		t.Fatal("invalid UTF-8 string was accepted")
	}
}
