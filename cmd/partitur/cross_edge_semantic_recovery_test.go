package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

type semanticRecoverySnapshot struct {
	Events     []any
	Projection any
	Actions    []any
	Command    semanticCommandResult
}

type semanticCommandResult struct {
	ExitClass string
	Halt      string
}

func extractSemanticRecovery(repository, runID, trace string, exitCode int, stderr string) (semanticRecoverySnapshot, error) {
	normalizer := newSemanticNormalizer()
	events, err := extractSemanticJournal(filepath.Join(repository, ".partitur", "runs", runID, "journal.jsonl"), normalizer)
	if err != nil {
		return semanticRecoverySnapshot{}, err
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		return semanticRecoverySnapshot{}, err
	}
	input, err := store.LoadRunInput(runstate.RunID(runID))
	if err != nil {
		return semanticRecoverySnapshot{}, err
	}
	durableProjection := projectionSemanticValue(reflect.ValueOf(semanticDurableProjection{
		Projection: input.Projection,
		BaseCommit: input.BaseCommit,
		BaseTree:   input.BaseTree,
	}))
	normalizer.observeDurableProjection(durableProjection)
	if err := normalizer.assertBudgetClockFieldCompleteness(); err != nil {
		return semanticRecoverySnapshot{}, err
	}
	actions, err := extractSemanticActions(trace, normalizer)
	if err != nil {
		return semanticRecoverySnapshot{}, err
	}
	snapshot := semanticRecoverySnapshot{
		Events:     events,
		Projection: normalizer.normalize(durableProjection),
		Actions:    actions,
		Command: semanticCommandResult{
			ExitClass: semanticExitClass(exitCode),
			Halt:      semanticHalt(stderr),
		},
	}
	if err := snapshot.valid(); err != nil {
		return semanticRecoverySnapshot{}, err
	}
	return snapshot, nil
}

// semanticDurableProjection includes the replayed exported projection and
// the run-start base identities. The latter are semantic inputs to recovery,
// not generated IDs, so they must remain literal in the comparison.
type semanticDurableProjection struct {
	Projection recovery.Projection
	BaseCommit string
	BaseTree   string
}

func extractSemanticJournal(path string, normalizer *semanticNormalizer) ([]any, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var events []any
	openIntervals := make(map[string]semanticOpenExecutionInterval)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event map[string]any
		decoder := json.NewDecoder(strings.NewReader(scanner.Text()))
		decoder.UseNumber()
		if err := decoder.Decode(&event); err != nil {
			return nil, fmt.Errorf("decode journal event: %w", err)
		}
		if err := validateTimestampDerivedCharge(event, openIntervals); err != nil {
			return nil, err
		}
		if isDiagnosticRecoveryEvent(event["type"]) {
			continue
		}
		normalizer.observeEventPayload(event)
		events = append(events, normalizer.normalize(event))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

type semanticOpenExecutionInterval struct {
	wallStart        string
	remainingAtStart int64
}

func validateTimestampDerivedCharge(event map[string]any, openIntervals map[string]semanticOpenExecutionInterval) error {
	eventType, _ := event["type"].(string)
	if eventType != string(runstate.EventExecutionStarted) && eventType != string(runstate.EventExecutionStopped) {
		return nil
	}
	payload, ok := event["payload"].(map[string]any)
	if !ok {
		return fmt.Errorf("timestamp-derived charge %s payload is not an object", eventType)
	}
	intervalID, err := semanticString(payload, "interval_id")
	if err != nil {
		return fmt.Errorf("timestamp-derived charge %s: %w", eventType, err)
	}
	if eventType == string(runstate.EventExecutionStarted) {
		wallStart, err := semanticString(payload, "wall_start")
		if err != nil {
			return fmt.Errorf("timestamp-derived charge execution.started: %w", err)
		}
		remainingAtStart, err := semanticInt64(payload, "remaining_at_start")
		if err != nil {
			return fmt.Errorf("timestamp-derived charge execution.started: %w", err)
		}
		openIntervals[intervalID] = semanticOpenExecutionInterval{wallStart: wallStart, remainingAtStart: remainingAtStart}
		return nil
	}

	interval, found := openIntervals[intervalID]
	if !found {
		return fmt.Errorf("timestamp-derived charge execution.stopped has no execution.started for interval %q", intervalID)
	}
	delete(openIntervals, intervalID)
	if payload["charging"] != "clamped" {
		return nil
	}
	observedAt, err := semanticString(payload, "observed_at")
	if err != nil {
		return fmt.Errorf("timestamp-derived charge execution.stopped: %w", err)
	}
	observed, err := time.Parse(time.RFC3339Nano, observedAt)
	if err != nil {
		return fmt.Errorf("timestamp-derived charge execution.stopped parse observed_at: %w", err)
	}
	started, err := time.Parse(time.RFC3339Nano, interval.wallStart)
	if err != nil {
		return fmt.Errorf("timestamp-derived charge execution.stopped parse wall_start: %w", err)
	}
	want := observed.Sub(started).Milliseconds()
	if want < 0 {
		want = 0
	}
	if want > interval.remainingAtStart {
		want = interval.remainingAtStart
	}
	charged, err := semanticInt64(payload, "charged_duration")
	if err != nil {
		return fmt.Errorf("timestamp-derived charge execution.stopped: %w", err)
	}
	if charged != want {
		return fmt.Errorf("timestamp-derived charged_duration=%d, want min(max(0, observed_at-wall_start), remaining_at_start)=%d", charged, want)
	}
	return nil
}

func semanticString(value map[string]any, key string) (string, error) {
	text, ok := value[key].(string)
	if !ok || text == "" {
		return "", fmt.Errorf("%s is not a non-empty string", key)
	}
	return text, nil
}

func semanticInt64(value map[string]any, key string) (int64, error) {
	number, ok := value[key].(json.Number)
	if !ok {
		return 0, fmt.Errorf("%s is not a JSON integer", key)
	}
	integer, err := number.Int64()
	if err != nil {
		return 0, fmt.Errorf("%s is not a JSON integer: %w", key, err)
	}
	return integer, nil
}

func isDiagnosticRecoveryEvent(eventType any) bool {
	return eventType == string(runstate.EventLog) || eventType == string(runstate.EventProgress)
}

func extractSemanticActions(path string, normalizer *semanticNormalizer) ([]any, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	var actions []any
	for {
		var decision any
		err := decoder.Decode(&decision)
		if err == io.EOF {
			return actions, nil
		}
		if err != nil {
			return nil, fmt.Errorf("decode recovery decision trace: %w", err)
		}
		actions = append(actions, normalizer.normalize(decision))
	}
}

func (snapshot semanticRecoverySnapshot) valid() error {
	if len(snapshot.Events) == 0 {
		return errors.New("semantic recovery extractor has no retained events")
	}
	if snapshot.Projection == nil {
		return errors.New("semantic recovery extractor has no durable projection")
	}
	if len(snapshot.Actions) == 0 {
		return errors.New("semantic recovery extractor has no recovery actions")
	}
	return nil
}

func compareSemanticRecovery(left, right semanticRecoverySnapshot) error {
	if err := left.valid(); err != nil {
		return fmt.Errorf("left semantic recovery: %w", err)
	}
	if err := right.valid(); err != nil {
		return fmt.Errorf("right semantic recovery: %w", err)
	}
	if !reflect.DeepEqual(left.Events, right.Events) {
		return semanticDifference("ordered events", left.Events, right.Events)
	}
	if !reflect.DeepEqual(left.Projection, right.Projection) {
		return semanticDifference("durable projection", left.Projection, right.Projection)
	}
	if !reflect.DeepEqual(left.Actions, right.Actions) {
		return semanticDifference("recovery actions", left.Actions, right.Actions)
	}
	if left.Command.ExitClass != right.Command.ExitClass {
		return fmt.Errorf("recovery exit class differs: %q != %q", left.Command.ExitClass, right.Command.ExitClass)
	}
	if left.Command.Halt != right.Command.Halt {
		return fmt.Errorf("recovery halt differs: %q != %q", left.Command.Halt, right.Command.Halt)
	}
	return nil
}

func semanticDifference(domain string, left, right any) error {
	return fmt.Errorf("semantic %s differ: left=%s right=%s", domain, semanticJSON(left), semanticJSON(right))
}

func semanticJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("<encode error: %v>", err)
	}
	return string(encoded)
}

