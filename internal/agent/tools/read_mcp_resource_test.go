package tools

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func TestReadMCPResourceTool_RejectsCrushSkillURI(t *testing.T) {
	t.Parallel()

	tool := NewReadMCPResourceTool(nil, nil)
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  ReadMCPResourceToolName,
		Input: `{"mcp_name":"stack-orchestrator","uri":"crush://skills/jq/SKILL.md"}`,
	})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "Do not use read_mcp_resource to load Crush skills")
}
