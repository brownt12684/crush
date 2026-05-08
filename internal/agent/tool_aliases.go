package agent

import (
	"context"
	"slices"
	"strings"

	"charm.land/fantasy"
	toolpkg "github.com/charmbracelet/crush/internal/agent/tools"
)

type toolAliasSource string

const (
	toolAliasSourceExact             toolAliasSource = "exact"
	toolAliasSourceCaseInsensitive   toolAliasSource = "case_insensitive"
	toolAliasSourceSeparatorVariant  toolAliasSource = "separator_variant"
	toolAliasSourceExplicitAlias     toolAliasSource = "alias"
	toolAliasSourceDeprecatedAlias   toolAliasSource = "deprecated_alias"
	toolAliasSourcePrefixedReference toolAliasSource = "prefixed_reference"
)

type toolNameResolution struct {
	RequestedToolName string   `json:"requested_tool_name"`
	CanonicalToolName string   `json:"canonical_tool_name,omitempty"`
	AliasApplied      bool     `json:"alias_applied,omitempty"`
	AliasSource       string   `json:"alias_source,omitempty"`
	Candidates        []string `json:"candidates,omitempty"`
	ExposedTools      []string `json:"exposed_tools,omitempty"`
	Ambiguous         bool     `json:"ambiguous,omitempty"`
}

type toolAliasRegistry struct {
	entries      map[string][]toolAliasEntry
	exposedTools []string
}

type toolAliasEntry struct {
	canonical string
	source    toolAliasSource
}

var toolCallPrefixes = []string{
	"assistant.",
	"assistant_",
	"functions.",
	"functions_",
	"tools.",
	"tools_",
}

func repairToolCallAliases(_ context.Context, options fantasy.ToolCallRepairOptions) (*fantasy.ToolCallContent, error) {
	resolution := resolveToolName(options.OriginalToolCall.ToolName, options.AvailableTools)
	if resolution.CanonicalToolName == "" || strings.EqualFold(resolution.CanonicalToolName, options.OriginalToolCall.ToolName) {
		return nil, nil
	}

	repaired := options.OriginalToolCall
	repaired.ToolName = resolution.CanonicalToolName
	return &repaired, nil
}

func resolveToolCallName(name string, availableTools []fantasy.AgentTool) (string, bool) {
	resolution := resolveToolName(name, availableTools)
	if resolution.CanonicalToolName == "" {
		return "", false
	}
	return resolution.CanonicalToolName, true
}

func resolveToolName(name string, availableTools []fantasy.AgentTool) toolNameResolution {
	registry := buildToolAliasRegistry(availableTools)
	return registry.Resolve(name)
}

func buildToolAliasRegistry(availableTools []fantasy.AgentTool) toolAliasRegistry {
	registry := toolAliasRegistry{
		entries: make(map[string][]toolAliasEntry),
	}

	exposed := make([]string, 0, len(availableTools))
	for _, tool := range availableTools {
		if tool == nil {
			continue
		}
		canonical := strings.TrimSpace(tool.Info().Name)
		if canonical == "" {
			continue
		}
		exposed = append(exposed, canonical)

		registry.register(canonical, canonical, toolAliasSourceExact)
		registry.registerSeparatorVariant(canonical, canonical, toolAliasSourceSeparatorVariant)

		for _, alias := range toolpkg.ToolAliases(tool) {
			registry.register(alias, canonical, toolAliasSourceExplicitAlias)
			registry.registerSeparatorVariant(alias, canonical, toolAliasSourceExplicitAlias)
		}
		for _, alias := range toolpkg.DeprecatedToolAliases(tool) {
			registry.register(alias, canonical, toolAliasSourceDeprecatedAlias)
			registry.registerSeparatorVariant(alias, canonical, toolAliasSourceDeprecatedAlias)
		}
	}

	registry.exposedTools = uniqueLowerStable(exposed)
	slices.Sort(registry.exposedTools)
	return registry
}

func (r toolAliasRegistry) register(alias string, canonical string, source toolAliasSource) {
	alias = strings.TrimSpace(alias)
	canonical = strings.TrimSpace(canonical)
	if alias == "" || canonical == "" {
		return
	}

	key := strings.ToLower(alias)
	entry := toolAliasEntry{
		canonical: canonical,
		source:    source,
	}

	existing := r.entries[key]
	for _, item := range existing {
		if item.canonical == entry.canonical && item.source == entry.source {
			return
		}
	}
	r.entries[key] = append(existing, entry)
}

