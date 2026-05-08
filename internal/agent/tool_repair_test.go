package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"charm.land/fantasy"
	toolpkg "github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/stretchr/testify/require"
)

type repairTestModel struct {
	generateFunc func(ctx context.Context, call fantasy.Call) (*fantasy.Response, error)
}

func (m *repairTestModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	if m.generateFunc != nil {
		return m.generateFunc(ctx, call)
	}
	return nil, errors.New("generate not implemented")
}

func (m *repairTestModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	return nil, errors.New("stream not implemented")
}

func (m *repairTestModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, errors.New("generate object not implemented")
}

func (m *repairTestModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, errors.New("stream object not implemented")
}

func (m *repairTestModel) Provider() string { return "mock-provider" }
func (m *repairTestModel) Model() string    { return "mock-model" }

type writeSpyTool struct {
	callCount int
	lastCall  fantasy.ToolCall
}

func (t *writeSpyTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{
		Name:        "write",
		Description: "Create or overwrite a file with given content.",
		Parameters: map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The path to the file to write",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "The content to write to the file",
			},
		},
		Required: []string{"file_path", "content"},
	}
}

func (t *writeSpyTool) Run(_ context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	t.callCount++
	t.lastCall = call
	return fantasy.WithResponseMetadata(
		fantasy.NewTextResponse("written"),
		map[string]string{"source": "write-spy"},
	), nil
}

func (t *writeSpyTool) ProviderOptions() fantasy.ProviderOptions   { return nil }
func (t *writeSpyTool) SetProviderOptions(fantasy.ProviderOptions) {}

type bashRuntimeSpyTool struct {
	callCount int
	lastCall  fantasy.ToolCall
	runFunc   func(call fantasy.ToolCall) fantasy.ToolResponse
}

func (t *bashRuntimeSpyTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{
		Name:        toolpkg.BashToolName,
		Description: "Execute shell commands.",
		Parameters: map[string]any{
			"description": map[string]any{"type": "string"},
			"command":     map[string]any{"type": "string"},
			"working_dir": map[string]any{"type": "string"},
		},
		Required: []string{"description", "command"},
	}
}

func (t *bashRuntimeSpyTool) Run(_ context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	t.callCount++
	t.lastCall = call
	if t.runFunc != nil {
		return t.runFunc(call), nil
	}
	return fantasy.NewTextResponse("ok"), nil
}

func (t *bashRuntimeSpyTool) ProviderOptions() fantasy.ProviderOptions   { return nil }
func (t *bashRuntimeSpyTool) SetProviderOptions(fantasy.ProviderOptions) {}

type crushInfoSpyTool struct {
	callCount int
	lastCall  fantasy.ToolCall
}

func (t *crushInfoSpyTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{
		Name:        toolpkg.CrushInfoToolName,
		Description: "Return Crush runtime information.",
		Parameters:  map[string]any{},
	}
}

func (t *crushInfoSpyTool) Run(_ context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	t.callCount++
	t.lastCall = call
	return fantasy.NewTextResponse("config info"), nil
}

func (t *crushInfoSpyTool) ProviderOptions() fantasy.ProviderOptions   { return nil }
func (t *crushInfoSpyTool) SetProviderOptions(fantasy.ProviderOptions) {}

