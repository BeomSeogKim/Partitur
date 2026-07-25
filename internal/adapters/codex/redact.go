package codex

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/adapterkit"
)

type sessionRedactingSink struct {
	adapterkit.EventSink
	sensitive []string
}

func (s sessionRedactingSink) Log(level, message string) error {
	return s.EventSink.Log(
		adapterkit.SanitizeMessage(level, s.sensitive...),
		adapterkit.SanitizeMessage(message, s.sensitive...),
	)
}

func (s sessionRedactingSink) Progress(message string) error {
	return s.EventSink.Progress(adapterkit.SanitizeMessage(message, s.sensitive...))
}

func (s sessionRedactingSink) Artifact(artifactID, path string) error {
	return s.EventSink.Artifact(
		redactSensitive(artifactID, s.sensitive),
		redactSensitive(path, s.sensitive),
	)
}

func (s sessionRedactingSink) Proposal(id string, amendment json.RawMessage, requiresDecision bool) error {
	return s.EventSink.Proposal(
		redactSensitive(id, s.sensitive),
		redactJSON(amendment, s.sensitive),
		requiresDecision,
	)
}

func (s sessionRedactingSink) Question(id, question string) error {
	return s.EventSink.Question(
		redactSensitive(id, s.sensitive),
		adapterkit.SanitizeMessage(question, s.sensitive...),
	)
}

func redactJSON(raw json.RawMessage, sensitive []string) json.RawMessage {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return raw
	}
	redactJSONStrings(value, sensitive)
	redacted, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return redacted
}

func redactJSONStrings(value any, sensitive []string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			redactedKey := redactSensitive(key, sensitive)
			if redactedKey != key {
				delete(typed, key)
				typed[redactedKey] = child
			}
			if text, ok := child.(string); ok {
				typed[redactedKey] = redactSensitive(text, sensitive)
				continue
			}
			redactJSONStrings(child, sensitive)
		}
	case []any:
		for index, child := range typed {
			if text, ok := child.(string); ok {
				typed[index] = redactSensitive(text, sensitive)
				continue
			}
			redactJSONStrings(child, sensitive)
		}
	}
}

func redactSensitive(value string, sensitive []string) string {
	for _, secret := range sensitive {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}
