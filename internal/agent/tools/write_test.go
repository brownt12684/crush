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
	"github.com/charmbracelet/crush/internal/history"
	"github.com/stretchr/testify/require"
)

type mockFileTrackerService struct {
	reads map[string]time.Time
}

func (m *mockFileTrackerService) RecordRead(ctx context.Context, sessionID, path string) {
	if m.reads == nil {
		m.reads = make(map[string]time.Time)
	}
	m.reads[sessionID+"|"+path] = time.Now().UTC().Truncate(time.Second)
}

func (m *mockFileTrackerService) LastReadTime(ctx context.Context, sessionID, path string) (lastReadTime time.Time) {
	if m.reads == nil {
		return time.Time{}
	}
	return m.reads[sessionID+"|"+path]
}

func (m *mockFileTrackerService) ListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	return nil, nil
}

type mockSessionHistoryService struct {
	*mockHistoryService
	file history.File
	err  error
}

func (m *mockSessionHistoryService) GetByPathAndSession(ctx context.Context, path, sessionID string) (history.File, error) {
	if m.err != nil {
		return history.File{}, m.err
	}
	return m.file, nil
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

func newWriteToolForTestWithDeps(
	workingDir string,
	historySvc history.Service,
	tracker filetracker.Service,
) fantasy.AgentTool {
	permissions := &mockPermissionService{}
	return NewWriteTool(nil, permissions, historySvc, tracker, workingDir)
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

func TestWriteTool_AllowsSessionOwnedRewriteWithoutExplicitReread(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	targetPath := filepath.Join(workingDir, "backend", "src", "review_console", "main.py")
	require.NoError(t, os.MkdirAll(filepath.Dir(targetPath), 0o755))
	require.NoError(t, os.WriteFile(targetPath, []byte("old\n"), 0o644))

	tracker := &mockFileTrackerService{}
	historySvc := &mockSessionHistoryService{
		mockHistoryService: &mockHistoryService{},
		file:               history.File{Path: targetPath, Content: "old\n"},
	}
	tool := newWriteToolForTestWithDeps(workingDir, historySvc, tracker)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runWriteTool(t, tool, ctx, WriteParams{
		FilePath: targetPath,
		Content:  "new\n",
	})

	require.False(t, resp.IsError)
	data, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	require.Equal(t, "new\n", string(data))
	require.False(t, tracker.LastReadTime(ctx, "test-session", targetPath).IsZero())
}

func TestWriteTool_BlocksRewriteWhenDiskContentDiffersFromSessionHistory(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	targetPath := filepath.Join(workingDir, "backend", "src", "review_console", "main.py")
	require.NoError(t, os.MkdirAll(filepath.Dir(targetPath), 0o755))
	require.NoError(t, os.WriteFile(targetPath, []byte("current\n"), 0o644))

	tracker := &mockFileTrackerService{}
	historySvc := &mockSessionHistoryService{
		mockHistoryService: &mockHistoryService{},
		file:               history.File{Path: targetPath, Content: "stale\n"},
	}
	tool := newWriteToolForTestWithDeps(workingDir, historySvc, tracker)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runWriteTool(t, tool, ctx, WriteParams{
		FilePath: targetPath,
		Content:  "new\n",
	})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "modified since it was last read")
}
