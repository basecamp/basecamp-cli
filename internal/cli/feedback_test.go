package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/commands"
	"github.com/basecamp/basecamp-cli/internal/feedback"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// runRoot runs the root with a feedback command decided the way Execute
// decides it, applies Execute's help rule for it, and returns what it
// printed and the error Execute would report.
func runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(newFeedbackCmd())
	cmd.AddCommand(commands.NewCommandsCmd())
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	var err error
	stdout := captureStdout(t, func() {
		executed, execErr := cmd.ExecuteC()
		err = execErr
		if err == nil {
			err = commands.UnavailableFeedbackHelp(executed)
		}
	})
	return buf.String() + string(stdout), err
}

// rootOutput is runRoot for an invocation that must succeed, so an
// assertion that something is absent never passes on a failed run.
func rootOutput(t *testing.T, args ...string) string {
	t.Helper()
	out, err := runRoot(t, args...)
	require.NoError(t, err, "basecamp %v", args)
	require.NotEmpty(t, out, "basecamp %v printed nothing", args)
	return out
}

// namesFeedback reports whether the JSON document out holds an object whose
// "name" is "feedback", wherever the listing puts it.
func namesFeedback(t *testing.T, out string) bool {
	t.Helper()
	var doc any
	require.NoError(t, json.Unmarshal([]byte(out), &doc), "not JSON: %s", out)
	var walk func(any) bool
	walk = func(v any) bool {
		switch v := v.(type) {
		case map[string]any:
			if v["name"] == "feedback" {
				return true
			}
			for _, child := range v {
				if walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range v {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	return walk(doc)
}

func isolateFeedback(t *testing.T) {
	t.Helper()
	isolateHelpTest(t)
	t.Setenv("BASECAMP_CACHE_DIR", "")
	t.Setenv(feedback.EnvOverride, "")
}

func assertFeedbackNotAvailable(t *testing.T, args ...string) {
	t.Helper()
	out, err := runRoot(t, args...)
	require.Error(t, err, "basecamp %v", args)
	e := output.AsError(err)
	assert.Equal(t, feedback.NotAvailable, e.Message, "basecamp %v", args)
	assert.NotEqual(t, 0, output.ExitCodeFor(e.Code), "basecamp %v exits non-zero", args)
	assert.NotContains(t, out, "--category", "basecamp %v describes nothing", args)
}

func TestFeedbackHiddenWhenNotAdvertised(t *testing.T) {
	isolateFeedback(t)

	assert.NotContains(t, rootOutput(t, "--help"), "feedback")
	assert.False(t, namesFeedback(t, rootOutput(t, "--help", "--agent")))
	assert.NotContains(t, rootOutput(t, "__complete", "f"), "feedback")
	assert.False(t, namesFeedback(t, rootOutput(t, "commands", "--json")))
	assert.Equal(t, feedback.NotAvailable+"\n", rootOutput(t, "help", "feedback"))
}

// While it is off, help and malformed flags get the answer running it gets:
// "not available", exiting non-zero, and no usage error naming its flags.
func TestFeedbackHelpAndBadFlagsFailWhileHidden(t *testing.T) {
	isolateFeedback(t)

	for _, args := range [][]string{
		{"feedback", "--help"},
		{"feedback", "-h"},
		{"feedback", "--category"},
		{"feedback", "--no-such-flag"},
		{"feedback", "--category", "bug", "--help"},
	} {
		assertFeedbackNotAvailable(t, args...)
	}

	t.Setenv(feedback.EnvOverride, "0")
	assertFeedbackNotAvailable(t, "feedback", "--category")
}

func TestFeedbackListedOnceAdvertised(t *testing.T) {
	isolateFeedback(t)
	feedback.DefaultStore().Record(feedback.Key("", "1", ""), true)

	assert.Contains(t, rootOutput(t, "--help"), "basecamp feedback")
	assert.True(t, namesFeedback(t, rootOutput(t, "--help", "--agent")))
	assert.Contains(t, rootOutput(t, "__complete", "f"), "feedback")
	assert.Contains(t, rootOutput(t, "feedback", "--help"), "--category")
	assert.True(t, namesFeedback(t, rootOutput(t, "commands", "--json")))

	_, err := runRoot(t, "feedback", "--category")
	require.Error(t, err)
	assert.NotEqual(t, feedback.NotAvailable, output.AsError(err).Message, "a visible command reports its own flag errors")
}

func TestFeedbackOverrideControlsVisibility(t *testing.T) {
	isolateFeedback(t)
	t.Setenv(feedback.EnvOverride, "1")
	require.Contains(t, rootOutput(t, "--help"), "basecamp feedback")

	feedback.DefaultStore().Record(feedback.Key("", "1", ""), true)
	t.Setenv(feedback.EnvOverride, "0")
	assert.NotContains(t, rootOutput(t, "--help"), "feedback")
}
