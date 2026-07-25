package adapterkit

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

const MaxEventMessageBytes = 4 << 10

var (
	bearerPattern = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`)
	secretPattern = regexp.MustCompile(`(?i)\b(api[_-]?key|authorization|password|secret|token)\b(\s*[:=]\s*)([^\s,;]+)`)
)

// SanitizeMessage redacts common credential shapes and known sensitive values,
// then returns valid UTF-8 capped to the event message limit.
func SanitizeMessage(message string, sensitive ...string) string {
	message = strings.ToValidUTF8(message, "\uFFFD")
	message = bearerPattern.ReplaceAllString(message, "Bearer [REDACTED]")
	message = secretPattern.ReplaceAllString(message, "$1$2[REDACTED]")
	for _, value := range sensitive {
		if value != "" {
			message = strings.ReplaceAll(message, value, "[REDACTED]")
		}
	}
	return TruncateUTF8(message, MaxEventMessageBytes)
}

// TruncateUTF8 caps a string by bytes without splitting a UTF-8 encoding.
func TruncateUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= limit {
		return value
	}
	const marker = "..."
	if limit <= len(marker) {
		return marker[:limit]
	}
	end := limit - len(marker)
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + marker
}

func sessionHintSecrets(raw json.RawMessage) []string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	var secrets []string
	collectSensitiveValues(value, false, &secrets)
	return secrets
}

func collectSensitiveValues(value any, sensitiveKey bool, secrets *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			lower := strings.ToLower(key)
			sensitive := sensitiveKey ||
				strings.Contains(lower, "session") ||
				strings.Contains(lower, "token") ||
				strings.Contains(lower, "secret")
			collectSensitiveValues(child, sensitive, secrets)
		}
	case []any:
		for _, child := range typed {
			collectSensitiveValues(child, sensitiveKey, secrets)
		}
	case string:
		if sensitiveKey && typed != "" {
			*secrets = append(*secrets, typed)
		}
	}
}
