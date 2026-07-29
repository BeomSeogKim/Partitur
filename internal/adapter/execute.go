//go:build linux || darwin

package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/adapterkit"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/launch"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

type runningExecute struct {
	process    *launch.Process
	stdin      *os.File
	frames     <-chan frameEvent
	stderrDone <-chan struct{}
	stderr     *limitedBuffer
	identity   string
}

type executeState struct {
	plan         ExecutePlan
	report       ExecuteReport
	outputs      map[string]string
	artifactSeen map[string]bool
	emittedIDs   map[string]bool
	blockingIDs  map[string]bool
	eventCount   int
}

func (c *Client) execute(ctx context.Context, plan ExecutePlan) (ExecuteReport, error) {
	if err := validateExecutePlan(plan); err != nil {
		return ExecuteReport{}, err
	}
	probeRequest, err := encodeProbeRequest()
	if err != nil {
		return ExecuteReport{}, err
	}
	executeRequest, err := encodeExecuteRequest(plan.Request)
	if err != nil {
		return ExecuteReport{}, err
	}

	deadline := c.now().Add(c.deadline)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()

	launchContext, cancelLaunch := context.WithDeadline(ctx, deadline)
	running, err := c.startExecute(launchContext, plan)
	cancelLaunch()
	if err != nil {
		return ExecuteReport{}, err
	}
	state := executeState{
		plan:         plan,
		outputs:      declaredOutputs(plan.Request.Brief.Outputs),
		artifactSeen: make(map[string]bool),
		emittedIDs:   make(map[string]bool),
		blockingIDs:  make(map[string]bool),
	}
	failWithoutOutcome := func(cause error) (ExecuteReport, error) {
		if cleanupErr := c.stopExecute(running); cleanupErr != nil {
			return state.report, sweepHalt(cleanupErr)
		}
		state.report.Stderr = adapterkit.SanitizeDiagnostic(running.stderr.String())
		return state.report, cause
	}
	failAfterOutputDrainWithoutOutcome := func(cause error) (ExecuteReport, error) {
		state.report.Result = nil
		if cleanupErr := c.sweepExecute(running); cleanupErr != nil {
			return state.report, sweepHalt(cleanupErr)
		}
		<-running.stderrDone
		_ = running.process.Wait()
		state.report.Stderr = adapterkit.SanitizeDiagnostic(running.stderr.String())
		return state.report, cause
	}

	if err := c.write(running.stdin, probeRequest); err != nil {
		return failWithoutOutcome(fmt.Errorf("write run probe request: %w", err))
	}
	for {
		select {
		case event := <-running.frames:
			if event.err != nil {
				return failWithoutOutcome(fmt.Errorf("read run probe response: %w", event.err))
			}
			probe, decodeErr := decodeProbeResponse(event.frame, plan.AdapterID)
			if decodeErr != nil {
				return failWithoutOutcome(decodeErr)
			}
			state.report.Probe = probe
			receipt, recordErr := plan.Recorder.RecordProbe(probe)
			if recordErr != nil {
				return failWithoutOutcome(recordErr)
			}
			if err := validateExecuteReceipt(receipt, string(runstate.EventAdapterProbed)); err != nil {
				return failWithoutOutcome(err)
			}
			goto probed
		case <-timer.C:
			return failWithoutOutcome(errors.New("run probe completion deadline expired"))
		case <-ctx.Done():
			return failWithoutOutcome(ctx.Err())
		}
	}

probed:
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	if err := c.write(running.stdin, executeRequest); err != nil {
		return c.finishFailure(running, &state, protocol.FailureAdapterUnavailable, "", err.Error())
	}

	cancelSignal := plan.Cancel
	cancelRequested := false
	cancelAcknowledged := false
	var cancelTimer *time.Timer
	var cancelTimeout <-chan time.Time
	defer func() {
		if cancelTimer != nil {
			cancelTimer.Stop()
		}
	}()
	cancelExpired := func() (ExecuteReport, error) {
		state.report.Result = nil
		return failWithoutOutcome(errors.New("cancel completion grace expired"))
	}
	interrupted := func(cause error) (ExecuteReport, error) {
		if cleanupErr := c.stopExecute(running); cleanupErr != nil {
			return state.report, sweepHalt(cleanupErr)
		}
		state.report.Stderr = adapterkit.SanitizeDiagnostic(running.stderr.String())
		return state.report, cause
	}
	for {
		if err := ctx.Err(); err != nil {
			return interrupted(err)
		}
		select {
		case event := <-running.frames:
			if event.err != nil {
				reason := ""
				if errors.Is(event.err, protocol.ErrPartialFrame) {
					reason = "partial_frame_eof"
				} else if errors.Is(event.err, protocol.ErrFrameTooLarge) {
					reason = "frame_too_large"
				}
				if reason != "" {
					return c.finishFailure(running, &state, protocol.FailureProtocolError, reason, event.err.Error())
				}
				return c.finishFailure(running, &state, protocol.FailureAdapterUnavailable, "", event.err.Error())
			}
			decoded, decodeErr := decodeExecuteFrame(event.frame)
			if decodeErr != nil {
				failure, ok := asProtocolFailure(decodeErr)
				if !ok {
					return state.report, decodeErr
				}
				return c.finishFailure(running, &state, protocol.FailureProtocolError, failure.reason, failure.detail)
			}
			if decoded.kind == executeFrameEvent {
				if eventErr := state.consumeEvent(decoded.event); eventErr != nil {
					if failure, ok := asProtocolFailure(eventErr); ok {
						return c.finishFailure(running, &state, protocol.FailureProtocolError, failure.reason, failure.detail)
					}
					return failWithoutOutcome(eventErr)
				}
				continue
			}
			if decoded.kind == executeFrameCancelAck {
				if !cancelRequested {
					return c.finishFailure(running, &state, protocol.FailureProtocolError, "strict_decode_failed", "unsolicited cancel acknowledgement")
				}
				if cancelAcknowledged {
					return c.finishFailure(running, &state, protocol.FailureProtocolError, "strict_decode_failed", "duplicate cancel acknowledgement")
				}
				cancelAcknowledged = true
				continue
			}
			if failure := state.validateBlocking(decoded.result); failure != nil {
				return c.finishFailure(running, &state, protocol.FailureProtocolError, failure.reason, failure.detail)
			}
			state.report.Result = &decoded.result
			goto response
		case <-cancelSignal:
			cancelRequest, encodeErr := encodeCancelRequest(protocol.CancelRequest{AttemptID: plan.Request.AttemptID})
			if encodeErr != nil {
				return c.finishFailure(running, &state, protocol.FailureAdapterUnavailable, "", encodeErr.Error())
			}
			if writeErr := c.write(running.stdin, cancelRequest); writeErr != nil {
				return c.finishFailure(running, &state, protocol.FailureAdapterUnavailable, "", writeErr.Error())
			}
			cancelRequested = true
			cancelSignal = nil
			cancelTimer = time.NewTimer(c.grace)
			cancelTimeout = cancelTimer.C
		case <-cancelTimeout:
			return cancelExpired()
		case <-ctx.Done():
			return interrupted(ctx.Err())
		}
	}

response:
	if err := running.stdin.Close(); err != nil {
		running.stdin = nil
		return c.finishFailure(running, &state, protocol.FailureAdapterUnavailable, "", err.Error())
	}
	running.stdin = nil
	for {
		select {
		case event := <-running.frames:
			if event.err == nil {
				decoded, decodeErr := decodeExecuteFrame(event.frame)
				if decodeErr == nil {
					if decoded.kind == executeFrameCancelAck {
						if cancelRequested {
							if !cancelAcknowledged {
								cancelAcknowledged = true
								continue
							}
						}
					}
				}
				return c.finishFailure(running, &state, protocol.FailureProtocolError, "strict_decode_failed", "frame followed execute response")
			}
			if errors.Is(event.err, io.EOF) {
				if cancelRequested {
					if !cancelAcknowledged {
						state.report.Result = nil
						return failAfterOutputDrainWithoutOutcome(errors.New("cancel acknowledgement absent"))
					}
				}
				goto outputDrained
			}
			reason := "strict_decode_failed"
			if errors.Is(event.err, protocol.ErrPartialFrame) {
				reason = "partial_frame_eof"
			} else if errors.Is(event.err, protocol.ErrFrameTooLarge) {
				reason = "frame_too_large"
			}
			return c.finishFailure(running, &state, protocol.FailureProtocolError, reason, event.err.Error())
		case <-cancelTimeout:
			return cancelExpired()
		case <-ctx.Done():
			if cleanupErr := c.stopExecute(running); cleanupErr != nil {
				return state.report, sweepHalt(cleanupErr)
			}
			state.report.Stderr = adapterkit.SanitizeDiagnostic(running.stderr.String())
			return state.report, ctx.Err()
		}
	}

outputDrained:
	select {
	case <-running.stderrDone:
	case <-cancelTimeout:
		return failAfterOutputDrainWithoutOutcome(errors.New("cancel completion grace expired"))
	case <-ctx.Done():
		if cleanupErr := c.sweepExecute(running); cleanupErr != nil {
			return state.report, sweepHalt(cleanupErr)
		}
		<-running.stderrDone
		_ = running.process.Wait()
		state.report.Stderr = adapterkit.SanitizeDiagnostic(running.stderr.String())
		return state.report, ctx.Err()
	}
	state.report.Stderr = adapterkit.SanitizeDiagnostic(running.stderr.String())
	wait := make(chan error, 1)
	go func() {
		wait <- running.process.Wait()
	}()
	var waitErr error
	select {
	case waitErr = <-wait:
	case <-cancelTimeout:
		// Drain the goroutine above rather than calling Wait again: two concurrent
		// Cmd.Wait calls on one process are a data race, which is why the context case
		// below takes the same shape.
		state.report.Result = nil
		if cleanupErr := c.sweepExecute(running); cleanupErr != nil {
			return state.report, sweepHalt(cleanupErr)
		}
		<-wait
		state.report.Stderr = adapterkit.SanitizeDiagnostic(running.stderr.String())
		return state.report, errors.New("cancel completion grace expired")
	case <-ctx.Done():
		if cleanupErr := c.sweepExecute(running); cleanupErr != nil {
			return state.report, sweepHalt(cleanupErr)
		}
		<-wait
		state.report.Stderr = adapterkit.SanitizeDiagnostic(running.stderr.String())
		return state.report, ctx.Err()
	}
	if waitErr != nil {
		state.report.Result = nil
		if cleanupErr := c.sweepExecute(running); cleanupErr != nil {
			return state.report, sweepHalt(cleanupErr)
		}
		return c.recordFailure(&state, protocol.FailureAdapterUnavailable, "", waitErr.Error())
	}
	if cleanupErr := c.verifyAndSweepExecute(running); cleanupErr != nil {
		return state.report, sweepHalt(cleanupErr)
	}
	reach(plan.Probe, faultpoint.PointExecuteAdapterSwept)
	if !cancelRequested {
		if err := c.recordStop(&state); err != nil {
			return state.report, err
		}
	}
	if err := c.recordResult(&state, cancelRequested); err != nil {
		return state.report, err
	}
	return state.report, nil
}