func semanticExitClass(code int) string {
	return fmt.Sprintf("exit_%d", code)
}

// semanticHalt intentionally parses the named halt from stderr before the
// comparison discards stderr wording. A changed Appendix D reason therefore
// remains observable even though diagnostics themselves are not compared.
func semanticHalt(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		if !strings.HasPrefix(line, "recovery halted:") {
			continue
		}
		marker := `reason="`
		start := strings.Index(line, marker)
		if start < 0 {
			return ""
		}
		remainder := line[start+len(marker):]
		end := strings.IndexByte(remainder, '"')
		if end < 0 {
			return ""
		}
		return remainder[:end]
	}
	return ""
}

type semanticIDClass string

const (
	semanticIDRun       semanticIDClass = "run"
	semanticIDEvent     semanticIDClass = "event"
	semanticIDAttempt   semanticIDClass = "attempt"
	semanticIDPrepare   semanticIDClass = "prepare"
	semanticIDProposal  semanticIDClass = "proposal"
	semanticIDDecision  semanticIDClass = "decision"
	semanticIDTxn       semanticIDClass = "txn"
	semanticIDCandidate semanticIDClass = "candidate"
	semanticIDInterval  semanticIDClass = "interval"
	semanticIDProcess   semanticIDClass = "process"
)

// generatedIdentifierClasses is intentionally closed and explicit: run,
// event, attempt (including workspace.NewID products), prepare, proposal,
// decision, transaction, candidate, and interval. Process identities form the
// separate semanticIDProcess class below. Content-addressed change-set, tree,
// commit, score, and plan-record values stay literal semantic evidence.
var generatedIdentifierClasses = map[string]semanticIDClass{
	"run_id":                 semanticIDRun,
	"event_id":               semanticIDEvent,
	"causation_id":           semanticIDEvent,
	"evidence_event_id":      semanticIDEvent,
	"approval_event_id":      semanticIDEvent,
	"attempt_id":             semanticIDAttempt,
	"previous_attempt_id":    semanticIDAttempt,
	"target_attempt_ids":     semanticIDAttempt,
	"superseded_attempt_ids": semanticIDAttempt,
	"cancelled_attempt_ids":  semanticIDAttempt,
	"prepare_id":             semanticIDPrepare,
	"proposal_id":            semanticIDProposal,
	"decision_id":            semanticIDDecision,
	"obsoleted_decision_ids": semanticIDDecision,
	"pending_decision_ids":   semanticIDDecision,
	"txn_id":                 semanticIDTxn,
	"transaction_id":         semanticIDTxn,
	"candidate_id":           semanticIDCandidate,
	"interval_id":            semanticIDInterval,
}

type semanticNormalizer struct {
	identifiers                map[semanticIDClass]map[string]string
	timestampDerivedChargeSeen bool
	budgetClockFields          map[semanticPath]struct{}
}

func newSemanticNormalizer() *semanticNormalizer {
	return &semanticNormalizer{
		identifiers:       make(map[semanticIDClass]map[string]string),
		budgetClockFields: make(map[semanticPath]struct{}),
	}
}

func (normalizer *semanticNormalizer) normalize(value any) any {
	return normalizer.normalizeAtPath(value, "", semanticNormalizationContext{})
}

type semanticNormalizationContext struct {
	clampedExecutionStopped bool
}

type semanticBudgetClockValueClass uint8

const (
	semanticBudgetClockLiteral semanticBudgetClockValueClass = iota
	semanticBudgetClockTimestamp
	semanticBudgetClockMagnitude
)

// semanticBudgetClockValueClassFor is shared by normalization and the
// completeness lock, so a timestamp normalized by the former cannot evade the
// latter because its key lacks a budget-shaped spelling.
func semanticBudgetClockValueClassFor(key string, value any) semanticBudgetClockValueClass {
	if isTimestampValue(key, value) {
		return semanticBudgetClockTimestamp
	}
	if isSemanticBudgetMagnitudeKey(key) {
		return semanticBudgetClockMagnitude
	}
	return semanticBudgetClockLiteral
}

func (normalizer *semanticNormalizer) normalizeAtPath(value any, path semanticPath, context semanticNormalizationContext) any {
	switch value := value.(type) {
	case map[string]any:
		return normalizer.normalizeMap(value, path, context)
	case []any:
		result := make([]any, len(value))
		for index, item := range value {
			result[index] = normalizer.normalizeAtPath(item, path.index(), context)
		}
		return result
	case semanticMapEntries:
		return normalizer.normalizeMapEntries(value, path, context)
	default:
		return value
	}
}

