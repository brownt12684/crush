package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/tidwall/sjson"
)

const (
	toolLoopRepeatThreshold            = 3
	toolLoopRecentObservationLimit     = 6
	toolLoopProgressTextThresholdRunes = 120
	toolLoopHandoffTextPreviewRunes    = 160
	toolLoopHandoffArrayPreviewLimit   = 4
	toolLoopSignatureDigestBytes       = 8
)

type toolCycleOutcomeClass string

const (
	toolCycleOutcomeSuccessProgressing   toolCycleOutcomeClass = "success_progressing"
	toolCycleOutcomeSuccessLowSignal     toolCycleOutcomeClass = "success_low_signal"
	toolCycleOutcomeFailureRetryable     toolCycleOutcomeClass = "failure_retryable"
	toolCycleOutcomeFailureNonRetryable  toolCycleOutcomeClass = "failure_nonretryable"
	toolCycleOutcomeFailureInvalidTool   toolCycleOutcomeClass = "failure_invalid_tool"
	toolCycleOutcomeFailureInvalidSchema toolCycleOutcomeClass = "failure_invalid_schema"
)

type toolLoopClassification string

const (
	toolLoopClassificationNotALoop                    toolLoopClassification = "not_a_loop"
	toolLoopClassificationRepeatedRetryableFailure    toolLoopClassification = "repeated_retryable_failure"
	toolLoopClassificationRepeatedLowSignalSuccess    toolLoopClassification = "repeated_low_signal_success"
	toolLoopClassificationRepeatedNonRetryableFailure toolLoopClassification = "repeated_nonretryable_failure"
	toolLoopClassificationRepeatedInvalidTool         toolLoopClassification = "repeated_invalid_tool_loop"
	toolLoopClassificationRepeatedInvalidSchema       toolLoopClassification = "repeated_invalid_schema_loop"
)

type toolLoopSuggestedAction string

const (
	toolLoopSuggestedActionRepairLocally     toolLoopSuggestedAction = "repair_locally"
	toolLoopSuggestedActionRequestSupervisor toolLoopSuggestedAction = "request_supervisor"
	toolLoopSuggestedActionPauseAutonomy     toolLoopSuggestedAction = "pause_autonomy"
	toolLoopSuggestedActionSwitchModel       toolLoopSuggestedAction = "switch_model"
)

type toolLoopKind string

const (
	toolLoopKindRepeatedLowSignalSuccess toolLoopKind = "repeated_low_signal_success"
	toolLoopKindRepeatedRetryableFailure toolLoopKind = "repeated_retryable_failure"
	toolLoopKindRepeatedNonRetryable     toolLoopKind = "repeated_nonretryable_failure"
	toolLoopKindRepeatedMetaToolProbe    toolLoopKind = "repeated_meta_tool_probe"
	toolLoopKindRepeatedInvalidTool      toolLoopKind = "repeated_invalid_tool_loop"
	toolLoopKindRepeatedInvalidSchema    toolLoopKind = "repeated_invalid_schema_loop"
)

type toolCycleObservation struct {
	SessionID           string                `json:"session_id"`
	ToolCallID          string                `json:"tool_call_id,omitempty"`
	ToolName            string                `json:"tool_name"`
	ToolInput           string                `json:"tool_input"`
	NormalizedInput     string                `json:"normalized_input"`
	NormalizedSignature string                `json:"normalized_signature"`
	OutcomeClass        toolCycleOutcomeClass `json:"outcome_class"`
	Retryable           bool                  `json:"retryable"`
	ProgressSignals     []string              `json:"progress_signals,omitempty"`
	ResultSummary       string                `json:"result_summary,omitempty"`
	ResultIsError       bool                  `json:"result_is_error,omitempty"`
	FailureKind         string                `json:"failure_kind,omitempty"`
	RepairAttempted     bool                  `json:"repair_attempted,omitempty"`
	RepairOutcome       string                `json:"repair_outcome,omitempty"`
	BlockedTool         bool                  `json:"blocked_tool,omitempty"`
}

