package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCompletionCmd tests the completion command structure.
func TestCompletionCmd(t *testing.T) {
	cmd := NewCompletionCmd()

	assert.Equal(t, "completion [shell]", cmd.Use)

	// Should have subcommands for each shell
	expected := []string{"bash", "zsh", "fish", "powershell"}
	for _, name := range expected {
		sub, _, err := cmd.Find([]string{name})
		assert.NoError(t, err, "expected subcommand %q to exist", name)
		assert.NotNil(t, sub, "expected subcommand %q to exist", name)
	}
}

// TestCompletionValidArgs tests that only valid shell names are accepted.
func TestCompletionValidArgs(t *testing.T) {
	cmd := NewCompletionCmd()

	validArgs := cmd.ValidArgs
	expected := []string{"bash", "zsh", "fish", "powershell"}

	assert.Equal(t, len(expected), len(validArgs))

	for _, exp := range expected {
		found := false
		for _, arg := range validArgs {
			if arg == exp {
				found = true
				break
			}
		}
		assert.True(t, found, "expected valid arg %q not found", exp)
	}
}

// TestCompletionBashOutput tests that bash completion generates valid output.
func TestCompletionBashOutput(t *testing.T) {
	root := &cobra.Command{Use: "bcq"}
	completionCmd := NewCompletionCmd()
	root.AddCommand(completionCmd)

	buf := &bytes.Buffer{}

	// Capture output by running the actual command
	err := root.GenBashCompletionV2(buf, true)
	require.NoError(t, err, "GenBashCompletionV2 failed")

	output := buf.String()

	// Should contain bash completion markers
	assert.True(t, strings.Contains(output, "bash completion"), "expected 'bash completion' in output")
	assert.True(t, strings.Contains(output, "__bcq_"), "expected '__bcq_' function prefix in output")
	assert.True(t, strings.Contains(output, "complete -o"), "expected 'complete -o' in output")
}

// TestCompletionZshOutput tests that zsh completion generates valid output.
func TestCompletionZshOutput(t *testing.T) {
	root := &cobra.Command{Use: "bcq"}
	root.AddCommand(NewCompletionCmd())

	buf := &bytes.Buffer{}

	err := root.GenZshCompletion(buf)
	require.NoError(t, err, "GenZshCompletion failed")

	output := buf.String()

	// Should contain zsh completion markers
	assert.True(t, strings.Contains(output, "#compdef bcq"), "expected '#compdef bcq' in output")
	assert.True(t, strings.Contains(output, "_bcq"), "expected '_bcq' function in output")
}

// TestCompletionFishOutput tests that fish completion generates valid output.
func TestCompletionFishOutput(t *testing.T) {
	root := &cobra.Command{Use: "bcq"}
	root.AddCommand(NewCompletionCmd())

	buf := &bytes.Buffer{}

	err := root.GenFishCompletion(buf, true)
	require.NoError(t, err, "GenFishCompletion failed")

	output := buf.String()

	// Should contain fish completion markers
	assert.True(t, strings.Contains(output, "fish completion"), "expected 'fish completion' in output")
	assert.True(t, strings.Contains(output, "__bcq_"), "expected '__bcq_' function prefix in output")
}

// TestCompletionPowershellOutput tests that powershell completion generates valid output.
func TestCompletionPowershellOutput(t *testing.T) {
	root := &cobra.Command{Use: "bcq"}
	root.AddCommand(NewCompletionCmd())

	buf := &bytes.Buffer{}

	err := root.GenPowerShellCompletionWithDesc(buf)
	require.NoError(t, err, "GenPowerShellCompletionWithDesc failed")

	output := buf.String()

	// Should contain powershell completion markers
	assert.True(t, strings.Contains(output, "powershell completion"), "expected 'powershell completion' in output")
	assert.True(t, strings.Contains(output, "__bcq"), "expected '__bcq' function in output")
}

// TestCompletionInvalidShell tests that invalid shell names are rejected.
func TestCompletionInvalidShell(t *testing.T) {
	root := &cobra.Command{Use: "bcq"}
	root.AddCommand(NewCompletionCmd())

	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"completion", "invalid"})

	err := root.Execute()
	require.NotNil(t, err, "expected error for invalid shell, got nil")

	// Cobra should reject invalid args
	assert.True(t, strings.Contains(err.Error(), "invalid"), "expected error to mention invalid arg, got: %v", err)
}

// TestCompletionRequiresArg tests that completion requires a shell argument.
func TestCompletionRequiresArg(t *testing.T) {
	root := &cobra.Command{Use: "bcq"}
	root.AddCommand(NewCompletionCmd())

	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"completion"})

	err := root.Execute()
	require.NotNil(t, err, "expected error when no shell specified, got nil")

	// Cobra should require exactly 1 arg
	assert.True(t, strings.Contains(err.Error(), "accepts 1 arg"), "expected error about args, got: %v", err)
}

// TestCompletionSubcommands tests that shell subcommands exist and have correct use strings.
func TestCompletionSubcommands(t *testing.T) {
	cmd := NewCompletionCmd()

	tests := []struct {
		name string
		use  string
	}{
		{"bash", "bash"},
		{"zsh", "zsh"},
		{"fish", "fish"},
		{"powershell", "powershell"},
		{"refresh", "refresh"},
		{"status", "status"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub, _, err := cmd.Find([]string{tt.name})
			require.NoError(t, err, "subcommand %q not found", tt.name)
			assert.Equal(t, tt.use, sub.Use)
			assert.NotEmpty(t, sub.Short, "expected non-empty Short description")
		})
	}
}
