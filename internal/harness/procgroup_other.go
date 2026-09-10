//go:build !unix

package harness

import "os/exec"

// startInOwnProcessGroup is a no-op where process groups are unavailable.
func startInOwnProcessGroup(*exec.Cmd) {}

// killProcessGroup kills the child alone; its descendants are out of reach.
func killProcessGroup(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