type toolLoopCallSummary struct {
	ToolName string `json:"tool_name"`
	Input    string `json:"input"`
}

type toolLoopResultSummary struct {
	ToolName  string `json:"tool_name"`
	IsError   bool   `json:"is_error"`
	Summary   string `json:"summary,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

type toolLoopHandoff struct {
	SessionID                  string                  `json:"session_id"`
	ToolName                   string                  `json:"tool_name"`
	NormalizedSignature        string                  `json:"normalized_signature"`
	StreakCount                int                     `json:"streak_count"`
	OutcomeClass               toolCycleOutcomeClass   `json:"outcome_class"`
	Classification             toolLoopClassification  `json:"classification"`
	LoopKind                   toolLoopKind            `json:"loop_kind,omitempty"`
	Retryable                  bool                    `json:"retryable"`
	RecentToolCalls            []toolLoopCallSummary   `json:"recent_tool_calls,omitempty"`
	RecentToolResults          []toolLoopResultSummary `json:"recent_tool_results,omitempty"`
	ProgressSignals            []string                `json:"progress_signals,omitempty"`
	SuggestedAction            toolLoopSuggestedAction `json:"suggested_action"`
	BlockedTool                bool                    `json:"blocked_tool,omitempty"`
	Reason                     string                  `json:"reason"`
	RepairAttempted            bool                    `json:"repair_attempted,omitempty"`
	RepairOutcome              string                  `json:"repair_outcome,omitempty"`
	SupervisorHandoffTriggered bool                    `json:"supervisor_handoff_triggered"`
}

type toolLoopGuardMetadata struct {
	ToolName        string       `json:"tool_name,omitempty"`
	NormalizedInput string       `json:"normalized_input,omitempty"`
	LoopKind        toolLoopKind `json:"loop_kind,omitempty"`
	BlockedTool     bool         `json:"blocked_tool,omitempty"`
	Reason          string       `json:"reason,omitempty"`
}

type toolLoopState struct {
	CurrentSignature string
	StreakCount      int
	Recent           []toolCycleObservation
}

type toolLoopTracker struct {
	states *csync.Map[string, toolLoopState]
}

func newToolLoopTracker() *toolLoopTracker {
	return &toolLoopTracker{
		states: csync.NewMap[string, toolLoopState](),
	}
}

func (t *toolLoopTracker) Reset(sessionID string) {
	if t == nil || t.states == nil || sessionID == "" {
		return
	}
	t.states.Del(sessionID)
}

func (t *toolLoopTracker) ResetIfSubstantiveAssistantProgress(sessionID string, assistantText string) {
	if t == nil || sessionID == "" {
		return
	}
	if isSubstantiveAssistantProgress(assistantText) {
		t.Reset(sessionID)
	}
}

func isSubstantiveAssistantProgress(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	return len([]rune(text)) >= toolLoopProgressTextThresholdRunes
}

func (t *toolLoopTracker) Observe(
	_ context.Context,
	observation toolCycleObservation,
) *toolLoopHandoff {
	if t == nil || t.states == nil || observation.SessionID == "" {
		return nil
	}

	if observation.RepairOutcome == toolCallRepairOutcomeSucceeded {
		t.Reset(observation.SessionID)
	}

	if observation.OutcomeClass == toolCycleOutcomeSuccessProgressing {
		t.Reset(observation.SessionID)
		return nil
	}

	state, _ := t.states.Get(observation.SessionID)
	if state.CurrentSignature == observation.NormalizedSignature {
		state.StreakCount++
	} else {
		state.CurrentSignature = observation.NormalizedSignature
		state.StreakCount = 1
	}

	state.Recent = append(state.Recent, observation)
	if len(state.Recent) > toolLoopRecentObservationLimit {
		state.Recent = append([]toolCycleObservation(nil), state.Recent[len(state.Recent)-toolLoopRecentObservationLimit:]...)
	}
	t.states.Set(observation.SessionID, state)

	classification := detectRepeatedLoop(observation, state.StreakCount)
	if classification == toolLoopClassificationNotALoop {
		return nil
	}

	return buildSupervisorHandoff(observation, state, classification)
}

func (t *toolLoopTracker) ShouldBlockMetaToolCall(sessionID string, call fantasy.ToolCall) bool {
	if t == nil || t.states == nil || sessionID == "" || !isHardGatedMetaProbeTool(call.Name) {
		return false
	}

	state, ok := t.states.Get(sessionID)
	if !ok || len(state.Recent) == 0 {
		return false
	}

	last := state.Recent[len(state.Recent)-1]
	if !strings.EqualFold(strings.TrimSpace(last.ToolName), strings.TrimSpace(call.Name)) {
		return false
	}
	if last.NormalizedInput != normalizeToolCallInput(call.Input) {
		return false
	}
	return last.OutcomeClass == toolCycleOutcomeSuccessLowSignal
}

func observeToolCycle(
	tracker *toolLoopTracker,
	ctx context.Context,
	observation toolCycleObservation,
) *toolLoopHandoff {
	if tracker == nil {
		return nil
	}
	return tracker.Observe(ctx, observation)
}

func classifyToolCycle(
	call fantasy.ToolCall,
	resp fantasy.ToolResponse,
	repairAudit *toolCallRepairAudit,
	sessionID string,
) toolCycleObservation {
	observation := toolCycleObservation{
		SessionID:       sessionID,
		ToolCallID:      call.ID,
		ToolName:        strings.TrimSpace(call.Name),
		ToolInput:       call.Input,
		NormalizedInput: normalizeToolCallInput(call.Input),
		ProgressSignals: nil,
		ResultSummary:   strings.TrimSpace(resp.Content),
		ResultIsError:   resp.IsError,
	}

	if repairAudit != nil && repairAudit.Runtime != nil {
		observation.RepairAttempted = true
		observation.RepairOutcome = repairAudit.Runtime.Outcome
	} else if repairAudit != nil && repairAudit.AttemptCount > 0 {
		observation.RepairAttempted = true
		observation.RepairOutcome = repairAudit.Outcome
	}

	switch {
	case resp.IsError:
		observation.classifyFailure(call, resp)
	default:
		observation.classifySuccess(call, resp)
	}

	observation.NormalizedSignature = strings.ToLower(strings.TrimSpace(observation.ToolName)) +
		"\x00" + observation.NormalizedInput +
		"\x00" + string(observation.OutcomeClass)
	return observation
}

func classifyPreExecutionToolCycle(
	toolCall fantasy.ToolCallContent,
	toolResult fantasy.ToolResultContent,
	metadataJSON string,
	sessionID string,
) toolCycleObservation {
	observation := toolCycleObservation{
		SessionID:       sessionID,
		ToolCallID:      toolCall.ToolCallID,
		ToolName:        strings.TrimSpace(toolCall.ToolName),
		ToolInput:       toolCall.Input,
		NormalizedInput: normalizeToolCallInput(toolCall.Input),
		ProgressSignals: []string{"no_progress_detected"},
		ResultSummary:   toolResultSummary(toolResult),
		ResultIsError:   toolResultIsErrorContent(toolResult),
	}

	if audit, ok := toolRepairAuditFromMetadataJSON(metadataJSON); ok {
		observation.RepairAttempted = audit.RepairAttempted
		observation.RepairOutcome = audit.Outcome
		observation.FailureKind = audit.FailureKind
		if audit.FailureKind == "" && strings.Contains(strings.ToLower(audit.InitialValidationError), "tool not found") {
			observation.FailureKind = toolFailureKindInvalidToolName
		}
	}
	if observation.FailureKind == "" && toolCall.ValidationError != nil {
		observation.FailureKind = validationFailureKind(toolCall.ValidationError)
	}

	switch observation.FailureKind {
	case toolFailureKindInvalidToolName:
		observation.OutcomeClass = toolCycleOutcomeFailureInvalidTool
		observation.ProgressSignals = append(observation.ProgressSignals, toolFailureKindInvalidToolName)
	case toolFailureKindInvalidSchema:
		observation.OutcomeClass = toolCycleOutcomeFailureInvalidSchema
		observation.ProgressSignals = append(observation.ProgressSignals, toolFailureKindInvalidSchema)
	default:
		observation.OutcomeClass = toolCycleOutcomeFailureNonRetryable
	}

	observation.NormalizedSignature = strings.ToLower(strings.TrimSpace(observation.ToolName)) +
		"\x00" + observation.NormalizedInput +
		"\x00" + string(observation.OutcomeClass)
	return observation
}

func (o *toolCycleObservation) classifySuccess(call fantasy.ToolCall, resp fantasy.ToolResponse) {
	switch strings.TrimSpace(call.Name) {
	case tools.CrushInfoToolName:
		o.OutcomeClass = toolCycleOutcomeSuccessLowSignal
		o.ProgressSignals = []string{"low_signal_probe", "meta_tool_probe"}
	case tools.CrushLogsToolName,
		tools.ListMCPResourcesToolName,
		tools.ReadMCPResourceToolName,
		tools.JobOutputToolName,
		tools.ViewToolName,
		tools.LSToolName,
		tools.GlobToolName,
		tools.GrepToolName,
		tools.DiagnosticsToolName,
		tools.ReferencesToolName,
		tools.FetchToolName,
		tools.WebFetchToolName,
		tools.WebSearchToolName,
		tools.SourcegraphToolName:
		o.OutcomeClass = toolCycleOutcomeSuccessLowSignal
		o.ProgressSignals = []string{"read_only_probe"}
	case tools.WriteToolName:
		o.OutcomeClass = toolCycleOutcomeSuccessProgressing
		o.ProgressSignals = extractWriteProgressSignals(resp.Metadata)
	case tools.EditToolName:
		o.OutcomeClass = toolCycleOutcomeSuccessProgressing
		o.ProgressSignals = extractEditProgressSignals(resp.Metadata)
	case tools.MultiEditToolName:
		o.OutcomeClass = toolCycleOutcomeSuccessProgressing
		o.ProgressSignals = extractMultiEditProgressSignals(resp.Metadata)
	case tools.TodosToolName, tools.TodoToolAliasName:
		o.OutcomeClass = toolCycleOutcomeSuccessProgressing
		o.ProgressSignals = extractTodosProgressSignals(resp.Metadata)
	default:
		o.OutcomeClass = toolCycleOutcomeSuccessProgressing
	}
	if len(o.ProgressSignals) == 0 {
		o.ProgressSignals = []string{"tool_completed"}
	}
}

func (o *toolCycleObservation) classifyFailure(call fantasy.ToolCall, resp fantasy.ToolResponse) {
	o.Retryable = false
	o.ProgressSignals = []string{"no_progress_detected"}

	if guard, ok := toolLoopGuardMetadataFromJSON(resp.Metadata); ok {
		o.OutcomeClass = toolCycleOutcomeSuccessLowSignal
		o.ProgressSignals = []string{"low_signal_probe", "tool_blocked", string(guard.LoopKind)}
		o.BlockedTool = guard.BlockedTool
		return
	}
	if audit, ok := toolRepairAuditFromMetadataJSON(resp.Metadata); ok {
		switch audit.FailureKind {
		case toolFailureKindInvalidToolName:
			o.OutcomeClass = toolCycleOutcomeFailureInvalidTool
			o.FailureKind = audit.FailureKind
			o.ProgressSignals = append(o.ProgressSignals, audit.FailureKind)
			return
		case toolFailureKindInvalidSchema:
			o.OutcomeClass = toolCycleOutcomeFailureInvalidSchema
			o.FailureKind = audit.FailureKind
			o.ProgressSignals = append(o.ProgressSignals, audit.FailureKind)
			return
		}
	}

	switch strings.TrimSpace(call.Name) {
	case tools.BashToolName:
		if details, ok := runtimeRepairFailure(call.Name, resp); ok {
			o.Retryable = details.Retryable
			o.FailureKind = details.FailureKind
			if details.Retryable {
				o.OutcomeClass = toolCycleOutcomeFailureRetryable
				o.ProgressSignals = append(o.ProgressSignals, details.FailureKind)
			} else {
				o.OutcomeClass = toolCycleOutcomeFailureNonRetryable
			}
			return
		}
	case tools.EditToolName, tools.MultiEditToolName:
		lower := strings.ToLower(resp.Content)
		switch {
		case strings.Contains(lower, "old_string not found"),
			strings.Contains(lower, "modified since it was last read"),
			strings.Contains(lower, "use the view tool first"),
			strings.Contains(lower, "appears multiple times"):
			o.Retryable = true
			o.FailureKind = toolFailureKindRetryableRuntime
			o.OutcomeClass = toolCycleOutcomeFailureRetryable
			return
		case strings.Contains(lower, "no changes made"):
			o.OutcomeClass = toolCycleOutcomeFailureNonRetryable
			return
		}
	case tools.WriteToolName:
		lower := strings.ToLower(resp.Content)
		switch {
		case strings.Contains(lower, "modified since it was last read"):
			o.Retryable = true
			o.FailureKind = toolFailureKindRetryableRuntime
			o.OutcomeClass = toolCycleOutcomeFailureRetryable
			return
		case strings.Contains(lower, "already contains the exact content"),
			strings.Contains(lower, "no changes made"):
			o.OutcomeClass = toolCycleOutcomeFailureNonRetryable
			return
		}
	}

	o.OutcomeClass = toolCycleOutcomeFailureNonRetryable
}

func extractWriteProgressSignals(metadataJSON string) []string {
	var metadata tools.WriteResponseMetadata
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		return nil
	}
	signals := []string{"file_content_changed"}
	if metadata.Additions > 0 && metadata.Removals == 0 {
		signals = append(signals, "new_or_expanded_content")
	}
	return slices.Compact(signals)
}

func extractEditProgressSignals(metadataJSON string) []string {
	var metadata tools.EditResponseMetadata
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		return nil
	}
	signals := []string{"file_content_changed"}
	if metadata.Additions > 0 || metadata.Removals > 0 {
		signals = append(signals, "target_file_updated")
	}
	return slices.Compact(signals)
}

func extractMultiEditProgressSignals(metadataJSON string) []string {
	var metadata tools.MultiEditResponseMetadata
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		return nil
	}
	signals := []string{"file_content_changed"}
	if metadata.EditsApplied > 0 {
		signals = append(signals, fmt.Sprintf("edits_applied:%d", metadata.EditsApplied))
	}
	return slices.Compact(signals)
}

func extractTodosProgressSignals(metadataJSON string) []string {
	var metadata tools.TodosResponseMetadata
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		return nil
	}
	signals := []string{"todo_state_updated"}
	if metadata.JustStarted != "" {
		signals = append(signals, "todo_started")
	}
	if len(metadata.JustCompleted) > 0 {
		signals = append(signals, "todo_completed")
	}
	return slices.Compact(signals)
}

func detectRepeatedLoop(observation toolCycleObservation, streakCount int) toolLoopClassification {
	if streakCount < toolLoopRepeatThreshold {
		return toolLoopClassificationNotALoop
	}

	switch observation.OutcomeClass {
	case toolCycleOutcomeFailureRetryable:
		return toolLoopClassificationRepeatedRetryableFailure
	case toolCycleOutcomeFailureInvalidTool:
		return toolLoopClassificationRepeatedInvalidTool
	case toolCycleOutcomeFailureInvalidSchema:
		return toolLoopClassificationRepeatedInvalidSchema
	case toolCycleOutcomeSuccessLowSignal:
		return toolLoopClassificationRepeatedLowSignalSuccess
	case toolCycleOutcomeFailureNonRetryable:
		return toolLoopClassificationRepeatedNonRetryableFailure
	default:
		return toolLoopClassificationNotALoop
	}
}

func buildSupervisorHandoff(
	observation toolCycleObservation,
	state toolLoopState,
	classification toolLoopClassification,
) *toolLoopHandoff {
	recentObservations := make([]toolCycleObservation, 0, toolLoopRepeatThreshold)
	for i := len(state.Recent) - 1; i >= 0 && len(recentObservations) < toolLoopRepeatThreshold; i-- {
		if state.Recent[i].NormalizedSignature != observation.NormalizedSignature {
			continue
		}
		recentObservations = append(recentObservations, state.Recent[i])
	}
	slices.Reverse(recentObservations)

	handoff := &toolLoopHandoff{
		SessionID:                  observation.SessionID,
		ToolName:                   observation.ToolName,
		NormalizedSignature:        compactToolLoopSignature(observation.NormalizedSignature),
		StreakCount:                state.StreakCount,
		OutcomeClass:               observation.OutcomeClass,
		Classification:             classification,
		LoopKind:                   deriveLoopKind(classification, observation),
		Retryable:                  observation.Retryable,
		ProgressSignals:            slices.Compact(aggregateProgressSignals(recentObservations)),
		RepairAttempted:            observation.RepairAttempted,
		RepairOutcome:              observation.RepairOutcome,
		SuggestedAction:            suggestLoopAction(classification, observation),
		BlockedTool:                anyBlockedTool(recentObservations),
		SupervisorHandoffTriggered: true,
	}

	for _, item := range recentObservations {
		handoff.RecentToolCalls = append(handoff.RecentToolCalls, toolLoopCallSummary{
			ToolName: item.ToolName,
			Input:    summarizeToolInputForHandoff(item.ToolInput),
		})
		handoff.RecentToolResults = append(handoff.RecentToolResults, toolLoopResultSummary{
			ToolName:  item.ToolName,
			IsError:   item.ResultIsError,
			Summary:   summarizeToolLoopText(item.ResultSummary),
			Retryable: item.Retryable,
		})
	}

	handoff.Reason = buildLoopReason(classification, observation, state.StreakCount)
	return handoff
}

func aggregateProgressSignals(observations []toolCycleObservation) []string {
	var signals []string
	for _, observation := range observations {
		signals = append(signals, observation.ProgressSignals...)
	}
	return signals
}

func anyBlockedTool(observations []toolCycleObservation) bool {
	for _, observation := range observations {
		if observation.BlockedTool {
			return true
		}
	}
	return false
}

func compactToolLoopSignature(signature string) string {
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(signature))
	return "sha256:" + hex.EncodeToString(sum[:toolLoopSignatureDigestBytes])
}

func summarizeToolInputForHandoff(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}

	var decoded any
	if err := json.Unmarshal([]byte(input), &decoded); err == nil {
		sanitized := sanitizeToolLoopValue("", decoded)
		if encoded, err := json.Marshal(sanitized); err == nil {
			return summarizeToolLoopText(string(encoded))
		}
	}

	return summarizeToolLoopText(input)
}

func sanitizeToolLoopValue(field string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[key] = sanitizeToolLoopValue(key, child)
		}
		return out
	case []any:
		limit := len(typed)
		if limit > toolLoopHandoffArrayPreviewLimit {
			limit = toolLoopHandoffArrayPreviewLimit
		}
		out := make([]any, 0, limit+1)
		for i := 0; i < limit; i++ {
			out = append(out, sanitizeToolLoopValue(field, typed[i]))
		}
		if len(typed) > limit {
			out = append(out, fmt.Sprintf("<%d more item(s)>", len(typed)-limit))
		}
		return out
	case string:
		return summarizeToolLoopStringField(field, typed)
	default:
		return value
	}
}

func summarizeToolLoopStringField(field string, value string) string {
	field = strings.ToLower(strings.TrimSpace(field))
	switch field {
	case "content", "old_string", "new_string", "stdout", "stderr", "data", "command", "input":
		return fmt.Sprintf("<redacted len=%d>", len([]rune(value)))
	default:
		return summarizeToolLoopText(value)
	}
}

func summarizeToolLoopText(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= toolLoopHandoffTextPreviewRunes {
		return text
	}
	return string(runes[:toolLoopHandoffTextPreviewRunes]) + "…"
}

func deriveLoopKind(classification toolLoopClassification, observation toolCycleObservation) toolLoopKind {
	if isMetaProbeToolName(observation.ToolName) {
		return toolLoopKindRepeatedMetaToolProbe
	}

	switch classification {
	case toolLoopClassificationRepeatedRetryableFailure:
		return toolLoopKindRepeatedRetryableFailure
	case toolLoopClassificationRepeatedInvalidTool:
		return toolLoopKindRepeatedInvalidTool
	case toolLoopClassificationRepeatedInvalidSchema:
		return toolLoopKindRepeatedInvalidSchema
	case toolLoopClassificationRepeatedNonRetryableFailure:
		return toolLoopKindRepeatedNonRetryable
	case toolLoopClassificationRepeatedLowSignalSuccess:
		return toolLoopKindRepeatedLowSignalSuccess
	default:
		return ""
	}
}

func suggestLoopAction(classification toolLoopClassification, observation toolCycleObservation) toolLoopSuggestedAction {
	if deriveLoopKind(classification, observation) == toolLoopKindRepeatedMetaToolProbe {
		return toolLoopSuggestedActionRequestSupervisor
	}

	switch classification {
	case toolLoopClassificationRepeatedRetryableFailure:
		if observation.RepairAttempted && observation.RepairOutcome != "" && observation.RepairOutcome != toolCallRepairOutcomeSucceeded {
			return toolLoopSuggestedActionRequestSupervisor
		}
		return toolLoopSuggestedActionRepairLocally
	case toolLoopClassificationRepeatedInvalidTool:
		return toolLoopSuggestedActionRequestSupervisor
	case toolLoopClassificationRepeatedInvalidSchema:
		if observation.RepairAttempted && observation.RepairOutcome != "" && observation.RepairOutcome != toolCallRepairOutcomeSucceeded {
			return toolLoopSuggestedActionRequestSupervisor
		}
		return toolLoopSuggestedActionPauseAutonomy
	case toolLoopClassificationRepeatedNonRetryableFailure:
		return toolLoopSuggestedActionPauseAutonomy
	case toolLoopClassificationRepeatedLowSignalSuccess:
		return toolLoopSuggestedActionRequestSupervisor
	default:
		return toolLoopSuggestedActionPauseAutonomy
	}
}

func buildLoopReason(classification toolLoopClassification, observation toolCycleObservation, streakCount int) string {
	if deriveLoopKind(classification, observation) == toolLoopKindRepeatedMetaToolProbe {
		return fmt.Sprintf(
			"Detected %d consecutive %s diagnostic probes without new context. Crush blocked repeated no-progress meta inspection and paused autonomy for supervisor review.",
			streakCount,
			observation.ToolName,
		)
	}

	switch classification {
	case toolLoopClassificationRepeatedRetryableFailure:
		return fmt.Sprintf(
			"Detected %d consecutive retryable %s calls with no meaningful progress. Crush paused autonomy to avoid repeating the same failing tool cycle.",
			streakCount,
			observation.ToolName,
		)
	case toolLoopClassificationRepeatedInvalidTool:
		return fmt.Sprintf(
			"Detected %d consecutive invalid tool requests for %s. Crush stopped the repeated invalid tool pattern locally instead of letting the turn thrash.",
			streakCount,
			observation.ToolName,
		)
	case toolLoopClassificationRepeatedInvalidSchema:
		return fmt.Sprintf(
			"Detected %d consecutive invalid schema failures for %s. Crush paused autonomy instead of repeating the same malformed tool call.",
			streakCount,
			observation.ToolName,
		)
	case toolLoopClassificationRepeatedNonRetryableFailure:
		return fmt.Sprintf(
			"Detected %d consecutive non-retryable %s failures with no meaningful progress. Crush paused autonomy instead of continuing a dead-end loop.",
			streakCount,
			observation.ToolName,
		)
	case toolLoopClassificationRepeatedLowSignalSuccess:
		return fmt.Sprintf(
			"Detected %d consecutive low-signal successful %s calls without task progress. Crush paused autonomy and escalated for supervisor review.",
			streakCount,
			observation.ToolName,
		)
	default:
		return ""
	}
}

func toolRepairAuditFromMetadataJSON(metadata string) (toolCallRepairAudit, bool) {
	if metadata == "" || !json.Valid([]byte(metadata)) {
		return toolCallRepairAudit{}, false
	}

	var envelope struct {
		Repair *toolCallRepairAudit `json:"repair"`
	}
	if err := json.Unmarshal([]byte(metadata), &envelope); err != nil || envelope.Repair == nil {
		return toolCallRepairAudit{}, false
	}
	return *envelope.Repair, true
}

func toolResultSummary(result fantasy.ToolResultContent) string {
	if errResult, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](result.Result); ok && errResult.Error != nil {
		return strings.TrimSpace(errResult.Error.Error())
	}
	if textResult, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](result.Result); ok {
		return strings.TrimSpace(textResult.Text)
	}
	if mediaResult, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentMedia](result.Result); ok {
		return strings.TrimSpace(mediaResult.Text)
	}
	return ""
}

func toolResultIsErrorContent(result fantasy.ToolResultContent) bool {
	_, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](result.Result)
	return ok
}

func validationFailureKind(err error) string {
	if err == nil {
		return ""
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "tool not found"):
		return toolFailureKindInvalidToolName
	case strings.Contains(lower, "missing required parameter"),
		strings.Contains(lower, "invalid json input"),
		strings.Contains(lower, "validation failed"):
		return toolFailureKindInvalidSchema
	default:
		return toolFailureKindInvalidSchema
	}
}

func mergeToolLoopMetadata(existing string, handoff toolLoopHandoff) string {
	data, err := json.Marshal(handoff)
	if err != nil {
		return existing
	}

	if existing == "" || !json.Valid([]byte(existing)) {
		return `{"loop":` + string(data) + `}`
	}

	merged, err := sjson.SetRaw(existing, "loop", string(data))
	if err != nil {
		return existing
	}
	return merged
}

func buildMetaToolGuardResponse(call fantasy.ToolCall) fantasy.ToolResponse {
	reason := fmt.Sprintf(
		"Repeated %s call blocked because Crush already returned the same diagnostic output without new context. Continue with a different tool or the next implementation step.",
		call.Name,
	)
	resp := fantasy.NewTextErrorResponse(reason)
	resp.Metadata = mergeToolLoopGuardMetadata(resp.Metadata, toolLoopGuardMetadata{
		ToolName:        strings.TrimSpace(call.Name),
		NormalizedInput: normalizeToolCallInput(call.Input),
		LoopKind:        toolLoopKindRepeatedMetaToolProbe,
		BlockedTool:     true,
		Reason:          reason,
	})
	return resp
}

func mergeToolLoopGuardMetadata(existing string, guard toolLoopGuardMetadata) string {
	data, err := json.Marshal(guard)
	if err != nil {
		return existing
	}

	if existing == "" || !json.Valid([]byte(existing)) {
		return `{"guard":` + string(data) + `}`
	}

	merged, err := sjson.SetRaw(existing, "guard", string(data))
	if err != nil {
		return existing
	}
	return merged
}

func toolLoopGuardMetadataFromJSON(metadata string) (toolLoopGuardMetadata, bool) {
	if metadata == "" || !json.Valid([]byte(metadata)) {
		return toolLoopGuardMetadata{}, false
	}

	var envelope struct {
		Guard *toolLoopGuardMetadata `json:"guard"`
	}
	if err := json.Unmarshal([]byte(metadata), &envelope); err != nil || envelope.Guard == nil {
		return toolLoopGuardMetadata{}, false
	}
	return *envelope.Guard, true
}

func isHardGatedMetaProbeTool(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), tools.CrushInfoToolName)
}

func isMetaProbeToolName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), tools.CrushInfoToolName)
}
