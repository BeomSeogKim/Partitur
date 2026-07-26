package adapter

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

func TestProtocolNegotiationAndFeatures(t *testing.T) {
	tests := []struct {
		name     string
		version  int
		features string
		want     []string
		wantKind DiagnosticKind
	}{
		{name: "protocol 1 absent", version: 1},
		{name: "protocol 1 empty rejected", version: 1, features: `,"features":[]`, wantKind: DiagnosticMalformedResponse},
		{name: "protocol 1 null rejected", version: 1, features: `,"features":null`, wantKind: DiagnosticMalformedResponse},
		{name: "protocol 2 absent", version: 2},
		{name: "protocol 2 empty", version: 2, features: `,"features":[]`, want: []string{}},
		{
			name:     "protocol 2 open ordered list",
			version:  2,
			features: `,"features":["future_token","typed_resolutions","future_token"]`,
			want:     []string{"future_token", "typed_resolutions", "future_token"},
		},
		{name: "protocol 2 null rejected", version: 2, features: `,"features":null`, wantKind: DiagnosticMalformedResponse},
		{name: "protocol 2 non-string rejected", version: 2, features: `,"features":["ok",1]`, wantKind: DiagnosticMalformedResponse},
		{name: "below range", version: 0, wantKind: DiagnosticUnsupportedProtocol},
		{name: "above range", version: 3, wantKind: DiagnosticUnsupportedProtocol},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := decodeProbeResponse(validProbeFrame("fake", test.version, test.features, `{}`), "fake")
			if test.wantKind != "" {
				assertWireFailure(t, err, test.wantKind)
				return
			}
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !reflect.DeepEqual(result.Features, test.want) {
				t.Fatalf("features = %#v, want %#v", result.Features, test.want)
			}
		})
	}
}

func TestEnforcementPresenceDefaultsFalse(t *testing.T) {
	allTrue := `{"path_grants":true,"read_only":true,"network_grants":true,"shell_grants":true,"read_grants":true}`
	result, err := decodeProbeResponse(validProbeFrame("fake", 2, "", allTrue), "fake")
	if err != nil {
		t.Fatal(err)
	}
	if result.Enforcement != (protocol.Enforcement{
		PathGrants: true, ReadOnly: true, NetworkGrants: true, ShellGrants: true, ReadGrants: true,
	}) {
		t.Fatalf("all-true enforcement = %#v", result.Enforcement)
	}

	tests := []struct {
		name string
		body string
		read func(protocol.Enforcement) bool
	}{
		{"path_grants", `{"read_only":true,"network_grants":true,"shell_grants":true,"read_grants":true}`, func(e protocol.Enforcement) bool { return e.PathGrants }},
		{"read_only", `{"path_grants":true,"network_grants":true,"shell_grants":true,"read_grants":true}`, func(e protocol.Enforcement) bool { return e.ReadOnly }},
		{"network_grants", `{"path_grants":true,"read_only":true,"shell_grants":true,"read_grants":true}`, func(e protocol.Enforcement) bool { return e.NetworkGrants }},
		{"shell_grants", `{"path_grants":true,"read_only":true,"network_grants":true,"read_grants":true}`, func(e protocol.Enforcement) bool { return e.ShellGrants }},
		{"read_grants", `{"path_grants":true,"read_only":true,"network_grants":true,"shell_grants":true}`, func(e protocol.Enforcement) bool { return e.ReadGrants }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := decodeProbeResponse(validProbeFrame("fake", 2, "", test.body), "fake")
			if err != nil {
				t.Fatal(err)
			}
			if test.read(result.Enforcement) {
				t.Fatalf("omitted %s decoded true", test.name)
			}
		})
	}
	_, err = decodeProbeResponse(validProbeFrame("fake", 2, "", `{"read_only":null}`), "fake")
	assertWireFailure(t, err, DiagnosticMalformedResponse)
}

