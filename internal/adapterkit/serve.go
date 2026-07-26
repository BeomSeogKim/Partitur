// Package adapterkit implements the common adapter-side protocol from DESIGN §4.
package adapterkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

const MaxEventsPerAttempt = 10_000

var (
	ErrEventSinkClosed = errors.New("event sink is closed")
	ErrEventLimit      = errors.New("attempt event limit exceeded")
	ErrInvalidEvent    = errors.New("invalid event")
)

type Handler interface {
	Probe(context.Context) (*protocol.ProbeResult, error)
	Execute(context.Context, *protocol.ExecuteRequest, EventSink) (*protocol.ExecuteResult, error)
}

type EventSink interface {
	Log(level, message string) error
	Progress(message string) error
	Artifact(artifactID, path string) error
	Proposal(id string, amendment json.RawMessage, requiresDecision bool) error
	Question(id, question string) error
}

type frameRead struct {
	frame []byte
	err   error
}

type execution struct {
	id     json.RawMessage
	cancel context.CancelFunc
	sink   *eventSink
}

type executionCompletion struct {
	execution *execution
	result    *protocol.ExecuteResult
	err       error
}

// Serve runs one adapter process. It supports macOS and Linux; vendor process
// execution is unavailable on other platforms.
func Serve(handler Handler, stdin io.Reader, stdout, stderr io.Writer) error {
	return serveContext(context.Background(), handler, stdin, stdout, stderr)
}

// ServeProcess runs one adapter process on the standard streams and handles
// process termination signals.
func ServeProcess(handler Handler) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	// Go 1.26 keeps this registration active until stop: its buffered,
	// non-blocking relay absorbs repeated SIGTERMs while shutdown drains.
	defer stop()
	return serveContext(ctx, handler, os.Stdin, os.Stdout, os.Stderr)
}