func (normalizer *semanticNormalizer) normalizeMap(value map[string]any, path semanticPath, context semanticNormalizationContext) map[string]any {
	process, ownerProcess := normalizer.processName(value)
	eventType, _ := value["type"].(string)
	if eventType == string(runstate.EventExecutionStopped) {
		context.clampedExecutionStopped = executionStoppedIsClamped(value["payload"])
	}
	result := make(map[string]any, len(value))
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		item := value[key]
		normalizedKey := semanticFieldName(key)
		fieldPath := path.child(normalizedKey)
		valueClass := semanticBudgetClockValueClassFor(normalizedKey, item)
		identifierClass := generatedIdentifierClass(normalizedKey, path.last())
		switch {
		case process != "" && (normalizedKey == "pid" || normalizedKey == "session_id"):
			result[key] = process + "." + normalizedKey
		case process != "" && (normalizedKey == "start" || normalizedKey == "start_identity"):
			result[key] = normalizeProcessStart(item, process)
		case ownerProcess != "" && normalizedKey == "owner_pid":
			result[key] = ownerProcess + ".pid"
		case ownerProcess != "" && normalizedKey == "owner_start_identity":
			result[key] = normalizeProcessStart(item, ownerProcess)
		case context.clampedExecutionStopped && isTimestampDerivedChargePath(fieldPath):
			result[key] = semanticTimestampDerivedCharge
			normalizer.timestampDerivedChargeSeen = true
		case normalizer.timestampDerivedChargeSeen && isTimestampDerivedBudgetPath(fieldPath):
			result[key] = semanticTimestampDerivedBudget
		case valueClass == semanticBudgetClockTimestamp:
			result[key] = "<timestamp>"
		case identifierClass != "":
			result[key] = normalizer.normalizeIdentifier(identifierClass, item)
		default:
			result[key] = normalizer.normalizeAtPath(item, fieldPath, context)
		}
	}
	return result
}

const (
	semanticTimestampDerivedCharge = "<timestamp-derived-charge>"
	semanticTimestampDerivedBudget = "<timestamp-derived-budget>"
)

type semanticPath string

func (path semanticPath) child(component string) semanticPath {
	if path == "" {
		return semanticPath(component)
	}
	return path + "." + semanticPath(component)
}

func (path semanticPath) index() semanticPath {
	return path + "[]"
}

func (path semanticPath) last() string {
	components := strings.Split(string(path), ".")
	return strings.TrimSuffix(components[len(components)-1], "[]")
}

type semanticBudgetClockFieldClass struct {
	normalization string
	literalReason string
}

// semanticBudgetClockFieldClasses is the closed contract for budget magnitudes
// and timestamps normalized by semanticBudgetClockValueClassFor. Every
// observed occurrence in the comparison projection or retained event payload
// must be declared here. The literal entries preserve semantic evidence rather
// than normalizing it.
var semanticBudgetClockFieldClasses = map[semanticPath]semanticBudgetClockFieldClass{
	semanticPath("event.payload.remaining_at_start"): {
		literalReason: "recorded execution-start budget ceiling used to validate the later charge",
	},
	semanticPath("event.payload.wall_start"): {
		literalReason: "execution-start timestamp is normalized by the timestamp rule, not as a derived magnitude",
	},
	semanticPath("event.payload.charged_duration"): {
		normalization: semanticTimestampDerivedCharge,
	},
	semanticPath("event.payload.duration_ms"): {
		literalReason: "criterion duration is not derived from a normalized persisted timestamp pair",
	},
	semanticPath("event.payload.quiesce_silence_limit_ms"): {
		literalReason: "configured quiesce silence limit is fixed by the approval-prepared payload, not clock-derived",
	},
	semanticPath("event.payload.observed_at"): {
		literalReason: "clamped observation timestamp is normalized by the timestamp rule, not as a derived magnitude",
	},
	semanticPath("event.payload.disposition.charged"): {
		literalReason: "failure disposition charged is a classification, not a magnitude",
	},
	semanticPath("projection.state.consumed_budget_ms"): {
		normalization: semanticTimestampDerivedBudget,
	},
	semanticPath("projection.scheduler.remaining_time"): {
		normalization: semanticTimestampDerivedBudget,
	},
	semanticPath("projection.current_head_attempt.failure_classification.remaining_time_ms"): {
		normalization: semanticTimestampDerivedBudget,
	},
	semanticPath("projection.current_head_attempt.failure_classification.retries_consumed"): {
		literalReason: "retries consumed is a count, not a clock-derived magnitude",
	},
	semanticPath("projection.state.pending_prepare.quiesce_silence_limit_ms"): {
		literalReason: "pending prepare retains the configured quiesce silence limit, not a sampled clock value",
	},
	semanticPath("projection.state.pending_prepare.prepared_at"): {
		literalReason: "pending prepare timestamp is normalized by the timestamp rule, not as a derived magnitude",
	},
	semanticPath("projection.state.pending_prepare.latest_quiesce_observed_at"): {
		literalReason: "latest quiesce timestamp is normalized by the timestamp rule, not as a derived magnitude",
	},
	semanticPath("projection.state.open_execution.wall_start"): {
		literalReason: "open execution timestamp is normalized by the timestamp rule, not as a derived magnitude",
	},
	semanticPath("projection.current_head_attempt.recorded_disposition.charged"): {
		literalReason: "recorded disposition charged is a classification, not a magnitude",
	},
	semanticPath("projection.state.attempts[].value.failure.disposition.charged"): {
		literalReason: "attempt failure disposition charged is a classification, not a magnitude",
	},
}

func isTimestampDerivedChargePath(path semanticPath) bool {
	return semanticBudgetClockFieldClasses[semanticPath("event").child(string(path))].normalization == semanticTimestampDerivedCharge
}

func isTimestampDerivedBudgetPath(path semanticPath) bool {
	return semanticBudgetClockFieldClasses[path].normalization == semanticTimestampDerivedBudget
}

func (normalizer *semanticNormalizer) observeEventPayload(event map[string]any) {
	payload, found := event["payload"]
	if !found {
		return
	}
	normalizer.observeBudgetClockFields(payload, semanticPath("event.payload"))
}

func (normalizer *semanticNormalizer) observeDurableProjection(projection any) {
	normalizer.observeBudgetClockFields(projection, "")
}

