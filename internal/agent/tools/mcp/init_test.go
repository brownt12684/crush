package mcp

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMCPSession_CancelOnClose(t *testing.T) {
	defer goleak.VerifyNone(t)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	server := mcp.NewServer(&mcp.Implementation{Name: "test-server"}, nil)
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	require.NoError(t, err)
	defer serverSession.Close()

	ctx, cancel := context.WithCancel(context.Background())

	client := mcp.NewClient(&mcp.Implementation{Name: "crush-test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)

	sess := &ClientSession{clientSession, cancel}

	// Verify the context is not cancelled before close.
	require.NoError(t, ctx.Err())

	err = sess.Close()
	require.NoError(t, err)

	// After Close, the context must be cancelled.
	require.ErrorIs(t, ctx.Err(), context.Canceled)
}

func TestNormalizeMCPName(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"stack-orchestrator":  "stack-orchestrator",
		"orchestrator":        "stack-orchestrator",
		"orchestrator/memory": "stack-orchestrator",
		"memory/orchestrator": "stack-orchestrator",
		"stack_orchestrator":  "stack-orchestrator",
		"docker":              "docker-mcp",
		"docker/mcp":          "docker-mcp",
		" custom-name ":       "custom-name",
	}

	for input, expected := range cases {
		require.Equal(t, expected, normalizeMCPName(input), input)
	}
}