func TestToolCallRepair_RepairsMissingFilePath(t *testing.T) {
	t.Setenv(toolCallRepairEnabledEnv, "true")
	t.Setenv(toolCallRepairMaxAttemptsEnv, "2")

	tool := &writeSpyTool{}
	audits := csync.NewMap[string, toolCallRepairAudit]()

	var mainCalls, repairCalls int
	model := &repairTestModel{
		generateFunc: func(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
			if call.ToolChoice != nil && *call.ToolChoice == fantasy.ToolChoiceRequired {
				repairCalls++
				return toolCallResponse("repair-call-1", "write", `{"file_path":"notes.txt","content":"hello"}`, fantasy.FinishReasonToolCalls), nil
			}

			mainCalls++
			if mainCalls == 1 {
				return toolCallResponse("call-1", "write", `{"content":"hello"}`, fantasy.FinishReasonToolCalls), nil
			}
			return textResponse("done"), nil
		},
	}

	agent := fantasy.NewAgent(
		model,
		fantasy.WithTools(tool),
		fantasy.WithRepairToolCall(newToolCallRepairer(model, audits)),
		fantasy.WithStopConditions(fantasy.StepCountIs(3)),
	)

	result, err := agent.Generate(context.Background(), fantasy.AgentCall{
		Prompt: "Write hello to notes.txt",
	})
	require.NoError(t, err)
	require.Len(t, result.Steps, 2)
	require.Equal(t, 1, repairCalls)
	require.Equal(t, 1, tool.callCount)

	stepCalls := result.Steps[0].Content.ToolCalls()
	require.Len(t, stepCalls, 1)
	require.False(t, stepCalls[0].Invalid)
	assertJSONEq(t, `{"file_path":"notes.txt","content":"hello"}`, stepCalls[0].Input)
	assertJSONEq(t, `{"file_path":"notes.txt","content":"hello"}`, tool.lastCall.Input)

	toolResults := result.Steps[0].Content.ToolResults()
	require.Len(t, toolResults, 1)
	require.Equal(t, "write", toolResults[0].ToolName)
	output, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](toolResults[0].Result)
	require.True(t, ok)
	require.Equal(t, "written", output.Text)

	audit, ok := audits.Get("call-1")
	require.True(t, ok)
	require.Equal(t, toolCallRepairOutcomeSucceeded, audit.Outcome)
	require.Equal(t, 1, audit.AttemptCount)
	require.Equal(t, "write", audit.RepairedToolName)
	assertJSONEq(t, `{"file_path":"notes.txt","content":"hello"}`, audit.RepairedInput)
	require.Equal(t, []string{"content", "file_path"}, audit.RequiredFields)
	require.Equal(t, []string{"file_path"}, audit.MissingFields)
}

func TestToolCallRepair_ValidCallDoesNotTriggerRepair(t *testing.T) {
	t.Setenv(toolCallRepairEnabledEnv, "true")
	t.Setenv(toolCallRepairMaxAttemptsEnv, "2")

	tool := &writeSpyTool{}
	audits := csync.NewMap[string, toolCallRepairAudit]()

	var mainCalls, repairCalls int
	model := &repairTestModel{
		generateFunc: func(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
			if call.ToolChoice != nil && *call.ToolChoice == fantasy.ToolChoiceRequired {
				repairCalls++
				return nil, errors.New("repair path should not run for a valid tool call")
			}

			mainCalls++
			if mainCalls == 1 {
				return toolCallResponse("call-1", "write", `{"file_path":"notes.txt","content":"hello"}`, fantasy.FinishReasonToolCalls), nil
			}
			return textResponse("done"), nil
		},
	}

	agent := fantasy.NewAgent(
		model,
		fantasy.WithTools(tool),
		fantasy.WithRepairToolCall(newToolCallRepairer(model, audits)),
		fantasy.WithStopConditions(fantasy.StepCountIs(3)),
	)

	result, err := agent.Generate(context.Background(), fantasy.AgentCall{
		Prompt: "Write hello to notes.txt",
	})
	require.NoError(t, err)
	require.Len(t, result.Steps, 2)
	require.Equal(t, 0, repairCalls)
	require.Equal(t, 1, tool.callCount)

	stepCalls := result.Steps[0].Content.ToolCalls()
	require.Len(t, stepCalls, 1)
	require.False(t, stepCalls[0].Invalid)
	assertJSONEq(t, `{"file_path":"notes.txt","content":"hello"}`, stepCalls[0].Input)
	require.Equal(t, 0, audits.Len())
}

func TestToolCallRepair_AliasRewriteRecordsCanonicalizationMetadata(t *testing.T) {
	t.Parallel()

	tool := toolpkg.WithAliases(&writeSpyTool{}, []string{"write_file"}, nil)
	audits := csync.NewMap[string, toolCallRepairAudit]()

	repaired, err := repairToolCall(context.Background(), nil, toolCallRepairConfig{
		Enabled:         true,
		MaxAttempts:     defaultToolCallRepairMaxAttempts,
		MaxOutputTokens: defaultToolCallRepairMaxOutput,
	}, audits, fantasy.ToolCallRepairOptions{
		OriginalToolCall: fantasy.ToolCallContent{
			ToolCallID: "call-1",
			ToolName:   "write_file",
			Input:      `{"file_path":"notes.txt","content":"hello"}`,
		},
		AvailableTools: []fantasy.AgentTool{tool},
	})
	require.NoError(t, err)
	require.NotNil(t, repaired)
	require.Equal(t, "write", repaired.ToolName)
	assertJSONEq(t, `{"file_path":"notes.txt","content":"hello"}`, repaired.Input)

	audit, ok := audits.Get("call-1")
	require.True(t, ok)
	require.Equal(t, "write_file", audit.RequestedToolName)
	require.Equal(t, "write", audit.CanonicalToolName)
	require.True(t, audit.AliasApplied)
	require.Equal(t, string(toolAliasSourceExplicitAlias), audit.AliasSource)
	require.Equal(t, toolFailureKindAliasRewritten, audit.FailureKind)
	require.Equal(t, toolCallRepairOutcomeSucceeded, audit.Outcome)
	require.True(t, audit.RepairSucceeded)
	require.False(t, audit.RepairAttempted)
	require.Equal(t, "write", audit.RepairedToolName)
}