func (normalizer *semanticNormalizer) observeBudgetClockFields(value any, path semanticPath) {
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			normalizedKey := semanticFieldName(key)
			fieldPath := path.child(normalizedKey)
			if semanticBudgetClockValueClassFor(normalizedKey, item) != semanticBudgetClockLiteral {
				normalizer.budgetClockFields[fieldPath] = struct{}{}
			}
			normalizer.observeBudgetClockFields(item, fieldPath)
		}
	case []any:
		for _, item := range value {
			normalizer.observeBudgetClockFields(item, path.index())
		}
	case semanticMapEntries:
		for _, entry := range value {
			normalizer.observeBudgetClockFields(entry.Value, path.index().child("value"))
		}
	}
}

func isSemanticBudgetMagnitudeKey(key string) bool {
	return strings.Contains(key, "budget") ||
		strings.Contains(key, "duration") ||
		strings.HasPrefix(key, "remaining_") ||
		strings.Contains(key, "charged") ||
		strings.HasSuffix(key, "_consumed") ||
		strings.HasSuffix(key, "_ms") ||
		strings.HasSuffix(key, "_limit") ||
		strings.Contains(key, "elapsed") ||
		strings.Contains(key, "deadline") ||
		strings.Contains(key, "timeout")
}

func (normalizer *semanticNormalizer) assertBudgetClockFieldCompleteness() error {
	paths := make([]string, 0, len(normalizer.budgetClockFields))
	for path := range normalizer.budgetClockFields {
		paths = append(paths, string(path))
	}
	sort.Strings(paths)
	for _, text := range paths {
		path := semanticPath(text)
		class, declared := semanticBudgetClockFieldClasses[path]
		if !declared {
			return fmt.Errorf("unclassified budget/clock-derived field at %s", path)
		}
		if class.normalization == "" && class.literalReason == "" {
			return fmt.Errorf("budget/clock-derived field at %s has neither normalization nor literal reason", path)
		}
	}
	return nil
}

func executionStoppedIsClamped(value any) bool {
	payload, ok := value.(map[string]any)
	return ok && payload["charging"] == "clamped"
}

func (normalizer *semanticNormalizer) normalizeMapEntries(entries semanticMapEntries, path semanticPath, context semanticNormalizationContext) []any {
	result := make([]any, len(entries))
	class := mapKeyClass(path.last())
	for index, entry := range entries {
		entryPath := path.index()
		key := normalizer.normalizeAtPath(entry.Key, entryPath.child("key"), context)
		if class != "" {
			key = normalizer.normalizeIdentifier(class, entry.Key)
		}
		result[index] = map[string]any{"key": key, "value": normalizer.normalizeAtPath(entry.Value, entryPath.child("value"), context)}
	}
	sort.Slice(result, func(left, right int) bool { return semanticJSON(result[left]) < semanticJSON(result[right]) })
	return result
}

func generatedIdentifierClass(key, parent string) semanticIDClass {
	if class := generatedIdentifierClasses[key]; class != "" {
		return class
	}
	switch key {
	case "routed_proposal_id":
		return semanticIDProposal
	case "question_decision_id":
		return semanticIDDecision
	case "id":
		switch semanticFieldName(parent) {
		case "pending_prepare":
			return semanticIDPrepare
		case "pending_decisions":
			return semanticIDDecision
		case "open_execution":
			return semanticIDInterval
		case "application_candidate":
			return semanticIDCandidate
		}
	}
	return ""
}

func (normalizer *semanticNormalizer) normalizeIdentifier(class semanticIDClass, value any) any {
	if values, ok := value.([]any); ok {
		result := make([]any, len(values))
		for index, item := range values {
			result[index] = normalizer.normalizeIdentifier(class, item)
		}
		return result
	}
	text, ok := value.(string)
	if !ok || text == "" {
		return normalizer.normalizeAtPath(value, "", semanticNormalizationContext{})
	}
	entries := normalizer.identifiers[class]
	if entries == nil {
		entries = make(map[string]string)
		normalizer.identifiers[class] = entries
	}
	if canonical, found := entries[text]; found {
		return canonical
	}
	canonical := fmt.Sprintf("%s#%d", class, len(entries)+1)
	entries[text] = canonical
	return canonical
}

func (normalizer *semanticNormalizer) processName(value map[string]any) (string, string) {
	pid, hasPID := mapValue(value, "pid")
	start, hasStart := mapValue(value, "start", "start_identity")
	if hasPID && hasStart {
		return normalizer.processIdentifier(pid, start), ""
	}
	ownerPID, hasOwnerPID := mapValue(value, "owner_pid")
	ownerStart, hasOwnerStart := mapValue(value, "owner_start_identity")
	if hasOwnerPID && hasOwnerStart {
		return "", normalizer.processIdentifier(ownerPID, ownerStart)
	}
	return "", ""
}

func (normalizer *semanticNormalizer) processIdentifier(pid, start any) string {
	key := semanticJSON(map[string]any{"pid": pid, "start": start})
	return normalizer.normalizeIdentifier(semanticIDProcess, key).(string)
}

func normalizeProcessStart(value any, process string) any {
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, item := range value {
			if semanticFieldName(key) == "platform" {
				result[key] = item
			} else {
				result[key] = process + ".start." + semanticFieldName(key)
			}
		}
		return result
	default:
		return process + ".start"
	}
}

func mapValue(value map[string]any, keys ...string) (any, bool) {
	for key, item := range value {
		for _, want := range keys {
			if semanticFieldName(key) == want {
				return item, true
			}
		}
	}
	return nil, false
}

func mapKeyClass(parent string) semanticIDClass {
	switch semanticFieldName(parent) {
	case "attempts", "adapter_launches", "adapter_observations", "change_sets", "verified_attempts", "acceptances", "resolved_human_gates":
		return semanticIDAttempt
	case "pending_decisions":
		return semanticIDDecision
	case "routed_amendments":
		return semanticIDProposal
	default:
		return ""
	}
}

func isTimestampValue(key string, value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, text); err == nil {
		return true
	}
	switch key {
	case "ts", "prepared_at", "latest_quiesce_observed_at", "wall_start", "observed_at":
		return true
	default:
		return false
	}
}

type semanticMapEntry struct {
	Key   any
	Value any
}

type semanticMapEntries []semanticMapEntry

var rawMessageType = reflect.TypeOf(json.RawMessage{})

