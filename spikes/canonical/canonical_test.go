package canonical

import (
	"crypto/sha256"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
)

func TestRFC8785AppendixBNumbers(t *testing.T) {
	vectors := []struct {
		bits     uint64
		expected string
	}{
		{0x0000000000000000, "0"},
		{0x8000000000000000, "0"},
		{0x0000000000000001, "5e-324"},
		{0x8000000000000001, "-5e-324"},
		{0x7fefffffffffffff, "1.7976931348623157e+308"},
		{0xffefffffffffffff, "-1.7976931348623157e+308"},
		{0x4340000000000000, "9007199254740992"},
		{0xc340000000000000, "-9007199254740992"},
		{0x4430000000000000, "295147905179352830000"},
		{0x44b52d02c7e14af5, "9.999999999999997e+22"},
		{0x44b52d02c7e14af6, "1e+23"},
		{0x44b52d02c7e14af7, "1.0000000000000001e+23"},
		{0x444b1ae4d6e2ef4e, "999999999999999700000"},
		{0x444b1ae4d6e2ef4f, "999999999999999900000"},
		{0x444b1ae4d6e2ef50, "1e+21"},
		{0x3eb0c6f7a0b5ed8c, "9.999999999999997e-7"},
		{0x3eb0c6f7a0b5ed8d, "0.000001"},
		{0x41b3de4355555553, "333333333.3333332"},
		{0x41b3de4355555554, "333333333.33333325"},
		{0x41b3de4355555555, "333333333.3333333"},
		{0x41b3de4355555556, "333333333.3333334"},
		{0x41b3de4355555557, "333333333.33333343"},
		{0xbecbf647612f3696, "-0.0000033333333333333333"},
		{0x43143ff3c1cb0959, "1424953923781206.2"},
	}
	for _, vector := range vectors {
		actual, err := appendNumber(nil, math.Float64frombits(vector.bits))
		if err != nil {
			t.Fatalf("%016x: %v", vector.bits, err)
		}
		if string(actual) != vector.expected {
			t.Errorf("%016x: got %q, want %q", vector.bits, actual, vector.expected)
		}
	}
	for _, bits := range []uint64{0x7fffffffffffffff, 0x7ff0000000000000, 0xfff0000000000000} {
		if _, err := appendNumber(nil, math.Float64frombits(bits)); err == nil {
			t.Errorf("%016x: expected rejection", bits)
		}
	}
}

func TestPublishedFirst1000NumberVectorChecksum(t *testing.T) {
	hash := sha256.New()
	for index, bits := range first1000NumberBits() {
		number, err := appendNumber(nil, math.Float64frombits(bits))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(hash, "%x,%s\n", bits, number)
		if index == 999 {
			break
		}
	}
	const expected = "be18b62b6f69cdab33a7e0dae0d9cfa869fda80ddc712221570f9f40a5878687"
	if actual := fmt.Sprintf("%x", hash.Sum(nil)); actual != expected {
		t.Fatalf("first 1000 number-vector checksum = %s, want %s", actual, expected)
	}
}

func TestPublishedCanonicalizationVector(t *testing.T) {
	input := []byte(`{
	  "numbers": [333333333.33333329, 1E30, 4.50, 2e-3, 0.000000000000000000000000001],
	  "string": "\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/",
	  "literals": [null, true, false]
	}`)
	value, err := ParseJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"literals":[null,true,false],"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],"string":"€$\u000f\nA'B\"\\\\\"/"}`
	if string(actual) != expected {
		t.Fatalf("got:\n%s\nwant:\n%s", actual, expected)
	}
}

func TestUTF16OrderingDiffersFromGoStringOrdering(t *testing.T) {
	// U+10000 becomes D800 DC00 in UTF-16 and therefore sorts before U+E000.
	values := []string{"\ue000", "\U00010000"}
	goOrder := slices.Clone(values)
	slices.Sort(goOrder)
	jcsOrder := slices.Clone(values)
	sortUTF16(jcsOrder)
	if slices.Equal(goOrder, jcsOrder) {
		t.Fatalf("expected observable ordering difference, both were %q", goOrder)
	}
	if jcsOrder[0] != "\U00010000" || goOrder[0] != "\ue000" {
		t.Fatalf("Go order=%q JCS order=%q", goOrder, jcsOrder)
	}
}

func TestNoUnicodeNormalization(t *testing.T) {
	composed, _ := Encode(map[string]any{"v": "\u00c5"})
	decomposed, _ := Encode(map[string]any{"v": "A\u030a"})
	if string(composed) == string(decomposed) {
		t.Fatal("canonically equivalent Unicode spellings were normalized")
	}
}

