package agent

import (
	"testing"

	"charm.land/fantasy"
	toolpkg "github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/stretchr/testify/require"
)

func TestResolveToolCallName(t *testing.T) {
	t.Parallel()

	available := []fantasy.AgentTool{
		toolpkg.WithAliases(&fakeTool{name: "bash"}, []string{"execute"}, []string{"bash_commands"}),
		toolpkg.WithAliases(&fakeTool{name: "view"}, nil, []string{"read"}),
		&fakeTool{name: "ls"},
		&fakeTool{name: "list-files"},
	}

	tests := []struct {
		name     string
		toolName string
		want     string
		ok       bool
	}{
		{name: "read alias", toolName: "read", want: "view", ok: true},
		{name: "case insensitive alias", toolName: "Read", want: "view", ok: true},
		{name: "assistant prefix", toolName: "assistant.view", want: "view", ok: true},
		{name: "assistant prefix plus alias", toolName: "assistant.read", want: "view", ok: true},
		{name: "functions prefix", toolName: "functions.Bash", want: "bash", ok: true},
		{name: "bash commands alias", toolName: "bash_commands", want: "bash", ok: true},
		{name: "execute alias", toolName: "execute", want: "bash", ok: true},
		{name: "case insensitive canonical", toolName: "VIEW", want: "view", ok: true},
		{name: "separator variant canonical", toolName: "list_files", want: "list-files", ok: true},
		{name: "unknown tool", toolName: "nonexistent", want: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := resolveToolCallName(tt.toolName, available)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestResolveToolCallName_RequiresAvailableTarget(t *testing.T) {
	t.Parallel()

	available := []fantasy.AgentTool{
		toolpkg.WithAliases(&fakeTool{name: "view"}, nil, []string{"read"}),
	}

	got, ok := resolveToolCallName("execute", available)
	require.False(t, ok)
	require.Empty(t, got)
}

func TestResolveToolName_AmbiguousAlias(t *testing.T) {
	t.Parallel()

	available := []fantasy.AgentTool{
		toolpkg.WithAliases(&fakeTool{name: "view"}, []string{"inspect"}, nil),
		toolpkg.WithAliases(&fakeTool{name: "fetch"}, []string{"inspect"}, nil),
	}

	resolution := resolveToolName("inspect", available)
	require.True(t, resolution.Ambiguous)
	require.Empty(t, resolution.CanonicalToolName)
	require.Equal(t, []string{"fetch", "view"}, resolution.Candidates)
	require.Equal(t, []string{"fetch", "view"}, resolution.ExposedTools)
}

func TestRepairToolCallAliases(t *testing.T) {
	t.Parallel()

	available := []fantasy.AgentTool{
		toolpkg.WithAliases(&fakeTool{name: "bash"}, []string{"execute"}, []string{"bash_commands"}),
		toolpkg.WithAliases(&fakeTool{name: "view"}, nil, []string{"read"}),
	}

	repaired, err := repairToolCallAliases(t.Context(), fantasy.ToolCallRepairOptions{
		OriginalToolCall: fantasy.ToolCallContent{
			ToolCallID: "call-1",
			ToolName:   "assistant.read",
			Input:      `{"file_path":"README.md"}`,
		},
		AvailableTools: available,
	})
	require.NoError(t, err)
	require.NotNil(t, repaired)
	require.Equal(t, "view", repaired.ToolName)
	require.Equal(t, `{"file_path":"README.md"}`, repaired.Input)

	unmodified, err := repairToolCallAliases(t.Context(), fantasy.ToolCallRepairOptions{
		OriginalToolCall: fantasy.ToolCallContent{
			ToolCallID: "call-2",
			ToolName:   "view",
			Input:      `{"file_path":"README.md"}`,
		},
		AvailableTools: available,
	})
	require.NoError(t, err)
	require.Nil(t, unmodified)
}
