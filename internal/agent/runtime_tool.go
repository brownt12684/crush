package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/hooks"
)

type runtimeToolContextKey string

const (
	runtimeToolMessagesContextKey       runtimeToolContextKey = "runtime_tool_messages"
	runtimeToolAvailableToolsContextKey runtimeToolContextKey = "runtime_tool_available_tools"
)

func withRuntimeToolMessages(ctx context.Context, messages []fantasy.Message) context.Context {
	return context.WithValue(ctx, runtimeToolMessagesContextKey, append([]fantasy.Message(nil), messages...))
}

func withRuntimeToolAvailableTools(ctx context.Context, availableTools []fantasy.AgentTool) context.Context {
	return context.WithValue(ctx, runtimeToolAvailableToolsContextKey, append([]fantasy.AgentTool(nil), availableTools...))
}

func runtimeToolMessagesFromContext(ctx context.Context) []fantasy.Message {
	messages, _ := ctx.Value(runtimeToolMessagesContextKey).([]fantasy.Message)
	return append([]fantasy.Message(nil), messages...)
}

func runtimeToolAvailableToolsFromContext(ctx context.Context) []fantasy.AgentTool {
	availableTools, _ := ctx.Value(runtimeToolAvailableToolsContextKey).([]fantasy.AgentTool)
	return append([]fantasy.AgentTool(nil), availableTools...)
}

type runtimeRepairTool struct {
	inner         fantasy.AgentTool
	model         fantasy.LanguageModel
	postToolHooks *hooks.Runner
	audits        *csync.Map[string, toolCallRepairAudit]
	loopTracker   *toolLoopTracker
	cfg           toolCallRepairConfig
}

func newRuntimeRepairTool(
	inner fantasy.AgentTool,
	model fantasy.LanguageModel,
	postToolHooks *hooks.Runner,
	audits *csync.Map[string, toolCallRepairAudit],
	loopTracker *toolLoopTracker,
	cfg toolCallRepairConfig,
) *runtimeRepairTool {
	return &runtimeRepairTool{
		inner:         inner,
		model:         model,
		postToolHooks: postToolHooks,
		audits:        audits,
		loopTracker:   loopTracker,
		cfg:           cfg,
	}
}

func wrapToolsWithRuntimeRepair(
	toolList []fantasy.AgentTool,
	model fantasy.LanguageModel,
	postToolHooks *hooks.Runner,
	audits *csync.Map[string, toolCallRepairAudit],
	loopTracker *toolLoopTracker,
) []fantasy.AgentTool {
	if len(toolList) == 0 {
		return toolList
	}
	cfg := toolCallRepairConfigFromEnv()
	out := make([]fantasy.AgentTool, len(toolList))
	for i, tool := range toolList {
		out[i] = newRuntimeRepairTool(tool, model, postToolHooks, audits, loopTracker, cfg)
	}
	return out
}

func (t *runtimeRepairTool) Info() fantasy.ToolInfo {
	return t.inner.Info()
}

func (t *runtimeRepairTool) ProviderOptions() fantasy.ProviderOptions {
	return t.inner.ProviderOptions()
}

func (t *runtimeRepairTool) SetProviderOptions(opts fantasy.ProviderOptions) {
	t.inner.SetProviderOptions(opts)
}

func (t *runtimeRepairTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	if resp, blocked := t.blockRepeatedMetaToolCall(ctx, call); blocked {
		finalResp := t.applyRepeatedLoopGuard(ctx, call, call, resp)
		t.runPostToolHook(ctx, call, call, finalResp)
		return finalResp, nil
	}

	resp, err := t.inner.Run(ctx, call)
	if err != nil {
		return resp, err
	}

	finalCall := call
	finalResp := resp
	runtimeAudit, hadRuntimeFailure := t.repairExecutionFailure(ctx, call, resp)
	if hadRuntimeFailure {
		finalCall, finalResp = runtimeAudit.finalCall, runtimeAudit.finalResp
	}
	finalResp = t.applyRepeatedLoopGuard(ctx, call, finalCall, finalResp)

	t.runPostToolHook(ctx, call, finalCall, finalResp)
	return finalResp, nil
}

