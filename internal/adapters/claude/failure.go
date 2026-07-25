package claude

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
		"no conversation found",
		"session not found",
		"unknown session",
		"invalid session id",
		"could not find session",
		"conversation not found",
	)
}

func (i invocationResult) result() *protocol.ExecuteResult {
	if i.err == nil && !i.stream.resultIsError {
		return nil
	}

	if errors.Is(i.err, os.ErrNotExist) || errors.Is(i.err, os.ErrPermission) ||
		strings.Contains(errorText(i.err), "start vendor process") {
		return failed(protocol.FailureAdapterUnavailable, "Claude CLI is unavailable")
	}
	if strings.Contains(errorText(i.err), "read vendor stdout") {
		return failed(protocol.FailureProtocolError, "process Claude stream")
	}
	if i.stream.incompatible && i.err != nil && !i.stream.sawResult {
		return failed(protocol.FailureProtocolError, "Claude returned an incompatible structured stream")
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
		"permission denied",
		"permission was denied",
		"tool use denied",
		"not allowed by permissions",
		"sandbox violation",
		"access denied",
	):
		return protocol.FailureGrantDenied
	case containsAny(evidence,
		"authentication failed",
		"authentication error",
		"not authenticated",
		"not logged in",
		"unauthorized",
		"invalid api key",
		"invalid_api_key",
	):
		return protocol.FailureAuthentication
	case containsAny(evidence,
		"rate limit",
		"rate_limit",
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
		detail = "Claude task failed"
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
