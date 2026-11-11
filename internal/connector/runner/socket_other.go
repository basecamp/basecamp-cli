//go:build !unix

package runner

// checkSocketDir has nothing to hold a directory to off Unix, where the
// connector does not run.
func checkSocketDir(string) error { return nil }
