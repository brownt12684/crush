package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Client struct {
	projectPath string
	uvCommand   string
	source      string
}

type PrepareResult struct {
	Route         string `json:"route"`
	SelectedModel string `json:"selected_model"`
	SystemAppend  string `json:"system_append"`
}

type CheckpointStartResult struct {
	SessionID string        `json:"session_id"`
	Prepare   PrepareResult `json:"prepare"`
}

func NewFromEnv() *Client {
	project := strings.TrimSpace(os.Getenv("CRUSH_ORCHESTRATOR_PROJECT"))
	if project == "" {
		return nil
	}
	uvCmd := strings.TrimSpace(os.Getenv("CRUSH_ORCHESTRATOR_UV"))
	if uvCmd == "" {
		uvCmd = "uv"
	}
	source := strings.TrimSpace(os.Getenv("CRUSH_ORCHESTRATOR_SOURCE"))
	if source == "" {
		source = "crushlocal"
	}
	return &Client{
		projectPath: project,
		uvCommand:   uvCmd,
		source:      source,
	}
}

func (c *Client) Enabled() bool {
	return c != nil && c.projectPath != ""
}

func (c *Client) CheckpointStart(
	ctx context.Context,
	sessionID string,
	task string,
	cwd string,
	ensureModel bool,
	metadata map[string]any,
) (*CheckpointStartResult, error) {
	if !c.Enabled() {
		return nil, nil
	}
	args := []string{
		"run",
		"--project", c.projectPath,
		"stack-orchestrator",
		"checkpoint-start",
		"--source", c.source,
		"--session-id", sessionID,
		"--task", task,
		"--cwd", cwd,
		"--json",
	}
	if ensureModel {
		args = append(args, "--ensure-model")
	}
	if len(metadata) > 0 {
		raw, err := json.Marshal(metadata)
		if err != nil {
			return nil, err
		}
		args = append(args, "--metadata-json", string(raw))
	}
	out, err := c.runJSON(ctx, args...)
	if err != nil {
		return nil, err
	}
	var result CheckpointStartResult
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("decode checkpoint-start response: %w", err)
	}
	return &result, nil
}

func (c *Client) CheckpointEnd(
	ctx context.Context,
	sessionID string,
	task string,
	outcomeSummary string,
	success bool,
	validated bool,
	metadata map[string]any,
) error {
	if !c.Enabled() {
		return nil
	}
	args := []string{
		"run",
		"--project", c.projectPath,
		"stack-orchestrator",
		"checkpoint-end",
		"--source", c.source,
		"--session-id", sessionID,
		"--task", task,
		"--outcome-summary", outcomeSummary,
		"--json",
	}
	if success {
		args = append(args, "--success")
	}
	if validated {
		args = append(args, "--validated")
	}
	if len(metadata) > 0 {
		raw, err := json.Marshal(metadata)
		if err != nil {
			return err
		}
		args = append(args, "--metadata-json", string(raw))
	}
	_, err := c.runJSON(ctx, args...)
	return err
}

func (c *Client) runJSON(ctx context.Context, args ...string) ([]byte, error) {
	if !c.Enabled() {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, c.uvCommand, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			return nil, fmt.Errorf("run orchestrator command: %w", err)
		}
		return nil, fmt.Errorf("orchestrator command failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return output, nil
}
