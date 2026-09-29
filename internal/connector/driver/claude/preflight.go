package claude

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// Product is Claude Code's name for a person.
const Product = "Claude Code"

var _ driver.Preflighter = (*Driver)(nil)

// Preflight implements driver.Preflighter: `claude --version`, the session's
// flags in `claude --help`, and `claude auth status`, which reads the stored
// login without calling a model. A token that is present but expired still
// reads as logged in; only a session finds that out.
func (d *Driver) Preflight(ctx context.Context, policy driver.PermissionPolicy) driver.Preflight {
	return d.probe(policy).Check(ctx)
}

func (d *Driver) probe(policy driver.PermissionPolicy) driver.WorkerProbe {
	return driver.WorkerProbe{
		Product:  Product,
		Binary:   d.opts.Binary,
		Env:      d.env(driver.SessionConfig{Env: driver.BuildEnv(driver.BaseEnv, d.opts.Lookup, nil)}),
		Help:     []string{"--help"},
		Flags:    SessionFlags(policy, d.opts.Model),
		Update:   "Update Claude Code: claude update",
		Login:    []string{"auth", "status", "--json"},
		LoggedIn: loggedIn,
		LoginFix: "run `claude` and log in",
	}
}

// SessionFlags is every flag a session passes, new or resumed: what the
// preflight asks Claude Code's help for.
func SessionFlags(policy driver.PermissionPolicy, model string) []string {
	if policy == nil {
		return nil
	}
	cfg := driver.SessionConfig{Policy: policy}
	var flags []string
	for _, resume := range []bool{false, true} {
		args, err := Args(cfg, "00000000-0000-4000-8000-000000000000", resume, "mcp.json", model)
		if err != nil {
			continue
		}
		for _, f := range driver.FlagsOf(args) {
			if !slices.Contains(flags, f) {
				flags = append(flags, f)
			}
		}
	}
	return flags
}

// loggedIn reads `claude auth status --json`, which exits non-zero when
// logged out and says so either way.
func loggedIn(r driver.ProbeResult) (bool, bool) {
	var status struct {
		LoggedIn *bool `json:"loggedIn"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &status); err != nil || status.LoggedIn == nil {
		return false, false
	}
	return *status.LoggedIn, true
}