func validateExecutePlan(plan ExecutePlan) error {
	recorder := plan.Recorder
	if plan.AdapterID == "" || !filepath.IsAbs(plan.AdapterPath) ||
		!filepath.IsAbs(plan.TrampolinePath) || plan.AttemptRoot == "" ||
		plan.LaunchID == "" || plan.IntervalID == "" || plan.IntervalOpened.IsZero() ||
		plan.Request.OutputDir == "" || plan.Request.AttemptID == "" ||
		plan.RecordIdentity == nil || recorder.RecordProbe == nil ||
		recorder.RecordArtifact == nil || recorder.RecordExecutionStopped == nil ||
		recorder.RecordOutcome == nil || recorder.ObserveLog == nil ||
		recorder.ObserveProgress == nil {
		return fmt.Errorf("%w: required field absent", ErrInvalidExecutePlan)
	}
	return nil
}

func (c *Client) startExecute(ctx context.Context, plan ExecutePlan) (*runningExecute, error) {
	childStdin, stdin, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create adapter stdin: %w", err)
	}
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		_ = childStdin.Close()
		return nil, fmt.Errorf("create adapter stdout: %w", err)
	}
	stderr, childStderr, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		_ = childStdin.Close()
		_ = stdout.Close()
		_ = childStdout.Close()
		return nil, fmt.Errorf("create adapter stderr: %w", err)
	}
	closeAll := func() {
		_ = stdin.Close()
		_ = childStdin.Close()
		_ = stdout.Close()
		_ = childStdout.Close()
		_ = stderr.Close()
		_ = childStderr.Close()
	}
	process, err := c.launch(ctx, launch.Request{
		Kind:           launch.Adapter,
		TrampolinePath: plan.TrampolinePath,
		AttemptRoot:    plan.AttemptRoot,
		LaunchID:       plan.LaunchID,
		Executable:     plan.AdapterPath,
		Arguments:      []string{},
		Environment:    slices.Clone(c.environment),
		Directory:      plan.Directory,
		Stdin:          childStdin,
		Stdout:         childStdout,
		Stderr:         childStderr,
		RecordIdentity: plan.RecordIdentity,
		Probe:          plan.Probe,
	})
	if err != nil {
		closeAll()
		return nil, err
	}
	_ = childStdin.Close()
	_ = childStdout.Close()
	_ = childStderr.Close()

	frames := make(chan frameEvent, 2)
	go func() {
		defer stdout.Close()
		c.read(stdout, frames)
	}()
	stderrBuffer := &limitedBuffer{limit: MaxProbeStderrBytes}
	stderrDone := make(chan struct{})
	go func() {
		defer stderr.Close()
		_, _ = c.copyStderr(stderrBuffer, stderr)
		close(stderrDone)
	}()
	identity, err := processIdentityString(process.Identity.Start)
	if err != nil {
		_ = stdin.Close()
		_ = c.sessions.terminate(process.Identity.SessionID, "invalid", c.grace)
		<-stderrDone
		_ = process.Wait()
		return nil, err
	}
	return &runningExecute{
		process: process, stdin: stdin, frames: frames,
		stderrDone: stderrDone, stderr: stderrBuffer, identity: identity,
	}, nil
}

