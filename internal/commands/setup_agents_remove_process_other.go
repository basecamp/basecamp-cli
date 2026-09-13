//go:build !unix

package commands

import "os/exec"

func startRemoveProcessGroup(*exec.Cmd) {}

func killRemoveProcessGroup(command *exec.Cmd) error {
	return command.Process.Kill()
}
