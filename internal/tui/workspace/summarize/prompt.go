package summarize

import "fmt"

// Hint types for prompt construction.
const (
	HintGap    = "gap"    // summarizing missed messages
	HintTicker = "ticker" // one-line ambient display
	HintScan   = "scan"   // front page overview
)

// BuildPrompt constructs an LLM prompt for the given segments and hint.
func BuildPrompt(segments []Segment, targetChars int, hint string) string {
	var context string
	switch hint {
	case HintGap:
		context = "Summarize what happened in this conversation while the user was away."
	case HintTicker:
		context = "Write a one-line summary of the latest activity."
	case HintScan:
		context = "Provide a brief overview of this conversation's current topic."
	default:
		context = "Summarize this conversation."
	}

	prompt := fmt.Sprintf("%s Keep it under %d characters.\n\n", context, targetChars)
	for _, seg := range segments {
		prompt += fmt.Sprintf("[%s] %s: %s\n", seg.Time, seg.Author, seg.Text)
	}
	return prompt
}
