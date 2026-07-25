package adapterkit

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeMessageRedactsAndCaps(t *testing.T) {
	message := "Authorization: Bearer abc123 token=secret-value session-123 " + strings.Repeat("界", 2000)
	got := SanitizeMessage(message, "session-123")
	if strings.Contains(got, "abc123") || strings.Contains(got, "secret-value") || strings.Contains(got, "session-123") {
		t.Fatalf("message was not redacted: %q", got)
	}
	if len(got) > MaxEventMessageBytes {
		t.Fatalf("message size = %d", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("message is not valid UTF-8")
	}
}

func TestSanitizeDiagnosticRedactsAndKeepsDiagnosticBound(t *testing.T) {
	message := "late-session Authorization: Bearer abc123 " + strings.Repeat("界", MaxDiagnosticBytes)
	got := SanitizeDiagnostic(message, "late-session")
	if strings.Contains(got, "late-session") || strings.Contains(got, "abc123") {
		t.Fatalf("diagnostic leaked sensitive data: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("diagnostic redaction marker missing: %q", got)
	}
	if len(got) > MaxDiagnosticBytes {
		t.Fatalf("diagnostic length = %d", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("diagnostic is not valid UTF-8")
	}
}

func TestTruncateUTF8SmallLimits(t *testing.T) {
	for limit := 0; limit < 16; limit++ {
		got := TruncateUTF8("界界界界界界", limit)
		if len(got) > limit || !utf8.ValidString(got) {
			t.Fatalf("limit=%d value=%q bytes=%d", limit, got, len(got))
		}
	}
}