func processIdentityString(identity runstate.StartIdentity) (string, error) {
	switch identity := identity.(type) {
	case runstate.LinuxStartIdentity:
		return "linux-proc-start:" + identity.BootID + ":" + identity.StartTicks, nil
	case runstate.DarwinStartIdentity:
		return fmt.Sprintf("darwin-proc-start:%d.%06d", identity.StartTVSec, identity.StartTVUsec), nil
	default:
		return "", fmt.Errorf("unsupported start identity %T", identity)
	}
}

func (state *executeState) consumeEvent(value any) error {
	state.eventCount++
	if state.eventCount > adapterkit.MaxEventsPerAttempt {
		return &executeProtocolFailure{reason: "event_limit_exceeded", detail: "execute event limit exceeded"}
	}
	switch event := value.(type) {
	case *protocol.LogEvent:
		if strings.TrimSpace(event.Level) == "" || strings.TrimSpace(event.Message) == "" {
			return &executeProtocolFailure{reason: "strict_decode_failed", detail: "invalid log event"}
		}
		event.Message = adapterkit.SanitizeMessage(event.Message)
		state.plan.Recorder.ObserveLog(*event)
	case *protocol.ProgressEvent:
		if strings.TrimSpace(event.Message) == "" {
			return &executeProtocolFailure{reason: "strict_decode_failed", detail: "invalid progress event"}
		}
		event.Message = adapterkit.SanitizeMessage(event.Message)
		state.plan.Recorder.ObserveProgress(*event)
	case *protocol.ArtifactEvent:
		return state.consumeArtifact(*event)
	case *protocol.ProposalEvent:
		if strings.TrimSpace(event.ID) == "" || !validAmendment(event.Amendment) {
			return &executeProtocolFailure{reason: "strict_decode_failed", detail: "invalid proposal event"}
		}
		if state.emittedIDs[event.ID] {
			return &executeProtocolFailure{reason: "duplicate_emitted_id", detail: "duplicate emitted id"}
		}
		state.emittedIDs[event.ID] = true
		if !state.plan.MayPropose {
			return &executeProtocolFailure{reason: "proposal_without_authority", detail: "proposal emitted without authority"}
		}
		if state.plan.Draft && !event.RequiresDecision {
			return &executeProtocolFailure{reason: "draft_non_blocking_proposal", detail: "draft proposal must block"}
		}
		if event.RequiresDecision {
			state.blockingIDs[event.ID] = true
		}
		state.report.Proposals = append(state.report.Proposals, *event)
	case *protocol.QuestionEvent:
		if strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.Question) == "" {
			return &executeProtocolFailure{reason: "strict_decode_failed", detail: "invalid question event"}
		}
		if state.emittedIDs[event.ID] {
			return &executeProtocolFailure{reason: "duplicate_emitted_id", detail: "duplicate emitted id"}
		}
		state.emittedIDs[event.ID] = true
		state.blockingIDs[event.ID] = true
		state.report.Questions = append(state.report.Questions, *event)
	default:
		return &executeProtocolFailure{reason: "strict_decode_failed", detail: "unknown event"}
	}
	return nil
}

