package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"charm.land/fantasy"
	toolpkg "github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/stretchr/testify/require"
)

func TestToolLoopTracker_RepeatedEditOldStringNotFoundTriggersHandoff(t *testing.T) {
	t.Parallel()

	tracker := newToolLoopTracker()
	call := fantasy.ToolCall{
		ID:    "call-1",
		Name:  toolpkg.EditToolName,
		Input: `{"file_path":"main.go","old_string":"missing","new_string":"replacement"}`,
	}
	resp := fantasy.NewTextErrorResponse("old_string not found in file. Make sure it matches exactly, including whitespace and line breaks.")

	var handoff *toolLoopHandoff
	for range 3 {
		handoff = observeToolCycle(tracker, context.Background(), classifyToolCycle(call, resp, nil, "sess-1"))
	}

	require.NotNil(t, handoff)
	require.Equal(t, toolLoopClassificationRepeatedRetryableFailure, handoff.Classification)
	require.Equal(t, toolCycleOutcomeFailureRetryable, handoff.OutcomeClass)
	require.Equal(t, 3, handoff.StreakCount)
	require.True(t, handoff.SupervisorHandoffTriggered)
}

func TestToolLoopTracker_RepeatedEditNoOpTriggersHandoff(t *testing.T) {
	t.Parallel()

	tracker := newToolLoopTracker()
	call := fantasy.ToolCall{
		ID:    "call-1",
		Name:  toolpkg.EditToolName,
		Input: `{"file_path":"main.go","old_string":"same","new_string":"same"}`,
	}
	resp := fantasy.NewTextErrorResponse("new content is the same as old content. No changes made.")

	var handoff *toolLoopHandoff
	for range 3 {
		handoff = observeToolCycle(tracker, context.Background(), classifyToolCycle(call, resp, nil, "sess-1"))
	}

	require.NotNil(t, handoff)
	require.Equal(t, toolLoopClassificationRepeatedNonRetryableFailure, handoff.Classification)
	require.Equal(t, toolCycleOutcomeFailureNonRetryable, handoff.OutcomeClass)
	require.Equal(t, toolLoopSuggestedActionPauseAutonomy, handoff.SuggestedAction)
}

func TestToolLoopTracker_RepeatedCrushInfoTriggersLowSignalHandoff(t *testing.T) {
	t.Parallel()

	tracker := newToolLoopTracker()
	call := fantasy.ToolCall{
		ID:    "call-1",
		Name:  toolpkg.CrushInfoToolName,
		Input: `{}`,
	}
	resp := fantasy.NewTextResponse("config info")

	var handoff *toolLoopHandoff
	for range 3 {
		handoff = observeToolCycle(tracker, context.Background(), classifyToolCycle(call, resp, nil, "sess-1"))
	}

	require.NotNil(t, handoff)
	require.Equal(t, toolLoopClassificationRepeatedLowSignalSuccess, handoff.Classification)
	require.Equal(t, toolCycleOutcomeSuccessLowSignal, handoff.OutcomeClass)
	require.Equal(t, toolLoopKindRepeatedMetaToolProbe, handoff.LoopKind)
	require.Equal(t, toolLoopSuggestedActionRequestSupervisor, handoff.SuggestedAction)
}

func TestToolLoopTracker_SingleCrushInfoDoesNotTriggerLoop(t *testing.T) {
	t.Parallel()

	tracker := newToolLoopTracker()
	call := fantasy.ToolCall{
		ID:    "call-1",
		Name:  toolpkg.CrushInfoToolName,
		Input: `{}`,
	}
	resp := fantasy.NewTextResponse("config info")

	handoff := observeToolCycle(tracker, context.Background(), classifyToolCycle(call, resp, nil, "sess-1"))
	require.Nil(t, handoff)
}

func TestToolLoopTracker_RepeatedViewSameTargetTriggersLowSignalHandoff(t *testing.T) {
	t.Parallel()

	tracker := newToolLoopTracker()
	call := fantasy.ToolCall{
		ID:    "call-1",
		Name:  toolpkg.ViewToolName,
		Input: `{"file_path":"main.go"}`,
	}
	resp := fantasy.NewTextResponse("package main")

	var handoff *toolLoopHandoff
	for range 3 {
		handoff = observeToolCycle(tracker, context.Background(), classifyToolCycle(call, resp, nil, "sess-1"))
	}

	require.NotNil(t, handoff)
	require.Equal(t, toolLoopClassificationRepeatedLowSignalSuccess, handoff.Classification)
	require.Equal(t, toolCycleOutcomeSuccessLowSignal, handoff.OutcomeClass)
	require.Equal(t, toolLoopKindRepeatedLowSignalSuccess, handoff.LoopKind)
}

