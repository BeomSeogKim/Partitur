package adapterkit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

type testHandler struct {
	probe   func(context.Context) (*protocol.ProbeResult, error)
	execute func(context.Context, *protocol.ExecuteRequest, EventSink) (*protocol.ExecuteResult, error)
}

func (h testHandler) Probe(ctx context.Context) (*protocol.ProbeResult, error) {
	if h.probe != nil {
		return h.probe(ctx)
	}
	return &protocol.ProbeResult{
		Protocol: protocol.ProtocolVersion,
		Adapter:  protocol.AdapterIdentity{ID: "test", Version: "test"},
	}, nil
}

func (h testHandler) Execute(ctx context.Context, request *protocol.ExecuteRequest, sink EventSink) (*protocol.ExecuteResult, error) {
	if h.execute != nil {
		return h.execute(ctx, request, sink)
	}
	return &protocol.ExecuteResult{Outcome: protocol.OutcomeCompleted}, nil
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *protocol.Error `json:"error"`
}

func TestServeProbeUnknownMethodMalformedAndOversized(t *testing.T) {
	var input strings.Builder
	input.WriteString("\n")
	input.WriteString(`{"jsonrpc":"2.0","id":"probe-id","method":"probe"}` + "\n")
	input.WriteString(`{"jsonrpc":"2.0","id":2,"method":"unknown"}` + "\n")
	input.WriteString(`{"jsonrpc":` + "\n")
	input.WriteString(strings.Repeat("x", protocol.MaxFrameBytes+1) + "\n")
	input.WriteString(`{"jsonrpc":"2.0","id":3,"method":"probe"}` + "\n")

	output, _, err := serveStatic(t, testHandler{}, input.String())
	if err != nil {
		t.Fatal(err)
	}
	messages := decodeMessages(t, output)
	if len(messages) != 5 {
		t.Fatalf("message count = %d: %s", len(messages), output)
	}
	if string(messages[0].ID) != `"probe-id"` || messages[0].Error != nil {
		t.Fatalf("probe response = %+v", messages[0])
	}
	wantCodes := []int{protocol.CodeMethodNotFound, protocol.CodeParseError, protocol.CodeFrameTooLarge}
	for index, code := range wantCodes {
		if messages[index+1].Error == nil || messages[index+1].Error.Code != code {
			t.Fatalf("message %d = %+v, want code %d", index+1, messages[index+1], code)
		}
	}
	if messages[4].Error != nil {
		t.Fatalf("continued probe = %+v", messages[4])
	}
}

func TestServeRejectsConcurrentExecuteAndCancelsDuringExecute(t *testing.T) {
	cancelled := make(chan struct{})
	handler := testHandler{execute: func(ctx context.Context, _ *protocol.ExecuteRequest, _ EventSink) (*protocol.ExecuteResult, error) {
		<-ctx.Done()
		close(cancelled)
		return &protocol.ExecuteResult{Outcome: protocol.OutcomeCancelled}, nil
	}}
	input := executeLine("first", "attempt-1") +
		executeLine("second", "attempt-2") +
		`{"jsonrpc":"2.0","id":"cancel","method":"cancel","params":{"attempt_id":"attempt-1"}}` + "\n"

	output, _, err := serveStatic(t, handler, input)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("execute context was not cancelled")
	}

	messages := decodeMessages(t, output)
	if len(messages) != 3 {
		t.Fatalf("message count = %d: %s", len(messages), output)
	}
	if messages[0].Error == nil || messages[0].Error.Code != protocol.CodeExecuteInProgress {
		t.Fatalf("concurrent response = %+v", messages[0])
	}
	if string(messages[1].ID) != `"cancel"` || messages[1].Error != nil {
		t.Fatalf("cancel response = %+v", messages[1])
	}
	if string(messages[2].ID) != `"first"` {
		t.Fatalf("execute response = %+v", messages[2])
	}
}

