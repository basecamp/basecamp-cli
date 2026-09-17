//go:build !unix

package setup

import (
	"fmt"
	"os"
	"runtime"
)

// On platforms without Unix ownership and modes, this package cannot verify
// that nobody else can change connect.json: that would take reading owners
// and ACLs, which it does not do. connect.json is the trust anchor, so setup
// fails closed there rather than write or read one it cannot vouch for.

func errUnsupported() error {
	return fmt.Errorf("%w: on %s this CLI cannot verify who can change connect.json, so the connector's trust file is not supported here", ErrNotPrivate, runtime.GOOS)
}

func checkAncestors(string) error             { return errUnsupported() }
func checkPrivateDir(string) error            { return errUnsupported() }
func checkPrivateFile(*os.File, string) error { return errUnsupported() }
func openNoFollow(string) (*os.File, error)   { return nil, errUnsupported() }
func syncDir(string)                          {}