func TestToolCallRepair_InvalidUnknownToolRecordsStructuredFailure(t *testing.T) {
	t.Parallel()

	available := []fantasy.AgentTool{
		toolpkg.WithAliases(&fakeTool{name: "bash"}, []string{"execute"}, nil),
		toolpkg.WithAliases(&fakeTool{name: "view"}, nil, []string{"read"}),
	}
	audits := csync.NewMap[string, toolCallRepairAudit]()

	repaired, err := repairToolCall(context.Background(), nil, toolCallRepairConfig{
		Enabled:         true,
		MaxAttempts:     defaultToolCallRepairMaxAttempts,
		MaxOutputTokens: defaultToolCallRepairMaxOutput,
	}, audits, fantasy.ToolCallRepairOptions{
		OriginalToolCall: fantasy.ToolCallContent{
			ToolCallID: "call-1",
			ToolName:   "viewer",
			Input:      `{"file_path":"README.md"}`,
		},
		ValidationError: errors.New("tool not found: viewer"),
		AvailableTools:  available,
	})
	require.NoError(t, err)
	require.Nil(t, repaired)

	audit, ok := audits.Get("call-1")
	require.True(t, ok)
	require.Equal(t, "viewer", audit.RequestedToolName)
	require.Equal(t, toolFailureKindInvalidToolName, audit.FailureKind)
	require.Equal(t, toolCallRepairOutcomeExhausted, audit.Outcome)
	require.Equal(t, toolCallRepairExhaustedNoTool, audit.ExhaustedReason)
	require.True(t, audit.RepairExhausted)
	require.False(t, audit.RepairAttempted)
	require.Equal(t, []string{"view"}, audit.Candidates)
	require.Equal(t, []string{"bash", "view"}, audit.ExposedTools)
}

func TestToolCallRepair_StopsOnDuplicateInvalidRetry(t *testing.T) {
	t.Setenv(toolCallRepairEnabledEnv, "true")
	t.Setenv(toolCallRepairMaxAttemptsEnv, "2")

	tool := &writeSpyTool{}
	audits := csync.NewMap[string, toolCallRepairAudit]()

	var repairCalls int
	model := &repairTestModel{
		generateFunc: func(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
			if call.ToolChoice != nil && *call.ToolChoice == fantasy.ToolChoiceRequired {
				repairCalls++
				return toolCallResponse("repair-call-1", "write", `{"content":"hello"}`, fantasy.FinishReasonToolCalls), nil
			}
			return toolCallResponse("call-1", "write", `{"content":"hello"}`, fantasy.FinishReasonStop), nil
		},
	}

	agent := fantasy.NewAgent(
		model,
		fantasy.WithTools(tool),
		fantasy.WithRepairToolCall(newToolCallRepairer(model, audits)),
		fantasy.WithStopConditions(fantasy.StepCountIs(2)),
	)

	result, err := agent.Generate(context.Background(), fantasy.AgentCall{
		Prompt: "Write hello to notes.txt",
	})
	require.NoError(t, err)
	require.Len(t, result.Steps, 1)
	require.Equal(t, 1, repairCalls)
	require.Equal(t, 0, tool.callCount)

	stepCalls := result.Steps[0].Content.ToolCalls()
	require.Len(t, stepCalls, 1)
	require.True(t, stepCalls[0].Invalid)

	toolResults := result.Steps[0].Content.ToolResults()
	require.Len(t, toolResults, 1)
	output, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](toolResults[0].Result)
	require.True(t, ok)
	require.ErrorContains(t, output.Error, "missing required parameter: file_path")

	audit, ok := audits.Get("call-1")
	require.True(t, ok)
	require.Equal(t, toolCallRepairOutcomeExhausted, audit.Outcome)
	require.Equal(t, toolCallRepairExhaustedDuplicate, audit.ExhaustedReason)
	require.Equal(t, 1, audit.AttemptCount)
	require.Len(t, audit.Attempts, 1)
	require.True(t, audit.Attempts[0].DuplicateInvalid)
}

