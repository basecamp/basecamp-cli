//go:build windows

package tui

import "os"

// Windows has no pollable console read, so the probe is skipped and the pessimistic
// defaults stand.
func CalibrateWidths(in, out *os.File) {}
