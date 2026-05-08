package tools

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"charm.land/fantasy"
)

type (
	sessionIDContextKey string
	messageIDContextKey string
	supportsImagesKey   string
	modelNameKey        string
)

const (
	// SessionIDContextKey is the key for the session ID in the context.
	SessionIDContextKey sessionIDContextKey = "session_id"
	// MessageIDContextKey is the key for the message ID in the context.
	MessageIDContextKey messageIDContextKey = "message_id"
	// SupportsImagesContextKey is the key for the model's image support capability.
	SupportsImagesContextKey supportsImagesKey = "supports_images"
	// ModelNameContextKey is the key for the model name in the context.
	ModelNameContextKey modelNameKey = "model_name"
)

// getContextValue is a generic helper that retrieves a typed value from context.
// If the value is not found or has the wrong type, it returns the default value.
func getContextValue[T any](ctx context.Context, key any, defaultValue T) T {
	value := ctx.Value(key)
	if value == nil {
		return defaultValue
	}
	if typedValue, ok := value.(T); ok {
		return typedValue
	}
	return defaultValue
}

// GetSessionFromContext retrieves the session ID from the context.
func GetSessionFromContext(ctx context.Context) string {
	return getContextValue(ctx, SessionIDContextKey, "")
}

// GetMessageFromContext retrieves the message ID from the context.
func GetMessageFromContext(ctx context.Context) string {
	return getContextValue(ctx, MessageIDContextKey, "")
}

// GetSupportsImagesFromContext retrieves whether the model supports images from the context.
func GetSupportsImagesFromContext(ctx context.Context) bool {
	return getContextValue(ctx, SupportsImagesContextKey, false)
}

// GetModelNameFromContext retrieves the model name from the context.
func GetModelNameFromContext(ctx context.Context) string {
	return getContextValue(ctx, ModelNameContextKey, "")
}

// NewPermissionDeniedResponse returns a tool response indicating the user
// denied permission, with StopTurn set so the agent loop does not retry.
func NewPermissionDeniedResponse() fantasy.ToolResponse {
	resp := fantasy.NewTextErrorResponse("User denied permission")
	resp.StopTurn = true
	return resp
}

// FirstLineDescription returns just the first non-empty line from the embedded
// markdown description. The full description can be used by setting
// CRUSH_SHORT_TOOL_DESCRIPTIONS=0.
func FirstLineDescription(content []byte) string {
	if !testing.Testing() {
		if v, err := strconv.ParseBool(os.Getenv("CRUSH_SHORT_TOOL_DESCRIPTIONS")); err == nil && !v {
			return strings.TrimSpace(string(content))
		}
	}
	for line := range strings.SplitSeq(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

type toolAliasProvider interface {
	ToolAliases() []string
	DeprecatedToolAliases() []string
}

type aliasedTool struct {
	inner             fantasy.AgentTool
	aliases           []string
	deprecatedAliases []string
}

func WithAliases(tool fantasy.AgentTool, aliases []string, deprecatedAliases []string) fantasy.AgentTool {
	if tool == nil {
		return nil
	}

	aliases = normalizeAliasList(aliases)
	deprecatedAliases = normalizeAliasList(deprecatedAliases)
	if len(aliases) == 0 && len(deprecatedAliases) == 0 {
		return tool
	}

	return &aliasedTool{
		inner:             tool,
		aliases:           aliases,
		deprecatedAliases: deprecatedAliases,
	}
}

func (t *aliasedTool) Info() fantasy.ToolInfo {
	return t.inner.Info()
}

func (t *aliasedTool) Run(ctx context.Context, params fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return t.inner.Run(ctx, params)
}

func (t *aliasedTool) ProviderOptions() fantasy.ProviderOptions {
	return t.inner.ProviderOptions()
}

func (t *aliasedTool) SetProviderOptions(opts fantasy.ProviderOptions) {
	t.inner.SetProviderOptions(opts)
}

func (t *aliasedTool) ToolAliases() []string {
	return append([]string(nil), t.aliases...)
}

func (t *aliasedTool) DeprecatedToolAliases() []string {
	return append([]string(nil), t.deprecatedAliases...)
}

func ToolAliases(tool fantasy.AgentTool) []string {
	provider, ok := tool.(toolAliasProvider)
	if !ok {
		return nil
	}
	return append([]string(nil), provider.ToolAliases()...)
}

func DeprecatedToolAliases(tool fantasy.AgentTool) []string {
	provider, ok := tool.(toolAliasProvider)
	if !ok {
		return nil
	}
	return append([]string(nil), provider.DeprecatedToolAliases()...)
}

func normalizeAliasList(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}