func TestToolLoopTracker_DifferentReadTargetsDoNotTriggerLoop(t *testing.T) {
	t.Parallel()

	tracker := newToolLoopTracker()
	resp := fantasy.NewTextResponse("file contents")

	first := observeToolCycle(tracker, context.Background(), classifyToolCycle(
		fantasy.ToolCall{ID: "call-1", Name: toolpkg.ViewToolName, Input: `{"file_path":"a.go"}`},
		resp,
		nil,
		"sess-1",
	))
	second := observeToolCycle(tracker, context.Background(), classifyToolCycle(
		fantasy.ToolCall{ID: "call-2", Name: toolpkg.ViewToolName, Input: `{"file_path":"b.go"}`},
		resp,
		nil,
		"sess-1",
	))

	require.Nil(t, first)
	require.Nil(t, second)
}

func TestToolLoopTracker_ChangedInputResetsStreak(t *testing.T) {
	t.Parallel()

	tracker := newToolLoopTracker()
	resp := fantasy.NewTextErrorResponse("old_string not found in file. Make sure it matches exactly, including whitespace and line breaks.")

	first := fantasy.ToolCall{ID: "call-1", Name: toolpkg.EditToolName, Input: `{"file_path":"a.go","old_string":"missing","new_string":"replacement"}`}
	second := fantasy.ToolCall{ID: "call-2", Name: toolpkg.EditToolName, Input: `{"file_path":"b.go","old_string":"missing","new_string":"replacement"}`}

	require.Nil(t, observeToolCycle(tracker, context.Background(), classifyToolCycle(first, resp, nil, "sess-1")))
	require.Nil(t, observeToolCycle(tracker, context.Background(), classifyToolCycle(first, resp, nil, "sess-1")))
	require.Nil(t, observeToolCycle(tracker, context.Background(), classifyToolCycle(second, resp, nil, "sess-1")))

	handoff := observeToolCycle(tracker, context.Background(), classifyToolCycle(first, resp, nil, "sess-1"))
	require.Nil(t, handoff)
}

func TestToolLoopTracker_SuccessfulRepairResetsStreak(t *testing.T) {
	t.Parallel()

	tracker := newToolLoopTracker()
	failureCall := fantasy.ToolCall{ID: "call-1", Name: toolpkg.EditToolName, Input: `{"file_path":"main.go","old_string":"missing","new_string":"replacement"}`}
	failureResp := fantasy.NewTextErrorResponse("old_string not found in file. Make sure it matches exactly, including whitespace and line breaks.")

	require.Nil(t, observeToolCycle(tracker, context.Background(), classifyToolCycle(failureCall, failureResp, nil, "sess-1")))
	require.Nil(t, observeToolCycle(tracker, context.Background(), classifyToolCycle(failureCall, failureResp, nil, "sess-1")))

	repairedResp := fantasy.WithResponseMetadata(
		fantasy.NewTextResponse("Content replaced in file: main.go"),
		toolpkg.EditResponseMetadata{Additions: 1, Removals: 1},
	)
	repairedAudit := &toolCallRepairAudit{
		Runtime: &toolRuntimeRepairAudit{Outcome: toolCallRepairOutcomeSucceeded},
	}
	repairedCall := fantasy.ToolCall{ID: "call-2", Name: toolpkg.EditToolName, Input: `{"file_path":"main.go","old_string":"present","new_string":"replacement"}`}
	require.Nil(t, observeToolCycle(tracker, context.Background(), classifyToolCycle(repairedCall, repairedResp, repairedAudit, "sess-1")))

	require.Nil(t, observeToolCycle(tracker, context.Background(), classifyToolCycle(failureCall, failureResp, nil, "sess-1")))
	handoff := observeToolCycle(tracker, context.Background(), classifyToolCycle(failureCall, failureResp, nil, "sess-1"))
	require.Nil(t, handoff)
}