func (t *runtimeRepairTool) blockRepeatedMetaToolCall(
	ctx context.Context,
	call fantasy.ToolCall,
) (fantasy.ToolResponse, bool) {
	sessionID := tools.GetSessionFromContext(ctx)
	if t.loopTracker == nil || sessionID == "" {
		return fantasy.ToolResponse{}, false
	}
	if !t.loopTracker.ShouldBlockMetaToolCall(sessionID, call) {
		return fantasy.ToolResponse{}, false
	}
	return buildMetaToolGuardResponse(call), true
}

type runtimeRepairOutcome struct {
	finalCall fantasy.ToolCall
	finalResp fantasy.ToolResponse
}

func (t *runtimeRepairTool) repairExecutionFailure(
	ctx context.Context,
	call fantasy.ToolCall,
	resp fantasy.ToolResponse,
) (runtimeRepairOutcome, bool) {
	initialFailure, ok := runtimeRepairFailure(call.Name, resp)
	if !ok {
		return runtimeRepairOutcome{}, false
	}

	outcome := runtimeRepairOutcome{
		finalCall: call,
		finalResp: resp,
	}

	audit := toolRuntimeRepairAudit{
		FeatureEnabled:   t.cfg.Enabled,
		OriginalToolName: call.Name,
		OriginalInput:    call.Input,
		InitialFailure:   initialFailure,
	}

	if !initialFailure.Retryable {
		audit.Outcome = toolCallRepairOutcomeSkipped
		audit.ExhaustedReason = toolCallRepairSkippedNotRetryable
		recordRuntimeToolCallRepairAudit(t.audits, call.ID, audit)
		return outcome, true
	}
	if !t.cfg.Enabled || t.cfg.MaxAttempts <= 0 {
		audit.Outcome = toolCallRepairOutcomeSkipped
		audit.ExhaustedReason = toolCallRepairSkippedDisabled
		recordRuntimeToolCallRepairAudit(t.audits, call.ID, audit)
		return outcome, true
	}

	currentCallContent := fantasy.ToolCallContent{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Input:      call.Input,
	}
	currentResp := resp
	currentFailure := initialFailure
	seenCalls := map[string]struct{}{
		toolCallRepairSignature(currentCallContent): {},
	}
	availableTools := runtimeToolAvailableToolsFromContext(ctx)
	if len(availableTools) == 0 {
		availableTools = []fantasy.AgentTool{t.inner}
	}
	recentMessages := runtimeToolMessagesFromContext(ctx)

	deterministicRepairedCall, deterministicRepairApplied := deterministicExecutionFailureRepair(currentCallContent, currentFailure)
	if deterministicRepairApplied {
		attemptAudit := toolRuntimeRepairAttempt{
			Attempt:  1,
			ToolName: deterministicRepairedCall.ToolName,
			Input:    deterministicRepairedCall.Input,
		}
		audit.Attempts = append(audit.Attempts, attemptAudit)
		repairedResp, err := t.inner.Run(ctx, fantasy.ToolCall{
			ID:    call.ID,
			Name:  deterministicRepairedCall.ToolName,
			Input: deterministicRepairedCall.Input,
		})
		if err != nil {
			audit.Attempts[len(audit.Attempts)-1].ValidationError = err.Error()
			currentCallContent = deterministicRepairedCall
			currentResp = fantasy.NewTextErrorResponse(err.Error())
			outcome.finalCall = fantasy.ToolCall{
				ID:    call.ID,
				Name:  deterministicRepairedCall.ToolName,
				Input: deterministicRepairedCall.Input,
			}
			outcome.finalResp = currentResp
		} else {
			currentCallContent = deterministicRepairedCall
			currentResp = repairedResp
			outcome.finalCall = fantasy.ToolCall{
				ID:    call.ID,
				Name:  deterministicRepairedCall.ToolName,
				Input: deterministicRepairedCall.Input,
			}
			outcome.finalResp = repairedResp
			seenCalls[toolCallRepairSignature(deterministicRepairedCall)] = struct{}{}
			if !repairedResp.IsError {
				audit.Outcome = toolCallRepairOutcomeSucceeded
				audit.RepairedToolName = deterministicRepairedCall.ToolName
				audit.RepairedInput = deterministicRepairedCall.Input
				recordRuntimeToolCallRepairAudit(t.audits, call.ID, audit)
				return outcome, true
			}

			nextFailure, retryable := runtimeRepairFailure(deterministicRepairedCall.ToolName, repairedResp)
			if retryable {
				audit.Attempts[len(audit.Attempts)-1].ExecutionFailure = &nextFailure
				if !nextFailure.Retryable {
					audit.Outcome = toolCallRepairOutcomeExhausted
					audit.ExhaustedReason = toolCallRepairSkippedNotRetryable
					recordRuntimeToolCallRepairAudit(t.audits, call.ID, audit)
					return outcome, true
				}
				currentFailure = nextFailure
			} else {
				audit.Attempts[len(audit.Attempts)-1].ValidationError = "repaired tool call still failed"
				audit.Outcome = toolCallRepairOutcomeExhausted
				audit.ExhaustedReason = toolCallRepairSkippedNotRetryable
				recordRuntimeToolCallRepairAudit(t.audits, call.ID, audit)
				return outcome, true
			}
		}
	}

	if t.model == nil {
		audit.Outcome = toolCallRepairOutcomeSkipped
		audit.ExhaustedReason = toolCallRepairSkippedModelUnavailable
		recordRuntimeToolCallRepairAudit(t.audits, call.ID, audit)
		return outcome, true
	}

	for attempt := 1; attempt <= t.cfg.MaxAttempts; attempt++ {
		attemptAudit := toolRuntimeRepairAttempt{Attempt: attempt}
		repairedCall, repairErr := repairExecutionFailureCall(
			ctx,
			t.model,
			t.cfg,
			executionFailureRepairOptions{
				OriginalToolCall: currentCallContent,
				ToolResult: toolResponseToToolResultContent(
					currentCallContent.ToolCallID,
					currentCallContent.ToolName,
					currentResp,
				),
				Stdout:         currentFailure.Stdout,
				Stderr:         currentFailure.Stderr,
				ExitCode:       currentFailure.ExitCode,
				Messages:       recentMessages,
				AvailableTools: availableTools,
				SameToolOnly:   true,
			},
		)

		if repairedCall != nil {
			attemptAudit.ToolName = repairedCall.ToolName
			attemptAudit.Input = repairedCall.Input
			if duplicateInvalidAttempt(seenCalls, *repairedCall) {
				attemptAudit.DuplicateRetry = true
				audit.Attempts = append(audit.Attempts, attemptAudit)
				audit.Outcome = toolCallRepairOutcomeExhausted
				audit.ExhaustedReason = toolCallRepairExhaustedDuplicate
				recordRuntimeToolCallRepairAudit(t.audits, call.ID, audit)
				return outcome, true
			}
		}

		if repairErr != nil {
			attemptAudit.ValidationError = repairErr.Error()
			audit.Attempts = append(audit.Attempts, attemptAudit)
			if repairedCall != nil {
				currentCallContent = *repairedCall
			}
			continue
		}
		if repairedCall == nil {
			attemptAudit.ValidationError = "repair response did not return a tool call"
			audit.Attempts = append(audit.Attempts, attemptAudit)
			continue
		}

		repairedResp, err := t.inner.Run(ctx, fantasy.ToolCall{
			ID:    call.ID,
			Name:  repairedCall.ToolName,
			Input: repairedCall.Input,
		})
		if err != nil {
			attemptAudit.ValidationError = err.Error()
			audit.Attempts = append(audit.Attempts, attemptAudit)
			currentCallContent = *repairedCall
			currentResp = fantasy.NewTextErrorResponse(err.Error())
			outcome.finalCall = fantasy.ToolCall{
				ID:    call.ID,
				Name:  repairedCall.ToolName,
				Input: repairedCall.Input,
			}
			outcome.finalResp = currentResp
			continue
		}

		currentCallContent = *repairedCall
		currentResp = repairedResp
		outcome.finalCall = fantasy.ToolCall{
			ID:    call.ID,
			Name:  repairedCall.ToolName,
			Input: repairedCall.Input,
		}
		outcome.finalResp = repairedResp

		if !repairedResp.IsError {
			audit.Attempts = append(audit.Attempts, attemptAudit)
			audit.Outcome = toolCallRepairOutcomeSucceeded
			audit.RepairedToolName = repairedCall.ToolName
			audit.RepairedInput = repairedCall.Input
			recordRuntimeToolCallRepairAudit(t.audits, call.ID, audit)
			return outcome, true
		}

		nextFailure, retryable := runtimeRepairFailure(repairedCall.ToolName, repairedResp)
		if retryable {
			attemptAudit.ExecutionFailure = &nextFailure
		} else {
			attemptAudit.ValidationError = "repaired tool call still failed"
		}
		audit.Attempts = append(audit.Attempts, attemptAudit)
		if !retryable || !nextFailure.Retryable {
			audit.Outcome = toolCallRepairOutcomeExhausted
			audit.ExhaustedReason = toolCallRepairSkippedNotRetryable
			recordRuntimeToolCallRepairAudit(t.audits, call.ID, audit)
			return outcome, true
		}
		currentFailure = nextFailure
	}

	audit.Outcome = toolCallRepairOutcomeExhausted
	audit.ExhaustedReason = toolCallRepairExhaustedMaxAttempts
	recordRuntimeToolCallRepairAudit(t.audits, call.ID, audit)
	return outcome, true
}

