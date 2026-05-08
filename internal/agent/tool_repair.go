package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"

	"charm.land/fantasy"
	fantasyschema "charm.land/fantasy/schema"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/csync"
)

const (
	toolCallRepairEnabledEnv     = "CRUSH_ENABLE_TOOL_CALL_REPAIR"
	toolCallRepairMaxAttemptsEnv = "CRUSH_TOOL_CALL_REPAIR_MAX_ATTEMPTS"

	defaultToolCallRepairMaxAttempts = 2
	defaultToolCallRepairMaxOutput   = 256

	toolCallRepairOutcomeSucceeded = "succeeded"
	toolCallRepairOutcomeExhausted = "exhausted"
	toolCallRepairOutcomeSkipped   = "skipped"

	toolCallRepairExhaustedNoTool         = "target_tool_not_found"
	toolCallRepairExhaustedDuplicate      = "duplicate_invalid_retry"
	toolCallRepairExhaustedMaxAttempts    = "repair_attempts_exhausted"
	toolCallRepairExhaustedSameToolOnly   = "same_tool_constraint_violation"
	toolCallRepairSkippedDisabled         = "repair_disabled"
	toolCallRepairSkippedModelUnavailable = "repair_model_unavailable"
	toolCallRepairSkippedNotRetryable     = "not_retryable"

	toolFailureKindAliasRewritten      = "alias_rewritten"
	toolFailureKindInvalidToolName     = "invalid_tool_name"
	toolFailureKindInvalidSchema       = "invalid_schema"
	toolFailureKindRetryableRuntime    = "retryable_runtime_failure"
	toolFailureKindLowSignalLoop       = "low_signal_loop"
	toolFailureKindRepeatedInvalidTool = "repeated_invalid_tool_loop"
	toolFailureKindRepeatedSchemaLoop  = "repeated_invalid_schema_loop"
	toolFailureKindRepeatedRuntimeLoop = "repeated_retryable_runtime_loop"
)

type toolCallRepairConfig struct {
	Enabled         bool
	MaxAttempts     int
	MaxOutputTokens int64
}

type toolCallRepairAttempt struct {
	Attempt          int    `json:"attempt"`
	ToolName         string `json:"tool_name,omitempty"`
	Input            string `json:"input,omitempty"`
	ValidationError  string `json:"validation_error,omitempty"`
	DuplicateInvalid bool   `json:"duplicate_invalid,omitempty"`
}

