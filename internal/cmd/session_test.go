package cmd

import (
	"context"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

func TestBuildSessionShowMeta_ExposesRuntimeRepairState(t *testing.T) {
	t.Parallel()

	now := time.Now().Unix()
	sess := session.Session{
		ID:               "12345678-1234-1234-1234-1234567890ab",
		Title:            "Runtime repair test",
		CreatedAt:        now,
		UpdatedAt:        now,
		PromptTokens:     42,
		CompletionTokens: 24,
	}
	msgs := []*message.Message{
		{
			ID:        "msg-1",
			SessionID: sess.ID,
			Role:      message.Tool,
			CreatedAt: now,
			Parts: []message.ContentPart{
				message.ToolResult{
					ToolCallID: "call-1",
					Name:       "bash",
					Content:    "failure",
					IsError:    true,
					Metadata:   `{"repair":{"runtime":{"original_tool_name":"bash","attempt_count":1,"outcome":"succeeded","initial_failure":{"retryable":true,"failure_kind":"windows_path_translation_failure"}}}}`,
				},
			},
		},
	}

	meta := buildSessionShowMeta(context.Background(), sess, msgs)
	require.Len(t, meta.Repairs, 1)
	require.Equal(t, "runtime", meta.Repairs[0].Scope)
	require.Equal(t, "succeeded", meta.Repairs[0].Outcome)
	require.Equal(t, "windows_path_translation_failure", meta.Repairs[0].FailureKind)
	require.Contains(t, meta.Repairs[0].Summary, "runtime failure observed")
	require.Contains(t, meta.Repairs[0].Summary, "repair attempt started")
	require.Contains(t, meta.Repairs[0].Summary, "repair attempt succeeded")
}

func TestBuildSessionShowMeta_ExposesValidationRepairAliasAndInvalidToolState(t *testing.T) {
	t.Parallel()

	now := time.Now().Unix()
	sess := session.Session{
		ID:               "12345678-1234-1234-1234-1234567890ab",
		Title:            "Validation repair test",
		CreatedAt:        now,
		UpdatedAt:        now,
		PromptTokens:     42,
		CompletionTokens: 24,
	}
	msgs := []*message.Message{
		{
			ID:        "msg-1",
			SessionID: sess.ID,
			Role:      message.Tool,
			CreatedAt: now,
			Parts: []message.ContentPart{
				message.ToolResult{
					ToolCallID: "call-1",
					Name:       "view",
					Content:    "README contents",
					Metadata:   `{"repair":{"requested_tool_name":"read","original_tool_name":"read","canonical_tool_name":"view","resolved_tool_name":"view","alias_applied":true,"alias_source":"deprecated_alias","failure_kind":"alias_rewritten","repair_succeeded":true,"outcome":"succeeded"}}`,
				},
			},
		},
		{
			ID:        "msg-2",
			SessionID: sess.ID,
			Role:      message.Tool,
			CreatedAt: now + 1,
			Parts: []message.ContentPart{
				message.ToolResult{
					ToolCallID: "call-2",
					Name:       "viewer",
					Content:    "tool not found: viewer",
					IsError:    true,
					Metadata:   `{"repair":{"requested_tool_name":"viewer","original_tool_name":"viewer","failure_kind":"invalid_tool_name","candidates":["view"],"exposed_tools":["bash","view"],"repair_exhausted":true,"outcome":"exhausted"}}`,
				},
			},
		},
	}

	meta := buildSessionShowMeta(context.Background(), sess, msgs)
	require.Len(t, meta.Repairs, 2)

	require.Equal(t, "validation", meta.Repairs[0].Scope)
	require.Equal(t, "read", meta.Repairs[0].RequestedToolName)
	require.Equal(t, "view", meta.Repairs[0].CanonicalToolName)
	require.True(t, meta.Repairs[0].AliasApplied)
	require.Equal(t, "deprecated_alias", meta.Repairs[0].AliasSource)
	require.Contains(t, meta.Repairs[0].Summary, "alias rewritten to view")

	require.Equal(t, "validation", meta.Repairs[1].Scope)
	require.Equal(t, "invalid_tool_name", meta.Repairs[1].FailureKind)
	require.True(t, meta.Repairs[1].RepairExhausted)
	require.Equal(t, []string{"view"}, meta.Repairs[1].Candidates)
	require.Equal(t, []string{"bash", "view"}, meta.Repairs[1].ExposedTools)
	require.Contains(t, meta.Repairs[1].Summary, "viewer invalid tool name")
	require.Contains(t, meta.Repairs[1].Summary, "candidates=view")
}

func TestBuildSessionShowMeta_ExposesLoopHandoffState(t *testing.T) {
	t.Parallel()

	now := time.Now().Unix()
	sess := session.Session{
		ID:               "12345678-1234-1234-1234-1234567890ab",
		Title:            "Loop handoff test",
		CreatedAt:        now,
		UpdatedAt:        now,
		PromptTokens:     42,
		CompletionTokens: 24,
	}
	msgs := []*message.Message{
		{
			ID:        "msg-1",
			SessionID: sess.ID,
			Role:      message.Tool,
			CreatedAt: now,
			Parts: []message.ContentPart{
				message.ToolResult{
					ToolCallID: "call-1",
					Name:       "crush_info",
					Content:    "Crush detected a repeated tool loop and paused autonomy.",
					IsError:    true,
					Metadata:   `{"loop":{"tool_name":"crush_info","normalized_signature":"crush_info\u0000{}\u0000success_low_signal","classification":"repeated_low_signal_success","outcome_class":"success_low_signal","loop_kind":"repeated_meta_tool_probe","streak_count":3,"suggested_action":"request_supervisor","blocked_tool":true,"reason":"Detected 3 consecutive crush_info diagnostic probes without new context.","recent_tool_calls":[{"tool_name":"crush_info","input":"{}"}],"recent_tool_results":[{"tool_name":"crush_info","is_error":true,"summary":"blocked","retryable":false}],"supervisor_handoff_triggered":true}}`,
				},
			},
		},
	}

	meta := buildSessionShowMeta(context.Background(), sess, msgs)
	require.Len(t, meta.Loops, 1)
	require.Equal(t, "repeated_low_signal_success", meta.Loops[0].Classification)
	require.Equal(t, "success_low_signal", meta.Loops[0].OutcomeClass)
	require.Equal(t, "repeated_meta_tool_probe", meta.Loops[0].LoopKind)
	require.Equal(t, 3, meta.Loops[0].StreakCount)
	require.Equal(t, "request_supervisor", meta.Loops[0].SuggestedAction)
	require.True(t, meta.Loops[0].BlockedTool)
	require.Equal(t, "crush_info\u0000{}\u0000success_low_signal", meta.Loops[0].NormalizedSignature)
	require.Len(t, meta.Loops[0].RecentToolCalls, 1)
	require.Len(t, meta.Loops[0].RecentToolResults, 1)
	require.Contains(t, meta.Loops[0].Summary, "supervisor handoff triggered=true")
	require.Contains(t, meta.Loops[0].Summary, "blocked=true")
}
