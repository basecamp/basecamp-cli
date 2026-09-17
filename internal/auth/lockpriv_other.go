//go:build !unix

package auth

// Without Unix ownership and modes there is nothing to check here: a
// required lock rests on the platform's own access control for the user's
// configuration directory.
func requirePrivateLockPath(string, string) error { return nil }