func TestToolCallRepair_ExhaustionStillReturnsNormalError(t *testing.T) {
	t.Setenv(toolCallRepairEnabledEnv, "true")
	t.Setenv(toolCallRepairMaxAttemptsEnv, "2")

	tool := &writeSpyTool{}
	audits := csync.NewMap[string, toolCallRepairAudit]()

	var repairCalls int
	model := &repairTestModel{
		generateFunc: func(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
			if call.ToolChoice != nil && *call.ToolChoice == fantasy.ToolChoiceRequired {
				repairCalls++
				switch repairCalls {
				case 1:
					return toolCallResponse("repair-call-1", "write", `{"file_path":123,"content":"hello"}`, fantasy.FinishReasonToolCalls), nil
				default:
					return toolCallResponse("repair-call-2", "write", `{"file_path":false,"content":"hello"}`, fantasy.FinishReasonToolCalls), nil
				}
			}
			return toolCallResponse("call-1", "write", `{"content":"hello"}`, fantasy.FinishReasonStop), nil
		},
	}

	agent := fantasy.NewAgent(
		model,
		fantasy.WithTools(tool),
		fantasy.WithRepairToolCall(newToolCallRepairer(model, audits)),
		fantasy.WithStopConditions(fantasy.StepCountIs(2)),
	)

	result, err := agent.Generate(context.Background(), fantasy.AgentCall{
		Prompt: "Write hello to notes.txt",
	})
	require.NoError(t, err)
	require.Len(t, result.Steps, 1)
	require.Equal(t, 2, repairCalls)
	require.Equal(t, 0, tool.callCount)

	stepCalls := result.Steps[0].Content.ToolCalls()
	require.Len(t, stepCalls, 1)
	require.True(t, stepCalls[0].Invalid)

	toolResults := result.Steps[0].Content.ToolResults()
	require.Len(t, toolResults, 1)
	output, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](toolResults[0].Result)
	require.True(t, ok)
	require.ErrorContains(t, output.Error, "missing required parameter: file_path")

	audit, ok := audits.Get("call-1")
	require.True(t, ok)
	require.Equal(t, toolCallRepairOutcomeExhausted, audit.Outcome)
	require.Equal(t, toolCallRepairExhaustedMaxAttempts, audit.ExhaustedReason)
	require.Equal(t, 2, audit.AttemptCount)
	require.Len(t, audit.Attempts, 2)
	require.Contains(t, audit.Attempts[0].ValidationError, "validation failed")
	require.Contains(t, audit.Attempts[1].ValidationError, "validation failed")
}

func TestConvertToToolResult_MergesRepairMetadata(t *testing.T) {
	t.Parallel()

	a := &sessionAgent{
		toolRepairAudits: csync.NewMap[string, toolCallRepairAudit](),
	}
	a.toolRepairAudits.Set("call-1", toolCallRepairAudit{
		FeatureEnabled:         true,
		OriginalToolName:       "write",
		OriginalInput:          `{"content":"hello"}`,
		InitialValidationError: "missing required parameter: file_path",
		MissingFields:          []string{"file_path"},
		AttemptCount:           1,
		Outcome:                toolCallRepairOutcomeSucceeded,
		RepairedToolName:       "write",
		RepairedInput:          `{"file_path":"notes.txt","content":"hello"}`,
	})

	toolResult := a.convertToToolResult(fantasy.ToolResultContent{
		ToolCallID:     "call-1",
		ToolName:       "write",
		ClientMetadata: `{"source":"write-spy"}`,
		Result: fantasy.ToolResultOutputContentError{
			Error: errors.New("missing required parameter: file_path"),
		},
	})

	require.True(t, toolResult.IsError)
	require.Equal(t, "missing required parameter: file_path", toolResult.Content)

	var metadata struct {
		Source string              `json:"source"`
		Repair toolCallRepairAudit `json:"repair"`
	}
	require.NoError(t, json.Unmarshal([]byte(toolResult.Metadata), &metadata))
	require.Equal(t, "write-spy", metadata.Source)
	require.Equal(t, toolCallRepairOutcomeSucceeded, metadata.Repair.Outcome)
	require.Equal(t, "write", metadata.Repair.OriginalToolName)
	require.Equal(t, 1, metadata.Repair.AttemptCount)

	_, ok := a.toolRepairAudits.Get("call-1")
	require.False(t, ok, "repair audit should be consumed after persistence")
}

