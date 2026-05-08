package tools

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

type mockBashPermissionService struct {
	*pubsub.Broker[permission.PermissionRequest]
}

func (m *mockBashPermissionService) Request(ctx context.Context, req permission.CreatePermissionRequest) (bool, error) {
	return true, nil
}

func (m *mockBashPermissionService) Grant(req permission.PermissionRequest) {}

func (m *mockBashPermissionService) Deny(req permission.PermissionRequest) {}

func (m *mockBashPermissionService) GrantPersistent(req permission.PermissionRequest) {}

func (m *mockBashPermissionService) AutoApproveSession(sessionID string) {}

func (m *mockBashPermissionService) SetSkipRequests(skip bool) {}

func (m *mockBashPermissionService) SkipRequests() bool {
	return false
}

func (m *mockBashPermissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
	return make(<-chan pubsub.Event[permission.PermissionNotification])
}

func TestBashTool_DefaultAutoBackgroundThreshold(t *testing.T) {
	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "default threshold",
		Command:     "echo done",
	})

	require.False(t, resp.IsError)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.False(t, meta.Background)
	require.Empty(t, meta.ShellID)
	require.Contains(t, meta.Output, "done")
}

func TestBashTool_CustomAutoBackgroundThreshold(t *testing.T) {
	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description:         "custom threshold",
		Command:             "i=0; while [ $i -lt 5000000 ]; do i=$((i+1)); done; echo done",
		AutoBackgroundAfter: 1,
	})

	require.False(t, resp.IsError)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.True(t, meta.Background)
	require.NotEmpty(t, meta.ShellID)
	require.Contains(t, resp.Content, "moved to background")

	bgManager := shell.GetBackgroundShellManager()
	require.NoError(t, bgManager.Kill(meta.ShellID))
}

func TestBashTool_NonZeroExitReturnsStructuredError(t *testing.T) {
	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "failing command",
		Command:     `echo nope >&2; exit 1`,
	})

	require.True(t, resp.IsError)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, 1, meta.ExitCode)
	require.Contains(t, meta.Stderr, "nope")
	require.Equal(t, `echo nope >&2; exit 1`, meta.Command)
	require.False(t, meta.Retryable)
}

func TestNormalizeBashInvocation_WindowsCDChain(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only normalization")
	}

	command, workingDir := normalizeBashInvocation(`cd C:\projects\foo && pwd`, `C:\base`)
	require.Equal(t, "pwd", command)
	require.Equal(t, `C:\projects\foo`, workingDir)
}

func TestNormalizeBashInvocation_WindowsExecutablePathWithSpaces(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only normalization")
	}

	command, workingDir := normalizeBashInvocation(`C:/Program Files/Python311/python.exe -m pip install pytest`, `C:\base`)
	require.Equal(t, `"C:/Program Files/Python311/python.exe" -m pip install pytest`, command)
	require.Equal(t, `C:\base`, workingDir)
}

func TestClassifyBashFailure_WindowsPathTranslation(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only classification")
	}

	retryable, failureKind := classifyBashFailure(
		`cd C:\projects\missing && pwd`,
		`C:\projects`,
		"",
		`C:projectsmissing: no such file or directory`,
		nil,
	)
	require.True(t, retryable)
	require.Equal(t, BashFailureKindWindowsPathTranslation, failureKind)
}

func TestClassifyBashFailure_WindowsExecutablePathWithSpaces(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only classification")
	}

	retryable, failureKind := classifyBashFailure(
		`C:/Program Files/Python311/python.exe -m pip install pytest`,
		`C:\projects`,
		"not found",
		"",
		nil,
	)
	require.True(t, retryable)
	require.Equal(t, BashFailureKindWindowsPathTranslation, failureKind)
}

func newBashToolForTest(workingDir string) fantasy.AgentTool {
	permissions := &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	return NewBashTool(permissions, workingDir, attribution, "test-model")
}

func runBashTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params BashParams) fantasy.ToolResponse {
	t.Helper()

	input, err := json.Marshal(params)
	require.NoError(t, err)

	call := fantasy.ToolCall{
		ID:    "test-call",
		Name:  BashToolName,
		Input: string(input),
	}

	resp, err := tool.Run(ctx, call)
	require.NoError(t, err)
	return resp
}