func deterministicExecutionFailureRepair(
	call fantasy.ToolCallContent,
	failure toolExecutionFailureDetails,
) (fantasy.ToolCallContent, bool) {
	if !stringsEqualFold(call.ToolName, tools.BashToolName) {
		return fantasy.ToolCallContent{}, false
	}
	if failure.FailureKind != tools.BashFailureKindWindowsShellMismatch {
		return fantasy.ToolCallContent{}, false
	}

	var params tools.BashParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return fantasy.ToolCallContent{}, false
	}

	repairedCommand, changed := stripUnsupportedWindowsOutputPipe(params.Command)
	if !changed {
		return fantasy.ToolCallContent{}, false
	}
	params.Command = repairedCommand
	encoded, err := json.Marshal(params)
	if err != nil {
		return fantasy.ToolCallContent{}, false
	}

	repaired := call
	repaired.Input = string(encoded)
	return repaired, true
}

func stripUnsupportedWindowsOutputPipe(command string) (string, bool) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return command, false
	}

	lower := strings.ToLower(trimmed)
	pipePatterns := []string{
		"| select-object -first",
		"| head -",
		"| tail -",
	}
	for _, pattern := range pipePatterns {
		idx := strings.Index(lower, pattern)
		if idx < 0 {
			continue
		}
		repaired := strings.TrimSpace(trimmed[:idx])
		if repaired == "" || repaired == trimmed {
			return command, false
		}
		return repaired, true
	}
	return command, false
}

