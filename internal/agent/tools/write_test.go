package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/stretchr/testify/require"
)

type mockFileTrackerService struct{}

func (m *mockFileTrackerService) RecordRead(ctx context.Context, sessionID, path string) {}

func (m *mockFileTrackerService) LastReadTime(ctx context.Context, sessionID, path string) (lastReadTime time.Time) {
	return lastReadTime
}

func (m *mockFileTrackerService) ListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	return nil, nil
}

func TestWriteTool_AllowsEmptyContentForNewFiles(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	tool := newWriteToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runWriteTool(t, tool, ctx, WriteParams{
		FilePath: "backend/app/__init__.py",
		Content:  "",
	})

	require.False(t, resp.IsError)
	targetPath := filepath.Join(workingDir, "backend", "app", "__init__.py")
	data, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	require.Empty(t, string(data))
}

func newWriteToolForTest(workingDir string) fantasy.AgentTool {
	permissions := &mockPermissionService{}
	history := &mockHistoryService{}
	var tracker filetracker.Service = &mockFileTrackerService{}
	return NewWriteTool(nil, permissions, history, tracker, workingDir)
}

func runWriteTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params WriteParams) fantasy.ToolResponse {
	t.Helper()

	input, err := json.Marshal(params)
	require.NoError(t, err)

	call := fantasy.ToolCall{
		ID:    "test-call",
		Name:  WriteToolName,
		Input: string(input),
	}

	resp, err := tool.Run(ctx, call)
	require.NoError(t, err)
	return resp
}