func serveContext(ctx context.Context, handler Handler, stdin io.Reader, stdout, stderr io.Writer) error {
	if handler == nil {
		return errors.New("handler is required")
	}

	reader := protocol.NewReader(stdin)
	writer := protocol.NewWriter(stdout)
	frames := make(chan frameRead, 1)
	completions := make(chan executionCompletion, 1)
	go readFrames(reader, frames)

	var running *execution
	var closing error
	ctxDone := ctx.Done()
	beginClosing := func(err error) bool {
		closing = err
		frames = nil
		if running == nil {
			return true
		}
		running.cancel()
		return false
	}

	for {
		select {
		case <-ctxDone:
			ctxDone = nil
			if beginClosing(nil) {
				return nil
			}

		case read := <-frames:
			if read.err != nil {
				switch {
				case errors.Is(read.err, protocol.ErrFrameTooLarge):
					if err := writer.WriteResponse(protocol.NewErrorResponse(nil, protocol.CodeFrameTooLarge, "frame too large")); err != nil {
						return stopExecution(running, completions, err)
					}
					continue
				case errors.Is(read.err, io.EOF):
					closing = nil
				case errors.Is(read.err, protocol.ErrPartialFrame):
					closing = protocol.ErrPartialFrame
					fmt.Fprintln(stderr, "fatal: stdin ended with a partial JSON-RPC frame")
				default:
					closing = fmt.Errorf("read JSON-RPC frame: %w", read.err)
					fmt.Fprintf(stderr, "fatal: %v\n", closing)
				}
				if beginClosing(closing) {
					return closing
				}
				continue
			}

			request, err := protocol.DecodeRequest(read.frame)
			if err != nil {
				code := protocol.CodeInvalidRequest
				message := "invalid request"
				if errors.Is(err, protocol.ErrParseRequest) {
					code = protocol.CodeParseError
					message = "parse error"
				}
				if err := writer.WriteResponse(protocol.NewErrorResponse(nil, code, message)); err != nil {
					return stopExecution(running, completions, err)
				}
				continue
			}

			switch request.Method {
			case "probe":
				if !emptyParams(request.Params) {
					if err := writeInvalidParams(writer, request.ID); err != nil {
						return stopExecution(running, completions, err)
					}
					continue
				}
				result, probeErr := handler.Probe(ctx)
				if probeErr != nil || result == nil {
					message := "probe failed"
					if probeErr != nil {
						message = SanitizeMessage(probeErr.Error())
					}
					if err := writer.WriteResponse(protocol.NewErrorResponse(request.ID, protocol.CodeInternalError, message)); err != nil {
						return stopExecution(running, completions, err)
					}
					continue
				}
				if err := writer.WriteResponse(protocol.NewResultResponse(request.ID, result)); err != nil {
					return stopExecution(running, completions, err)
				}

			case "execute":
				var executeRequest protocol.ExecuteRequest
				if err := protocol.DecodeStrict(request.Params, &executeRequest); err != nil || executeRequest.AttemptID == "" {
					if err := writeInvalidParams(writer, request.ID); err != nil {
						return stopExecution(running, completions, err)
					}
					continue
				}
				if running != nil {
					if err := writer.WriteResponse(protocol.NewErrorResponse(request.ID, protocol.CodeExecuteInProgress, "execute already in progress")); err != nil {
						return stopExecution(running, completions, err)
					}
					continue
				}

				ctx, cancel := context.WithCancel(ctx)
				current := &execution{
					id:     append(json.RawMessage(nil), request.ID...),
					cancel: cancel,
					sink:   newEventSink(writer, executeRequest.AttemptID, executeRequest.SessionHint),
				}
				running = current
				go func() {
					result, executeErr := handler.Execute(ctx, &executeRequest, current.sink)
					completions <- executionCompletion{execution: current, result: result, err: executeErr}
				}()

			case "cancel":
				var cancelRequest protocol.CancelRequest
				if err := protocol.DecodeStrict(request.Params, &cancelRequest); err != nil || cancelRequest.AttemptID == "" {
					if err := writeInvalidParams(writer, request.ID); err != nil {
						return stopExecution(running, completions, err)
					}
					continue
				}
				if running != nil && running.sink.attemptID == cancelRequest.AttemptID {
					running.cancel()
				}
				if err := writer.WriteResponse(protocol.NewResultResponse(request.ID, struct{}{})); err != nil {
					return stopExecution(running, completions, err)
				}

			default:
				if err := writer.WriteResponse(protocol.NewErrorResponse(request.ID, protocol.CodeMethodNotFound, "method not found")); err != nil {
					return stopExecution(running, completions, err)
				}
			}

		case completion := <-completions:
			completion.execution.sink.close()
			completion.execution.cancel()
			if running == completion.execution {
				running = nil
			}

			if closing != nil && frames == nil {
				return closing
			}

			result := completion.result
			if completion.err != nil || result == nil {
				detail := "execute handler failed"
				if completion.err != nil {
					detail = SanitizeMessage(completion.err.Error(), completion.execution.sink.sensitive...)
				}
				result = &protocol.ExecuteResult{
					Outcome: protocol.OutcomeFailed,
					Failure: &protocol.Failure{Kind: protocol.FailureTaskFailed, Detail: detail},
				}
			}
			if err := validateExecuteResult(result); err != nil {
				result = &protocol.ExecuteResult{
					Outcome: protocol.OutcomeFailed,
					Failure: &protocol.Failure{
						Kind:   protocol.FailureProtocolError,
						Detail: "invalid execute result",
					},
				}
			}
			result.Detail = SanitizeMessage(result.Detail, completion.execution.sink.sensitive...)
			if result.Failure != nil {
				result.Failure.Detail = SanitizeMessage(result.Failure.Detail, completion.execution.sink.sensitive...)
			}
			if err := writer.WriteResponse(protocol.NewResultResponse(completion.execution.id, result)); err != nil {
				return err
			}
			if frames == nil {
				return nil
			}
		}
	}
}

func readFrames(reader *protocol.Reader, frames chan<- frameRead) {
	for {
		frame, err := reader.ReadFrame()
		frames <- frameRead{frame: frame, err: err}
		if err != nil && !errors.Is(err, protocol.ErrFrameTooLarge) {
			return
		}
	}
}

func stopExecution(running *execution, completions <-chan executionCompletion, serveErr error) error {
	if running != nil {
		running.cancel()
		completion := <-completions
		completion.execution.sink.close()
	}
	return serveErr
}