func validAmendment(raw []byte) bool {
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) < 2 || trimmed[0] != '{' {
		return false
	}
	var object map[string]json.RawMessage
	return protocol.DecodeStrict(raw, &object) == nil
}

func (state *executeState) consumeArtifact(event protocol.ArtifactEvent) error {
	if strings.TrimSpace(event.ArtifactID) == "" {
		return &executeProtocolFailure{reason: "strict_decode_failed", detail: "artifact id is required"}
	}
	if state.artifactSeen[event.ArtifactID] {
		return &executeProtocolFailure{reason: "duplicate_artifact_instance", detail: "duplicate artifact notification"}
	}
	state.artifactSeen[event.ArtifactID] = true
	kind, declared := state.outputs[event.ArtifactID]
	if !declared {
		return &executeProtocolFailure{reason: "undeclared_artifact", detail: "artifact is not declared"}
	}
	if kind == "change_set" {
		return &executeProtocolFailure{reason: "change_set_emitted_as_artifact", detail: "change_set cannot be emitted as an artifact"}
	}
	path, source, err := resolveArtifactPath(state.plan.Request.OutputDir, event.Path)
	if err != nil {
		return &executeProtocolFailure{reason: "artifact_path_escape", detail: err.Error()}
	}
	observation := ArtifactObservation{
		ArtifactID: event.ArtifactID, Kind: kind, Path: path, SourcePath: source,
	}
	receipt, err := state.plan.Recorder.RecordArtifact(observation)
	if err != nil {
		return err
	}
	if err := validateExecuteReceipt(receipt, string(runstate.EventArtifactRecorded)); err != nil {
		return err
	}
	state.report.Artifacts = append(state.report.Artifacts, observation)
	return nil
}

