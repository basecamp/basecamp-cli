//go:build !unix

package runner

import "errors"

// checkSocketDir cannot say whose a directory is off Unix, and a socket in
// a directory that cannot be vouched for is not listened on or called. The
// connector's agents do not run there either.
func checkSocketDir(string) error {
	return errors.New("the connector does not run its own agents on this platform")
}
