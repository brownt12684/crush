package hooks

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/tidwall/gjson"
)

// SupportedOutputVersion is the highest envelope version this build
// understands. Hooks may omit `version` entirely (treated as 1) or pin
// an older version. Unknown higher versions are still parsed but logged.
const SupportedOutputVersion = 1

// Payload is the JSON structure piped to hook commands via stdin.
// ToolInput is emitted as a parsed JSON object for compatibility with
// Claude Code hooks (which expect tool_input to be an object, not a
// string).
type Payload struct {
	Event             string          `json:"event"`
	SessionID         string          `json:"session_id"`
	CWD               string          `json:"cwd"`
	ToolName          string          `json:"tool_name,omitempty"`
	ToolInput         json.RawMessage `json:"tool_input,omitempty"`
	Prompt            string          `json:"prompt,omitempty"`
	AssistantResponse string          `json:"assistant_response,omitempty"`
	FinishReason      string          `json:"finish_reason,omitempty"`
	Model             string          `json:"model,omitempty"`
	Provider          string          `json:"provider,omitempty"`
	Error             string          `json:"error,omitempty"`
	DurationMS        int64           `json:"duration_ms,omitempty"`
	NonInteractive    bool            `json:"non_interactive,omitempty"`
	ToolCalls         []string        `json:"tool_calls,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
}

// EventInput carries hook event metadata for both tool and lifecycle events.
type EventInput struct {
	SessionID         string
	ToolName          string
	ToolInputJSON     string
	Prompt            string
	AssistantResponse string
	FinishReason      string
	Model             string
	Provider          string
	Error             string
	MetadataJSON      string
	DurationMS        int64
	NonInteractive    bool
	ToolCalls         []string
}

// BuildPayload constructs the JSON stdin payload for a hook command.
func BuildPayload(eventName, sessionID, cwd, toolName, toolInputJSON string) []byte {
	return BuildPayloadFromInput(eventName, cwd, EventInput{
		SessionID:     sessionID,
		ToolName:      toolName,
		ToolInputJSON: toolInputJSON,
	})
}

// BuildPayloadFromInput constructs the JSON stdin payload for a hook command.
func BuildPayloadFromInput(eventName, cwd string, input EventInput) []byte {
	var toolInput json.RawMessage
	if json.Valid([]byte(input.ToolInputJSON)) {
		toolInput = json.RawMessage(input.ToolInputJSON)
	}

	var metadata json.RawMessage
	if json.Valid([]byte(input.MetadataJSON)) {
		metadata = json.RawMessage(input.MetadataJSON)
	}

	p := Payload{
		Event:             eventName,
		SessionID:         input.SessionID,
		CWD:               cwd,
		ToolName:          input.ToolName,
		ToolInput:         toolInput,
		Prompt:            input.Prompt,
		AssistantResponse: input.AssistantResponse,
		FinishReason:      input.FinishReason,
		Model:             input.Model,
		Provider:          input.Provider,
		Error:             input.Error,
		DurationMS:        input.DurationMS,
		NonInteractive:    input.NonInteractive,
		ToolCalls:         input.ToolCalls,
		Metadata:          metadata,
	}
	data, err := json.Marshal(p)
	if err != nil {
		return []byte("{}")
	}
	return data
}

// BuildEnv constructs the environment variable slice for a hook command.
// It includes all current process env vars plus hook-specific ones.
func BuildEnv(eventName, toolName, sessionID, cwd, projectDir, toolInputJSON string) []string {
	return BuildEnvFromInput(eventName, cwd, projectDir, EventInput{
		SessionID:     sessionID,
		ToolName:      toolName,
		ToolInputJSON: toolInputJSON,
	})
}

// BuildEnvFromInput constructs the environment variable slice for a hook command.
func BuildEnvFromInput(eventName, cwd, projectDir string, input EventInput) []string {
	env := os.Environ()
	payloadJSON := string(BuildPayloadFromInput(eventName, cwd, input))
	env = append(env,
		fmt.Sprintf("CRUSH_EVENT=%s", eventName),
		fmt.Sprintf("CRUSH_TOOL_NAME=%s", input.ToolName),
		fmt.Sprintf("CRUSH_SESSION_ID=%s", input.SessionID),
		fmt.Sprintf("CRUSH_CWD=%s", cwd),
		fmt.Sprintf("CRUSH_PROJECT_DIR=%s", projectDir),
		fmt.Sprintf("CRUSH_HOOK_PAYLOAD=%s", payloadJSON),
	)
	if input.Prompt != "" {
		env = append(env, fmt.Sprintf("CRUSH_PROMPT=%s", input.Prompt))
	}
	if input.AssistantResponse != "" {
		env = append(env, fmt.Sprintf("CRUSH_ASSISTANT_RESPONSE=%s", input.AssistantResponse))
	}
	if input.FinishReason != "" {
		env = append(env, fmt.Sprintf("CRUSH_FINISH_REASON=%s", input.FinishReason))
	}
	if input.Model != "" {
		env = append(env, fmt.Sprintf("CRUSH_MODEL=%s", input.Model))
	}
	if input.Provider != "" {
		env = append(env, fmt.Sprintf("CRUSH_PROVIDER=%s", input.Provider))
	}
	if input.Error != "" {
		env = append(env, fmt.Sprintf("CRUSH_ERROR=%s", input.Error))
	}
	if input.DurationMS > 0 {
		env = append(env, fmt.Sprintf("CRUSH_DURATION_MS=%d", input.DurationMS))
	}
	if input.NonInteractive {
		env = append(env, "CRUSH_NON_INTERACTIVE=1")
	}
	if len(input.ToolCalls) > 0 {
		env = append(env, fmt.Sprintf("CRUSH_TOOL_CALLS=%s", strings.Join(input.ToolCalls, ",")))
	}
	if input.MetadataJSON != "" {
		env = append(env, fmt.Sprintf("CRUSH_METADATA=%s", input.MetadataJSON))
	}

	// Extract tool-specific env vars from the JSON input.
	if input.ToolInputJSON != "" {
		if cmd := gjson.Get(input.ToolInputJSON, "command"); cmd.Exists() {
			env = append(env, fmt.Sprintf("CRUSH_TOOL_INPUT_COMMAND=%s", cmd.String()))
		}
		if fp := gjson.Get(input.ToolInputJSON, "file_path"); fp.Exists() {
			env = append(env, fmt.Sprintf("CRUSH_TOOL_INPUT_FILE_PATH=%s", fp.String()))
		}
	}

	return env
}

// parseStdout parses the JSON output from a hook command's stdout.
// Supports both Crush format and Claude Code format (hookSpecificOutput).
func parseStdout(stdout string) HookResult {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return HookResult{Decision: DecisionNone}
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		return HookResult{Decision: DecisionNone}
	}

	// Claude Code compat: if hookSpecificOutput is present, parse that.
	if hso, ok := raw["hookSpecificOutput"]; ok {
		return parseClaudeCodeOutput(hso)
	}

	var parsed struct {
		Version      int             `json:"version"`
		Decision     string          `json:"decision"`
		Halt         bool            `json:"halt"`
		Reason       string          `json:"reason"`
		Context      json.RawMessage `json:"context"`
		UpdatedInput json.RawMessage `json:"updated_input"`
	}
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		return HookResult{Decision: DecisionNone}
	}

	if parsed.Version > SupportedOutputVersion {
		slog.Debug("Hook output declared a newer envelope version than this build supports",
			"version", parsed.Version,
			"supported", SupportedOutputVersion,
		)
	}

	result := HookResult{
		Halt:    parsed.Halt,
		Reason:  parsed.Reason,
		Context: parseContext(parsed.Context),
	}
	result.Decision = parseDecision(parsed.Decision)
	result.UpdatedInput = rawToString(parsed.UpdatedInput)
	return result
}

// parseContext accepts either a single string or an array of strings and
// returns a newline-joined value with empty entries dropped.
func parseContext(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// String form.
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		return ""
	}
	// Array form.
	if raw[0] == '[' {
		var items []string
		if err := json.Unmarshal(raw, &items); err != nil {
			return ""
		}
		out := items[:0]
		for _, s := range items {
			if s != "" {
				out = append(out, s)
			}
		}
		return strings.Join(out, "\n")
	}
	return ""
}

// parseClaudeCodeOutput handles the Claude Code hook output format:
// {"hookSpecificOutput": {"permissionDecision": "allow", ...}}
func parseClaudeCodeOutput(data json.RawMessage) HookResult {
	var hso struct {
		PermissionDecision       string          `json:"permissionDecision"`
		PermissionDecisionReason string          `json:"permissionDecisionReason"`
		UpdatedInput             json.RawMessage `json:"updatedInput"`
	}
	if err := json.Unmarshal(data, &hso); err != nil {
		return HookResult{Decision: DecisionNone}
	}

	result := HookResult{
		Decision: parseDecision(hso.PermissionDecision),
		Reason:   hso.PermissionDecisionReason,
	}

	// Marshal updatedInput back to a string for our opaque format.
	if len(hso.UpdatedInput) > 0 && string(hso.UpdatedInput) != "null" {
		result.UpdatedInput = string(hso.UpdatedInput)
	}

	return result
}

// rawToString converts a json.RawMessage to a string suitable for use
// as opaque tool input. It accepts both a JSON object (nested) and a
// JSON string (stringified, for backward compatibility).
func rawToString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// If it's a JSON string, unwrap it.
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	}
	// Otherwise it's an object/array — use as-is.
	return string(raw)
}

func parseDecision(s string) Decision {
	switch strings.ToLower(s) {
	case "allow":
		return DecisionAllow
	case "deny":
		return DecisionDeny
	default:
		return DecisionNone
	}
}
