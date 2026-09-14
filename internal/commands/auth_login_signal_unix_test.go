//go:build unix

package commands

import (
	"bytes"
	"context"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// The signal is sent to this process while loginContext's handler is
// registered, so it cancels the login instead of ending the test binary.
func TestLoginContextExitsWithTheStatusOfTheSignalThatStoppedIt(t *testing.T) {
	for _, tc := range []struct {
		signal syscall.Signal
		code   string
		exit   int
	}{
		{syscall.SIGINT, output.CodeInterrupted, 130},
		{syscall.SIGTERM, output.CodeTerminated, 143},
	} {
		t.Run(tc.signal.String(), func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.SetContext(context.Background())
			ctx, stop := loginContext(cmd)
			defer stop()

			require.NoError(t, syscall.Kill(os.Getpid(), tc.signal))
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("the signal did not cancel the login context")
			}

			var out bytes.Buffer
			err := loginOutcome(ctx, context.Canceled, &out, output.NewRenderer(&out, false))
			var outErr *output.Error
			require.ErrorAs(t, err, &outErr)
			assert.Equal(t, tc.code, outErr.Code)
			assert.Equal(t, tc.exit, output.ExitCodeFor(outErr.Code))
			assert.Contains(t, out.String(), "Login canceled. Nothing was stored.")
		})
	}
}

func TestLoginContextStopReleasesTheSignalHandler(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	ctx, stop := loginContext(cmd)
	stop()

	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not cancel the login context")
	}
	assert.NotErrorAs(t, context.Cause(ctx), new(loginSignalError), "a stop is not a signal")
}
