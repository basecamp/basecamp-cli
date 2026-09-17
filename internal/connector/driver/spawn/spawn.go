// Package spawn chooses a spawn driver by the worker connect.json names: the
// coding agent started as a process per session.
package spawn

import (
	"fmt"
	"sort"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/claude"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/codex"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

// Options are what every spawn driver may take.
type Options struct {
	// Lookup reads the connector's environment for the worker's own
	// variables; os.LookupEnv when nil.
	Lookup func(string) (string, bool)
}

// constructors builds each worker's driver. A worker added to setup.Workers
// adds its row here.
var constructors = map[string]func(Options) driver.Driver{
	setup.WorkerClaude: func(o Options) driver.Driver { return claude.New(claude.Options{Lookup: o.Lookup}) },
	setup.WorkerCodex:  func(o Options) driver.Driver { return codex.New(codex.Options{Lookup: o.Lookup}) },
}

// New is the spawn driver for worker.
func New(worker string, opts Options) (driver.Driver, error) {
	build, ok := constructors[worker]
	if !ok {
		names := make([]string, 0, len(constructors))
		for name := range constructors {
			names = append(names, name)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("spawn: no driver for worker %q (have %v)", worker, names)
	}
	return build(opts), nil
}
