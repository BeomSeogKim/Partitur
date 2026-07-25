package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/adapterkit"
)

const maxProgressSummary = 512

type streamState struct {
	sink          adapterkit.EventSink
	sessionID     string
	sensitive     []string
	detail        string
	resultIsError bool
	sawResult     bool
	sawParseable  bool
	sawAssistant  bool
	incompatible  bool
}

type streamEvent struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	IsError   *bool           `json:"is_error"`
	Result    json.RawMessage `json:"result"`
	Message   json.RawMessage `json:"message"`
}

type assistantMessage struct {
	Content []assistantContent `json:"content"`
}

type assistantContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
}

func (s *streamState) consume(line []byte) error {
	var event streamEvent
	if err := json.Unmarshal(line, &event); err != nil || strings.TrimSpace(event.Type) == "" {
		s.incompatible = true
		return s.sink.Log("warn", "ignored malformed Claude stream event")
	}
	s.sawParseable = true

	switch event.Type {
	case "system":
		if event.Subtype == "init" && event.SessionID != "" {
			s.captureSession(event.SessionID)
		}
	case "assistant":
		s.sawAssistant = true
		var message assistantMessage
		if err := json.Unmarshal(event.Message, &message); err != nil {
			s.incompatible = true
			return s.sink.Log("warn", "ignored incompatible Claude assistant event")
		}
		for _, content := range message.Content {
			switch content.Type {
			case "text":
				summary := s.sanitize(summarizeText(content.Text))
				if summary != "" {
					if err := s.sink.Progress("assistant: " + summary); err != nil {
						return err
					}
				}
			case "tool_use":
				name := s.sanitize(adapterkit.TruncateUTF8(strings.TrimSpace(content.Name), 128))
				if name != "" {
					if err := s.sink.Progress("tool: " + name); err != nil {
						return err
					}
				}
			}
		}
	case "result":
		s.sawResult = true
		if event.IsError == nil {
			s.incompatible = true
		} else {
			s.resultIsError = *event.IsError
		}
		if event.SessionID != "" {
			s.captureSession(event.SessionID)
		}
		s.detail = s.sanitize(resultText(event.Result))
	default:
		eventType := s.sanitize(adapterkit.TruncateUTF8(event.Type, 128))
		return s.sink.Log("debug", fmt.Sprintf("ignored Claude stream event type %q", eventType))
	}
	return nil
}

func (s *streamState) sanitize(message string) string {
	return adapterkit.SanitizeMessage(message, s.sensitive...)
}

func (s *streamState) captureSession(sessionID string) {
	s.sessionID = sessionID
	if !slices.Contains(s.sensitive, sessionID) {
		s.sensitive = append(s.sensitive, sessionID)
	}
}

func summarizeText(value string) string {
	value = strings.TrimSpace(value)
	if newline := strings.IndexByte(value, '\n'); newline >= 0 {
		value = value[:newline]
	}
	return adapterkit.TruncateUTF8(value, maxProgressSummary)
}

func resultText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return "Claude returned a structured error"
}