func TestToolLoopTracker_RepeatedInvalidToolTriggersHandoff(t *testing.T) {
	t.Parallel()

	tracker := newToolLoopTracker()
	toolCall := fantasy.ToolCallContent{
		ToolCallID:      "call-1",
		ToolName:        "viewer",
		Input:           `{"file_path":"README.md"}`,
		Invalid:         true,
		ValidationError: errors.New("tool not found: viewer"),
	}
	toolResult := fantasy.ToolResultContent{
		ToolCallID: "call-1",
		ToolName:   "viewer",
		Result: fantasy.ToolResultOutputContentError{
			Error: errors.New("tool not found: viewer"),
		},
	}
	metadata := mergeToolRepairMetadata("", toolCallRepairAudit{
		RequestedToolName: "viewer",
		OriginalToolName:  "viewer",
		FailureKind:       toolFailureKindInvalidToolName,
		Candidates:        []string{"view"},
		ExposedTools:      []string{"bash", "view"},
		Outcome:           toolCallRepairOutcomeExhausted,
		RepairExhausted:   true,
	})

	var handoff *toolLoopHandoff
	for range 3 {
		handoff = observeToolCycle(tracker, context.Background(), classifyPreExecutionToolCycle(toolCall, toolResult, metadata, "sess-1"))
	}

	require.NotNil(t, handoff)
	require.Equal(t, toolLoopClassificationRepeatedInvalidTool, handoff.Classification)
	require.Equal(t, toolCycleOutcomeFailureInvalidTool, handoff.OutcomeClass)
	require.Equal(t, toolLoopKindRepeatedInvalidTool, handoff.LoopKind)
	require.Equal(t, 3, handoff.StreakCount)
	require.Equal(t, toolLoopSuggestedActionRequestSupervisor, handoff.SuggestedAction)
	require.True(t, handoff.SupervisorHandoffTriggered)
}

func TestToolLoopTracker_RepeatedInvalidSchemaTriggersHandoff(t *testing.T) {
	t.Parallel()

	tracker := newToolLoopTracker()
	toolCall := fantasy.ToolCallContent{
		ToolCallID:      "call-1",
		ToolName:        toolpkg.WriteToolName,
		Input:           `{"content":"hello"}`,
		Invalid:         true,
		ValidationError: errors.New("missing required parameter: file_path"),
	}
	toolResult := fantasy.ToolResultContent{
		ToolCallID: "call-1",
		ToolName:   toolpkg.WriteToolName,
		Result: fantasy.ToolResultOutputContentError{
			Error: errors.New("missing required parameter: file_path"),
		},
	}
	metadata := mergeToolRepairMetadata("", toolCallRepairAudit{
		RequestedToolName:      toolpkg.WriteToolName,
		OriginalToolName:       toolpkg.WriteToolName,
		CanonicalToolName:      toolpkg.WriteToolName,
		FailureKind:            toolFailureKindInvalidSchema,
		InitialValidationError: "missing required parameter: file_path",
		Outcome:                toolCallRepairOutcomeExhausted,
		RepairAttempted:        true,
		RepairExhausted:        true,
	})

	var handoff *toolLoopHandoff
	for range 3 {
		handoff = observeToolCycle(tracker, context.Background(), classifyPreExecutionToolCycle(toolCall, toolResult, metadata, "sess-1"))
	}

	require.NotNil(t, handoff)
	require.Equal(t, toolLoopClassificationRepeatedInvalidSchema, handoff.Classification)
	require.Equal(t, toolCycleOutcomeFailureInvalidSchema, handoff.OutcomeClass)
	require.Equal(t, toolLoopKindRepeatedInvalidSchema, handoff.LoopKind)
	require.Equal(t, 3, handoff.StreakCount)
	require.Equal(t, toolLoopSuggestedActionRequestSupervisor, handoff.SuggestedAction)
	require.True(t, handoff.SupervisorHandoffTriggered)
}

func TestToolLoopTracker_HandoffRedactsLargeWritePayloads(t *testing.T) {
	t.Parallel()

	tracker := newToolLoopTracker()
	largeContent := strings.Repeat("x", 2000)
	call := fantasy.ToolCall{
		ID:   "call-1",
		Name: toolpkg.WriteToolName,
		Input: `{"file_path":"backend/tests/test_api.py","content":"` +
			largeContent + `"}`,
	}
	resp := fantasy.NewTextErrorResponse("File backend/tests/test_api.py already contains the exact content. No changes made.")

	var handoff *toolLoopHandoff
	for range 3 {
		handoff = observeToolCycle(tracker, context.Background(), classifyToolCycle(call, resp, nil, "sess-1"))
	}

	require.NotNil(t, handoff)
	require.Equal(t, toolLoopClassificationRepeatedNonRetryableFailure, handoff.Classification)
	require.True(t, strings.HasPrefix(handoff.NormalizedSignature, "sha256:"))
	require.NotContains(t, handoff.NormalizedSignature, largeContent[:32])
	require.Len(t, handoff.RecentToolCalls, 3)
	require.Contains(t, handoff.RecentToolCalls[0].Input, `"file_path":"backend/tests/test_api.py"`)
	require.Contains(t, handoff.RecentToolCalls[0].Input, `redacted len=2000`)
	require.NotContains(t, handoff.RecentToolCalls[0].Input, largeContent[:64])
	require.Less(t, len(handoff.RecentToolCalls[0].Input), 200)
}