func TestCancelBeforeAndAfterExecuteAreNoOps(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- Serve(testHandler{}, stdinReader, stdoutWriter, io.Discard)
		_ = stdoutWriter.Close()
	}()

	output := bufio.NewReader(stdoutReader)
	writeLine(t, stdinWriter, `{"jsonrpc":"2.0","id":"before","method":"cancel","params":{"attempt_id":"attempt"}}`)
	before := readMessage(t, output)
	if before.Error != nil || string(before.ID) != `"before"` {
		t.Fatalf("before response = %+v", before)
	}

	writeLine(t, stdinWriter, strings.TrimSpace(executeLine("execute", "attempt")))
	execute := readMessage(t, output)
	if execute.Error != nil || string(execute.ID) != `"execute"` {
		t.Fatalf("execute response = %+v", execute)
	}

	writeLine(t, stdinWriter, `{"jsonrpc":"2.0","id":"after","method":"cancel","params":{"attempt_id":"attempt"}}`)
	after := readMessage(t, output)
	if after.Error != nil || string(after.ID) != `"after"` {
		t.Fatalf("after response = %+v", after)
	}

	_ = stdinWriter.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPartialFrameCancelsAndFails(t *testing.T) {
	handler := testHandler{execute: func(ctx context.Context, _ *protocol.ExecuteRequest, _ EventSink) (*protocol.ExecuteResult, error) {
		<-ctx.Done()
		return &protocol.ExecuteResult{Outcome: protocol.OutcomeCancelled}, nil
	}}
	output, stderr, err := serveStatic(t, handler, executeLine("execute", "attempt")+"{")
	if !errors.Is(err, protocol.ErrPartialFrame) {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(stderr, "partial JSON-RPC frame") {
		t.Fatalf("stderr = %q", stderr)
	}
	if output != "" {
		t.Fatalf("unexpected output = %q", output)
	}
}

func TestEventsPrecedeResponseAndSinkCloses(t *testing.T) {
	sinkReturned := make(chan EventSink, 1)
	handler := testHandler{execute: func(_ context.Context, _ *protocol.ExecuteRequest, sink EventSink) (*protocol.ExecuteResult, error) {
		const eventCount = 200
		var wait sync.WaitGroup
		wait.Add(eventCount)
		for index := 0; index < eventCount; index++ {
			go func(index int) {
				defer wait.Done()
				if err := sink.Progress(fmt.Sprintf("event-%d", index)); err != nil {
					t.Errorf("emit event: %v", err)
				}
			}(index)
		}
		wait.Wait()
		sinkReturned <- sink
		return &protocol.ExecuteResult{Outcome: protocol.OutcomeCompleted}, nil
	}}

	output, _, err := serveStatic(t, handler, executeLine("execute", "attempt"))
	if err != nil {
		t.Fatal(err)
	}
	messages := decodeMessages(t, output)
	if len(messages) != 201 {
		t.Fatalf("message count = %d", len(messages))
	}
	for index, message := range messages[:200] {
		if message.Method != "event" {
			t.Fatalf("message %d is not an event: %+v", index, message)
		}
	}
	if string(messages[200].ID) != `"execute"` || messages[200].Method != "" {
		t.Fatalf("last message is not execute response: %+v", messages[200])
	}
	if err := (<-sinkReturned).Progress("late"); !errors.Is(err, ErrEventSinkClosed) {
		t.Fatalf("late event error = %v", err)
	}
}

func TestServeStrictParams(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"execute","params":{"attempt_id":"a","unknown":true}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"cancel","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":3,"method":"probe","params":null}` + "\n"
	output, _, err := serveStatic(t, testHandler{}, input)
	if err != nil {
		t.Fatal(err)
	}
	for index, message := range decodeMessages(t, output) {
		if message.Error == nil || message.Error.Code != protocol.CodeInvalidParams {
			t.Fatalf("message %d = %+v", index, message)
		}
	}
}

func TestServeExecuteBudgetIngress(t *testing.T) {
	tests := []struct {
		name      string
		budget    string
		accepted  bool
		remaining int64
	}{
		{name: "zero", budget: `{"remaining_ms":0}`, accepted: true},
		{name: "safe integer maximum", budget: `{"remaining_ms":9007199254740991}`, accepted: true, remaining: 1<<53 - 1},
		{name: "old wire field", budget: `{"active_wall_clock_min":1}`},
		{name: "fraction", budget: `{"remaining_ms":1.5}`},
		{name: "negative", budget: `{"remaining_ms":-1}`},
		{name: "negative zero", budget: `{"remaining_ms":-0}`},
		{name: "outside safe integer range", budget: `{"remaining_ms":9007199254740992}`},
		{name: "outside int64 range", budget: `{"remaining_ms":9223372036854775808}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observed := make(chan int64, 1)
			handler := testHandler{execute: func(
				_ context.Context,
				request *protocol.ExecuteRequest,
				_ EventSink,
			) (*protocol.ExecuteResult, error) {
				observed <- request.Budget.RemainingMS
				return &protocol.ExecuteResult{Outcome: protocol.OutcomeCompleted}, nil
			}}
			input := fmt.Sprintf(
				`{"jsonrpc":"2.0","id":"execute","method":"execute","params":{"attempt_id":"attempt","budget":%s}}`+"\n",
				test.budget,
			)
			output, _, err := serveStatic(t, handler, input)
			if err != nil {
				t.Fatal(err)
			}
			messages := decodeMessages(t, output)
			if len(messages) != 1 {
				t.Fatalf("messages = %+v", messages)
			}
			if !test.accepted {
				if messages[0].Error == nil ||
					messages[0].Error.Code != protocol.CodeInvalidParams {
					t.Fatalf("response = %+v", messages[0])
				}
				select {
				case remaining := <-observed:
					t.Fatalf("handler received rejected budget %d", remaining)
				default:
				}
				return
			}
			if messages[0].Error != nil {
				t.Fatalf("response = %+v", messages[0])
			}
			select {
			case remaining := <-observed:
				if remaining != test.remaining {
					t.Fatalf("remaining_ms = %d, want %d", remaining, test.remaining)
				}
			default:
				t.Fatal("execute handler was not called")
			}
		})
	}
}

