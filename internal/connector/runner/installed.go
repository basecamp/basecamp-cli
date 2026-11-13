package runner

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Installed is one built-in coding agent's CLI as this computer has it.
type Installed struct {
	// Name is the adapter's name, as --orchestrator and --worker take it.
	Name string
	// Path is where its command was found on PATH, empty when it was not.
	Path string
	// Version is the first line the command answers --version with, as the
	// CLI wrote it. Empty when the command is missing or did not answer.
	Version string
	// Guarded says the CLI has guardrails of its own to run under, which
	// guarded permissions need.
	Guarded bool
	// Error is why Version is empty for a command that was found.
	Error string
}

// versionTimeout bounds one CLI's answer to --version. A CLI behind a
// version manager's shim takes a moment to start.
const versionTimeout = 10 * time.Second

// maxVersion bounds the version line kept.
const maxVersion = 80

// InstalledAgents asks each built-in adapter's CLI for its version, all at
// once. A nil look means exec.LookPath. It runs each CLI with --version and
// nothing else, which starts no session and spends nothing.
func InstalledAgents(ctx context.Context, look func(string) (string, error)) []Installed {
	if look == nil {
		look = exec.LookPath
	}
	found := make([]Installed, len(adapters))
	var wg sync.WaitGroup
	for i, adapter := range adapters {
		found[i] = Installed{Name: adapter.Name(), Guarded: adapter.Guarded()}
		path, err := look(adapter.Binary())
		if err != nil {
			continue
		}
		found[i].Path = path
		wg.Go(func() {
			found[i].Version, found[i].Error = versionOf(ctx, path)
		})
	}
	wg.Wait()
	return found
}

// head keeps the first limit bytes written to it and takes the rest without
// keeping them, so a command that prints without end fills no memory.
type head struct {
	kept  []byte
	limit int
}

func (h *head) Write(p []byte) (int, error) {
	h.kept = append(h.kept, p[:min(len(p), h.limit-len(h.kept))]...)
	return len(p), nil
}

// versionOf runs the command at path with --version and returns its first
// line, or why there is none.
func versionOf(ctx context.Context, path string) (version, problem string) {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version") //nolint:gosec // G204: the adapter's own command, found on PATH
	cmd.WaitDelay = time.Second
	// Only the first line is kept, so only the start of what it prints is.
	out := head{limit: 4 << 10}
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", "it did not answer --version in time"
		}
		return "", "it could not be asked its version: " + err.Error()
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out.kept)), "\n")
	if line = strings.TrimSpace(line); line == "" {
		return "", "it answered --version with nothing"
	}
	return clip(line, maxVersion), ""
}