func projectionSemanticValue(value reflect.Value) any {
	if !value.IsValid() {
		return nil
	}
	if value.Type() == rawMessageType {
		var decoded any
		decoder := json.NewDecoder(bytes.NewReader(value.Bytes()))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			return string(value.Bytes())
		}
		return decoded
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		return projectionSemanticValue(value.Elem())
	case reflect.Struct:
		result := make(map[string]any)
		if value.CanInterface() {
			start, ok := value.Interface().(runstate.StartIdentity)
			if ok {
				result["platform"] = start.Platform()
			}
		}
		for index := range value.NumField() {
			field := value.Type().Field(index)
			if !field.IsExported() {
				continue
			}
			result[semanticStructFieldName(field)] = projectionSemanticValue(value.Field(index))
		}
		return result
	case reflect.Map:
		entries := make(semanticMapEntries, 0, value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			entries = append(entries, semanticMapEntry{Key: projectionSemanticValue(iterator.Key()), Value: projectionSemanticValue(iterator.Value())})
		}
		sort.Slice(entries, func(left, right int) bool { return semanticJSON(entries[left].Key) < semanticJSON(entries[right].Key) })
		return entries
	case reflect.Slice, reflect.Array:
		result := make([]any, value.Len())
		for index := range value.Len() {
			result[index] = projectionSemanticValue(value.Index(index))
		}
		return result
	case reflect.String:
		return value.String()
	case reflect.Bool:
		return value.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return value.Uint()
	case reflect.Float32, reflect.Float64:
		return value.Float()
	default:
		return fmt.Sprint(value.Interface())
	}
}

func semanticStructFieldName(field reflect.StructField) string {
	if tag := strings.Split(field.Tag.Get("json"), ",")[0]; tag != "" && tag != "-" {
		return tag
	}
	return semanticFieldName(field.Name)
}

func semanticFieldName(value string) string {
	var output strings.Builder
	runes := []rune(value)
	for index, runeValue := range runes {
		if unicode.IsUpper(runeValue) && index > 0 &&
			(unicode.IsLower(runes[index-1]) || (index+1 < len(runes) && unicode.IsLower(runes[index+1]))) {
			output.WriteByte('_')
		}
		if runeValue == '-' {
			output.WriteByte('_')
			continue
		}
		output.WriteRune(unicode.ToLower(runeValue))
	}
	return output.String()
}

func TestSemanticRecoveryComparisonRejectsRetainedSeeds(t *testing.T) {
	for _, control := range []struct {
		name, signature string
		change          func(*semanticRecoverySnapshot)
	}{
		{
			name:      "idempotence_retained_reason",
			signature: "ordered events",
			change: func(snapshot *semanticRecoverySnapshot) {
				snapshot.Events[0].(map[string]any)["payload"].(map[string]any)["reason"] = "different-reason"
			},
		},
		{
			name:      "retained_failure_kind",
			signature: "ordered events",
			change: func(snapshot *semanticRecoverySnapshot) {
				snapshot.Events[0].(map[string]any)["payload"].(map[string]any)["kind"] = "different-kind"
			},
		},
		{
			name:      "retained_error_detail",
			signature: "ordered events",
			change: func(snapshot *semanticRecoverySnapshot) {
				snapshot.Events[0].(map[string]any)["payload"].(map[string]any)["error_detail"] = "different-detail"
			},
		},
		{
			name:      "determinism_retained_result_tree",
			signature: "durable projection",
			change: func(snapshot *semanticRecoverySnapshot) {
				snapshot.Projection.(map[string]any)["result_tree"] = "git-sha1:different-tree"
			},
		},
		{
			name:      "retained_action_kind",
			signature: "recovery actions",
			change: func(snapshot *semanticRecoverySnapshot) {
				snapshot.Actions[0].(map[string]any)["Action"].(map[string]any)["Kind"] = "different-action"
			},
		},
		{
			name:      "retained_exit_class",
			signature: "recovery exit class",
			change: func(snapshot *semanticRecoverySnapshot) {
				snapshot.Command.ExitClass = "exit_5"
			},
		},
		{
			name:      "retained_halt",
			signature: "recovery halt",
			change: func(snapshot *semanticRecoverySnapshot) {
				snapshot.Command.Halt = "journal_corrupt"
			},
		},
	} {
		control := control
		t.Run(control.name, func(t *testing.T) {
			left, right := semanticComparisonSeed("seed-reason"), semanticComparisonSeed("seed-reason")
			control.change(&right)
			err := compareSemanticRecovery(left, right)
			if err == nil || !strings.Contains(err.Error(), control.signature) {
				t.Fatalf("negative control error=%v, want signature %q", err, control.signature)
			}
			t.Logf("negative control measured signature=%q decoded=%v", control.signature, err)
		})
	}

	t.Run("ordered_event_sequence", func(t *testing.T) {
		first := map[string]any{"event_id": "one", "seq": json.Number("1"), "ts": "opaque", "run_id": "run", "score_revision": json.Number("1"), "type": "movement.started", "payload": map[string]any{}}
		second := map[string]any{"event_id": "two", "seq": json.Number("2"), "ts": "opaque", "run_id": "run", "score_revision": json.Number("1"), "type": "movement.failed", "payload": map[string]any{"reason": "retained"}}
		left := semanticSnapshot([]any{first, second}, map[string]any{"result_tree": "git-sha1:stable"}, []any{map[string]any{"CaseID": "RC-RESUME-020", "Action": map[string]any{"Kind": "append_run_failed"}}}, semanticCommandResult{ExitClass: "exit_4"})
		right := semanticSnapshot([]any{second, first}, map[string]any{"result_tree": "git-sha1:stable"}, []any{map[string]any{"CaseID": "RC-RESUME-020", "Action": map[string]any{"Kind": "append_run_failed"}}}, semanticCommandResult{ExitClass: "exit_4"})
		if err := compareSemanticRecovery(left, right); err == nil || !strings.Contains(err.Error(), "ordered events") {
			t.Fatalf("reordered journal error=%v, want ordered event difference", err)
		}
	})
}

func TestSemanticRecoveryComparisonRejectsEmptyDomains(t *testing.T) {
	for _, domain := range []struct {
		name  string
		clear func(*semanticRecoverySnapshot)
	}{
		{name: "events", clear: func(snapshot *semanticRecoverySnapshot) { snapshot.Events = nil }},
		{name: "projection", clear: func(snapshot *semanticRecoverySnapshot) { snapshot.Projection = nil }},
		{name: "actions", clear: func(snapshot *semanticRecoverySnapshot) { snapshot.Actions = nil }},
	} {
		domain := domain
		t.Run(domain.name, func(t *testing.T) {
			left, right := semanticComparisonSeed("seed-reason"), semanticComparisonSeed("seed-reason")
			domain.clear(&right)
			if err := compareSemanticRecovery(left, right); err == nil || !strings.Contains(err.Error(), "extractor has no") {
				t.Fatalf("empty %s error=%v, want extractor rejection", domain.name, err)
			}
		})
	}
}

