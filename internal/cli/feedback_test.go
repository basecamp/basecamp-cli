package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/commands"
	"github.com/basecamp/basecamp-cli/internal/feedback"
)

// rootOutput runs the root with a feedback command decided the way Execute
// decides it, and returns what it printed.
func rootOutput(t *testing.T, args ...string) string {
	t.Helper()
	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(newFeedbackCmd())
	cmd.AddCommand(commands.NewCommandsCmd())
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	stdout := captureStdout(t, func() { _ = cmd.Execute() })
	return buf.String() + string(stdout)
}

func isolateFeedback(t *testing.T) {
	t.Helper()
	isolateHelpTest(t)
	t.Setenv("BASECAMP_CACHE_DIR", "")
	t.Setenv(feedback.EnvOverride, "")
}

func TestFeedbackHiddenWhenNotAdvertised(t *testing.T) {
	isolateFeedback(t)

	assert.NotContains(t, rootOutput(t, "--help"), "feedback")
	assert.NotContains(t, rootOutput(t, "--help", "--agent"), "feedback")
	assert.NotContains(t, rootOutput(t, "__complete", "f"), "feedback")
	assert.Equal(t, feedback.NotAvailable+"\n", rootOutput(t, "feedback", "--help"))
	assert.NotContains(t, rootOutput(t, "commands", "--json"), `"feedback"`)
}

func TestFeedbackListedOnceAdvertised(t *testing.T) {
	isolateFeedback(t)
	feedback.DefaultStore().Record(feedback.Key("", "1"), true)

	assert.Contains(t, rootOutput(t, "--help"), "basecamp feedback")
	assert.Contains(t, rootOutput(t, "--help", "--agent"), `"feedback"`)
	assert.Contains(t, rootOutput(t, "__complete", "f"), "feedback")
	assert.Contains(t, rootOutput(t, "feedback", "--help"), "--category")
	assert.Contains(t, rootOutput(t, "commands", "--json"), `"name": "feedback"`)
}

func TestFeedbackOverrideControlsVisibility(t *testing.T) {
	isolateFeedback(t)
	t.Setenv(feedback.EnvOverride, "1")
	require.Contains(t, rootOutput(t, "--help"), "basecamp feedback")

	feedback.DefaultStore().Record(feedback.Key("", "1"), true)
	t.Setenv(feedback.EnvOverride, "0")
	assert.NotContains(t, rootOutput(t, "--help"), "feedback")
}
