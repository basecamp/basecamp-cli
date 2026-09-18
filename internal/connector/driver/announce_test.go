//go:build unix

package driver

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Invariant 7: the worker is announced as soon as it exists, not when a
// session is ready on top of it. StartWorker is where every driver's worker
// is started, so it is where the announcement is made — a driver cannot
// forget it, and the connector cannot be left arming a token socket for a
// process group it will not be told about until a handshake that is waiting
// on that socket has returned.
func TestAWorkerIsAnnouncedAsSoonAsItsProcessExists(t *testing.T) {
	var announced []Process
	cfg := SessionConfig{
		Scope:   Scope{WorkDir: t.TempDir()},
		Started: func(p Process) { announced = append(announced, p) },
	}
	w, err := StartWorker(context.Background(), cfg, Command{Path: "/bin/sleep", Args: []string{"30"}, Env: []string{}})
	require.NoError(t, err)
	t.Cleanup(func() { w.Terminate(time.Second) })

	require.Len(t, announced, 1, "announced once, by the time StartWorker returns")
	assert.Equal(t, w.Process(), announced[0], "and it is the worker StartWorker is handing back")
	assert.Positive(t, announced[0].PID)
	assert.Equal(t, announced[0].PID, announced[0].PGID, "which leads its own group, so the token socket can be told one number")
}

// A start that never made a process has nothing to announce: the connector
// would otherwise arm a socket for a group that does not exist.
func TestAStartThatLaunchedNothingAnnouncesNothing(t *testing.T) {
	announced := 0
	cfg := SessionConfig{
		Scope:   Scope{WorkDir: t.TempDir()},
		Started: func(Process) { announced++ },
	}
	_, err := StartWorker(context.Background(), cfg, Command{Path: "/nonexistent/agent-not-here"})
	require.ErrorIs(t, err, ErrNotStarted)
	_, err = StartWorker(context.Background(), SessionConfig{Launcher: refusingLauncher{}, Scope: cfg.Scope, Started: cfg.Started},
		Command{Path: "/bin/true"})
	require.ErrorIs(t, err, ErrNotStarted)
	assert.Zero(t, announced)
}