func emptyParams(params json.RawMessage) bool {
	trimmed := bytes.TrimSpace(params)
	if len(trimmed) == 0 {
		return true
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	var empty struct{}
	return protocol.DecodeStrict(trimmed, &empty) == nil
}

func writeInvalidParams(writer *protocol.Writer, id json.RawMessage) error {
	return writer.WriteResponse(protocol.NewErrorResponse(id, protocol.CodeInvalidParams, "invalid params"))
}

type eventSink struct {
	mu        sync.Mutex
	writer    *protocol.Writer
	closed    bool
	attemptID string
	sensitive []string
	count     int
}

func newEventSink(writer *protocol.Writer, attemptID string, sessionHint json.RawMessage) *eventSink {
	return &eventSink{
		writer:    writer,
		attemptID: attemptID,
		sensitive: sessionHintSecrets(sessionHint),
	}
}

func (s *eventSink) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

func (s *eventSink) emit(params any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrEventSinkClosed
	}
	if s.count >= MaxEventsPerAttempt {
		return ErrEventLimit
	}
	if err := s.writer.WriteNotification("event", params); err != nil {
		return err
	}
	s.count++
	return nil
}

func (s *eventSink) Log(level, message string) error {
	return s.emit(protocol.LogEvent{
		Type:    protocol.EventLog,
		Level:   level,
		Message: SanitizeMessage(message, s.sensitive...),
	})
}

func (s *eventSink) Progress(message string) error {
	return s.emit(protocol.ProgressEvent{
		Type:    protocol.EventProgress,
		Message: SanitizeMessage(message, s.sensitive...),
	})
}

func (s *eventSink) Artifact(artifactID, path string) error {
	if artifactID == "" || path == "" {
		return ErrInvalidEvent
	}
	return s.emit(protocol.ArtifactEvent{Type: protocol.EventArtifact, ArtifactID: artifactID, Path: path})
}

func (s *eventSink) Proposal(id string, amendment json.RawMessage, requiresDecision bool) error {
	trimmed := bytes.TrimSpace(amendment)
	if id == "" || len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return ErrInvalidEvent
	}
	return s.emit(protocol.ProposalEvent{
		Type:             protocol.EventProposal,
		ID:               id,
		Amendment:        amendment,
		RequiresDecision: requiresDecision,
	})
}

func (s *eventSink) Question(id, question string) error {
	if id == "" || question == "" {
		return ErrInvalidEvent
	}
	return s.emit(protocol.QuestionEvent{
		Type:     protocol.EventQuestion,
		ID:       id,
		Question: SanitizeMessage(question, s.sensitive...),
	})
}

func validateExecuteResult(result *protocol.ExecuteResult) error {
	validFailureKinds := map[protocol.FailureKind]struct{}{
		protocol.FailureAdapterUnavailable: {},
		protocol.FailureModelUnavailable:   {},
		protocol.FailureProviderTimeout:    {},
		protocol.FailureRateLimited:        {},
		protocol.FailureAuthentication:     {},
		protocol.FailureProtocolError:      {},
		protocol.FailureGrantDenied:        {},
		protocol.FailureTaskFailed:         {},
	}
	switch result.Outcome {
	case protocol.OutcomeFailed:
		if result.Failure == nil {
			return errors.New("failed outcome requires failure")
		}
		if _, ok := validFailureKinds[result.Failure.Kind]; !ok {
			return errors.New("unknown failure kind")
		}
	case protocol.OutcomeCompleted, protocol.OutcomeCancelled:
		if result.Failure != nil || len(result.PendingDecisionIDs) != 0 {
			return errors.New("terminal outcome has incompatible fields")
		}
	case protocol.OutcomeWaitingHuman:
		if result.Failure != nil || len(result.PendingDecisionIDs) == 0 {
			return errors.New("waiting_human requires pending decisions")
		}
	default:
		return errors.New("unknown execute outcome")
	}

	seen := make(map[string]struct{}, len(result.PendingDecisionIDs))
	for _, id := range result.PendingDecisionIDs {
		if id == "" {
			return errors.New("pending decision ID is empty")
		}
		if _, exists := seen[id]; exists {
			return errors.New("duplicate pending decision ID")
		}
		seen[id] = struct{}{}
	}
	return nil
}
