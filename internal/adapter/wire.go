package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

var probeRequestID = json.RawMessage(`"probe"`)

type responseEnvelope struct {
	JSONRPC json.RawMessage `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

type errorEnvelope struct {
	Code    json.RawMessage `json:"code"`
	Message json.RawMessage `json:"message"`
}

type rawProbeResult struct {
	Protocol     json.RawMessage `json:"protocol"`
	Adapter      json.RawMessage `json:"adapter"`
	Capabilities json.RawMessage `json:"capabilities"`
	Enforcement  json.RawMessage `json:"enforcement"`
	Features     json.RawMessage `json:"features"`
}

type rawAdapterIdentity struct {
	ID      json.RawMessage `json:"id"`
	Version json.RawMessage `json:"version"`
}

type rawCapabilities struct {
	RepoRead          json.RawMessage `json:"repo_read"`
	RepoWrite         json.RawMessage `json:"repo_write"`
	Shell             json.RawMessage `json:"shell"`
	Network           json.RawMessage `json:"network"`
	ResumableSessions json.RawMessage `json:"resumable_sessions"`
	Models            json.RawMessage `json:"models"`
}

type rawModel struct {
	ID      json.RawMessage `json:"id"`
	Aliases json.RawMessage `json:"aliases"`
}

type rawEnforcement struct {
	PathGrants    json.RawMessage `json:"path_grants"`
	ReadOnly      json.RawMessage `json:"read_only"`
	NetworkGrants json.RawMessage `json:"network_grants"`
	ShellGrants   json.RawMessage `json:"shell_grants"`
	ReadGrants    json.RawMessage `json:"read_grants"`
}

type wireFailure struct {
	kind   DiagnosticKind
	detail string
}

func (f *wireFailure) Error() string {
	return f.detail
}

func encodeProbeRequest() ([]byte, error) {
	request := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
	}{
		JSONRPC: "2.0",
		ID:      probeRequestID,
		Method:  "probe",
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func decodeProbeResponse(frame []byte, expectedAdapterID string) (protocol.ProbeResult, error) {
	if !utf8.Valid(frame) {
		return protocol.ProbeResult{}, &wireFailure{kind: DiagnosticInvalidUTF8, detail: "response is not valid UTF-8"}
	}

	var envelope responseEnvelope
	if err := protocol.DecodeStrict(frame, &envelope); err != nil {
		return protocol.ProbeResult{}, classifyDecodeError(err)
	}
	var jsonrpc string
	if err := decodeRequired(envelope.JSONRPC, &jsonrpc); err != nil || jsonrpc != "2.0" {
		return protocol.ProbeResult{}, malformed("invalid jsonrpc version")
	}
	if !bytes.Equal(bytes.TrimSpace(envelope.ID), probeRequestID) {
		return protocol.ProbeResult{}, malformed("response id does not match request")
	}

	hasResult := envelope.Result != nil
	hasError := envelope.Error != nil
	if hasResult == hasError {
		return protocol.ProbeResult{}, malformed("response must contain exactly one of result or error")
	}
	if hasError {
		if null(envelope.Error) {
			return protocol.ProbeResult{}, malformed("error must be an object")
		}
		var rpcError errorEnvelope
		if err := protocol.DecodeStrict(envelope.Error, &rpcError); err != nil {
			return protocol.ProbeResult{}, classifyDecodeError(err)
		}
		var code int
		var message string
		if err := decodeRequired(rpcError.Code, &code); err != nil {
			return protocol.ProbeResult{}, malformed("error code is required")
		}
		if err := decodeRequired(rpcError.Message, &message); err != nil {
			return protocol.ProbeResult{}, malformed("error message is required")
		}
		return protocol.ProbeResult{}, &wireFailure{
			kind:   DiagnosticErrorResponse,
			detail: fmt.Sprintf("adapter returned JSON-RPC error %d: %s", code, message),
		}
	}
	if null(envelope.Result) {
		return protocol.ProbeResult{}, malformed("result must be an object")
	}

	result, err := decodeProbeResult(envelope.Result)
	if err != nil {
		return protocol.ProbeResult{}, err
	}
	if result.Adapter.ID != expectedAdapterID {
		return protocol.ProbeResult{}, &wireFailure{
			kind:   DiagnosticWrongAdapter,
			detail: fmt.Sprintf("probe named adapter %q", result.Adapter.ID),
		}
	}
	return result, nil
}

func decodeProbeResult(data []byte) (protocol.ProbeResult, error) {
	var raw rawProbeResult
	if err := protocol.DecodeStrict(data, &raw); err != nil {
		return protocol.ProbeResult{}, classifyDecodeError(err)
	}

	var result protocol.ProbeResult
	if err := decodeRequired(raw.Protocol, &result.Protocol); err != nil {
		return protocol.ProbeResult{}, malformed("protocol is required")
	}
	if result.Protocol < protocol.MinimumProtocolVersion || result.Protocol > protocol.ProtocolVersion {
		return protocol.ProbeResult{}, &wireFailure{
			kind:   DiagnosticUnsupportedProtocol,
			detail: fmt.Sprintf("protocol %d is not supported", result.Protocol),
		}
	}
	if err := decodeAdapterIdentity(raw.Adapter, &result.Adapter); err != nil {
		return protocol.ProbeResult{}, err
	}
	if err := decodeCapabilities(raw.Capabilities, &result.Capabilities); err != nil {
		return protocol.ProbeResult{}, err
	}
	if err := decodeEnforcement(raw.Enforcement, &result.Enforcement); err != nil {
		return protocol.ProbeResult{}, err
	}

	if raw.Features != nil {
		if null(raw.Features) {
			return protocol.ProbeResult{}, malformed("features must be omitted or an array")
		}
		if err := protocol.DecodeStrict(raw.Features, &result.Features); err != nil {
			return protocol.ProbeResult{}, classifyDecodeError(err)
		}
	}
	return result, nil
}

func decodeAdapterIdentity(data []byte, identity *protocol.AdapterIdentity) error {
	if !present(data) {
		return malformed("adapter is required")
	}
	var raw rawAdapterIdentity
	if err := protocol.DecodeStrict(data, &raw); err != nil {
		return classifyDecodeError(err)
	}
	if err := decodeRequired(raw.ID, &identity.ID); err != nil {
		return malformed("adapter.id is required")
	}
	if err := decodeRequired(raw.Version, &identity.Version); err != nil {
		return malformed("adapter.version is required")
	}
	return nil
}

func decodeCapabilities(data []byte, capabilities *protocol.Capabilities) error {
	if !present(data) {
		return malformed("capabilities is required")
	}
	var raw rawCapabilities
	if err := protocol.DecodeStrict(data, &raw); err != nil {
		return classifyDecodeError(err)
	}
	requiredBools := []struct {
		name string
		raw  json.RawMessage
		out  *bool
	}{
		{"repo_read", raw.RepoRead, &capabilities.RepoRead},
		{"repo_write", raw.RepoWrite, &capabilities.RepoWrite},
		{"shell", raw.Shell, &capabilities.Shell},
		{"network", raw.Network, &capabilities.Network},
		{"resumable_sessions", raw.ResumableSessions, &capabilities.ResumableSessions},
	}
	for _, field := range requiredBools {
		if err := decodeRequired(field.raw, field.out); err != nil {
			return malformed("capabilities." + field.name + " is required")
		}
	}
	if !present(raw.Models) {
		return malformed("capabilities.models is required")
	}
	var models []rawModel
	if err := protocol.DecodeStrict(raw.Models, &models); err != nil {
		return classifyDecodeError(err)
	}
	capabilities.Models = make([]protocol.Model, len(models))
	for index, model := range models {
		if err := decodeRequired(model.ID, &capabilities.Models[index].ID); err != nil {
			return malformed(fmt.Sprintf("capabilities.models[%d].id is required", index))
		}
		if model.Aliases != nil {
			if null(model.Aliases) {
				return malformed(fmt.Sprintf("capabilities.models[%d].aliases must be omitted or an array", index))
			}
			if err := protocol.DecodeStrict(model.Aliases, &capabilities.Models[index].Aliases); err != nil {
				return classifyDecodeError(err)
			}
		}
	}
	return nil
}

func decodeEnforcement(data []byte, enforcement *protocol.Enforcement) error {
	if !present(data) {
		return malformed("enforcement is required")
	}
	var raw rawEnforcement
	if err := protocol.DecodeStrict(data, &raw); err != nil {
		return classifyDecodeError(err)
	}
	optionalBools := []struct {
		name string
		raw  json.RawMessage
		out  *bool
	}{
		{"path_grants", raw.PathGrants, &enforcement.PathGrants},
		{"read_only", raw.ReadOnly, &enforcement.ReadOnly},
		{"network_grants", raw.NetworkGrants, &enforcement.NetworkGrants},
		{"shell_grants", raw.ShellGrants, &enforcement.ShellGrants},
		{"read_grants", raw.ReadGrants, &enforcement.ReadGrants},
	}
	for _, field := range optionalBools {
		if field.raw == nil {
			continue
		}
		if err := decodeRequired(field.raw, field.out); err != nil {
			return malformed("enforcement." + field.name + " must be a boolean")
		}
	}
	return nil
}

func decodeRequired(data json.RawMessage, value any) error {
	if !present(data) {
		return errors.New("required value is absent or null")
	}
	return protocol.DecodeStrict(data, value)
}

func present(data json.RawMessage) bool {
	return data != nil && !null(data)
}

func null(data json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}

func classifyDecodeError(err error) error {
	if errors.Is(err, protocol.ErrDuplicateKey) {
		return &wireFailure{kind: DiagnosticDuplicateKey, detail: err.Error()}
	}
	if errors.Is(err, protocol.ErrInvalidUTF8) {
		return &wireFailure{kind: DiagnosticInvalidUTF8, detail: err.Error()}
	}
	return malformed(err.Error())
}

func malformed(detail string) error {
	return &wireFailure{kind: DiagnosticMalformedResponse, detail: detail}
}
