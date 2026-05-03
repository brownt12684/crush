package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/crush/internal/db"
)

const (
	defaultSource          = "crushlocal"
	orchestratorDBEnv      = "CRUSH_ORCHESTRATOR_DB"
	orchestratorProjectEnv = "CRUSH_ORCHESTRATOR_PROJECT"
	orchestratorSourceEnv  = "CRUSH_ORCHESTRATOR_SOURCE"
	defaultCheckpointLimit = 6
)

type SessionObservability struct {
	Source            string                  `json:"source"`
	DBPath            string                  `json:"db_path"`
	RecentCheckpoints []CheckpointObservation `json:"recent_checkpoints,omitempty"`
	LastDecision      *DecisionObservation    `json:"last_decision,omitempty"`
	LastThroughput    *ThroughputObservation  `json:"last_throughput,omitempty"`
}

type CheckpointObservation struct {
	Phase            string  `json:"phase"`
	Task             string  `json:"task,omitempty"`
	Summary          string  `json:"summary,omitempty"`
	CreatedAt        string  `json:"created_at"`
	Route            string  `json:"route,omitempty"`
	SelectedModel    string  `json:"selected_model,omitempty"`
	Client           string  `json:"client,omitempty"`
	MidSession       bool    `json:"mid_session,omitempty"`
	CWD              string  `json:"cwd,omitempty"`
	AssistantChars   int     `json:"assistant_chars,omitempty"`
	ToolCallsCount   int     `json:"tool_calls_count,omitempty"`
	ToolResultsCount int     `json:"tool_results_count,omitempty"`
	FinishReason     string  `json:"finish_reason,omitempty"`
	ElapsedSeconds   float64 `json:"elapsed_seconds,omitempty"`
	TokensPerSecond  float64 `json:"tokens_per_second,omitempty"`
	Error            string  `json:"error,omitempty"`
}

type DecisionObservation struct {
	Route          string  `json:"route,omitempty"`
	SelectedModel  string  `json:"selected_model,omitempty"`
	RequestedModel string  `json:"requested_model,omitempty"`
	Confidence     float64 `json:"confidence,omitempty"`
	TotalLatencyMS float64 `json:"total_latency_ms,omitempty"`
	CreatedAt      string  `json:"created_at"`
}

type ThroughputObservation struct {
	Route            string  `json:"route,omitempty"`
	Model            string  `json:"model,omitempty"`
	PromptTokens     int     `json:"prompt_tokens,omitempty"`
	CompletionTokens int     `json:"completion_tokens,omitempty"`
	TotalTokens      int     `json:"total_tokens,omitempty"`
	ElapsedSeconds   float64 `json:"elapsed_seconds,omitempty"`
	TokensPerSecond  float64 `json:"tokens_per_second,omitempty"`
	Success          bool    `json:"success,omitempty"`
	Validated        bool    `json:"validated,omitempty"`
	Phase            string  `json:"phase,omitempty"`
	CreatedAt        string  `json:"created_at"`
}

func SourceFromEnv() string {
	source := strings.TrimSpace(os.Getenv(orchestratorSourceEnv))
	if source == "" {
		return defaultSource
	}
	return source
}

func MemoryDBPathFromEnv() string {
	if dbPath := strings.TrimSpace(os.Getenv(orchestratorDBEnv)); dbPath != "" {
		return dbPath
	}
	project := strings.TrimSpace(os.Getenv(orchestratorProjectEnv))
	if project == "" {
		return ""
	}
	return filepath.Join(project, "data", "memory.sqlite3")
}

func LoadSessionObservability(ctx context.Context, sessionID string) (*SessionObservability, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}

	dbPath := MemoryDBPathFromEnv()
	if dbPath == "" {
		return nil, nil
	}
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat orchestrator db: %w", err)
	}

	conn, err := db.OpenPath(ctx, dbPath)
	if err != nil {
		return nil, fmt.Errorf("open orchestrator db: %w", err)
	}
	defer conn.Close()

	source := SourceFromEnv()
	observability := &SessionObservability{
		Source: source,
		DBPath: dbPath,
	}

	observability.RecentCheckpoints, err = loadCheckpoints(ctx, conn, source, sessionID, defaultCheckpointLimit)
	if err != nil {
		return nil, err
	}
	observability.LastDecision, err = loadDecision(ctx, conn, source, sessionID)
	if err != nil {
		return nil, err
	}
	observability.LastThroughput, err = loadThroughput(ctx, conn, source, sessionID)
	if err != nil {
		return nil, err
	}

	if len(observability.RecentCheckpoints) == 0 && observability.LastDecision == nil && observability.LastThroughput == nil {
		return nil, nil
	}

	return observability, nil
}