func TestServeRedactsSessionHintAndResultDetail(t *testing.T) {
	handler := testHandler{execute: func(_ context.Context, _ *protocol.ExecuteRequest, sink EventSink) (*protocol.ExecuteResult, error) {
		if err := sink.Progress("session-secret token=credential"); err != nil {
			return nil, err
		}
		return &protocol.ExecuteResult{
			Outcome: protocol.OutcomeCompleted,
			Detail:  "session-secret",
		}, nil
	}}
	input := `{"jsonrpc":"2.0","id":"execute","method":"execute","params":{"attempt_id":"attempt","session_hint":{"session_id":"session-secret"}}}` + "\n"
	output, _, err := serveStatic(t, handler, input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "session-secret") || strings.Contains(output, "credential") {
		t.Fatalf("sensitive value leaked: %s", output)
	}
}

func TestEventSinkLimitAndValidation(t *testing.T) {
	sink := newEventSink(protocol.NewWriter(io.Discard), "attempt", nil)
	if err := sink.Artifact("", "path"); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("invalid artifact error = %v", err)
	}
	if err := sink.Proposal("p", json.RawMessage(`[]`), false); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("invalid proposal error = %v", err)
	}
	for index := 0; index < MaxEventsPerAttempt; index++ {
		if err := sink.Progress("event"); err != nil {
			t.Fatalf("event %d: %v", index, err)
		}
	}
	if err := sink.Progress("overflow"); !errors.Is(err, ErrEventLimit) {
		t.Fatalf("overflow error = %v", err)
	}
}

func TestInvalidHandlerResultBecomesProtocolFailure(t *testing.T) {
	handler := testHandler{execute: func(context.Context, *protocol.ExecuteRequest, EventSink) (*protocol.ExecuteResult, error) {
		return &protocol.ExecuteResult{Outcome: protocol.OutcomeWaitingHuman}, nil
	}}
	output, _, err := serveStatic(t, handler, executeLine("execute", "attempt"))
	if err != nil {
		t.Fatal(err)
	}
	messages := decodeMessages(t, output)
	var result protocol.ExecuteResult
	if err := json.Unmarshal(messages[0].Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != protocol.OutcomeFailed || result.Failure == nil ||
		result.Failure.Kind != protocol.FailureProtocolError {
		t.Fatalf("result = %+v", result)
	}
}

func serveStatic(t *testing.T, handler Handler, input string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := Serve(handler, strings.NewReader(input), &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func executeLine(id, attemptID string) string {
	return fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%q,"method":"execute","params":{"attempt_id":%q,"budget":{"remaining_ms":0}}}`,
		id,
		attemptID,
	) + "\n"
}

func decodeMessages(t *testing.T, output string) []rpcMessage {
	t.Helper()
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return nil
	}
	lines := strings.Split(trimmed, "\n")
	messages := make([]rpcMessage, 0, len(lines))
	for _, line := range lines {
		var message rpcMessage
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		messages = append(messages, message)
	}
	return messages
}

func writeLine(t *testing.T, writer io.Writer, line string) {
	t.Helper()
	if _, err := io.WriteString(writer, line+"\n"); err != nil {
		t.Fatal(err)
	}
}

func readMessage(t *testing.T, reader *bufio.Reader) rpcMessage {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var message rpcMessage
	if err := json.Unmarshal(line, &message); err != nil {
		t.Fatal(err)
	}
	return message
}

func TestServeCompletesPromptly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- Serve(testHandler{}, strings.NewReader(""), &stdout, &stderr)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop on clean EOF")
	}
}