func resolveArtifactPath(outputDir, announced string) (string, string, error) {
	if announced == "" {
		return "", "", errors.New("artifact path is required")
	}
	if filepath.IsAbs(announced) {
		return "", "", errors.New("artifact path must be output_dir-relative")
	}
	for _, segment := range strings.FieldsFunc(announced, func(value rune) bool {
		return value == '/' || value == '\\'
	}) {
		if segment == ".." {
			return "", "", errors.New("artifact path contains parent traversal")
		}
	}
	root, err := filepath.EvalSymlinks(outputDir)
	if err != nil {
		return "", "", err
	}
	source := filepath.ToSlash(filepath.Clean(announced))
	candidate := filepath.Join(outputDir, filepath.FromSlash(source))
	candidate, err = filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", "", err
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", errors.New("artifact path is outside output_dir")
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() {
		return "", "", errors.New("artifact path is not a regular file")
	}
	return candidate, source, nil
}

func (state *executeState) validateBlocking(result protocol.ExecuteResult) *executeProtocolFailure {
	got := slices.Clone(result.PendingDecisionIDs)
	slices.Sort(got)
	if len(got) != len(state.blockingIDs) {
		return &executeProtocolFailure{reason: "blocking_set_mismatch", detail: "pending decision set does not match blocking events"}
	}
	for index, id := range got {
		if index > 0 && got[index-1] == id {
			return &executeProtocolFailure{reason: "blocking_set_mismatch", detail: "pending decision set contains duplicates"}
		}
		if !state.blockingIDs[id] {
			return &executeProtocolFailure{reason: "blocking_set_mismatch", detail: "pending decision set does not match blocking events"}
		}
	}
	if len(state.blockingIDs) != 0 && result.Outcome != protocol.OutcomeWaitingHuman {
		return &executeProtocolFailure{reason: "blocking_set_mismatch", detail: "blocking events require waiting_human"}
	}
	return nil
}

func (c *Client) finishFailure(running *runningExecute, state *executeState, kind protocol.FailureKind, reason, detail string) (ExecuteReport, error) {
	state.report.Result = nil
	if cleanupErr := c.stopExecute(running); cleanupErr != nil {
		return state.report, sweepHalt(cleanupErr)
	}
	state.report.Stderr = adapterkit.SanitizeDiagnostic(running.stderr.String())
	return c.recordFailure(state, kind, reason, detail)
}

func (c *Client) stopExecute(running *runningExecute) error {
	if running.stdin != nil {
		_ = running.stdin.Close()
		running.stdin = nil
	}
	if err := c.sweepExecute(running); err != nil {
		return err
	}
	for {
		event := <-running.frames
		if event.err != nil {
			break
		}
	}
	<-running.stderrDone
	_ = running.process.Wait()
	return nil
}

