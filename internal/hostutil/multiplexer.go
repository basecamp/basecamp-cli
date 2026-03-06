package hostutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Multiplexer identifies a detected terminal multiplexer.
type Multiplexer string

const (
	MultiplexerNone   Multiplexer = ""
	MultiplexerTmux   Multiplexer = "tmux"
	MultiplexerZellij Multiplexer = "zellij"
)

// DetectMultiplexer returns the active terminal multiplexer, if any.
func DetectMultiplexer() Multiplexer {
	if os.Getenv("TMUX") != "" {
		return MultiplexerTmux
	}
	if os.Getenv("ZELLIJ") != "" {
		return MultiplexerZellij
	}
	return MultiplexerNone
}

// SplitPane opens a new pane in the detected multiplexer running the given command.
// exe and args are passed as discrete tokens — never shell-interpolated.
func SplitPane(ctx context.Context, mux Multiplexer, exe string, args ...string) error {
	switch mux {
	case MultiplexerTmux:
		// tmux split-window runs argv[0] argv[1..] directly when multiple args follow -h.
		tmuxArgs := make([]string, 0, 3+len(args))
		tmuxArgs = append(tmuxArgs, "split-window", "-h", exe)
		tmuxArgs = append(tmuxArgs, args...)
		return exec.CommandContext(ctx, "tmux", tmuxArgs...).Run() //nolint:gosec // args are caller-controlled, not user input
	case MultiplexerZellij:
		// zellij new-pane -- exe arg1 arg2 ... (no shell involved)
		zellijArgs := make([]string, 0, 4+len(args))
		zellijArgs = append(zellijArgs, "action", "new-pane", "--", exe)
		zellijArgs = append(zellijArgs, args...)
		return exec.CommandContext(ctx, "zellij", zellijArgs...).Run() //nolint:gosec // args are caller-controlled, not user input
	default:
		return fmt.Errorf("no terminal multiplexer detected (need tmux or zellij)")
	}
}

// ApplyLayout sets the multiplexer pane layout.
func ApplyLayout(ctx context.Context, mux Multiplexer, layout string) error {
	switch mux {
	case MultiplexerTmux:
		return exec.CommandContext(ctx, "tmux", "select-layout", layout).Run() //nolint:gosec // layout is caller-controlled
	case MultiplexerZellij:
		// Zellij doesn't have a direct layout command; skip silently
		return nil
	default:
		return fmt.Errorf("no terminal multiplexer detected")
	}
}

// CurrentPaneCommands returns the commands running in each pane (tmux only).
func CurrentPaneCommands(ctx context.Context, mux Multiplexer) ([]string, error) {
	if mux != MultiplexerTmux {
		return nil, fmt.Errorf("pane introspection only supported for tmux")
	}
	out, err := exec.CommandContext(ctx, "tmux", "list-panes", "-F", "#{pane_current_command} #{pane_start_command}").Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return lines, nil
}
