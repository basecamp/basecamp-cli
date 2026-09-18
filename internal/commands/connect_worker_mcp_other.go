//go:build !unix

package commands

import "errors"

func execWorkerMCP(string, string, string, string) error {
	return errors.New("worker-mcp runs on Linux only")
}