func (c *Client) sweepExecute(running *runningExecute) error {
	return c.sessions.terminate(running.process.Identity.SessionID, running.identity, c.grace)
}

func (c *Client) verifyAndSweepExecute(running *runningExecute) error {
	empty, err := c.sessions.verifyEmpty(running.process.Identity.SessionID, running.identity)
	if err != nil {
		return err
	}
	if empty {
		return nil
	}
	return c.sweepExecute(running)
}

func (c *Client) recordStop(state *executeState) error {
	observed := c.now()
	duration := observed.Sub(state.plan.IntervalOpened)
	if duration < 0 {
		duration = 0
	}
	receipt, err := state.plan.Recorder.RecordExecutionStopped(ExecutionStop{
		IntervalID: state.plan.IntervalID, Reason: "normal", Charging: "measured",
		ChargedDurationMS: duration.Milliseconds(), ObservedAt: observed,
	})
	if err != nil {
		return err
	}
	if err := validateExecuteReceipt(receipt, string(runstate.EventExecutionStopped)); err != nil {
		return err
	}
	reach(state.plan.Probe, faultpoint.PointExecuteIntervalStopped)
	return nil
}

func (c *Client) recordResult(state *executeState, cancelRequested bool) error {
	result := *state.report.Result
	eventType := ""
	reason := ""
	switch result.Outcome {
	case protocol.OutcomeCompleted:
		eventType = string(runstate.EventPerformerCompleted)
	case protocol.OutcomeFailed:
		eventType = string(runstate.EventAttemptFailed)
	case protocol.OutcomeWaitingHuman:
		eventType = "attempt.blocked"
	case protocol.OutcomeCancelled:
		if cancelRequested {
			return nil
		}
		eventType = string(runstate.EventAttemptFailed)
		result.Failure = &protocol.Failure{Kind: protocol.FailureTaskFailed}
		reason = "unsolicited_cancel"
	}
	return state.recordOutcome(eventType, result, reason)
}

func (c *Client) recordFailure(state *executeState, kind protocol.FailureKind, reason, detail string) (ExecuteReport, error) {
	if err := c.recordStop(state); err != nil {
		return state.report, err
	}
	result := protocol.ExecuteResult{
		Outcome: protocol.OutcomeFailed,
		Failure: &protocol.Failure{Kind: kind, Detail: adapterkit.SanitizeMessage(detail)},
	}
	err := state.recordOutcome(string(runstate.EventAttemptFailed), result, reason)
	return state.report, err
}

func (state *executeState) recordOutcome(eventType string, result protocol.ExecuteResult, reason string) error {
	raised := make([]RaisedDecision, 0, len(state.report.Questions)+len(state.report.Proposals))
	for index := range state.report.Questions {
		question := state.report.Questions[index]
		raised = append(raised, RaisedDecision{Kind: protocol.EventQuestion, Question: &question})
	}
	for index := range state.report.Proposals {
		proposal := state.report.Proposals[index]
		raised = append(raised, RaisedDecision{Kind: protocol.EventProposal, Proposal: &proposal})
	}
	receipt, err := state.plan.Recorder.RecordOutcome(OutcomeObservation{
		EventType: eventType, Result: result, FailureReason: reason,
		Raised: raised, Stderr: adapterkit.SanitizeDiagnostic(state.report.Stderr),
	})
	if err != nil {
		return err
	}
	if err := validateExecuteReceipt(receipt, eventType); err != nil {
		return err
	}
	reach(state.plan.Probe, faultpoint.PointExecuteOutcomeRecorded)
	return nil
}

func reach(probe faultpoint.Probe, point faultpoint.PointID) {
	if probe != nil {
		probe.Reached(point)
	}
}

func validateExecuteReceipt(receipt faultpoint.DurabilityReceipt, eventType string) error {
	mutation := receipt.Mutation
	if mutation.Kind != faultpoint.JournalAppend || mutation.EventType != eventType ||
		mutation.EventID == "" || mutation.Sequence == 0 ||
		mutation.Timestamp == "" || mutation.Path == "" {
		return fmt.Errorf("invalid execute durability receipt: want durable %s journal append", eventType)
	}
	return nil
}

func declaredOutputs(outputs []protocol.OutputSpec) map[string]string {
	result := make(map[string]string, len(outputs))
	for _, output := range outputs {
		result[output.ArtifactID] = output.Kind
	}
	return result
}

func sweepHalt(err error) error {
	return &HaltError{Reason: ErrSweepUnverifiable, Cause: err}
}