func (r toolAliasRegistry) registerSeparatorVariant(alias string, canonical string, source toolAliasSource) {
	if strings.Contains(alias, "_") {
		r.register(strings.ReplaceAll(alias, "_", "-"), canonical, source)
	}
	if strings.Contains(alias, "-") {
		r.register(strings.ReplaceAll(alias, "-", "_"), canonical, source)
	}
}

func (r toolAliasRegistry) Resolve(name string) toolNameResolution {
	requested := strings.TrimSpace(name)
	resolution := toolNameResolution{
		RequestedToolName: requested,
		ExposedTools:      append([]string(nil), r.exposedTools...),
	}
	if requested == "" {
		return resolution
	}

	for _, candidate := range toolCallCandidates(requested) {
		matches := r.lookup(candidate)
		if len(matches) == 0 {
			continue
		}
		if len(matches) > 1 {
			resolution.Candidates = toolAliasCanonicalCandidates(matches)
			resolution.Ambiguous = true
			return resolution
		}

		match := matches[0]
		resolution.CanonicalToolName = match.canonical
		resolution.AliasApplied = requested != match.canonical || match.source != toolAliasSourceExact
		resolution.AliasSource = string(match.source)
		if requested != match.canonical && strings.EqualFold(requested, match.canonical) && match.source == toolAliasSourceExact {
			resolution.AliasSource = string(toolAliasSourceCaseInsensitive)
		}
		if candidate != requested && wasPrefixedReference(requested, candidate) {
			resolution.AliasSource = string(toolAliasSourcePrefixedReference)
		}
		if strings.EqualFold(candidate, match.canonical) && !strings.EqualFold(candidate, requested) && match.source == toolAliasSourceExact {
			resolution.AliasSource = string(toolAliasSourceCaseInsensitive)
		}
		return resolution
	}

	resolution.Candidates = r.suggest(requested)
	return resolution
}

func (r toolAliasRegistry) lookup(candidate string) []toolAliasEntry {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return nil
	}
	entries := append([]toolAliasEntry(nil), r.entries[strings.ToLower(candidate)]...)
	if len(entries) == 0 {
		return nil
	}

	if len(entries) == 1 {
		return entries
	}

	unique := make([]toolAliasEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if _, ok := seen[entry.canonical]; ok {
			continue
		}
		seen[entry.canonical] = struct{}{}
		unique = append(unique, entry)
	}
	return unique
}

func (r toolAliasRegistry) suggest(requested string) []string {
	normalizedRequested := normalizeSuggestionToken(requested)
	if normalizedRequested == "" {
		return nil
	}

	suggestions := make([]string, 0, len(r.exposedTools))
	for _, toolName := range r.exposedTools {
		normalizedTool := normalizeSuggestionToken(toolName)
		if normalizedTool == "" {
			continue
		}
		if strings.Contains(normalizedTool, normalizedRequested) || strings.Contains(normalizedRequested, normalizedTool) {
			suggestions = append(suggestions, toolName)
		}
	}
	return uniqueLowerStable(suggestions)
}

func toolCallCandidates(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}

	seen := map[string]struct{}{}
	candidates := make([]string, 0, 4)
	queue := []string{name}

	appendCandidate := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		key := strings.ToLower(candidate)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, candidate)
		queue = append(queue, candidate)
	}

	appendCandidate(name)

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		lowerCurrent := strings.ToLower(current)

		for _, prefix := range toolCallPrefixes {
			if strings.HasPrefix(lowerCurrent, prefix) {
				appendCandidate(current[len(prefix):])
			}
		}
	}

	return candidates
}

func toolAliasCanonicalCandidates(entries []toolAliasEntry) []string {
	candidates := make([]string, 0, len(entries))
	for _, entry := range entries {
		candidates = append(candidates, entry.canonical)
	}
	candidates = uniqueLowerStable(candidates)
	slices.Sort(candidates)
	return candidates
}

func uniqueLowerStable(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
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

func normalizeSuggestionToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "")
	value = strings.ReplaceAll(value, "-", "")
	value = strings.ReplaceAll(value, ".", "")
	return value
}

func wasPrefixedReference(requested string, candidate string) bool {
	requested = strings.ToLower(strings.TrimSpace(requested))
	candidate = strings.ToLower(strings.TrimSpace(candidate))
	if requested == "" || candidate == "" || requested == candidate {
		return false
	}
	for _, prefix := range toolCallPrefixes {
		if strings.HasPrefix(requested, prefix) && strings.TrimPrefix(requested, prefix) == candidate {
			return true
		}
	}
	return false
}
