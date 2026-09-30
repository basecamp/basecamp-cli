package commands

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

// workFolderCheck says which folder workers run in: the one the connector is
// started from (connector.DispatcherOptions.WorkDir), which setup and doctor
// take to be the one they run in. Workers may change files there without
// asking, so the home directory, or the filesystem's root, is a warning.
func workFolderCheck() setup.Check {
	c := setup.Check{Name: "Work folder"}
	dir, err := os.Getwd()
	if err != nil {
		c.Status = setup.StatusWarn
		c.Message = "Couldn't tell which folder this is: " + err.Error()
		return c
	}
	if home, err := os.UserHomeDir(); (err == nil && samePath(dir, home)) || samePath(dir, string(filepath.Separator)) {
		c.Status = setup.StatusWarn
		c.Message = fmt.Sprintf("Started from here, the agent works in %s, your home folder, and may change files anywhere in it without asking", dir)
		c.Hint = "Start the connector from a folder made for the agent, or from the project it should work on."
		return c
	}
	c.Status = setup.StatusPass
	c.Message = fmt.Sprintf("Started from here, the agent works in %s and may change files in it without asking", dir)
	return c
}

// samePath reports whether two paths name the same folder, through symlinks.
func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}
