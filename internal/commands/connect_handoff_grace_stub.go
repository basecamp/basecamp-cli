//go:build !dev

package commands

import "time"

// connectHandoffGrace is always zero in a release build, so the handoff's
// own default applies: only a dev build lets the environment change it
// (connect_handoff_grace_dev.go).
func connectHandoffGrace() (time.Duration, error) { return 0, nil }
