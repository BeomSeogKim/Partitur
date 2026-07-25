package codex

import (
	"errors"
	"os"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/adapterkit"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

type invocationResult struct {
	process adapterkit.ProcessResult
	err     error
	stderr  string
	stream  streamState
}

func (i invocationResult) staleSession() bool {
	if i.err == nil || i.stream.sawAssistant {
		return false
	}
	evidence := strings.ToLower(i.stderr + "\n" + i.stream.detail)
	return containsAny(evidence,
		"thread not found",
		"session not found",
		"unknown session",
		"invalid session id",
		"conversation not found",
		"no rollout found",
		"no saved session found",
	)
}

func (i invocationResult) result() *protocol.ExecuteResult {
	if i.err == nil && !i.stream.resultIsError {
		return nil
	}

	if errors.Is(i.err, os.ErrNotExist) || errors.Is(i.err, os.ErrPermission) ||
		strings.Contains(errorText(i.err), "start vendor process") {
		return failed(protocol.FailureAdapterUnavailable, "Codex CLI is unavailable")
	}
	if strings.Contains(errorText(i.err), "read vendor stdout") {
		return failed(protocol.FailureProtocolError, "process Codex stream")
	}
	if i.stream.incompatible && i.err != nil && !i.stream.sawResult {
		return failed(protocol.FailureProtocolError, "Codex returned an incompatible structured stream")
	}

	evidence := strings.ToLower(i.stderr + "\n" + i.stream.detail)
	if kind := classifyFailure(evidence); kind != "" {
		return failed(kind, i.failureDetail())
	}
	if i.stream.resultIsError || i.err != nil || i.process.ExitCode != 0 {
		return failed(protocol.FailureTaskFailed, i.failureDetail())
	}
	return nil
}

func classifyFailure(evidence string) protocol.FailureKind {
	switch {
	case containsAny(evidence,
		"denied by sandbox",
		"sandbox denied",
		"sandbox violation",
		"outside writable roots",
		"read-only file system",
		"write access denied",
		"permission denied",
		"network access is disabled",
		"network disabled by sandbox",
	):
		return protocol.FailureGrantDenied
	case containsAny(evidence,
		"not logged in",
		"authentication required",
		"authentication failed",
		"unauthorized",
		"status 401",
		"http 401",
		"invalid api key",
		"invalid_api_key",
	):
		return protocol.FailureAuthentication
	case containsAny(evidence,
		"rate limit",
		"rate_limit",
		"usage limit",
		"too many requests",
		"status 429",
		"http 429",
	):
		return protocol.FailureRateLimited
	case containsAny(evidence,
		"provider timeout",
		"request timed out",
		"request timeout",
		"deadline exceeded",
		"gateway timeout",
		"status 504",
		"http 504",
	):
		return protocol.FailureProviderTimeout
	case containsAny(evidence,
		"model unavailable",
		"model not found",
		"unknown model",
		"does not exist or you do not have access",
		"not available for your account",
	):
		return protocol.FailureModelUnavailable
	default:
		return ""
	}
}

func (i invocationResult) failureDetail() string {
	detail := strings.TrimSpace(i.stream.detail)
	if detail == "" {
		detail = firstLine(i.stderr)
	}
	if detail == "" {
		detail = "Codex task failed"
	}
	return adapterkit.TruncateUTF8(i.stream.sanitize(detail), maxFailureDetail)
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		value = value[:index]
	}
	return value
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return strings.ToLower(err.Error())
}

func containsAny(value string, patterns ...string) bool {
	for _, pattern := range patterns {
		if strings.Contains(value, pattern) {
			return true
		}
	}
	return false
}
