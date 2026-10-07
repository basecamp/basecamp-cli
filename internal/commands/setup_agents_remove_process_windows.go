package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

func startRemoveProcessGroup(*exec.Cmd) {}

func killRemoveProcessGroup(command *exec.Cmd) error {
	// Kill the wrapper and its descendants before the wrapper can be reaped.
	// Use the system binary, not a plugin-controlled executable on PATH.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	taskkill := filepath.Join(os.Getenv("SystemRoot"), "System32", "taskkill.exe")
	err := exec.CommandContext(ctx, taskkill, "/F", "/T", "/PID", strconv.Itoa(command.Process.Pid)).Run() //nolint:gosec // system binary and an integer PID
	if err != nil {
		// Still stop the wrapper when taskkill is unavailable or the process exited.
		_ = command.Process.Kill()
	}
	return err
}
