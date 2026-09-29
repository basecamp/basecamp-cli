package codex

import (
	"context"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// Product is Codex's name for a person.
const Product = "Codex"

var _ driver.Preflighter = (*Driver)(nil)

// Preflight implements driver.Preflighter: `codex --version`, the session's
// flags in `codex exec --help`, and `codex login status`. The login is only a
// warning: Codex's answer is a guess about stored credentials, and an API key
// in the environment works without one.
func (d *Driver) Preflight(ctx context.Context, policy driver.PermissionPolicy) driver.Preflight {
	return d.probe(policy).Check(ctx)
}

func (d *Driver) probe(policy driver.PermissionPolicy) driver.WorkerProbe {
	return driver.WorkerProbe{
		Product:    Product,
		Binary:     d.opts.Binary,
		Env:        d.env(driver.SessionConfig{Env: driver.BuildEnv(driver.BaseEnv, d.opts.Lookup, nil)}),
		Help:       []string{"exec", "--help"},
		Flags:      SessionFlags(policy, d.opts.Model),
		Update:     "Update Codex.",
		Login:      []string{"login", "status"},
		LoggedIn:   func(r driver.ProbeResult) (bool, bool) { return r.Exit == 0, r.Exit >= 0 },
		LoginFix:   "run `codex login`",
		LoginWarns: true,
	}
}

// SessionFlags is every flag a new session passes after `exec`: what the
// preflight asks `codex exec --help` for.
func SessionFlags(policy driver.PermissionPolicy, model string) []string {
	if policy == nil {
		return nil
	}
	args, err := Args(driver.SessionConfig{Policy: policy}, "", model)
	if err != nil {
		return nil
	}
	return driver.FlagsOf(args)
}
