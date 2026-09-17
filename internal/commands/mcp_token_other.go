//go:build !unix

package commands

import "github.com/basecamp/basecamp-cli/internal/output"

// readTaskToken is refused where the connector cannot run: its ledger's
// privacy cannot be established off Unix, so no worker is started there.
func readTaskToken(int) (string, error) {
	return "", output.ErrUsage("--connect-state is only available on Unix, where the connector runs")
}
