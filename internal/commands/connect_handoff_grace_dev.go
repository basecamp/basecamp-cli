//go:build dev

package commands

import (
	"fmt"
	"os"
	"time"
)

// connectHandoffGraceEnv sets, in a dev build only, how long before the run
// started a request may have been made and still be handed off, as a Go
// duration. The end-to-end tests shorten it, so a run that starts after the
// grace has passed is one they can reach in seconds rather than a minute. A
// release build reads no environment for it at all: see
// connect_handoff_grace_stub.go.
const connectHandoffGraceEnv = "BASECAMP_CONNECT_HANDOFF_GRACE"

// connectHandoffGrace is the grace connectHandoffGraceEnv sets, zero when
// it is unset, so the handoff's own default applies.
func connectHandoffGrace() (time.Duration, error) {
	return parseConnectHandoffGrace(os.Getenv(connectHandoffGraceEnv))
}

func parseConnectHandoffGrace(v string) (time.Duration, error) {
	if v == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s is not a duration: %w", connectHandoffGraceEnv, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration, not %s", connectHandoffGraceEnv, v)
	}
	return d, nil
}