func TestSemanticRecoveryNormalizer(t *testing.T) {
	t.Run("generated_id_classes", func(t *testing.T) {
		left := semanticNormalizerSeed("one", "opaque-time-one")
		right := semanticNormalizerSeed("two", "opaque-time-two")
		if err := compareSemanticRecovery(left, right); err != nil {
			t.Fatalf("alpha-renamed generated identities differ: %v", err)
		}
	})

	t.Run("timestamp_presence", func(t *testing.T) {
		left := semanticNormalizerSeed("one", "opaque-time-one")
		right := semanticNormalizerSeed("two", "opaque-time-two")
		if err := compareSemanticRecovery(left, right); err != nil {
			t.Fatalf("timestamp values differ after normalization: %v", err)
		}
		delete(right.Events[0].(map[string]any), "ts")
		if err := compareSemanticRecovery(left, right); err == nil || !strings.Contains(err.Error(), "ordered events") {
			t.Fatalf("missing timestamp error=%v, want ordered event difference", err)
		}
	})

	t.Run("timestamp_derived_charge", func(t *testing.T) {
		left := semanticTimestampDerivedChargeSeed("one", "2026-08-16T00:00:00.250Z", 250, 250, 750)
		right := semanticTimestampDerivedChargeSeed("two", "2026-08-16T00:00:00.500Z", 500, 500, 500)
		if err := compareSemanticRecovery(left, right); err != nil {
			t.Fatalf("timestamp-derived charges differ after normalization: %v", err)
		}
	})

	t.Run("timestamp_derived_charge_presence", func(t *testing.T) {
		left := semanticTimestampDerivedChargeSeed("one", "2026-08-16T00:00:00.250Z", 250, 250, 750)
		right := semanticTimestampDerivedChargeSeed("two", "2026-08-16T00:00:00.500Z", 500, 500, 500)
		delete(right.Events[1].(map[string]any)["payload"].(map[string]any), "charged_duration")
		if err := compareSemanticRecovery(left, right); err == nil || !strings.Contains(err.Error(), "ordered events") {
			t.Fatalf("missing timestamp-derived charge error=%v, want ordered event difference", err)
		}
	})

	t.Run("timestamp_derived_charge_projection", func(t *testing.T) {
		left := semanticTimestampDerivedChargeSeed("one", "2026-08-16T00:00:00.250Z", 250, 250, 750)
		right := semanticTimestampDerivedChargeSeed("two", "2026-08-16T00:00:00.500Z", 500, 500, 500)
		left.Projection.(map[string]any)["projection"].(map[string]any)["state"].(map[string]any)["consumed_budget_ms"] = "different-consumed-budget"
		if err := compareSemanticRecovery(left, right); err == nil || !strings.Contains(err.Error(), "durable projection") {
			t.Fatalf("retained derived budget error=%v, want durable projection difference", err)
		}
	})

	t.Run("timestamp_derived_charge_failure_classification_projection", func(t *testing.T) {
		left := semanticTimestampDerivedChargeSeed("one", "2026-08-16T00:00:00.250Z", 250, 250, 750)
		right := semanticTimestampDerivedChargeSeed("two", "2026-08-16T00:00:00.500Z", 500, 500, 500)
		if err := compareSemanticRecovery(left, right); err != nil {
			t.Fatalf("timestamp-derived failure classification budgets differ after normalization: %v", err)
		}
		left.Projection.(map[string]any)["projection"].(map[string]any)["current_head_attempt"].(map[string]any)["failure_classification"].(map[string]any)["remaining_time_ms"] = "different-remaining-time"
		if err := compareSemanticRecovery(left, right); err == nil || !strings.Contains(err.Error(), "durable projection") {
			t.Fatalf("retained failure-classification budget error=%v, want durable projection difference", err)
		}
	})

	t.Run("timestamp_derived_charge_formula", func(t *testing.T) {
		journal := semanticTimestampDerivedChargeJournal(t, "2026-08-16T00:00:00.000Z", "2026-08-16T00:00:00.250Z", 1000, 250)
		if _, err := extractSemanticJournal(journal, newSemanticNormalizer()); err != nil {
			t.Fatalf("valid timestamp-derived charge: %v", err)
		}
		journal = semanticTimestampDerivedChargeJournal(t, "2026-08-16T00:00:00.000Z", "2026-08-16T00:00:00.250Z", 1000, 249)
		if _, err := extractSemanticJournal(journal, newSemanticNormalizer()); err == nil || !strings.Contains(err.Error(), "timestamp-derived charged_duration") {
			t.Fatalf("invalid timestamp-derived charge error=%v", err)
		}
	})

	t.Run("timestamp_derived_charge_formula_negative_elapsed", func(t *testing.T) {
		journal := semanticTimestampDerivedChargeJournal(t, "2026-08-16T00:00:00.250Z", "2026-08-16T00:00:00.000Z", 1000, 0)
		if _, err := extractSemanticJournal(journal, newSemanticNormalizer()); err != nil {
			t.Fatalf("negative elapsed timestamp-derived charge: %v", err)
		}
	})

	t.Run("timestamp_derived_charge_formula_beyond_remaining_at_start", func(t *testing.T) {
		journal := semanticTimestampDerivedChargeJournal(t, "2026-08-16T00:00:00.000Z", "2026-08-16T00:00:01.500Z", 1000, 1000)
		if _, err := extractSemanticJournal(journal, newSemanticNormalizer()); err != nil {
			t.Fatalf("beyond remaining-at-start timestamp-derived charge: %v", err)
		}
	})

	for _, field := range []string{
		"result_tree", "change_set_ref", "base_commit", "semantic_hash", "file_hash", "plan_record_hash",
	} {
		field := field
		t.Run("stable_"+field+"_remains_literal", func(t *testing.T) {
			left := semanticNormalizerSeed("one", "opaque-time-one")
			right := semanticNormalizerSeed("two", "opaque-time-two")
			right.Projection.(map[string]any)[field] = "changed-" + field
			if err := compareSemanticRecovery(left, right); err == nil || !strings.Contains(err.Error(), "durable projection") {
				t.Fatalf("changed %s error=%v, want durable projection difference", field, err)
			}
		})
	}
}

