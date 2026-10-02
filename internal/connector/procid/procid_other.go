//go:build !unix

package procid

import "errors"

var errUnsupported = errors.New("procid: process identities are read on Unix only")

// OwnsWorker cannot answer off Unix, and an identity that cannot be
// established is never acted on.
func OwnsWorker(Process) (bool, error) { return false, errUnsupported }

// LookupProcess cannot answer off Unix.
func LookupProcess(int) (Process, error) { return Process{}, errUnsupported }
