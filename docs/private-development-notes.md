# Private Development Notes

This branch contains local orchestration and harness work for `crushlocal`
integration and observability.

## Scope

- Added per-turn stack-orchestrator checkpoint integration in the agent
  coordinator.
- Added orchestrator-selected system prompt append support.
- Added live session observability in `crush session show/last` by reading
  orchestrator SQLite state alongside Crush's local SQLite state.
- Added `PreTurn` and `PostTurn` hook events with richer hook payload/env
  metadata.
- Added Windows-aware hook shell selection.
- Strengthened loop detection for repeated failed tool calls.
- Added MCP name normalization aliases for `stack-orchestrator`.
- Added `todo` as an alias for `todos`.
- Tightened auto-summary handoff wording and interrupted-session prompt
  wrapping.

## Review Findings

These should be treated as known issues before publishing the work as a
stable fork baseline.

### 1. Orchestrator-selected model is persisted into global Crush config.

`internal/agent/coordinator.go` now calls `applySelectedModel`, which uses
`ConfigStore.UpdatePreferredModel(config.ScopeGlobal, ...)`.

This writes the orchestrator's per-turn model choice into the user's actual
global config file, which means:

- one session can permanently change the preferred model for later sessions
- concurrent sessions can race on global state
- route-driven temporary selections become sticky user preferences

This should likely be converted to an in-memory per-turn override instead of a
global config mutation.

### 2. Start and end checkpoints do not use the same normalized task for
attachment-driven turns.

`beginTurnOrchestration` uses `message.PromptWithTextAttachments(...)`, while
`finishTurnOrchestration` currently normalizes only `prompt`.

For turns driven primarily by pasted text attachments, this causes the start
checkpoint to have the real task while the end checkpoint can have an empty or
truncated task, which harms checkpoint correlation, memory attribution, and
throughput linking.

### 3. Windows PowerShell hook fallback does not preserve stdin-based hook
compatibility.

When `bash.exe` or `sh.exe` is unavailable, the hook runner falls back to
`pwsh.exe` or `powershell.exe` with `useStdin = false`.

That means hooks that depend on the historical JSON-on-stdin contract will not
receive stdin payload on those Windows systems, even though the env-based
payload is present. Existing hooks may silently stop working unless rewritten
to read `CRUSH_HOOK_PAYLOAD`.

## Validation Run

The following targeted tests were run successfully after the session
observability changes:

```text
go test ./internal/orchestrator ./internal/cmd ./internal/agent
```

## Local-Only Files

The following untracked files exist in this working tree but are workbench
helpers, not core Crush changes:

- `HANDOFF_PROMPT.md`
- `launch-codex.ps1`

Do not include them in the fork unless they are intentionally part of the
private development workflow.
