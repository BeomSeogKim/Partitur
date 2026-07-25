package codex

import (
	"encoding/json"
	"errors"
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
	ThreadID  string          `json:"thread_id"`
	SessionID string          `json:"session_id"`
	Message   string          `json:"message"`
	Error     json.RawMessage `json:"error"`
	Item      json.RawMessage `json:"item"`
}

type streamItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
	Tool string `json:"tool"`
}

func (s *streamState) consume(line []byte) error {
	var event streamEvent
	if err := json.Unmarshal(line, &event); err != nil || strings.TrimSpace(event.Type) == "" {
		s.incompatible = true
		return s.sink.Log("warn", "ignored malformed Codex stream event")
	}
	s.sawParseable = true

	switch event.Type {
	case "thread.started", "session.started":
		sessionID := event.ThreadID
		if sessionID == "" {
			sessionID = event.SessionID
		}
		if sessionID != "" {
			s.captureSession(sessionID)
		}
	case "turn.started":
	case "turn.completed":
		s.sawResult = true
		s.resultIsError = false
	case "turn.failed":
		s.sawResult = true
		s.resultIsError = true
		s.detail = s.sanitize(errorMessage(event.Message, event.Error))
	case "error":
		s.detail = s.sanitize(errorMessage(event.Message, event.Error))
	case "item.started":
		item, err := decodeItem(event.Item)
		if err != nil {
			s.incompatible = true
			return s.sink.Log("warn", "ignored incompatible Codex item event")
		}
		if isExecutionItem(item.Type) {
			name := item.Name
			if name == "" {
				name = item.Tool
			}
			if name == "" {
				name = item.Type
			}
			name = s.sanitize(adapterkit.TruncateUTF8(strings.TrimSpace(name), 128))
			if name != "" {
				return s.sink.Progress("tool: " + name)
			}
		}
	case "item.completed":
		item, err := decodeItem(event.Item)
		if err != nil {
			s.incompatible = true
			return s.sink.Log("warn", "ignored incompatible Codex item event")
		}
		if item.Type == "agent_message" {
			s.sawAssistant = true
			summary := s.sanitize(summarizeText(item.Text))
			if summary != "" {
				return s.sink.Progress("assistant: " + summary)
			}
		}
	default:
		eventType := s.sanitize(adapterkit.TruncateUTF8(event.Type, 128))
		return s.sink.Log("debug", fmt.Sprintf("ignored Codex stream event type %q", eventType))
	}
	return nil
}

func decodeItem(raw json.RawMessage) (streamItem, error) {
	var item streamItem
	if err := json.Unmarshal(raw, &item); err != nil || strings.TrimSpace(item.Type) == "" {
		return streamItem{}, errors.New("invalid item")
	}
	return item, nil
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

func errorMessage(message string, raw json.RawMessage) string {
	if strings.TrimSpace(message) != "" {
		return message
	}
	var structured struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &structured) == nil && strings.TrimSpace(structured.Message) != "" {
		return structured.Message
	}
	return "Codex returned a structured error"
}

func isExecutionItem(itemType string) bool {
	switch itemType {
	case "command_execution", "mcp_tool_call", "tool_call", "web_search":
		return true
	default:
		return false
	}
}