func loadCheckpoints(ctx context.Context, conn *sql.DB, source string, sessionID string, limit int) ([]CheckpointObservation, error) {
	const query = `
SELECT
	phase,
	task,
	summary,
	created_at,
	COALESCE(CAST(json_extract(metadata_json, '$.route') AS TEXT), ''),
	COALESCE(CAST(json_extract(metadata_json, '$.selected_model') AS TEXT), ''),
	COALESCE(CAST(json_extract(metadata_json, '$.client') AS TEXT), ''),
	COALESCE(CAST(json_extract(metadata_json, '$.mid_session') AS INTEGER), 0),
	COALESCE(CAST(json_extract(metadata_json, '$.cwd') AS TEXT), ''),
	COALESCE(CAST(json_extract(metadata_json, '$.assistant_chars') AS INTEGER), 0),
	COALESCE(CAST(json_extract(metadata_json, '$.tool_calls_count') AS INTEGER), 0),
	COALESCE(CAST(json_extract(metadata_json, '$.tool_results_count') AS INTEGER), 0),
	COALESCE(CAST(json_extract(metadata_json, '$.finish_reason') AS TEXT), ''),
	COALESCE(CAST(json_extract(metadata_json, '$.elapsed_seconds') AS REAL), 0),
	COALESCE(CAST(json_extract(metadata_json, '$.tokens_per_second') AS REAL), 0),
	COALESCE(CAST(json_extract(metadata_json, '$.error') AS TEXT), '')
FROM checkpoints
WHERE source = ? AND session_id = ?
ORDER BY id DESC
LIMIT ?`

	rows, err := conn.QueryContext(ctx, query, source, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("query checkpoints: %w", err)
	}
	defer rows.Close()

	var checkpoints []CheckpointObservation
	for rows.Next() {
		var (
			item           CheckpointObservation
			midSession     int64
			assistantChars int64
			toolCalls      int64
			toolResults    int64
		)
		if err := rows.Scan(
			&item.Phase,
			&item.Task,
			&item.Summary,
			&item.CreatedAt,
			&item.Route,
			&item.SelectedModel,
			&item.Client,
			&midSession,
			&item.CWD,
			&assistantChars,
			&toolCalls,
			&toolResults,
			&item.FinishReason,
			&item.ElapsedSeconds,
			&item.TokensPerSecond,
			&item.Error,
		); err != nil {
			return nil, fmt.Errorf("scan checkpoint: %w", err)
		}
		item.MidSession = midSession != 0
		item.AssistantChars = int(assistantChars)
		item.ToolCallsCount = int(toolCalls)
		item.ToolResultsCount = int(toolResults)
		item.Task = strings.TrimSpace(item.Task)
		item.Summary = strings.TrimSpace(item.Summary)
		checkpoints = append(checkpoints, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate checkpoints: %w", err)
	}
	return checkpoints, nil
}

func loadDecision(ctx context.Context, conn *sql.DB, source string, sessionID string) (*DecisionObservation, error) {
	const query = `
SELECT route, selected_model, requested_model, COALESCE(confidence, 0), total_latency_ms, created_at
FROM decision_metrics
WHERE source = ? AND session_id = ?
ORDER BY id DESC
LIMIT 1`

	var item DecisionObservation
	err := conn.QueryRowContext(ctx, query, source, sessionID).Scan(
		&item.Route,
		&item.SelectedModel,
		&item.RequestedModel,
		&item.Confidence,
		&item.TotalLatencyMS,
		&item.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query decision metric: %w", err)
	}
	return &item, nil
}

func loadThroughput(ctx context.Context, conn *sql.DB, source string, sessionID string) (*ThroughputObservation, error) {
	const query = `
SELECT
	route,
	model,
	prompt_tokens,
	completion_tokens,
	total_tokens,
	elapsed_seconds,
	tokens_per_second,
	success,
	validated,
	phase,
	created_at
FROM throughput_metrics
WHERE source = ? AND session_id = ?
ORDER BY id DESC
LIMIT 1`

	var (
		item      ThroughputObservation
		success   int64
		validated int64
	)
	err := conn.QueryRowContext(ctx, query, source, sessionID).Scan(
		&item.Route,
		&item.Model,
		&item.PromptTokens,
		&item.CompletionTokens,
		&item.TotalTokens,
		&item.ElapsedSeconds,
		&item.TokensPerSecond,
		&success,
		&validated,
		&item.Phase,
		&item.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query throughput metric: %w", err)
	}
	item.Success = success != 0
	item.Validated = validated != 0
	return &item, nil
}
