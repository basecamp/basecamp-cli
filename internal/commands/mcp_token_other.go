//go:build !linux

package commands

import (
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/sysfd"
)

// readTaskToken is refused wherever the descriptor carrying the token cannot
// be kept from the processes this one starts. Only Linux seals the
// descriptors this process inherited, in internal/cli's
// inherited_fds_linux.go, so only Linux may be handed a credential on one:
// accepting a token where nothing seals it would leave it inheritable through
// every hook that runs before this command, which is the whole point of
// handing it over on a descriptor instead of in a file or an environment.
//
// The connector runs on Linux, so this refuses nothing that works today.
func readTaskToken(sysfd.Descriptor) (string, error) {
	return "", output.ErrUsage("--connect-state is only available on Linux, where an inherited descriptor can be kept from the processes this one starts")
}