func TestSemanticRecoveryBudgetClockCompleteness(t *testing.T) {
	t.Run("declared_normalized_classes", func(t *testing.T) {
		events, projection := semanticBudgetClockCompletenessFixture()
		normalizer := newSemanticNormalizer()
		for _, event := range events {
			normalizer.observeEventPayload(event)
		}
		normalizer.observeDurableProjection(projection)
		if err := normalizer.assertBudgetClockFieldCompleteness(); err != nil {
			t.Fatalf("declared budget/clock field classes do not cover fixture: %v", err)
		}
	})

	t.Run("literal_allow_list", func(t *testing.T) {
		events, projection := semanticBudgetClockCompletenessFixture()
		normalizer := newSemanticNormalizer()
		for _, event := range events {
			normalizer.observeEventPayload(event)
		}
		normalizer.observeDurableProjection(projection)
		if err := normalizer.assertBudgetClockFieldCompleteness(); err != nil {
			t.Fatalf("literal budget/clock fields require stated allow-list reasons: %v", err)
		}
	})

	t.Run("time_shaped_fields_are_collected", func(t *testing.T) {
		events, projection := semanticBudgetClockCompletenessFixture()
		normalizer := newSemanticNormalizer()
		for _, event := range events {
			normalizer.observeEventPayload(event)
		}
		normalizer.observeDurableProjection(projection)
		for _, path := range []semanticPath{
			"event.payload.quiesce_silence_limit_ms",
			"event.payload.observed_at",
			"event.payload.wall_start",
			"projection.state.pending_prepare.quiesce_silence_limit_ms",
			"projection.state.pending_prepare.prepared_at",
			"projection.state.pending_prepare.latest_quiesce_observed_at",
			"projection.state.open_execution.wall_start",
		} {
			if _, found := normalizer.budgetClockFields[path]; !found {
				t.Fatalf("time-shaped field %q was not collected", path)
			}
		}
	})

	t.Run("synthetic_unclassified_timestamp_field", func(t *testing.T) {
		_, projection := semanticBudgetClockCompletenessFixture()
		projection.(map[string]any)["projection"].(map[string]any)["scheduler"].(map[string]any)["wall_start"] = "2026-08-16T00:00:00.000Z"
		normalizer := newSemanticNormalizer()
		normalizer.observeDurableProjection(projection)
		err := normalizer.assertBudgetClockFieldCompleteness()
		if err == nil || !strings.Contains(err.Error(), "unclassified budget/clock-derived field at projection.scheduler.wall_start") {
			t.Fatalf("synthetic timestamp field error=%v, want unclassified projection path", err)
		}
	})

	t.Run("synthetic_derived_projection_field", func(t *testing.T) {
		_, projection := semanticBudgetClockCompletenessFixture()
		projection.(map[string]any)["projection"].(map[string]any)["scheduler"].(map[string]any)["unclassified_budget_ms"] = 1
		normalizer := newSemanticNormalizer()
		normalizer.observeDurableProjection(projection)
		err := normalizer.assertBudgetClockFieldCompleteness()
		if err == nil || !strings.Contains(err.Error(), "unclassified budget/clock-derived field at projection.scheduler.unclassified_budget_ms") {
			t.Fatalf("synthetic derived field error=%v, want unclassified projection path", err)
		}
	})
}

func TestSemanticRecoveryCommandResult(t *testing.T) {
	if got := semanticHalt("run terminal: state=\"FAILED\" reason=\"criterion_errored\"\n"); got != "" {
		t.Fatalf("terminal stderr halt=%q, want empty", got)
	}
	if got := semanticHalt("recovery halted: run_id=\"run-1\" reason=\"owner_unverifiable\"\n"); got != "owner_unverifiable" {
		t.Fatalf("recovery halted stderr=%q, want named halt", got)
	}
}

func TestSemanticRecoveryJournalExcludesOnlyDiagnostics(t *testing.T) {
	journal := filepath.Join(t.TempDir(), "journal.jsonl")
	entries := []map[string]any{
		{"event_id": "log", "seq": 1, "ts": "opaque", "run_id": "run", "score_revision": 1, "type": string(runstate.EventLog), "payload": map[string]any{}},
		{"event_id": "progress", "seq": 2, "ts": "opaque", "run_id": "run", "score_revision": 1, "type": string(runstate.EventProgress), "payload": map[string]any{}},
		{"event_id": "failure", "seq": 3, "ts": "opaque", "run_id": "run", "score_revision": 1, "type": string(runstate.EventAttemptFailed), "payload": map[string]any{"kind": "task_failed", "reason": "retained"}},
	}
	var contents bytes.Buffer
	for _, entry := range entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		contents.Write(encoded)
		contents.WriteByte('\n')
	}
	if err := os.WriteFile(journal, contents.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := extractSemanticJournal(journal, newSemanticNormalizer())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].(map[string]any)["type"] != string(runstate.EventAttemptFailed) {
		t.Fatalf("retained events=%s, want only attempt.failed", semanticJSON(events))
	}
}

func semanticComparisonSeed(reason string) semanticRecoverySnapshot {
	return semanticSnapshot(
		[]any{map[string]any{
			"event_id": "event-1", "seq": json.Number("1"), "ts": "2026-08-16T00:00:00.000Z", "run_id": "run-1",
			"score_revision": json.Number("1"), "type": "attempt.failed", "payload": map[string]any{"kind": "task_failed", "reason": reason, "error_detail": "stable-detail"},
		}},
		map[string]any{"result_tree": "git-sha1:stable", "semantic_hash": "sha256:stable"},
		[]any{map[string]any{"CaseID": "RC-RESUME-015", "Action": map[string]any{"Kind": "recover_incomplete_attempt", "Steps": []any{"sweep_recorded_session"}, "Replan": true}}},
		semanticCommandResult{ExitClass: "exit_4"},
	)
}