func TestRuntimeToolRepair_RewritesRetryableBashFailure(t *testing.T) {
	t.Setenv(toolCallRepairEnabledEnv, "true")
	t.Setenv(toolCallRepairMaxAttemptsEnv, "2")

	tool := &bashRuntimeSpyTool{
		runFunc: func(call fantasy.ToolCall) fantasy.ToolResponse {
			if strings.Contains(call.Input, `C:\\projects\\repo`) {
				return fantasy.NewTextResponse("fixed")
			}
			return fantasy.WithResponseMetadata(
				fantasy.NewTextErrorResponse("path translation failed"),
				toolpkg.BashResponseMetadata{
					Command:          `cd C:\projects\repo && pwd`,
					WorkingDirectory: `C:\workspace`,
					ExitCode:         127,
					Stdout:           "",
					Stderr:           "no such file or directory",
					Retryable:        true,
					FailureKind:      toolpkg.BashFailureKindWindowsPathTranslation,
				},
			)
		},
	}
	audits := csync.NewMap[string, toolCallRepairAudit]()
	model := &repairTestModel{
		generateFunc: func(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
			require.NotNil(t, call.ToolChoice)
			return toolCallResponse("repair-call-1", toolpkg.BashToolName, `{"description":"fixed","command":"pwd","working_dir":"C:\\projects\\repo"}`, fantasy.FinishReasonToolCalls), nil
		},
	}
	wrapped := newRuntimeRepairTool(tool, model, nil, audits, nil, toolCallRepairConfigFromEnv())
	ctx := withRuntimeToolAvailableTools(
		withRuntimeToolMessages(
			context.WithValue(context.Background(), toolpkg.SessionIDContextKey, "sess-1"),
			[]fantasy.Message{fantasy.NewUserMessage("run the command")},
		),
		[]fantasy.AgentTool{tool},
	)

	resp, err := wrapped.Run(ctx, fantasy.ToolCall{
		ID:    "call-1",
		Name:  toolpkg.BashToolName,
		Input: `{"description":"bad","command":"cd C:\\bad && pwd"}`,
	})
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Equal(t, 2, tool.callCount)

	audit, ok := audits.Get("call-1")
	require.True(t, ok)
	require.NotNil(t, audit.Runtime)
	require.Equal(t, toolCallRepairOutcomeSucceeded, audit.Runtime.Outcome)
	require.Equal(t, toolpkg.BashFailureKindWindowsPathTranslation, audit.Runtime.InitialFailure.FailureKind)
	require.Equal(t, 1, audit.Runtime.AttemptCount)
	assertJSONEq(t, `{"description":"fixed","command":"pwd","working_dir":"C:\\projects\\repo"}`, audit.Runtime.RepairedInput)
}