func (t *runtimeRepairTool) runPostToolHook(
	ctx context.Context,
	originalCall fantasy.ToolCall,
	finalCall fantasy.ToolCall,
	resp fantasy.ToolResponse,
) {
	if t.postToolHooks == nil {
		return
	}

	metadataJSON := resp.Metadata
	if t.audits != nil && originalCall.ID != "" {
		if audit, ok := t.audits.Get(originalCall.ID); ok {
			metadataJSON = mergeToolRepairMetadata(metadataJSON, audit)
		}
	}

	var (
		exitCode    *int
		retryable   *bool
		failureKind string
		repairJSON  string
	)
	if details, ok := runtimeRepairFailure(finalCall.Name, resp); ok {
		exitCode = &details.ExitCode
		retryable = &details.Retryable
		failureKind = details.FailureKind
	}
	if t.audits != nil && originalCall.ID != "" {
		if audit, ok := t.audits.Get(originalCall.ID); ok {
			if data, err := json.Marshal(audit); err == nil {
				repairJSON = string(data)
			}
		}
	}

	result, err := t.postToolHooks.RunEvent(ctx, hooks.EventPostToolUse, hooks.EventInput{
		SessionID:     tools.GetSessionFromContext(ctx),
		ToolName:      finalCall.Name,
		ToolInputJSON: finalCall.Input,
		ToolCallID:    originalCall.ID,
		ToolResultJSON: toolResponseJSON(
			originalCall.ID,
			finalCall.Name,
			resp,
			metadataJSON,
		),
		MetadataJSON: metadataJSON,
		ExitCode:     exitCode,
		Retryable:    retryable,
		FailureKind:  failureKind,
		RepairJSON:   repairJSON,
	})
	if err != nil {
		slog.Warn("Post-tool hook execution error", "tool", finalCall.Name, "error", err)
		return
	}
	if result.Decision != hooks.DecisionNone || result.Halt || result.Reason != "" {
		slog.Debug("Post-tool hook completed with decision",
			"tool", finalCall.Name,
			"decision", result.Decision.String(),
			"halt", result.Halt,
			"reason", result.Reason,
		)
	}
}

