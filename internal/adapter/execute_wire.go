package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

var (
	executeRequestID = json.RawMessage(`"execute"`)
	cancelRequestID  = json.RawMessage(`"cancel"`)
)

type executeFrameKind int

const (
	executeFrameResponse executeFrameKind = iota
	executeFrameEvent
	executeFrameCancelAck
)

type executeFrame struct {
	kind   executeFrameKind
	result protocol.ExecuteResult
	event  any
}

type rawRPCFrame struct {
	JSONRPC json.RawMessage `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  json.RawMessage `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

type rawEvent struct {
	Type json.RawMessage `json:"type"`
}

func encodeExecuteRequest(request protocol.ExecuteRequest) ([]byte, error) {
	frame := struct {
		JSONRPC string                  `json:"jsonrpc"`
		ID      json.RawMessage         `json:"id"`
		Method  string                  `json:"method"`
		Params  protocol.ExecuteRequest `json:"params"`
	}{
		JSONRPC: "2.0",
		ID:      executeRequestID,
		Method:  "execute",
		Params:  request,
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// SerializedExecuteRequestBytes returns the JSON-RPC frame size without its
// newline transport delimiter.
func SerializedExecuteRequestBytes(request protocol.ExecuteRequest) (int, error) {
	encoded, err := encodeExecuteRequest(request)
	if err != nil {
		return 0, err
	}
	return len(encoded) - 1, nil
}

func encodeCancelRequest(request protocol.CancelRequest) ([]byte, error) {
	frame := struct {
		JSONRPC string                 `json:"jsonrpc"`
		ID      json.RawMessage        `json:"id"`
		Method  string                 `json:"method"`
		Params  protocol.CancelRequest `json:"params"`
	}{
		JSONRPC: "2.0",
		ID:      cancelRequestID,
		Method:  "cancel",
		Params:  request,
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func decodeExecuteFrame(frame []byte) (executeFrame, error) {
	if !utf8.Valid(frame) {
		return executeFrame{}, protocolFailure("strict_decode_failed", "frame is not valid UTF-8")
	}
	var envelope rawRPCFrame
	if err := protocol.DecodeStrict(frame, &envelope); err != nil {
		return executeFrame{}, protocolFailure("strict_decode_failed", err.Error())
	}
	var version string
	if err := decodeRequired(envelope.JSONRPC, &version); err != nil || version != "2.0" {
		return executeFrame{}, protocolFailure("strict_decode_failed", "invalid jsonrpc version")
	}

	if envelope.Method != nil {
		if envelope.ID != nil || envelope.Result != nil || envelope.Error != nil {
			return executeFrame{}, protocolFailure("strict_decode_failed", "event notification has response fields")
		}
		var method string
		if err := decodeRequired(envelope.Method, &method); err != nil || method != "event" {
			return executeFrame{}, protocolFailure("strict_decode_failed", "unknown notification method")
		}
		event, err := decodeExecuteEvent(envelope.Params)
		if err != nil {
			return executeFrame{}, err
		}
		return executeFrame{kind: executeFrameEvent, event: event}, nil
	}

	if envelope.Params != nil || envelope.Result == nil || envelope.Error != nil {
		return executeFrame{}, protocolFailure("strict_decode_failed", "invalid response shape")
	}
	responseID := bytes.TrimSpace(envelope.ID)
	if bytes.Equal(responseID, executeRequestID) {
		var result protocol.ExecuteResult
		if err := protocol.DecodeStrict(envelope.Result, &result); err != nil {
			return executeFrame{}, protocolFailure("strict_decode_failed", err.Error())
		}
		if err := validateExecuteResult(result); err != nil {
			return executeFrame{}, err
		}
		return executeFrame{kind: executeFrameResponse, result: result}, nil
	}
	if bytes.Equal(responseID, cancelRequestID) {
		if err := decodeEmptyObject(envelope.Result); err != nil {
			return executeFrame{}, protocolFailure("strict_decode_failed", err.Error())
		}
		return executeFrame{kind: executeFrameCancelAck}, nil
	}
	return executeFrame{}, protocolFailure("strict_decode_failed", "unknown response id")
}

func decodeEmptyObject(value json.RawMessage) error {
	var object map[string]json.RawMessage
	if err := protocol.DecodeStrict(value, &object); err != nil {
		return err
	}
	if object == nil {
		return errors.New("cancel result must be an object")
	}
	if len(object) != 0 {
		return errors.New("cancel result must be empty")
	}
	return nil
}

func decodeExecuteEvent(params json.RawMessage) (any, error) {
	if !present(params) {
		return nil, protocolFailure("strict_decode_failed", "event params are required")
	}
	var raw rawEvent
	if err := json.Unmarshal(params, &raw); err != nil {
		return nil, protocolFailure("strict_decode_failed", err.Error())
	}
	var eventType protocol.EventType
	if err := decodeRequired(raw.Type, &eventType); err != nil {
		return nil, protocolFailure("strict_decode_failed", "event type is required")
	}
	var target any
	switch eventType {
	case protocol.EventLog:
		target = &protocol.LogEvent{}
	case protocol.EventProgress:
		target = &protocol.ProgressEvent{}
	case protocol.EventArtifact:
		target = &protocol.ArtifactEvent{}
	case protocol.EventProposal:
		target = &protocol.ProposalEvent{}
	case protocol.EventQuestion:
		target = &protocol.QuestionEvent{}
	default:
		return nil, protocolFailure("strict_decode_failed", fmt.Sprintf("unknown event type %q", eventType))
	}
	if err := protocol.DecodeStrict(params, target); err != nil {
		return nil, protocolFailure("strict_decode_failed", err.Error())
	}
	return target, nil
}

func validateExecuteResult(result protocol.ExecuteResult) error {
	validFailureKinds := map[protocol.FailureKind]bool{
		protocol.FailureAdapterUnavailable: true,
		protocol.FailureModelUnavailable:   true,
		protocol.FailureProviderTimeout:    true,
		protocol.FailureRateLimited:        true,
		protocol.FailureAuthentication:     true,
		protocol.FailureProtocolError:      true,
		protocol.FailureGrantDenied:        true,
		protocol.FailureTaskFailed:         true,
	}
	switch result.Outcome {
	case protocol.OutcomeCompleted:
		if result.Failure != nil || len(result.PendingDecisionIDs) != 0 {
			return protocolFailure("strict_decode_failed", "completed result has failure or pending decisions")
		}
	case protocol.OutcomeFailed:
		if result.Failure == nil || !validFailureKinds[result.Failure.Kind] ||
			len(result.PendingDecisionIDs) != 0 {
			return protocolFailure("strict_decode_failed", "failed result has invalid failure or pending decisions")
		}
	case protocol.OutcomeWaitingHuman:
		if result.Failure != nil {
			return protocolFailure("strict_decode_failed", "waiting_human result has failure")
		}
	case protocol.OutcomeCancelled:
		if result.Failure != nil || len(result.PendingDecisionIDs) != 0 {
			return protocolFailure("strict_decode_failed", "cancelled result has failure or pending decisions")
		}
	default:
		return protocolFailure("strict_decode_failed", "unknown execute outcome")
	}
	seen := make(map[string]bool, len(result.PendingDecisionIDs))
	for _, id := range result.PendingDecisionIDs {
		if id == "" || seen[id] {
			return protocolFailure("strict_decode_failed", "pending decision ids must be nonempty and unique")
		}
		seen[id] = true
	}
	return nil
}

type executeProtocolFailure struct {
	reason string
	detail string
}

func (e *executeProtocolFailure) Error() string {
	return e.detail
}

func protocolFailure(reason, detail string) error {
	return &executeProtocolFailure{reason: reason, detail: detail}
}

func asProtocolFailure(err error) (*executeProtocolFailure, bool) {
	var failure *executeProtocolFailure
	return failure, errors.As(err, &failure)
}