func TestRuntimeToolRepair_ExhaustionReturnsErrorResult(t *testing.T) {
	t.Setenv(toolCallRepairEnabledEnv, "true")
	t.Setenv(toolCallRepairMaxAttemptsEnv, "2")

	tool := &bashRuntimeSpyTool{
		runFunc: func(call fantasy.ToolCall) fantasy.ToolResponse {
			return fantasy.WithResponseMetadata(
				fantasy.NewTextErrorResponse("path translation failed"),
				toolpkg.BashResponseMetadata{
					Command:          `cd C:\projects\missing && pwd`,
					WorkingDirectory: `C:\workspace`,
					ExitCode:         127,
					Stdout:           "",
					Stderr:           "no such file or directory",
					Retryable:        true,
					FailureKind:      toolpkg.BashFailureKindWindowsPathTranslation,
				},
			)
		},
	}
	audits := csync.NewMap[string, toolCallRepairAudit]()
	repairCalls := 0
	model := &repairTestModel{
		generateFunc: func(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
			repairCalls++
			switch repairCalls {
			case 1:
				return toolCallResponse("repair-call-1", toolpkg.BashToolName, `{"description":"fixed","command":"pwd","working_dir":"C:\\projects\\missing-1"}`, fantasy.FinishReasonToolCalls), nil
			default:
				return toolCallResponse("repair-call-2", toolpkg.BashToolName, `{"description":"fixed","command":"pwd","working_dir":"C:\\projects\\missing-2"}`, fantasy.FinishReasonToolCalls), nil
			}
		},
	}
	wrapped := newRuntimeRepairTool(tool, model, nil, audits, nil, toolCallRepairConfigFromEnv())
	ctx := withRuntimeToolAvailableTools(
		withRuntimeToolMessages(
			context.WithValue(context.Background(), toolpkg.SessionIDContextKey, "sess-1"),
			[]fantasy.Message{fantasy.NewUserMessage("run the command")},
		),
		[]fantasy.AgentTool{tool},
	)

	resp, err := wrapped.Run(ctx, fantasy.ToolCall{
		ID:    "call-1",
		Name:  toolpkg.BashToolName,
		Input: `{"description":"bad","command":"cd C:\\bad && pwd"}`,
	})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Equal(t, 3, tool.callCount)

	audit, ok := audits.Get("call-1")
	require.True(t, ok)
	require.NotNil(t, audit.Runtime)
	require.Equal(t, toolCallRepairOutcomeExhausted, audit.Runtime.Outcome)
	require.Equal(t, toolCallRepairExhaustedMaxAttempts, audit.Runtime.ExhaustedReason)
	require.Equal(t, 2, audit.Runtime.AttemptCount)
}

func TestRuntimeToolRepair_StripsUnsupportedWindowsOutputPipeDeterministically(t *testing.T) {
	t.Setenv(toolCallRepairEnabledEnv, "true")
	t.Setenv(toolCallRepairMaxAttemptsEnv, "2")

	tool := &bashRuntimeSpyTool{
		runFunc: func(call fantasy.ToolCall) fantasy.ToolResponse {
			if strings.Contains(call.Input, "Select-Object") || strings.Contains(call.Input, "| head") {
				return fantasy.WithResponseMetadata(
					fantasy.NewTextErrorResponse("shell mismatch"),
					toolpkg.BashResponseMetadata{
						Command:          `cd C:\projects\repo && python -m pytest tests/test_api.py -v 2>&1 | Select-Object -First 80`,
						WorkingDirectory: `C:\workspace`,
						ExitCode:         127,
						Stdout:           "",
						Stderr:           `"Select-Object": executable file not found in $PATH`,
						Retryable:        true,
						FailureKind:      toolpkg.BashFailureKindWindowsShellMismatch,
					},
				)
			}
			return fantasy.NewTextResponse("fixed")
		},
	}

	audits := csync.NewMap[string, toolCallRepairAudit]()
	wrapped := newRuntimeRepairTool(tool, nil, nil, audits, nil, toolCallRepairConfigFromEnv())
	ctx := withRuntimeToolAvailableTools(
		withRuntimeToolMessages(
			context.WithValue(context.Background(), toolpkg.SessionIDContextKey, "sess-1"),
			[]fantasy.Message{fantasy.NewUserMessage("run the command")},
		),
		[]fantasy.AgentTool{tool},
	)

	resp, err := wrapped.Run(ctx, fantasy.ToolCall{
		ID:    "call-1",
		Name:  toolpkg.BashToolName,
		Input: `{"description":"Run backend tests","command":"cd C:\\projects\\repo && python -m pytest tests/test_api.py -v 2>&1 | head -80"}`,
	})
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Equal(t, 2, tool.callCount)
	require.NotContains(t, tool.lastCall.Input, "head")
	require.NotContains(t, tool.lastCall.Input, "Select-Object")

	audit, ok := audits.Get("call-1")
	require.True(t, ok)
	require.NotNil(t, audit.Runtime)
	require.Equal(t, toolCallRepairOutcomeSucceeded, audit.Runtime.Outcome)
	require.Equal(t, toolpkg.BashFailureKindWindowsShellMismatch, audit.Runtime.InitialFailure.FailureKind)
	require.Equal(t, 1, audit.Runtime.AttemptCount)
	require.NotContains(t, audit.Runtime.RepairedInput, "head")
	require.NotContains(t, audit.Runtime.RepairedInput, "Select-Object")
}