func (t *runtimeRepairTool) applyRepeatedLoopGuard(
	ctx context.Context,
	originalCall fantasy.ToolCall,
	finalCall fantasy.ToolCall,
	resp fantasy.ToolResponse,
) fantasy.ToolResponse {
	sessionID := tools.GetSessionFromContext(ctx)
	if t.loopTracker == nil || sessionID == "" {
		return resp
	}

	var audit *toolCallRepairAudit
	if t.audits != nil && originalCall.ID != "" {
		if existing, ok := t.audits.Get(originalCall.ID); ok {
			copyAudit := existing
			audit = &copyAudit
		}
	}

	observation := classifyToolCycle(finalCall, resp, audit, sessionID)
	handoff := observeToolCycle(t.loopTracker, ctx, observation)
	if handoff == nil {
		return resp
	}

	slog.Warn("Repeated tool loop detected",
		"session_id", handoff.SessionID,
		"tool", handoff.ToolName,
		"classification", handoff.Classification,
		"streak_count", handoff.StreakCount,
		"suggested_action", handoff.SuggestedAction,
	)

	loopResp := fantasy.NewTextErrorResponse(buildSupervisorPauseMessage(*handoff))
	loopResp.StopTurn = true
	loopResp.Metadata = mergeToolLoopMetadata(resp.Metadata, *handoff)
	return loopResp
}

func runtimeRepairFailure(toolName string, resp fantasy.ToolResponse) (toolExecutionFailureDetails, bool) {
	if !resp.IsError || !stringsEqualFold(toolName, tools.BashToolName) || resp.Metadata == "" {
		return toolExecutionFailureDetails{}, false
	}

	var metadata tools.BashResponseMetadata
	if err := json.Unmarshal([]byte(resp.Metadata), &metadata); err != nil {
		return toolExecutionFailureDetails{}, false
	}
	if metadata.ExitCode == 0 && metadata.FailureKind == "" && !metadata.Retryable {
		return toolExecutionFailureDetails{}, false
	}
	return runtimeFailureFromBashMetadata(metadata, resp.Content), true
}

func toolResponseToToolResultContent(toolCallID string, toolName string, resp fantasy.ToolResponse) fantasy.ToolResultContent {
	result := fantasy.ToolResultContent{
		ToolCallID:     toolCallID,
		ToolName:       toolName,
		ClientMetadata: resp.Metadata,
	}

	switch {
	case resp.IsError:
		result.Result = fantasy.ToolResultOutputContentError{
			Error: errors.New(resp.Content),
		}
	case resp.Type == "image" || resp.Type == "media":
		result.Result = fantasy.ToolResultOutputContentMedia{
			Text:      resp.Content,
			Data:      string(resp.Data),
			MediaType: resp.MediaType,
		}
	default:
		result.Result = fantasy.ToolResultOutputContentText{
			Text: resp.Content,
		}
	}

	return result
}

func toolResponseJSON(toolCallID string, toolName string, resp fantasy.ToolResponse, metadataJSON string) string {
	payload := map[string]any{
		"tool_call_id": toolCallID,
		"tool_name":    toolName,
		"type":         resp.Type,
		"content":      resp.Content,
		"is_error":     resp.IsError,
		"stop_turn":    resp.StopTurn,
	}
	if resp.MediaType != "" {
		payload["media_type"] = resp.MediaType
	}
	if metadataJSON != "" {
		var metadata any
		if json.Unmarshal([]byte(metadataJSON), &metadata) == nil {
			payload["metadata"] = metadata
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(data)
}

func stringsEqualFold(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func buildSupervisorPauseMessage(handoff toolLoopHandoff) string {
	message := fmt.Sprintf(
		"Crush detected a repeated tool loop and paused autonomy.\n\nTool: %s\nClassification: %s\nStreak: %d\nSuggested action: %s\nReason: %s",
		handoff.ToolName,
		handoff.Classification,
		handoff.StreakCount,
		handoff.SuggestedAction,
		handoff.Reason,
	)
	if handoff.BlockedTool {
		message += "\nBlocked tool: true"
	}
	if handoff.LoopKind != "" {
		message += "\nLoop kind: " + string(handoff.LoopKind)
	}
	return message
}
