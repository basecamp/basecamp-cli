package summarize

import (
	"fmt"
	"strings"
)

// Hint types for prompt construction.
const (
	HintGap    = "gap"    // summarizing missed messages
	HintTicker = "ticker" // one-line ambient display
	HintScan   = "scan"   // front page overview
)

// BuildPrompt constructs an LLM prompt for the given segments and hint.
func BuildPrompt(segments []Segment, targetChars int, hint string) string {
	var ctx string
	switch hint {
	case HintGap:
		ctx = "Summarize what happened in this conversation while the user was away."
	case HintTicker:
		ctx = "Write a one-line summary of the latest activity."
	case HintScan:
		ctx = "Provide a brief overview of this conversation's current topic."
	default:
		ctx = "Summarize this conversation."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s Keep it under %d characters.\n\n", ctx, targetChars)
	for _, seg := range segments {
		fmt.Fprintf(&b, "[%s] %s: %s\n", seg.Time, seg.Author, seg.Text)
	}
	return b.String()
}
