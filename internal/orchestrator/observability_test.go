package orchestrator

import (
	"context"
	"path/filepath"
	"testing"

	crushdb "github.com/charmbracelet/crush/internal/db"
	"github.com/stretchr/testify/require"
)

func TestMemoryDBPathFromEnv(t *testing.T) {
	t.Setenv(orchestratorDBEnv, `C:\tmp\memory.sqlite3`)
	t.Setenv(orchestratorProjectEnv, `C:\tmp\project`)

	require.Equal(t, `C:\tmp\memory.sqlite3`, MemoryDBPathFromEnv())
}

func TestLoadSessionObservability(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "memory.sqlite3")

	conn, err := crushdb.OpenPath(ctx, dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	schema := []string{
		`CREATE TABLE checkpoints (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			source TEXT NOT NULL,
			phase TEXT NOT NULL,
			task TEXT NOT NULL,
			summary TEXT NOT NULL DEFAULT '',
			metadata_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE decision_metrics (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			source TEXT NOT NULL,
			session_id TEXT,
			task_excerpt TEXT NOT NULL,
			task_hash TEXT NOT NULL,
			route TEXT NOT NULL,
			selected_model TEXT NOT NULL,
			requested_model TEXT NOT NULL DEFAULT '',
			alternatives_json TEXT NOT NULL DEFAULT '[]',
			confidence REAL,
			total_latency_ms REAL NOT NULL,
			classify_ms REAL NOT NULL DEFAULT 0,
			recall_local_ms REAL NOT NULL DEFAULT 0,
			recall_superlocalmemory_ms REAL NOT NULL DEFAULT 0,
			select_model_ms REAL NOT NULL DEFAULT 0,
			ensure_model_ms REAL NOT NULL DEFAULT 0,
			finalize_ms REAL NOT NULL DEFAULT 0,
			metadata_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE throughput_metrics (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			source TEXT NOT NULL,
			session_id TEXT,
			task_excerpt TEXT NOT NULL,
			task_hash TEXT NOT NULL,
			route TEXT NOT NULL,
			model TEXT NOT NULL,
			prompt_tokens INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			elapsed_seconds REAL NOT NULL DEFAULT 0,
			tokens_per_second REAL NOT NULL DEFAULT 0,
			success INTEGER NOT NULL DEFAULT 0,
			validated INTEGER NOT NULL DEFAULT 0,
			phase TEXT NOT NULL DEFAULT 'turn',
			metadata_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	}
	for _, stmt := range schema {
		_, err := conn.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}

	const sessionID = "session-123"
	const source = "crushlocal"

	_, err = conn.ExecContext(
		ctx,
		`INSERT INTO checkpoints (session_id, source, phase, task, summary, metadata_json, created_at)
		 VALUES
		 (?, ?, 'start', 'Reply with exactly ok', 'session started', '{"route":"messaging","selected_model":"qwen/qwen3.5-9b","client":"crushlocal","mid_session":1,"cwd":"C:\\projects\\test"}', '2026-05-03 00:11:50'),
		 (?, ?, 'end', 'Reply with exactly ok', 'ok', '{"route":"messaging","selected_model":"qwen/qwen3.5-9b","client":"crushlocal","mid_session":1,"assistant_chars":2,"tool_calls_count":0,"tool_results_count":0,"finish_reason":"end_turn","elapsed_seconds":20.1826755,"tokens_per_second":612.6}', '2026-05-03 00:12:11'),
		 ('other-session', ?, 'start', 'ignore me', 'session started', '{}', '2026-05-03 00:00:00')`,
		sessionID, source,
		sessionID, source,
		source,
	)
	require.NoError(t, err)

	_, err = conn.ExecContext(
		ctx,
		`INSERT INTO decision_metrics (source, session_id, task_excerpt, task_hash, route, selected_model, requested_model, confidence, total_latency_ms, metadata_json, created_at)
		 VALUES (?, ?, 'Reply with exactly ok', 'hash-1', 'messaging', 'qwen/qwen3.5-9b', '', 0.91, 4585.25, '{}', '2026-05-03 00:11:50')`,
		source,
		sessionID,
	)
	require.NoError(t, err)

	_, err = conn.ExecContext(
		ctx,
		`INSERT INTO throughput_metrics (source, session_id, task_excerpt, task_hash, route, model, prompt_tokens, completion_tokens, total_tokens, elapsed_seconds, tokens_per_second, success, validated, phase, metadata_json, created_at)
		 VALUES (?, ?, 'Reply with exactly ok', 'hash-1', 'messaging', 'qwen/qwen3.5-9b', 12362, 2, 12364, 20.1826755, 612.6, 1, 0, 'turn', '{}', '2026-05-03 00:12:11')`,
		source,
		sessionID,
	)
	require.NoError(t, err)

	t.Setenv(orchestratorDBEnv, dbPath)
	t.Setenv(orchestratorSourceEnv, source)

	obs, err := LoadSessionObservability(ctx, sessionID)
	require.NoError(t, err)
	require.NotNil(t, obs)
	require.Equal(t, dbPath, obs.DBPath)
	require.Equal(t, source, obs.Source)
	require.Len(t, obs.RecentCheckpoints, 2)

	require.Equal(t, "end", obs.RecentCheckpoints[0].Phase)
	require.Equal(t, "messaging", obs.RecentCheckpoints[0].Route)
	require.Equal(t, "qwen/qwen3.5-9b", obs.RecentCheckpoints[0].SelectedModel)
	require.Equal(t, 2, obs.RecentCheckpoints[0].AssistantChars)
	require.InDelta(t, 612.6, obs.RecentCheckpoints[0].TokensPerSecond, 0.001)

	require.NotNil(t, obs.LastDecision)
	require.Equal(t, "messaging", obs.LastDecision.Route)
	require.Equal(t, "qwen/qwen3.5-9b", obs.LastDecision.SelectedModel)
	require.InDelta(t, 4585.25, obs.LastDecision.TotalLatencyMS, 0.001)

	require.NotNil(t, obs.LastThroughput)
	require.Equal(t, "turn", obs.LastThroughput.Phase)
	require.Equal(t, 12364, obs.LastThroughput.TotalTokens)
	require.True(t, obs.LastThroughput.Success)
	require.False(t, obs.LastThroughput.Validated)
}

func TestLoadSessionObservabilityWithoutConfiguredDB(t *testing.T) {
	t.Setenv(orchestratorDBEnv, "")
	t.Setenv(orchestratorProjectEnv, "")

	obs, err := LoadSessionObservability(context.Background(), "session-123")
	require.NoError(t, err)
	require.Nil(t, obs)
}
