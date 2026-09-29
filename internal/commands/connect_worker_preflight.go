package commands

import (
	"context"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/spawn"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// connectWorkerPreflight starts the worker connect.json names the way the
// connector would — its driver's binary, found on this PATH, with a
// session's environment — and asks it, with no work and no model call,
// whether it starts, knows the connector's flags, and is logged in. It
// reports false for a worker whose driver has no preflight (the acp driver
// has its own, acpPreflightCheck). A variable so tests can answer for the
// worker.
var connectWorkerPreflight = func(ctx context.Context, file setup.File) (driver.Preflight, bool) {
	if file.Driver == setup.DriverACP {
		return driver.Preflight{}, false
	}
	d, err := spawn.New(file.WorkerName(), spawn.Options{})
	if err != nil {
		return driver.Preflight{}, false
	}
	p, ok := d.(driver.Preflighter)
	if !ok {
		return driver.Preflight{}, false
	}
	return p.Preflight(ctx, connector.DefaultPolicy()), true
}

// preflightNames are doctor's names for a preflight's checks.
var preflightNames = map[string]string{
	driver.PreflightStarts: "starts",
	driver.PreflightFlags:  "options",
	driver.PreflightLogin:  "login",
}

// preflightChecks are a preflight as doctor's rows: "Claude Code starts",
// "Claude Code options", "Claude Code login".
func preflightChecks(p driver.Preflight) []setup.Check {
	checks := make([]setup.Check, 0, len(p.Checks))
	for _, c := range p.Checks {
		status := setup.StatusPass
		switch c.Status {
		case driver.PreflightFail:
			status = setup.StatusFail
		case driver.PreflightWarn:
			status = setup.StatusWarn
		case driver.PreflightPass:
		}
		checks = append(checks, setup.Check{
			Name:    p.Product + " " + preflightNames[c.Name],
			Status:  status,
			Message: richtext.SanitizeSingleLine(c.Message),
			Hint:    richtext.SanitizeSingleLine(c.Hint),
		})
	}
	return checks
}