func TestNumericPolicySplitAndRawRejections(t *testing.T) {
	extension, err := ParseJSON([]byte(`{"fraction":1.25,"huge":1e300}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Encode(extension); err != nil {
		t.Fatal(err)
	}
	for _, input := range [][]byte{
		[]byte(`-0`),
		[]byte(`-0.0`),
		[]byte(`1e9999`),
		[]byte(`1e-9999`),
		[]byte(`{"a":1,"a":2}`),
		[]byte(`"\udead"`),
		{'"', 0xff, '"'},
	} {
		if _, err := ParseJSON(input); err == nil {
			t.Errorf("%q: expected rejection", input)
		}
	}
	for _, value := range []float64{-float64(1<<53 - 1), 0, float64(1<<53 - 1)} {
		if err := ValidateSafeInteger(value); err != nil {
			t.Errorf("%v: %v", value, err)
		}
	}
	for _, value := range []float64{1.5, float64(1 << 53), -float64(1 << 53)} {
		if err := ValidateSafeInteger(value); err == nil {
			t.Errorf("%v: expected schema rejection", value)
		}
	}
}

func TestYAMLCorpus(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		want      string
		wantError string
	}{
		{"clip", "v: |\n  line\n", `{"v":"line\n"}`, ""},
		{"strip", "v: |-\n  line\n", `{"v":"line"}`, ""},
		{"keep", "v: |+\n  line\n\n", `{"v":"line\n\n"}`, ""},
		{"folded_clip", "v: >\n  one\n  two\n", `{"v":"one two\n"}`, ""},
		{"quoted_timestamp", "v: \"2026-07-25T10:20:30Z\"\n", `{"v":"2026-07-25T10:20:30Z"}`, ""},
		{"explicit_string", "v: !!str 2026-07-25\n", `{"v":"2026-07-25"}`, ""},
		{"yaml12_strings", "v: [yes, no, on, off]\n", `{"v":["yes","no","on","off"]}`, ""},
		{"bool_case", "v: [TRUE, False]\n", `{"v":[true,false]}`, ""},
		{"integer_forms", "v: [0x10, 0o20, 0b10000, 1_000]\n", `{"v":[16,16,16,1000]}`, ""},
		{"fraction", "v: 1.25\n", `{"v":1.25}`, ""},
		{"implicit_timestamp", "v: 2026-07-25T10:20:30Z\n", "", "tag \"!!timestamp\""},
		{"binary", "v: !!binary SGVsbG8=\n", "", "explicit tag \"!!binary\""},
		{"sexagesimal_policy", "v: 12:34:56\n", "", "sexagesimal-looking"},
		{"custom_tag", "v: !money 12\n", "", "explicit tag \"!money\""},
		{"anchor", "v: &a 1\nw: *a\n", "", "anchors and aliases"},
		{"merge", "base: &b {x: 1}\nv: {<<: *b}\n", "", "anchors and aliases"},
		{"duplicate", "v: 1\nv: 2\n", "", "duplicate mapping key"},
		{"nan", "v: .nan\n", "", "not a finite"},
		{"infinity", "v: .inf\n", "", "not a finite"},
		{"negative_zero", "v: -0.0\n", "", "negative zero"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, err := ParseYAML([]byte(test.yaml))
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error=%v, want substring %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			actual, err := Encode(value)
			if err != nil {
				t.Fatal(err)
			}
			if string(actual) != test.want {
				t.Fatalf("got %q, want %q", actual, test.want)
			}
		})
	}
}

func first1000NumberBits() []uint64 {
	static := []uint64{
		0x0000000000000000, 0x8000000000000000, 0x0000000000000001, 0x8000000000000001,
		0xc46696695dbd1cc3, 0xc43211ede4974a35, 0xc3fce97ca0f21056, 0xc3c7213080c1a6ac,
		0xc39280f39a348556, 0xc35d9b1f5d20d557, 0xc327af4c4a80aaac, 0xc2f2f2a36ecd5556,
		0xc2be51057e155558, 0xc28840d131aaaaac, 0xc253670dc1555557, 0xc21f0b4935555557,
		0xc1e8d5d42aaaaaac, 0xc1b3de4355555556, 0xc17fca0555555556, 0xc1496e6aaaaaaaab,
		0xc114585555555555, 0xc0e046aaaaaaaaab, 0xc0aa0aaaaaaaaaaa, 0xc074d55555555555,
		0xc040aaaaaaaaaaab, 0xc00aaaaaaaaaaaab, 0xbfd5555555555555, 0xbfa1111111111111,
		0xbf6b4e81b4e81b4f, 0xbf35d867c3ece2a5, 0xbf0179ec9cbd821e, 0xbecbf647612f3696,
		0xbe965e9f80f29212, 0xbe61e54c672874db, 0xbe2ca213d840baf8, 0xbdf6e80fe033c8c6,
		0xbdc2533fe68fd3d2, 0xbd8d51ffd74c861c, 0xbd5774ccac3d3817, 0xbd22c3d6f030f9ac,
		0xbcee0624b3818f79, 0xbcb804ea293472c7, 0xbc833721ba905bd3, 0xbc4ebe9c5db3c61e,
		0xbc18987d17c304e5, 0xbbe3ad30dfcf371d, 0xbbaf7b816618582f, 0xbb792f9ab81379bf,
		0xbb442615600f9499, 0xbb101e77800c76e1, 0xbad9ca58cce0be35, 0xbaa4a1e0a3e6fe90,
		0xba708180831f320d, 0xba3a68cd9e985016, 0x446696695dbd1cc3, 0x443211ede4974a35,
		0x43fce97ca0f21056, 0x43c7213080c1a6ac, 0x439280f39a348556, 0x435d9b1f5d20d557,
		0x4327af4c4a80aaac, 0x42f2f2a36ecd5556, 0x42be51057e155558, 0x428840d131aaaaac,
		0x4253670dc1555557, 0x421f0b4935555557, 0x41e8d5d42aaaaaac, 0x41b3de4355555556,
		0x417fca0555555556, 0x41496e6aaaaaaaab, 0x4114585555555555, 0x40e046aaaaaaaaab,
		0x40aa0aaaaaaaaaaa, 0x4074d55555555555, 0x4040aaaaaaaaaaab, 0x400aaaaaaaaaaaab,
		0x3fd5555555555555, 0x3fa1111111111111, 0x3f6b4e81b4e81b4f, 0x3f35d867c3ece2a5,
		0x3f0179ec9cbd821e, 0x3ecbf647612f3696, 0x3e965e9f80f29212, 0x3e61e54c672874db,
		0x3e2ca213d840baf8, 0x3df6e80fe033c8c6, 0x3dc2533fe68fd3d2, 0x3d8d51ffd74c861c,
		0x3d5774ccac3d3817, 0x3d22c3d6f030f9ac, 0x3cee0624b3818f79, 0x3cb804ea293472c7,
		0x3c833721ba905bd3, 0x3c4ebe9c5db3c61e, 0x3c18987d17c304e5, 0x3be3ad30dfcf371d,
		0x3baf7b816618582f, 0x3b792f9ab81379bf, 0x3b442615600f9499, 0x3b101e77800c76e1,
		0x3ad9ca58cce0be35, 0x3aa4a1e0a3e6fe90, 0x3a708180831f320d, 0x3a3a68cd9e985016,
		0x4024000000000000, 0x4014000000000000, 0x3fe0000000000000, 0x3fa999999999999a,
		0x3f747ae147ae147b, 0x3f40624dd2f1a9fc, 0x3f0a36e2eb1c432d, 0x3ed4f8b588e368f1,
		0x3ea0c6f7a0b5ed8d, 0x3e6ad7f29abcaf48, 0x3e35798ee2308c3a, 0x3ed539223589fa95,
		0x3ed4ff26cd5a7781, 0x3ed4f95a762283ff, 0x3ed4f8c60703520c, 0x3ed4f8b72f19cd0d,
		0x3ed4f8b5b31c0c8d, 0x3ed4f8b58d1c461a, 0x3ed4f8b5894f7f0e, 0x3ed4f8b588ee37f3,
		0x3ed4f8b588e47da4, 0x3ed4f8b588e3849c, 0x3ed4f8b588e36bb5, 0x3ed4f8b588e36937,
		0x3ed4f8b588e368f8, 0x3ed4f8b588e368f1, 0x3ff0000000000000, 0xbff0000000000000,
		0xbfeffffffffffffa, 0xbfeffffffffffffb, 0x3feffffffffffffa, 0x3feffffffffffffb,
		0x3feffffffffffffc, 0x3feffffffffffffe, 0xbfefffffffffffff, 0xbfefffffffffffff,
		0x3fefffffffffffff, 0x3fefffffffffffff, 0x3fd3333333333332, 0x3fd3333333333333,
		0x3fd3333333333334, 0x0010000000000000, 0x000ffffffffffffd, 0x000fffffffffffff,
		0x7fefffffffffffff, 0xffefffffffffffff, 0x4340000000000000, 0xc340000000000000,
		0x4430000000000000, 0x44b52d02c7e14af5, 0x44b52d02c7e14af6, 0x44b52d02c7e14af7,
		0x444b1ae4d6e2ef4e, 0x444b1ae4d6e2ef4f, 0x444b1ae4d6e2ef50, 0x3eb0c6f7a0b5ed8c,
		0x3eb0c6f7a0b5ed8d, 0x41b3de4355555553, 0x41b3de4355555554, 0x41b3de4355555555,
		0x41b3de4355555556, 0x41b3de4355555557, 0xbecbf647612f3696, 0x43143ff3c1cb0959,
	}
	for index := 0; len(static) < 1000; index++ {
		static = append(static, 0x0010000000000000+uint64(index))
	}
	return static
}