func TestRuntimeToolRepair_RepeatedCrushInfoBlockedAndEscalates(t *testing.T) {
	t.Parallel()

	tool := &crushInfoSpyTool{}
	tracker := newToolLoopTracker()
	wrapped := newRuntimeRepairTool(tool, nil, nil, nil, tracker, toolCallRepairConfigFromEnv())
	ctx := context.WithValue(context.Background(), toolpkg.SessionIDContextKey, "sess-1")
	call := fantasy.ToolCall{
		ID:    "call-1",
		Name:  toolpkg.CrushInfoToolName,
		Input: `{}`,
	}

	first, err := wrapped.Run(ctx, call)
	require.NoError(t, err)
	require.False(t, first.IsError)
	require.False(t, first.StopTurn)
	require.Equal(t, 1, tool.callCount)

	second, err := wrapped.Run(ctx, call)
	require.NoError(t, err)
	require.True(t, second.IsError)
	require.False(t, second.StopTurn)
	require.Equal(t, 1, tool.callCount)
	require.Contains(t, second.Content, "Repeated crush_info call blocked")

	var secondMetadata struct {
		Guard struct {
			ToolName    string `json:"tool_name"`
			LoopKind    string `json:"loop_kind"`
			BlockedTool bool   `json:"blocked_tool"`
		} `json:"guard"`
	}
	require.NoError(t, json.Unmarshal([]byte(second.Metadata), &secondMetadata))
	require.Equal(t, toolpkg.CrushInfoToolName, secondMetadata.Guard.ToolName)
	require.Equal(t, string(toolLoopKindRepeatedMetaToolProbe), secondMetadata.Guard.LoopKind)
	require.True(t, secondMetadata.Guard.BlockedTool)

	third, err := wrapped.Run(ctx, call)
	require.NoError(t, err)
	require.True(t, third.IsError)
	require.True(t, third.StopTurn)
	require.Equal(t, 1, tool.callCount)

	var thirdMetadata struct {
		Loop struct {
			ToolName            string `json:"tool_name"`
			Classification      string `json:"classification"`
			OutcomeClass        string `json:"outcome_class"`
			LoopKind            string `json:"loop_kind"`
			StreakCount         int    `json:"streak_count"`
			SuggestedAction     string `json:"suggested_action"`
			BlockedTool         bool   `json:"blocked_tool"`
			NormalizedSignature string `json:"normalized_signature"`
		} `json:"loop"`
	}
	require.NoError(t, json.Unmarshal([]byte(third.Metadata), &thirdMetadata))
	require.Equal(t, toolpkg.CrushInfoToolName, thirdMetadata.Loop.ToolName)
	require.Equal(t, string(toolLoopClassificationRepeatedLowSignalSuccess), thirdMetadata.Loop.Classification)
	require.Equal(t, string(toolCycleOutcomeSuccessLowSignal), thirdMetadata.Loop.OutcomeClass)
	require.Equal(t, string(toolLoopKindRepeatedMetaToolProbe), thirdMetadata.Loop.LoopKind)
	require.Equal(t, 3, thirdMetadata.Loop.StreakCount)
	require.Equal(t, string(toolLoopSuggestedActionRequestSupervisor), thirdMetadata.Loop.SuggestedAction)
	require.True(t, thirdMetadata.Loop.BlockedTool)
	require.True(t, strings.HasPrefix(thirdMetadata.Loop.NormalizedSignature, "sha256:"))
}

func toolCallResponse(id, toolName, input string, finishReason fantasy.FinishReason) *fantasy.Response {
	return &fantasy.Response{
		Content: fantasy.ResponseContent{
			fantasy.ToolCallContent{
				ToolCallID: id,
				ToolName:   toolName,
				Input:      input,
			},
		},
		Usage:        fantasy.Usage{TotalTokens: 10},
		FinishReason: finishReason,
	}
}

func textResponse(text string) *fantasy.Response {
	return &fantasy.Response{
		Content: fantasy.ResponseContent{
			fantasy.TextContent{Text: text},
		},
		Usage:        fantasy.Usage{TotalTokens: 10},
		FinishReason: fantasy.FinishReasonStop,
	}
}

func assertJSONEq(t *testing.T, expected, actual string) {
	t.Helper()

	var expectedObj any
	var actualObj any

	require.NoError(t, json.Unmarshal([]byte(expected), &expectedObj))
	require.NoError(t, json.Unmarshal([]byte(actual), &actualObj), fmt.Sprintf("actual JSON: %s", actual))
	require.Equal(t, expectedObj, actualObj)
}
