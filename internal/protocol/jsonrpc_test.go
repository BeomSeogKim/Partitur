package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestReaderRoundTripAndBlankLines(t *testing.T) {
	input := "\n \t\r\n" +
		`{"jsonrpc":"2.0","id":"a","method":"probe"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"cancel","params":{"attempt_id":"x"}}` + "\n"
	reader := NewReader(strings.NewReader(input))

	first, err := reader.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	request, err := DecodeRequest(first)
	if err != nil {
		t.Fatal(err)
	}
	if string(request.ID) != `"a"` {
		t.Fatalf("id = %s", request.ID)
	}

	second, err := reader.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	request, err = DecodeRequest(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(request.ID) != "2" {
		t.Fatalf("id = %s", request.ID)
	}
	if _, err := reader.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Fatalf("EOF error = %v", err)
	}
}

func TestReaderOversizedFrameContinues(t *testing.T) {
	input := strings.Repeat("x", MaxFrameBytes+1) + "\n" +
		`{"jsonrpc":"2.0","id":1,"method":"probe"}` + "\n"
	reader := NewReader(strings.NewReader(input))
	if _, err := reader.ReadFrame(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized error = %v", err)
	}
	frame, err := reader.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRequest(frame); err != nil {
		t.Fatal(err)
	}
}

func TestReaderPartialFrame(t *testing.T) {
	reader := NewReader(strings.NewReader(`{"jsonrpc":"2.0"`))
	if _, err := reader.ReadFrame(); !errors.Is(err, ErrPartialFrame) {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeRequestStrictness(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{"malformed", []byte(`{"jsonrpc":`), ErrParseRequest},
		{"invalid UTF-8", append([]byte(`{"jsonrpc":"2.0","id":"`), 0xff), ErrParseRequest},
		{"duplicate top-level key", []byte(`{"jsonrpc":"2.0","id":1,"method":"probe","method":"cancel"}`), ErrInvalidRequest},
		{"duplicate nested key", []byte(`{"jsonrpc":"2.0","id":1,"method":"cancel","params":{"attempt_id":"x","attempt_id":"y"}}`), ErrInvalidRequest},
		{"unknown field", []byte(`{"jsonrpc":"2.0","id":1,"method":"probe","extra":true}`), ErrInvalidRequest},
		{"missing id", []byte(`{"jsonrpc":"2.0","method":"probe"}`), ErrInvalidRequest},
		{"null id", []byte(`{"jsonrpc":"2.0","id":null,"method":"probe"}`), ErrInvalidRequest},
		{"boolean id", []byte(`{"jsonrpc":"2.0","id":true,"method":"probe"}`), ErrInvalidRequest},
		{"wrong version", []byte(`{"jsonrpc":"1.0","id":1,"method":"probe"}`), ErrInvalidRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeRequest(test.data); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDecodeStrictRejectsDuplicateKeysAndInvalidUTF8(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "top-level duplicate", data: []byte(`{"key":1,"key":2}`)},
		{name: "nested duplicate", data: []byte(`{"outer":[{"key":1,"key":2}]}`)},
		{name: "escaped equivalent duplicate", data: []byte(`{"key":1,"\u006bey":2}`)},
		{name: "invalid UTF-8", data: append([]byte(`{"key":"`), 0xff, '"', '}')},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var value any
			if err := DecodeStrict(test.data, &value); err == nil {
				t.Fatal("expected strict decoding error")
			}
		})
	}

	var value any
	if err := DecodeStrict([]byte(`{"outer":[{"key":1},{"key":2}]}`), &value); err != nil {
		t.Fatalf("valid nested object rejected: %v", err)
	}
}

func TestEnforcementExtensionDefaultsFailClosed(t *testing.T) {
	var enforcement Enforcement
	if err := DecodeStrict([]byte(`{"path_grants":true,"read_only":true,"network_grants":true}`), &enforcement); err != nil {
		t.Fatal(err)
	}
	if !enforcement.PathGrants || !enforcement.ReadOnly || !enforcement.NetworkGrants ||
		enforcement.ShellGrants || enforcement.ReadGrants {
		t.Fatalf("unexpected enforcement defaults: %#v", enforcement)
	}
	if ProtocolVersion != 1 {
		t.Fatalf("protocol version = %d, want 1", ProtocolVersion)
	}
}

func TestWriterSerializesConcurrentMessages(t *testing.T) {
	var output bytes.Buffer
	writer := NewWriter(&output)
	const count = 200
	var wait sync.WaitGroup
	wait.Add(count)
	for index := 0; index < count; index++ {
		go func(index int) {
			defer wait.Done()
			if err := writer.WriteNotification("event", ProgressEvent{
				Type:    EventProgress,
				Message: strconv.Itoa(index),
			}); err != nil {
				t.Errorf("write: %v", err)
			}
		}(index)
	}
	wait.Wait()

	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'})
	if len(lines) != count {
		t.Fatalf("line count = %d", len(lines))
	}
	for _, line := range lines {
		var message Notification
		if err := json.Unmarshal(line, &message); err != nil {
			t.Fatalf("invalid JSON line %q: %v", line, err)
		}
	}
}

func TestResponseEchoesStringAndNumberIDs(t *testing.T) {
	var output bytes.Buffer
	writer := NewWriter(&output)
	for _, id := range []json.RawMessage{json.RawMessage(`"request-\u0031"`), json.RawMessage(`1.25e2`)} {
		if err := writer.WriteResponse(NewResultResponse(id, struct{}{})); err != nil {
			t.Fatal(err)
		}
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'})
	if !bytes.Contains(lines[0], []byte(`"id":"request-\u0031"`)) {
		t.Fatalf("string id was not preserved: %s", lines[0])
	}
	if !bytes.Contains(lines[1], []byte(`"id":1.25e2`)) {
		t.Fatalf("number id was not preserved: %s", lines[1])
	}
}
