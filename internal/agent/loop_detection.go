package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"io"

	"charm.land/fantasy"
)

const (
	loopDetectionWindowSize = 10
	loopDetectionMaxRepeats = 5
	// Failed tool calls are much stronger evidence of a bad loop than
	// repeated successful reads, so cut these off far earlier.
	loopDetectionFailedMaxRepeats = 2
)

// hasRepeatedToolCalls checks whether the agent is stuck in a loop by looking
// at recent steps. It examines the last windowSize steps and returns true if:
//   - any tool-call signature appears more than maxRepeats times across a full
//     window, or
//   - any failed individual tool interaction appears more than
//     loopDetectionFailedMaxRepeats times, even before a full window fills.
func hasRepeatedToolCalls(steps []fantasy.StepResult, windowSize, maxRepeats int) bool {
	if len(steps) == 0 {
		return false
	}

	windowStart := 0
	if len(steps) > windowSize {
		windowStart = len(steps) - windowSize
	}
	window := steps[windowStart:]
	enforceGeneralThreshold := len(steps) >= windowSize
	counts := make(map[string]int)
	failedCounts := make(map[string]int)

	for _, step := range window {
		signatures := getToolInteractionSignatures(step.Content)
		for _, sig := range signatures {
			if sig.signature == "" {
				continue
			}
			counts[sig.signature]++
			if enforceGeneralThreshold && counts[sig.signature] > maxRepeats {
				return true
			}
			if sig.failed {
				failedCounts[sig.signature]++
				if failedCounts[sig.signature] > loopDetectionFailedMaxRepeats {
					return true
				}
			}
		}
	}

	return false
}

type toolInteractionSignature struct {
	signature string
	failed    bool
}

// getToolInteractionSignatures computes stable per-tool-call signatures for
// the tool interactions in a single step's content. It pairs tool calls with
// their results (matched by ToolCallID). If the step contains no tool calls,
// it returns nil.
func getToolInteractionSignatures(content fantasy.ResponseContent) []toolInteractionSignature {
	toolCalls := content.ToolCalls()
	if len(toolCalls) == 0 {
		return nil
	}

	// Index tool results by their ToolCallID for fast lookup.
	resultsByID := make(map[string]fantasy.ToolResultContent)
	for _, tr := range content.ToolResults() {
		resultsByID[tr.ToolCallID] = tr
	}

	signatures := make([]toolInteractionSignature, 0, len(toolCalls))
	for _, tc := range toolCalls {
		h := sha256.New()
		output := ""
		failed := false
		if tr, ok := resultsByID[tc.ToolCallID]; ok {
			output = toolResultOutputString(tr.Result)
			failed = toolResultIsError(tr.Result)
		}
		io.WriteString(h, tc.ToolName)
		io.WriteString(h, "\x00")
		io.WriteString(h, tc.Input)
		io.WriteString(h, "\x00")
		io.WriteString(h, output)
		io.WriteString(h, "\x00")
		signatures = append(signatures, toolInteractionSignature{
			signature: hex.EncodeToString(h.Sum(nil)),
			failed:    failed,
		})
	}
	return signatures
}

// getToolInteractionSignature collapses all tool interactions in a step into a
// single string for backwards-compatible tests and diagnostics.
func getToolInteractionSignature(content fantasy.ResponseContent) string {
	signatures := getToolInteractionSignatures(content)
	if len(signatures) == 0 {
		return ""
	}
	h := sha256.New()
	for _, sig := range signatures {
		io.WriteString(h, sig.signature)
		io.WriteString(h, "\x00")
	}
	return hex.EncodeToString(h.Sum(nil))
}

// toolResultOutputString converts a ToolResultOutputContent to a stable string
// representation for signature comparison.
func toolResultOutputString(result fantasy.ToolResultOutputContent) string {
	if result == nil {
		return ""
	}
	if text, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](result); ok {
		return text.Text
	}
	if errResult, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](result); ok {
		if errResult.Error != nil {
			return errResult.Error.Error()
		}
		return ""
	}
	if media, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentMedia](result); ok {
		return media.Data
	}
	return ""
}

func toolResultIsError(result fantasy.ToolResultOutputContent) bool {
	if result == nil {
		return false
	}
	_, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](result)
	return ok
}
