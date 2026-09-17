//go:build !unix

package commands

// processAlive cannot be answered here; the connector runs on macOS and Linux.
func processAlive(int) bool { return false }