func semanticNormalizerSeed(prefix, timestamp string) semanticRecoverySnapshot {
	processID := json.Number("101")
	adapterProcessID := json.Number("202")
	if prefix == "two" {
		processID = json.Number("303")
		adapterProcessID = json.Number("404")
	}
	return semanticSnapshot(
		[]any{map[string]any{
			"event_id": prefix + "-event", "causation_id": prefix + "-cause", "seq": json.Number("1"), "ts": timestamp,
			"run_id": prefix + "-run", "score_revision": json.Number("1"), "attempt_id": prefix + "-attempt", "type": "criterion.started",
			"payload": map[string]any{
				"prepare_id": prefix + "-prepare", "proposal_id": prefix + "-proposal", "decision_id": prefix + "-decision",
				"txn_id": prefix + "-txn", "candidate_id": prefix + "-candidate", "interval_id": prefix + "-interval",
				"criterion_process": map[string]any{
					"pid": processID, "session_id": processID,
					"start_identity": map[string]any{"platform": "darwin", "start_tv_sec": json.Number("9"), "start_tv_usec": json.Number("8")},
				},
				"result_tree": "git-sha1:stable", "plan_record_hash": "sha256:stable",
			},
		}},
		map[string]any{
			"attempts": semanticMapEntries{{Key: prefix + "-attempt", Value: map[string]any{"failure": map[string]any{"reason": "stable"}}}},
			"adapter_launches": semanticMapEntries{{Key: prefix + "-attempt", Value: map[string]any{"process": map[string]any{
				"pid": adapterProcessID, "session_id": adapterProcessID,
				"start": map[string]any{"platform": "darwin", "start_tv_sec": json.Number("7"), "start_tv_usec": json.Number("6")},
			}}}},
			"result_tree": "git-sha1:stable", "change_set_ref": "refs/partitur/runs/run/change-set", "base_commit": "git-sha1:base",
			"semantic_hash": "sha256:stable", "file_hash": "sha256:stable", "plan_record_hash": "sha256:stable",
		},
		[]any{map[string]any{"CaseID": "RC-RESUME-015", "Action": map[string]any{
			"Kind": "recover_incomplete_attempt", "AttemptID": prefix + "-attempt", "CandidateID": prefix + "-candidate",
			"Steps": []any{"sweep_recorded_session"}, "Replan": true,
		}}},
		semanticCommandResult{ExitClass: "exit_4"},
	)
}

func semanticTimestampDerivedChargeSeed(prefix, observedAt string, charged, consumed, remaining int64) semanticRecoverySnapshot {
	return semanticSnapshot(
		[]any{
			map[string]any{
				"event_id": prefix + "-started", "seq": json.Number("1"), "ts": "2026-08-16T00:00:00.000Z", "run_id": prefix + "-run",
				"score_revision": json.Number("1"), "type": string(runstate.EventExecutionStarted),
				"payload": map[string]any{"interval_id": prefix + "-interval", "phase": "acceptance", "wall_start": "2026-08-16T00:00:00.000Z", "remaining_at_start": json.Number("1000")},
			},
			map[string]any{
				"event_id": prefix + "-stopped", "causation_id": prefix + "-started", "seq": json.Number("2"), "ts": observedAt, "run_id": prefix + "-run",
				"score_revision": json.Number("1"), "type": string(runstate.EventExecutionStopped),
				"payload": map[string]any{"interval_id": prefix + "-interval", "reason": "recovered", "charging": "clamped", "charged_duration": charged, "observed_at": observedAt},
			},
		},
		map[string]any{
			"projection": map[string]any{
				"state":                map[string]any{"consumed_budget_ms": consumed},
				"scheduler":            map[string]any{"remaining_time": remaining},
				"current_head_attempt": map[string]any{"failure_classification": map[string]any{"remaining_time_ms": remaining}},
			},
		},
		[]any{map[string]any{"CaseID": "RC-RESUME-015", "Action": map[string]any{"Kind": "recover_incomplete_attempt", "Replan": true}}},
		semanticCommandResult{ExitClass: "exit_4"},
	)
}

func semanticBudgetClockCompletenessFixture() ([]map[string]any, any) {
	return []map[string]any{
			{"payload": map[string]any{"remaining_at_start": 1000, "wall_start": "2026-08-16T00:00:00.000Z"}},
			{"payload": map[string]any{"charging": "clamped", "charged_duration": 250}},
			{"payload": map[string]any{"duration_ms": 100}},
			{"payload": map[string]any{"quiesce_silence_limit_ms": 60_000, "observed_at": "2026-08-16T00:00:00.250Z"}},
			{"payload": map[string]any{"disposition": map[string]any{"charged": "none"}}},
		}, map[string]any{
			"projection": map[string]any{
				"state": map[string]any{
					"consumed_budget_ms": 250,
					"open_execution":     map[string]any{"wall_start": "2026-08-16T00:00:00.000Z"},
					"pending_prepare": map[string]any{
						"quiesce_silence_limit_ms":   60_000,
						"prepared_at":                "2026-08-16T00:00:00.000Z",
						"latest_quiesce_observed_at": "2026-08-16T00:00:00.250Z",
					},
					"attempts": semanticMapEntries{{Key: "attempt", Value: map[string]any{
						"failure": map[string]any{"disposition": map[string]any{"charged": "none"}},
					}}},
				},
				"scheduler": map[string]any{"remaining_time": 750},
				"current_head_attempt": map[string]any{
					"failure_classification": map[string]any{"remaining_time_ms": 750, "retries_consumed": 1},
					"recorded_disposition":   map[string]any{"charged": "none"},
				},
			},
		}
}

func semanticTimestampDerivedChargeJournal(t *testing.T, wallStart, observedAt string, remainingAtStart, charged int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	entries := []map[string]any{
		{
			"event_id": "started", "seq": 1, "ts": "2026-08-16T00:00:00.000Z", "run_id": "run", "score_revision": 1,
			"type":    string(runstate.EventExecutionStarted),
			"payload": map[string]any{"interval_id": "interval", "phase": "acceptance", "wall_start": wallStart, "remaining_at_start": remainingAtStart},
		},
		{
			"event_id": "stopped", "seq": 2, "ts": observedAt, "run_id": "run", "score_revision": 1,
			"type":    string(runstate.EventExecutionStopped),
			"payload": map[string]any{"interval_id": "interval", "reason": "recovered", "charging": "clamped", "charged_duration": charged, "observed_at": observedAt},
		},
	}
	var contents bytes.Buffer
	for _, entry := range entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		contents.Write(encoded)
		contents.WriteByte('\n')
	}
	if err := os.WriteFile(path, contents.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func semanticSnapshot(events []any, projection any, actions []any, command semanticCommandResult) semanticRecoverySnapshot {
	normalizer := newSemanticNormalizer()
	normalizedEvents := make([]any, len(events))
	for index, event := range events {
		normalizedEvents[index] = normalizer.normalize(event)
	}
	normalizedProjection := normalizer.normalize(projection)
	normalizedActions := make([]any, len(actions))
	for index, action := range actions {
		normalizedActions[index] = normalizer.normalize(action)
	}
	return semanticRecoverySnapshot{
		Events: normalizedEvents, Projection: normalizedProjection, Actions: normalizedActions, Command: command,
	}
}
