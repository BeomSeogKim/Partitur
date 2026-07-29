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
	plan                 ExecutePlan
	report               ExecuteReport
	outputs              map[string]string
	artifactSeen         map[string]bool
	emittedIDs           map[string]bool
	blockingIDs          map[string]bool
	eventCount           int
	cancellationInFlight bool
	cancelSignal         <-chan struct{}
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
	cancelSignal := plan.Cancel
	state.cancelSignal = plan.Cancel
	cancelRequested := false
	cancelAcknowledged := false
	var cancelTimer *time.Timer
	var cancelTimeout <-chan time.Time
	defer func() {
		if cancelTimer != nil {
			cancelTimer.Stop()
		}
	}()
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
	cancelExpired := func() (ExecuteReport, error) {
		state.report.Result = nil
		return failWithoutOutcome(errors.New("cancel completion grace expired"))
	}
	observeCancellation := func() {
		// §6's outer grace has to start wherever the cancellation is first seen, or a drain
		// that never finishes has nothing bounding it. No protocol `cancel` is issued from
		// the post-response points: the call being drained has already answered.
		state.cancellationInFlight = true
		cancelSignal = nil
		if cancelTimer == nil {
			cancelTimer = time.NewTimer(c.grace)
			cancelTimeout = cancelTimer.C
		}
	}
	type writeOutcome uint8
	const (
		writeCompleted writeOutcome = iota
		writeCancelled
		writeExpired
		writeInterrupted
		writeDeadlineExpired
	)
	// deadline is the run-probe completion deadline, which §4 says covers the request write;
	// the execute and cancel writes pass nil because their bound is the cancellation grace.
	writeBounded := func(request []byte, deadline <-chan time.Time) (error, writeOutcome) {
		// Capture the handle before the goroutine starts. Every non-completion exit below
		// leaves the write still blocked, and the sweep that follows sets running.stdin to
		// nil — reading the field from inside the goroutine would race that assignment.
		// Closing the file under a blocked Write is safe and is what unblocks it.
		stdin := running.stdin
		completed := make(chan error, 1)
		go func() {
			completed <- c.write(stdin, request)
		}()
		// A write that has already finished is taken over a cancellation that is also
		// ready: discarding a completed write would lose work for nothing, and the very
		// next select observes the cancellation anyway. Without this the two are peers and
		// Go picks between them at random.
		select {
		case writeErr := <-completed:
			return writeErr, writeCompleted
		default:
		}
		select {
		case writeErr := <-completed:
			return writeErr, writeCompleted
		case <-cancelSignal:
			if ctx.Err() != nil {
				return nil, writeInterrupted
			}
			observeCancellation()
			return nil, writeCancelled
		case <-cancelTimeout:
			return nil, writeExpired
		case <-deadline:
			return nil, writeDeadlineExpired
		case <-ctx.Done():
			return nil, writeInterrupted
		}
	}
	interrupted := func(cause error) (ExecuteReport, error) {
		if cleanupErr := c.stopExecute(running); cleanupErr != nil {
			return state.report, sweepHalt(cleanupErr)
		}
		state.report.Stderr = adapterkit.SanitizeDiagnostic(running.stderr.String())
		return state.report, cause
	}

	// §4 puts the run-probe request write inside the completion deadline, and §6's watch
	// runs for the driver's whole tenure — so a cancellation arriving here has to be seen
	// even though no `execute` has been authorized and there is nothing to protocol-cancel.
	probeWriteErr, probeWriteResult := writeBounded(probeRequest, timer.C)
	switch probeWriteResult {
	case writeCancelled:
		return failWithoutOutcome(errors.New("cancel observed while writing run probe request"))
	case writeDeadlineExpired:
		return failWithoutOutcome(errors.New("run probe completion deadline expired"))
	case writeInterrupted:
		return failWithoutOutcome(ctx.Err())
	}
	if probeWriteErr != nil {
		return failWithoutOutcome(fmt.Errorf("write run probe request: %w", probeWriteErr))
	}
	c.observeWindow(executeWindowProbeResponse)
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
		case <-cancelSignal:
			observeCancellation()
			return failWithoutOutcome(errors.New("cancel observed while awaiting run probe response"))
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
	writeErr, writeResult := writeBounded(executeRequest, nil)
	switch writeResult {
	case writeCancelled:
		state.report.Result = nil
		return failWithoutOutcome(errors.New("cancel observed while writing execute request"))
	case writeExpired:
		return cancelExpired()
	case writeInterrupted:
		return interrupted(ctx.Err())
	case writeCompleted:
		if writeErr != nil {
			return c.finishFailure(running, &state, protocol.FailureAdapterUnavailable, "", writeErr.Error())
		}
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
			// Suppression starts at the observation, not at the frame: §6 anchors it to the
			// durable request, which governs "even if the core had not yet signalled".
			// Setting it after a successful write would leave the encode and write failures
			// below relying on their own returns instead of the shared rule.
			observeCancellation()
			cancelRequest, encodeErr := encodeCancelRequest(protocol.CancelRequest{AttemptID: plan.Request.AttemptID})
			if encodeErr != nil {
				state.report.Result = nil
				return failWithoutOutcome(encodeErr)
			}
			writeErr, writeResult := writeBounded(cancelRequest, nil)
			switch writeResult {
			case writeExpired:
				return cancelExpired()
			case writeInterrupted:
				return interrupted(ctx.Err())
			case writeCompleted:
				if writeErr != nil {
					state.report.Result = nil
					return failWithoutOutcome(writeErr)
				}
			}
			cancelRequested = true
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
		case <-cancelSignal:
			// Suppression is already handled by the recording entry points, which read the
			// signal themselves. Observing it here is about liveness only.
			observeCancellation()
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
	c.observeWindow(executeWindowStderrDrain)
	for stderrPending := true; stderrPending; {
		select {
		case <-running.stderrDone:
			stderrPending = false
		case <-cancelSignal:
			observeCancellation()
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
	}
	state.report.Stderr = adapterkit.SanitizeDiagnostic(running.stderr.String())
	c.observeWindow(executeWindowProcessWait)
	wait := make(chan error, 1)
	go func() {
		wait <- running.process.Wait()
	}()
	var waitErr error
	for waiting := true; waiting; {
		select {
		case waitErr = <-wait:
			waiting = false
		case <-cancelSignal:
			observeCancellation()
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
	}
	if waitErr != nil {
		state.report.Result = nil
		if cleanupErr := c.sweepExecute(running); cleanupErr != nil {
			return state.report, sweepHalt(cleanupErr)
		}
		return c.recordFailure(&state, protocol.FailureAdapterUnavailable, "", waitErr.Error(), waitErr)
	}
	if cleanupErr := c.verifyAndSweepExecute(running); cleanupErr != nil {
		return state.report, sweepHalt(cleanupErr)
	}
	reach(plan.Probe, faultpoint.PointExecuteAdapterSwept)
	if err := c.recordStop(&state); err != nil {
		return state.report, err
	}
	if err := c.recordResult(&state); err != nil {
		return state.report, err
	}
	if state.terminalRecordingSuppressed() {
		// Every other suppressed exit already clears this, so leaving the one clean exit
		// populated would make `Result != nil` mean "authoritative" everywhere except
		// here. Callers read the outcome directly — driver.go treats a `completed` as
		// licence to run acceptance — so a response the journal deliberately ignored must
		// not survive in the report either.
		state.report.Result = nil
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
	return c.recordFailure(state, kind, reason, detail, errors.New(detail))
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
	if state.terminalRecordingSuppressed() {
		return nil
	}
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

func (c *Client) recordResult(state *executeState) error {
	if state.terminalRecordingSuppressed() {
		return nil
	}
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
		eventType = string(runstate.EventAttemptFailed)
		result.Failure = &protocol.Failure{Kind: protocol.FailureTaskFailed}
		reason = "unsolicited_cancel"
	}
	return state.recordOutcome(eventType, result, reason)
}

func (c *Client) recordFailure(state *executeState, kind protocol.FailureKind, reason, detail string, cause error) (ExecuteReport, error) {
	if state.terminalRecordingSuppressed() {
		return state.report, cause
	}
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
	if state.terminalRecordingSuppressed() {
		return nil
	}
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

// terminalRecordingSuppressed reads the signal itself rather than trusting a flag some
// earlier select happened to set. Round 3 moved the rule off the twenty call sites and into
// these four entry points, which closed the *where*; a cancellation arriving after the
// execute response still escaped, because no select past that point watched the channel.
// Observing it here closes the *when* the same way: no caller and no select has to know.
func (state *executeState) terminalRecordingSuppressed() bool {
	if state.cancellationInFlight {
		return true
	}
	select {
	case <-state.cancelSignal:
		state.cancellationInFlight = true
		return true
	default:
		return false
	}
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
