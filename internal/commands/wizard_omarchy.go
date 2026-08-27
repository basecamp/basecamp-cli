package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	omarchyBasecampPluginID     = "37signals.basecamp"
	omarchyBasecampPluginSource = "https://github.com/basecamp/omarchy-basecamp-plugin.git"
	omarchySetupTimeout         = time.Minute
)

type omarchyPluginOutcome struct {
	Detected bool
	Status   string
	Detail   string
	Manual   string
}

func (o omarchyPluginOutcome) failed() bool {
	return o.Status == "failed"
}

func detectOmarchy() bool {
	if os.Getenv("OMARCHY_PATH") != "" {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(home, ".local", "state", "omarchy"))
	return err == nil && info.IsDir()
}

func setupOmarchyPlugin(ctx context.Context) omarchyPluginOutcome {
	if !detectOmarchy() {
		return omarchyPluginOutcome{}
	}
	return ensureOmarchyPlugin(ctx, runOmarchySetupCommand)
}

type omarchySetupRunner func(context.Context, ...string) (string, error)

func ensureOmarchyPlugin(ctx context.Context, run omarchySetupRunner) omarchyPluginOutcome {
	outcome := omarchyPluginOutcome{Detected: true}
	ctx, cancel := context.WithTimeout(ctx, omarchySetupTimeout)
	defer cancel()

	output, err := run(ctx, "plugin", "list", "--json")
	if err != nil {
		outcome.Status = "failed"
		outcome.Detail = "could not inspect installed plugins"
		outcome.Manual = "omarchy plugin list --json"
		return outcome
	}

	var plugins []struct {
		ID string `json:"id"`
	}
	trimmed := strings.TrimSpace(output)
	if !strings.HasPrefix(trimmed, "[") || json.Unmarshal([]byte(trimmed), &plugins) != nil {
		outcome.Status = "failed"
		outcome.Detail = "could not read the installed plugin list"
		outcome.Manual = "omarchy plugin list --json"
		return outcome
	}

	installed := false
	for _, plugin := range plugins {
		if plugin.ID == omarchyBasecampPluginID {
			installed = true
			break
		}
	}

	if installed {
		if _, err := run(ctx, "plugin", "update", omarchyBasecampPluginID, "--yes"); err != nil {
			outcome.Status = "failed"
			outcome.Detail = "could not update the Basecamp plugin"
			outcome.Manual = "omarchy plugin update " + omarchyBasecampPluginID
			return outcome
		}
		outcome.Status = "updated"
		return outcome
	}

	if _, err := run(ctx, "plugin", "add", omarchyBasecampPluginSource, "--enable", "--yes"); err != nil {
		outcome.Status = "failed"
		outcome.Detail = "could not install the Basecamp plugin"
		outcome.Manual = "omarchy plugin add " + omarchyBasecampPluginSource + " --enable"
		return outcome
	}
	outcome.Status = "installed"
	return outcome
}

func runOmarchySetupCommand(ctx context.Context, args ...string) (string, error) {
	path, err := exec.LookPath("omarchy")
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // executable is resolved from the fixed omarchy command
	output, err := cmd.CombinedOutput()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return string(output), ctxErr
	}
	return string(output), err
}
