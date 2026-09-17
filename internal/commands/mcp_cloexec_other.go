//go:build !unix

package commands

// markCloseOnExec has nothing to do where the connector does not run: the
// command refuses --connect-state there.
func markCloseOnExec(int) {}
