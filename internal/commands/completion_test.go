package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestCompletionCmd tests the completion command structure.
func TestCompletionCmd(t *testing.T) {
	cmd := NewCompletionCmd()

	if cmd.Use != "completion [shell]" {
		t.Errorf("expected Use 'completion [shell]', got %q", cmd.Use)
	}

	// Should have subcommands for each shell
	expected := []string{"bash", "zsh", "fish", "powershell"}
	for _, name := range expected {
		sub, _, err := cmd.Find([]string{name})
		if err != nil {
			t.Errorf("expected subcommand %q to exist, got error: %v", name, err)
		}
		if sub == nil {
			t.Errorf("expected subcommand %q to exist, got nil", name)
		}
	}
}

// TestCompletionValidArgs tests that only valid shell names are accepted.
func TestCompletionValidArgs(t *testing.T) {
	cmd := NewCompletionCmd()

	validArgs := cmd.ValidArgs
	expected := []string{"bash", "zsh", "fish", "powershell"}

	if len(validArgs) != len(expected) {
		t.Errorf("expected %d valid args, got %d", len(expected), len(validArgs))
	}

	for _, exp := range expected {
		found := false
		for _, arg := range validArgs {
			if arg == exp {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected valid arg %q not found", exp)
		}
	}
}

// TestCompletionBashOutput tests that bash completion generates valid output.
func TestCompletionBashOutput(t *testing.T) {
	root := &cobra.Command{Use: "bcq"}
	completionCmd := NewCompletionCmd()
	root.AddCommand(completionCmd)

	buf := &bytes.Buffer{}

	// Capture output by running the actual command
	if err := root.GenBashCompletionV2(buf, true); err != nil {
		t.Fatalf("GenBashCompletionV2 failed: %v", err)
	}

	output := buf.String()

	// Should contain bash completion markers
	if !strings.Contains(output, "bash completion") {
		t.Error("expected 'bash completion' in output")
	}
	if !strings.Contains(output, "__bcq_") {
		t.Error("expected '__bcq_' function prefix in output")
	}
	if !strings.Contains(output, "complete -o") {
		t.Error("expected 'complete -o' in output")
	}
}

// TestCompletionZshOutput tests that zsh completion generates valid output.
func TestCompletionZshOutput(t *testing.T) {
	root := &cobra.Command{Use: "bcq"}
	root.AddCommand(NewCompletionCmd())

	buf := &bytes.Buffer{}

	if err := root.GenZshCompletion(buf); err != nil {
		t.Fatalf("GenZshCompletion failed: %v", err)
	}

	output := buf.String()

	// Should contain zsh completion markers
	if !strings.Contains(output, "#compdef bcq") {
		t.Error("expected '#compdef bcq' in output")
	}
	if !strings.Contains(output, "_bcq") {
		t.Error("expected '_bcq' function in output")
	}
}

// TestCompletionFishOutput tests that fish completion generates valid output.
func TestCompletionFishOutput(t *testing.T) {
	root := &cobra.Command{Use: "bcq"}
	root.AddCommand(NewCompletionCmd())

	buf := &bytes.Buffer{}

	if err := root.GenFishCompletion(buf, true); err != nil {
		t.Fatalf("GenFishCompletion failed: %v", err)
	}

	output := buf.String()

	// Should contain fish completion markers
	if !strings.Contains(output, "fish completion") {
		t.Error("expected 'fish completion' in output")
	}
	if !strings.Contains(output, "__bcq_") {
		t.Error("expected '__bcq_' function prefix in output")
	}
}

// TestCompletionPowershellOutput tests that powershell completion generates valid output.
func TestCompletionPowershellOutput(t *testing.T) {
	root := &cobra.Command{Use: "bcq"}
	root.AddCommand(NewCompletionCmd())

	buf := &bytes.Buffer{}

	if err := root.GenPowerShellCompletionWithDesc(buf); err != nil {
		t.Fatalf("GenPowerShellCompletionWithDesc failed: %v", err)
	}

	output := buf.String()

	// Should contain powershell completion markers
	if !strings.Contains(output, "powershell completion") {
		t.Error("expected 'powershell completion' in output")
	}
	if !strings.Contains(output, "__bcq") {
		t.Error("expected '__bcq' function in output")
	}
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
	if err == nil {
		t.Fatal("expected error for invalid shell, got nil")
	}

	// Cobra should reject invalid args
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected error to mention invalid arg, got: %v", err)
	}
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
	if err == nil {
		t.Fatal("expected error when no shell specified, got nil")
	}

	// Cobra should require exactly 1 arg
	if !strings.Contains(err.Error(), "accepts 1 arg") {
		t.Errorf("expected error about args, got: %v", err)
	}
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
			if err != nil {
				t.Fatalf("subcommand %q not found: %v", tt.name, err)
			}
			if sub.Use != tt.use {
				t.Errorf("expected Use %q, got %q", tt.use, sub.Use)
			}
			if sub.Short == "" {
				t.Error("expected non-empty Short description")
			}
		})
	}
}