func TestRequiredCapabilityAndModelFields(t *testing.T) {
	valid := string(validProbeFrame("fake", 2, "", `{}`))
	fragments := []string{
		`"repo_read":true,`,
		`"repo_write":true,`,
		`"shell":true,`,
		`"network":true,`,
		`"resumable_sessions":true,`,
		`,"models":[{"id":"model","aliases":["alias"]}]`,
	}
	for _, fragment := range fragments {
		t.Run(fragment, func(t *testing.T) {
			changed := strings.Replace(valid, fragment, "", 1)
			if changed == valid {
				t.Fatalf("test did not remove %s", fragment)
			}
			_, err := decodeProbeResponse([]byte(changed), "fake")
			assertWireFailure(t, err, DiagnosticMalformedResponse)
		})
	}

	missingModelID := strings.Replace(valid, `"id":"model",`, "", 1)
	_, err := decodeProbeResponse([]byte(missingModelID), "fake")
	assertWireFailure(t, err, DiagnosticMalformedResponse)

	nullAliases := strings.Replace(valid, `"aliases":["alias"]`, `"aliases":null`, 1)
	_, err = decodeProbeResponse([]byte(nullAliases), "fake")
	assertWireFailure(t, err, DiagnosticMalformedResponse)
}

func TestStrictProbeResponseFailures(t *testing.T) {
	valid := string(validProbeFrame("fake", 2, "", `{}`))
	tests := []struct {
		name string
		data []byte
		kind DiagnosticKind
	}{
		{"invalid UTF-8", append([]byte(valid[:len(valid)-1]), 0xff, '}'), DiagnosticInvalidUTF8},
		{
			"duplicate before incompatible value",
			[]byte(strings.Replace(valid, `"protocol":2`, `"protocol":2,"protocol":"bad"`, 1)),
			DiagnosticDuplicateKey,
		},
		{
			"unknown result field",
			[]byte(strings.Replace(valid, `"protocol":2`, `"unknown":true,"protocol":2`, 1)),
			DiagnosticMalformedResponse,
		},
		{
			"wrong adapter",
			validProbeFrame("other", 2, "", `{}`),
			DiagnosticWrongAdapter,
		},
		{
			"error response",
			[]byte(`{"jsonrpc":"2.0","id":"probe","error":{"code":-32603,"message":"failed"}}`),
			DiagnosticErrorResponse,
		},
		{
			"result plus null error",
			[]byte(strings.TrimSuffix(valid, "}") + `,"error":null}`),
			DiagnosticMalformedResponse,
		},
		{
			"null result",
			[]byte(`{"jsonrpc":"2.0","id":"probe","result":null}`),
			DiagnosticMalformedResponse,
		},
		{
			"mismatched id",
			[]byte(strings.Replace(valid, `"id":"probe"`, `"id":"other"`, 1)),
			DiagnosticMalformedResponse,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeProbeResponse(test.data, "fake")
			assertWireFailure(t, err, test.kind)
		})
	}
}

func TestRequiredProbeShape(t *testing.T) {
	tests := []struct {
		name   string
		remove string
	}{
		{"adapter", `"adapter":{"id":"fake","version":"1.2.3"},`},
		{"capabilities", `"capabilities":{"repo_read":true,"repo_write":true,"shell":true,"network":true,"resumable_sessions":true,"models":[{"id":"model","aliases":["alias"]}]},`},
		{"enforcement", `,"enforcement":{}`},
	}
	valid := string(validProbeFrame("fake", 2, "", `{}`))
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := strings.Replace(valid, test.remove, "", 1)
			if changed == valid {
				t.Fatalf("test did not remove %s", test.name)
			}
			frame := []byte(changed)
			_, err := decodeProbeResponse(frame, "fake")
			assertWireFailure(t, err, DiagnosticMalformedResponse)
		})
	}
}

func validProbeFrame(adapterID string, version int, features, enforcement string) []byte {
	return []byte(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":"probe","result":{"protocol":%d,"adapter":{"id":%q,"version":"1.2.3"},"capabilities":{"repo_read":true,"repo_write":true,"shell":true,"network":true,"resumable_sessions":true,"models":[{"id":"model","aliases":["alias"]}]},"enforcement":%s%s}}`,
		version, adapterID, enforcement, features,
	))
}

func assertWireFailure(t *testing.T, err error, kind DiagnosticKind) {
	t.Helper()
	var failure *wireFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want wire failure %q", err, kind)
	}
	if failure.kind != kind {
		t.Fatalf("kind = %q, want %q (error %v)", failure.kind, kind, err)
	}
}