type toolExecutionFailureDetails struct {
	Command          string `json:"command,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	ExitCode         int    `json:"exit_code,omitempty"`
	Stdout           string `json:"stdout,omitempty"`
	Stderr           string `json:"stderr,omitempty"`
	Retryable        bool   `json:"retryable"`
	FailureKind      string `json:"failure_kind,omitempty"`
	Content          string `json:"content,omitempty"`
}

type toolRuntimeRepairAttempt struct {
	Attempt          int                          `json:"attempt"`
	ToolName         string                       `json:"tool_name,omitempty"`
	Input            string                       `json:"input,omitempty"`
	ValidationError  string                       `json:"validation_error,omitempty"`
	ExecutionFailure *toolExecutionFailureDetails `json:"execution_failure,omitempty"`
	DuplicateRetry   bool                         `json:"duplicate_retry,omitempty"`
}

type toolRuntimeRepairAudit struct {
	FeatureEnabled bool `json:"feature_enabled"`

	OriginalToolName string `json:"original_tool_name"`
	OriginalInput    string `json:"original_input"`

	InitialFailure toolExecutionFailureDetails `json:"initial_failure"`

	Attempts     []toolRuntimeRepairAttempt `json:"attempts,omitempty"`
	AttemptCount int                        `json:"attempt_count"`
	Outcome      string                     `json:"outcome,omitempty"`

	ExhaustedReason  string `json:"exhausted_reason,omitempty"`
	RepairedToolName string `json:"repaired_tool_name,omitempty"`
	RepairedInput    string `json:"repaired_input,omitempty"`
}

type toolCallRepairAudit struct {
	FeatureEnabled         bool                    `json:"feature_enabled"`
	RequestedToolName      string                  `json:"requested_tool_name,omitempty"`
	OriginalToolName       string                  `json:"original_tool_name"`
	OriginalInput          string                  `json:"original_input"`
	CanonicalToolName      string                  `json:"canonical_tool_name,omitempty"`
	ResolvedToolName       string                  `json:"resolved_tool_name,omitempty"`
	AliasApplied           bool                    `json:"alias_applied,omitempty"`
	AliasSource            string                  `json:"alias_source,omitempty"`
	Candidates             []string                `json:"candidates,omitempty"`
	ExposedTools           []string                `json:"exposed_tools,omitempty"`
	FailureKind            string                  `json:"failure_kind,omitempty"`
	Retryable              bool                    `json:"retryable,omitempty"`
	InitialValidationError string                  `json:"initial_validation_error,omitempty"`
	RequiredFields         []string                `json:"required_fields,omitempty"`
	MissingFields          []string                `json:"missing_fields,omitempty"`
	Attempts               []toolCallRepairAttempt `json:"attempts,omitempty"`
	AttemptCount           int                     `json:"attempt_count"`
	RepairAttempted        bool                    `json:"repair_attempted,omitempty"`
	RepairSucceeded        bool                    `json:"repair_succeeded,omitempty"`
	RepairExhausted        bool                    `json:"repair_exhausted,omitempty"`
	Outcome                string                  `json:"outcome,omitempty"`
	ExhaustedReason        string                  `json:"exhausted_reason,omitempty"`
	RepairedToolName       string                  `json:"repaired_tool_name,omitempty"`
	RepairedInput          string                  `json:"repaired_input,omitempty"`
	Runtime                *toolRuntimeRepairAudit `json:"runtime,omitempty"`
}

type executionFailureRepairOptions struct {
	OriginalToolCall fantasy.ToolCallContent
	ToolResult       fantasy.ToolResultContent
	Stdout           string
	Stderr           string
	ExitCode         int
	Messages         []fantasy.Message
	AvailableTools   []fantasy.AgentTool
	SameToolOnly     bool
}

func newToolCallRepairer(
	model fantasy.LanguageModel,
	audits *csync.Map[string, toolCallRepairAudit],
) fantasy.RepairToolCallFunction {
	cfg := toolCallRepairConfigFromEnv()
	return func(ctx context.Context, options fantasy.ToolCallRepairOptions) (*fantasy.ToolCallContent, error) {
		return repairToolCall(ctx, model, cfg, audits, options)
	}
}

func toolCallRepairConfigFromEnv() toolCallRepairConfig {
	cfg := toolCallRepairConfig{
		Enabled:         true,
		MaxAttempts:     defaultToolCallRepairMaxAttempts,
		MaxOutputTokens: defaultToolCallRepairMaxOutput,
	}

	if str, ok := os.LookupEnv(toolCallRepairEnabledEnv); ok {
		if enabled, err := strconv.ParseBool(str); err == nil {
			cfg.Enabled = enabled
		}
	}
	if str, ok := os.LookupEnv(toolCallRepairMaxAttemptsEnv); ok {
		if attempts, err := strconv.Atoi(str); err == nil {
			if attempts < 0 {
				attempts = 0
			}
			if attempts > defaultToolCallRepairMaxAttempts {
				attempts = defaultToolCallRepairMaxAttempts
			}
			cfg.MaxAttempts = attempts
		}
	}
	if cfg.MaxAttempts == 0 {
		cfg.Enabled = false
	}

	return cfg
}

func repairToolCall(
	ctx context.Context,
	model fantasy.LanguageModel,
	cfg toolCallRepairConfig,
	audits *csync.Map[string, toolCallRepairAudit],
	options fantasy.ToolCallRepairOptions,
) (*fantasy.ToolCallContent, error) {
	current := options.OriginalToolCall
	audit := toolCallRepairAudit{
		FeatureEnabled:         cfg.Enabled,
		RequestedToolName:      options.OriginalToolCall.ToolName,
		OriginalToolName:       options.OriginalToolCall.ToolName,
		OriginalInput:          options.OriginalToolCall.Input,
		InitialValidationError: errorString(options.ValidationError),
	}

	targetTool, resolution, ok := resolveRepairTargetTool(current.ToolName, options.AvailableTools)
	audit.CanonicalToolName = resolution.CanonicalToolName
	audit.ResolvedToolName = resolution.CanonicalToolName
	audit.AliasApplied = resolution.AliasApplied
	audit.AliasSource = resolution.AliasSource
	audit.Candidates = resolution.Candidates
	audit.ExposedTools = resolution.ExposedTools
	if !ok {
		audit.FailureKind = toolFailureKindInvalidToolName
		recordValidationToolCallRepairAudit(audits, current.ToolCallID, &audit, toolCallRepairOutcomeExhausted, toolCallRepairExhaustedNoTool)
		return nil, nil
	}

	targetToolName := resolution.CanonicalToolName
	if targetToolName != "" && targetToolName != current.ToolName {
		current.ToolName = targetToolName
	}

	requiredFields, missingFields := requiredAndMissingFields(current.Input, targetTool)
	audit.RequiredFields = requiredFields
	audit.MissingFields = missingFields

	currentValidationErr := validateToolCallAgainstSchema(current, targetTool)
	if currentValidationErr == nil {
		if audit.AliasApplied {
			audit.FailureKind = toolFailureKindAliasRewritten
		}
		audit.RepairedToolName = current.ToolName
		audit.RepairedInput = current.Input
		recordValidationToolCallRepairAudit(audits, current.ToolCallID, &audit, toolCallRepairOutcomeSucceeded, "")
		return &current, nil
	}
	if audit.InitialValidationError == "" {
		audit.InitialValidationError = currentValidationErr.Error()
	}
	audit.FailureKind = toolFailureKindInvalidSchema

	if !cfg.Enabled || cfg.MaxAttempts <= 0 {
		recordValidationToolCallRepairAudit(audits, current.ToolCallID, &audit, toolCallRepairOutcomeSkipped, toolCallRepairSkippedDisabled)
		return nil, nil
	}
	if model == nil {
		recordValidationToolCallRepairAudit(audits, current.ToolCallID, &audit, toolCallRepairOutcomeSkipped, toolCallRepairSkippedModelUnavailable)
		return nil, nil
	}

	seenInvalid := map[string]struct{}{
		toolCallRepairSignature(current): {},
	}

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		repaired, attemptErr := requestToolCallRepair(
			ctx,
			model,
			cfg,
			targetTool,
			targetToolName,
			current,
			currentValidationErr,
			options.Messages,
			requiredFields,
			missingFields,
		)
		if repaired == nil {
			audit.Attempts = append(audit.Attempts, toolCallRepairAttempt{
				Attempt:         attempt,
				ValidationError: errorString(attemptErr),
			})
			currentValidationErr = attemptErr
			continue
		}

		repaired.ToolCallID = options.OriginalToolCall.ToolCallID
		repaired.ProviderExecuted = false

		attemptAudit := toolCallRepairAttempt{
			Attempt:  attempt,
			ToolName: repaired.ToolName,
			Input:    repaired.Input,
		}

		resolvedName, resolved := resolveToolCallName(repaired.ToolName, options.AvailableTools)
		if !resolved || resolvedName != targetToolName {
			attemptAudit.ValidationError = fmt.Sprintf("repair must return only tool %s", targetToolName)
			audit.Attempts = append(audit.Attempts, attemptAudit)
			currentValidationErr = fmt.Errorf("%s", attemptAudit.ValidationError)
			if duplicateInvalidAttempt(seenInvalid, *repaired) {
				attemptAudit.DuplicateInvalid = true
				audit.Attempts[len(audit.Attempts)-1] = attemptAudit
				recordValidationToolCallRepairAudit(audits, current.ToolCallID, &audit, toolCallRepairOutcomeExhausted, toolCallRepairExhaustedDuplicate)
				return nil, nil
			}
			current = *repaired
			continue
		}

		repaired.ToolName = targetToolName
		validationErr := validateToolCallAgainstSchema(*repaired, targetTool)
		if validationErr == nil {
			audit.Attempts = append(audit.Attempts, attemptAudit)
			audit.RepairedToolName = repaired.ToolName
			audit.RepairedInput = repaired.Input
			recordValidationToolCallRepairAudit(audits, current.ToolCallID, &audit, toolCallRepairOutcomeSucceeded, "")
			return repaired, nil
		}

		attemptAudit.ValidationError = validationErr.Error()
		if duplicateInvalidAttempt(seenInvalid, *repaired) {
			attemptAudit.DuplicateInvalid = true
			audit.Attempts = append(audit.Attempts, attemptAudit)
			recordValidationToolCallRepairAudit(audits, current.ToolCallID, &audit, toolCallRepairOutcomeExhausted, toolCallRepairExhaustedDuplicate)
			return nil, nil
		}

		audit.Attempts = append(audit.Attempts, attemptAudit)
		current = *repaired
		currentValidationErr = validationErr
	}

	recordValidationToolCallRepairAudit(audits, current.ToolCallID, &audit, toolCallRepairOutcomeExhausted, toolCallRepairExhaustedMaxAttempts)
	return nil, nil
}

func repairExecutionFailureCall(
	ctx context.Context,
	model fantasy.LanguageModel,
	cfg toolCallRepairConfig,
	options executionFailureRepairOptions,
) (*fantasy.ToolCallContent, error) {
	targetTool, resolution, ok := resolveRepairTargetTool(options.OriginalToolCall.ToolName, options.AvailableTools)
	if !ok {
		return nil, fmt.Errorf("%s", toolCallRepairExhaustedNoTool)
	}
	targetToolName := resolution.CanonicalToolName

	repaired, err := requestExecutionFailureRepair(
		ctx,
		model,
		cfg,
		targetTool,
		targetToolName,
		options,
	)
	if repaired == nil || err != nil {
		return repaired, err
	}

	repaired.ToolCallID = options.OriginalToolCall.ToolCallID
	repaired.ProviderExecuted = false

	resolvedName, resolved := resolveToolCallName(repaired.ToolName, options.AvailableTools)
	if !resolved || (options.SameToolOnly && resolvedName != targetToolName) {
		return repaired, fmt.Errorf("%s", toolCallRepairExhaustedSameToolOnly)
	}

	repaired.ToolName = targetToolName
	if err := validateToolCallAgainstSchema(*repaired, targetTool); err != nil {
		return repaired, err
	}

	return repaired, nil
}

func resolveRepairTargetTool(name string, availableTools []fantasy.AgentTool) (fantasy.AgentTool, toolNameResolution, bool) {
	resolution := resolveToolName(name, availableTools)
	if resolution.CanonicalToolName == "" {
		return nil, resolution, false
	}
	for _, tool := range availableTools {
		if tool.Info().Name == resolution.CanonicalToolName {
			return tool, resolution, true
		}
	}
	return nil, resolution, false
}

func requiredAndMissingFields(input string, tool fantasy.AgentTool) ([]string, []string) {
	required := append([]string(nil), tool.Info().Required...)
	if len(required) == 0 {
		return nil, nil
	}
	slices.Sort(required)

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		return required, nil
	}

	missing := make([]string, 0, len(required))
	for _, field := range required {
		if _, ok := parsed[field]; !ok {
			missing = append(missing, field)
		}
	}
	return required, missing
}

func requestToolCallRepair(
	ctx context.Context,
	model fantasy.LanguageModel,
	cfg toolCallRepairConfig,
	targetTool fantasy.AgentTool,
	targetToolName string,
	current fantasy.ToolCallContent,
	validationErr error,
	messages []fantasy.Message,
	requiredFields []string,
	missingFields []string,
) (*fantasy.ToolCallContent, error) {
	prompt := make([]fantasy.Message, 0, len(messages)+2)
	prompt = append(prompt, fantasy.NewSystemMessage(
		"You repair invalid tool calls. Return exactly one corrected call for the same tool and no text.",
	))
	prompt = append(prompt, messages...)
	prompt = append(prompt, fantasy.NewUserMessage(buildToolCallRepairPrompt(
		targetToolName,
		current.Input,
		validationErr,
		requiredFields,
		missingFields,
	)))

	toolChoice := fantasy.ToolChoiceRequired
	temperature := 0.0

	resp, err := model.Generate(ctx, fantasy.Call{
		Prompt:          prompt,
		Tools:           []fantasy.Tool{repairFunctionTool(targetTool)},
		ToolChoice:      &toolChoice,
		MaxOutputTokens: &cfg.MaxOutputTokens,
		Temperature:     &temperature,
		UserAgent:       userAgent,
	})
	if err != nil {
		return nil, fmt.Errorf("repair model call failed: %w", err)
	}

	toolCalls := extractRepairToolCalls(resp.Content)
	switch len(toolCalls) {
	case 0:
		return nil, fmt.Errorf("repair response did not return a tool call")
	case 1:
		return &toolCalls[0], nil
	default:
		return nil, fmt.Errorf("repair response returned %d tool calls", len(toolCalls))
	}
}

func requestExecutionFailureRepair(
	ctx context.Context,
	model fantasy.LanguageModel,
	cfg toolCallRepairConfig,
	targetTool fantasy.AgentTool,
	targetToolName string,
	options executionFailureRepairOptions,
) (*fantasy.ToolCallContent, error) {
	prompt := make([]fantasy.Message, 0, len(options.Messages)+2)
	prompt = append(prompt, fantasy.NewSystemMessage(
		"You repair failed tool executions. Return exactly one corrected call for the same tool and no text.",
	))
	prompt = append(prompt, trimRecentMessages(options.Messages, 10)...)
	prompt = append(prompt, fantasy.NewUserMessage(buildExecutionFailureRepairPrompt(
		targetToolName,
		options.OriginalToolCall.Input,
		options.ToolResult,
		options.Stdout,
		options.Stderr,
		options.ExitCode,
		options.SameToolOnly,
	)))

	toolChoice := fantasy.ToolChoiceRequired
	temperature := 0.0

	resp, err := model.Generate(ctx, fantasy.Call{
		Prompt:          prompt,
		Tools:           []fantasy.Tool{repairFunctionTool(targetTool)},
		ToolChoice:      &toolChoice,
		MaxOutputTokens: &cfg.MaxOutputTokens,
		Temperature:     &temperature,
		UserAgent:       userAgent,
	})
	if err != nil {
		return nil, fmt.Errorf("repair model call failed: %w", err)
	}

	toolCalls := extractRepairToolCalls(resp.Content)
	switch len(toolCalls) {
	case 0:
		return nil, fmt.Errorf("repair response did not return a tool call")
	case 1:
		return &toolCalls[0], nil
	default:
		return nil, fmt.Errorf("repair response returned %d tool calls", len(toolCalls))
	}
}

func extractRepairToolCalls(content fantasy.ResponseContent) []fantasy.ToolCallContent {
	toolCalls := make([]fantasy.ToolCallContent, 0, 1)
	for _, part := range content {
		toolCall, ok := fantasy.AsContentType[fantasy.ToolCallContent](part)
		if !ok || toolCall.ProviderExecuted {
			continue
		}
		toolCalls = append(toolCalls, toolCall)
	}
	return toolCalls
}

func buildToolCallRepairPrompt(
	toolName string,
	originalInput string,
	validationErr error,
	requiredFields []string,
	missingFields []string,
) string {
	requiredJSON, _ := json.Marshal(requiredFields)
	missingJSON, _ := json.Marshal(missingFields)

	return strings.Join([]string{
		"repair_tool_call",
		"tool_name=" + toolName,
		"validation_error=" + errorString(validationErr),
		"required_fields=" + string(requiredJSON),
		"missing_fields=" + string(missingJSON),
		"original_input=" + originalInput,
		"instruction=Return only a corrected call for the same tool.",
	}, "\n")
}

func buildExecutionFailureRepairPrompt(
	toolName string,
	originalInput string,
	toolResult fantasy.ToolResultContent,
	stdout string,
	stderr string,
	exitCode int,
	sameToolOnly bool,
) string {
	constraint := "same_tool_only=false"
	if sameToolOnly {
		constraint = "same_tool_only=true"
	}

	resultType := string(toolResult.Result.GetType())

	lines := []string{
		"repair_tool_execution",
		"tool_name=" + toolName,
		constraint,
		"tool_result_type=" + resultType,
		fmt.Sprintf("exit_code=%d", exitCode),
		"stdout=" + stdout,
		"stderr=" + stderr,
		"original_input=" + originalInput,
		"instruction=Return only a corrected call for the same tool. Prefer fixing quoting, working_dir, and Windows path handling.",
	}

	if errResult, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](toolResult.Result); ok {
		lines = append(lines, "tool_error="+errResult.Error.Error())
	} else if textResult, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](toolResult.Result); ok {
		lines = append(lines, "tool_output="+textResult.Text)
	}

	return strings.Join(lines, "\n")
}

func repairFunctionTool(tool fantasy.AgentTool) fantasy.FunctionTool {
	info := tool.Info()
	inputSchema := map[string]any{
		"type":       "object",
		"properties": info.Parameters,
		"required":   info.Required,
	}
	fantasyschema.Normalize(inputSchema)
	return fantasy.FunctionTool{
		Name:            info.Name,
		Description:     info.Description,
		InputSchema:     inputSchema,
		ProviderOptions: tool.ProviderOptions(),
	}
}

func validateToolCallAgainstSchema(toolCall fantasy.ToolCallContent, tool fantasy.AgentTool) error {
	var obj any
	if err := json.Unmarshal([]byte(toolCall.Input), &obj); err != nil {
		return fmt.Errorf("invalid JSON input: %w", err)
	}

	inputSchema, err := repairSchemaForTool(tool)
	if err != nil {
		return err
	}
	if err := fantasyschema.ValidateAgainstSchema(obj, inputSchema); err != nil {
		return err
	}
	return nil
}

func repairSchemaForTool(tool fantasy.AgentTool) (fantasyschema.Schema, error) {
	info := tool.Info()
	rawSchema := map[string]any{
		"type":       "object",
		"properties": info.Parameters,
		"required":   info.Required,
	}
	fantasyschema.Normalize(rawSchema)

	data, err := json.Marshal(rawSchema)
	if err != nil {
		return fantasyschema.Schema{}, fmt.Errorf("failed to marshal tool schema: %w", err)
	}

	var schema fantasyschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		return fantasyschema.Schema{}, fmt.Errorf("failed to decode tool schema: %w", err)
	}
	return schema, nil
}

func trimRecentMessages(messages []fantasy.Message, limit int) []fantasy.Message {
	if limit <= 0 || len(messages) <= limit {
		return append([]fantasy.Message(nil), messages...)
	}
	return append([]fantasy.Message(nil), messages[len(messages)-limit:]...)
}

func duplicateInvalidAttempt(seen map[string]struct{}, toolCall fantasy.ToolCallContent) bool {
	signature := toolCallRepairSignature(toolCall)
	if _, ok := seen[signature]; ok {
		return true
	}
	seen[signature] = struct{}{}
	return false
}

func toolCallRepairSignature(toolCall fantasy.ToolCallContent) string {
	return strings.ToLower(strings.TrimSpace(toolCall.ToolName)) + "\n" + normalizeToolCallInput(toolCall.Input)
}

func normalizeToolCallInput(input string) string {
	var obj any
	if err := json.Unmarshal([]byte(input), &obj); err != nil {
		return strings.TrimSpace(input)
	}

	normalized, err := json.Marshal(obj)
	if err != nil {
		return strings.TrimSpace(input)
	}
	return string(normalized)
}

func runtimeFailureFromBashMetadata(metadata tools.BashResponseMetadata, content string) toolExecutionFailureDetails {
	return toolExecutionFailureDetails{
		Command:          metadata.Command,
		WorkingDirectory: metadata.WorkingDirectory,
		ExitCode:         metadata.ExitCode,
		Stdout:           metadata.Stdout,
		Stderr:           metadata.Stderr,
		Retryable:        metadata.Retryable,
		FailureKind:      metadata.FailureKind,
		Content:          content,
	}
}

func recordValidationToolCallRepairAudit(
	audits *csync.Map[string, toolCallRepairAudit],
	toolCallID string,
	audit *toolCallRepairAudit,
	outcome string,
	exhaustedReason string,
) {
	if audits == nil || audit == nil || toolCallID == "" {
		return
	}

	current, ok := audits.Get(toolCallID)
	if !ok {
		current = toolCallRepairAudit{}
	}

	audit.Runtime = current.Runtime
	audit.Outcome = outcome
	audit.ExhaustedReason = exhaustedReason
	audit.AttemptCount = len(audit.Attempts)
	audit.RepairAttempted = audit.AttemptCount > 0
	audit.RepairSucceeded = outcome == toolCallRepairOutcomeSucceeded
	audit.RepairExhausted = outcome == toolCallRepairOutcomeExhausted
	current = *audit
	audits.Set(toolCallID, current)

	logAttrs := []any{
		"tool_call_id", toolCallID,
		"requested_tool", audit.RequestedToolName,
		"original_tool", audit.OriginalToolName,
		"canonical_tool", audit.CanonicalToolName,
		"resolved_tool", audit.ResolvedToolName,
		"alias_applied", audit.AliasApplied,
		"alias_source", audit.AliasSource,
		"failure_kind", audit.FailureKind,
		"outcome", outcome,
		"attempt_count", audit.AttemptCount,
	}
	if exhaustedReason != "" {
		logAttrs = append(logAttrs, "reason", exhaustedReason)
	}
	if audit.RepairedToolName != "" {
		logAttrs = append(logAttrs, "repaired_tool", audit.RepairedToolName)
	}

	if outcome == toolCallRepairOutcomeSucceeded {
		slog.Info("Tool call repaired before execution", logAttrs...)
		return
	}
	if outcome == toolCallRepairOutcomeSkipped {
		slog.Warn("Tool call validation failed without repair", logAttrs...)
		return
	}
	slog.Warn("Tool call repair exhausted", logAttrs...)
}

func recordRuntimeToolCallRepairAudit(
	audits *csync.Map[string, toolCallRepairAudit],
	toolCallID string,
	audit toolRuntimeRepairAudit,
) {
	if audits == nil || toolCallID == "" {
		return
	}

	audit.AttemptCount = len(audit.Attempts)

	current, ok := audits.Get(toolCallID)
	if !ok {
		current = toolCallRepairAudit{}
	}
	current.Runtime = &audit
	audits.Set(toolCallID, current)

	logAttrs := []any{
		"tool_call_id", toolCallID,
		"tool", audit.OriginalToolName,
		"failure_kind", audit.InitialFailure.FailureKind,
		"retryable", audit.InitialFailure.Retryable,
		"outcome", audit.Outcome,
		"attempt_count", audit.AttemptCount,
	}
	if audit.ExhaustedReason != "" {
		logAttrs = append(logAttrs, "reason", audit.ExhaustedReason)
	}
	if audit.RepairedToolName != "" {
		logAttrs = append(logAttrs, "repaired_tool", audit.RepairedToolName)
	}

	switch audit.Outcome {
	case toolCallRepairOutcomeSucceeded:
		slog.Info("Runtime tool failure repaired", logAttrs...)
	case toolCallRepairOutcomeSkipped:
		slog.Warn("Runtime tool failure observed without repair", logAttrs...)
	default:
		slog.Warn("Runtime tool failure repair exhausted", logAttrs...)
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
