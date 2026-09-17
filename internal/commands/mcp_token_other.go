//go:build !unix

package commands

import (
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/sysfd"
)

// readTaskToken is refused where the connector cannot run: its ledger's
// privacy cannot be established off Unix, so no worker is started there.
func readTaskToken(sysfd.Descriptor) (string, error) {
	return "", output.ErrUsage("--connect-state is only available on Unix, where the connector runs")
}
