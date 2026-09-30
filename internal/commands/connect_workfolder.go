package commands

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// workFolderCheck says which folder workers run in: the one the connector is
// started from (connector.DispatcherOptions.WorkDir), which setup and doctor
// take to be the one they run in. Workers may change files there without
// asking, so the home directory, or a filesystem root, is a warning.
func workFolderCheck() setup.Check {
	c := setup.Check{Name: "Work folder"}
	dir, err := os.Getwd()
	if err != nil {
		// The connector can't start without it (connector.NewDispatcher).
		c.Status = setup.StatusFail
		c.Message = "Couldn't read which folder this is: " + richtext.SanitizeSingleLine(err.Error())
		c.Hint = "Change to a folder that exists, then run this again."
		return c
	}
	// The path is shown on one line: a folder name can carry newlines or
	// terminal escapes, and check messages are printed as they are.
	shown := richtext.SanitizeSingleLine(dir)
	switch home, err := os.UserHomeDir(); {
	case err == nil && samePath(dir, home):
		c.Status = setup.StatusWarn
		c.Message = fmt.Sprintf("Started from here, the agent works in %s, your home folder, and may change files anywhere in it without asking", shown)
	case isFilesystemRoot(dir):
		c.Status = setup.StatusWarn
		c.Message = fmt.Sprintf("Started from here, the agent works in %s, the root of the filesystem, and may change any file it can reach without asking", shown)
	default:
		c.Status = setup.StatusPass
		c.Message = fmt.Sprintf("Started from here, the agent works in %s and may change files in it without asking", shown)
		return c
	}
	c.Hint = "Start the connector from a folder made for the agent, or from the project it should work on."
	return c
}

// isFilesystemRoot reports whether dir is a root: / on Unix, a volume root
// such as C:\ or a UNC share on Windows. A root is its own parent, once
// symlinks are resolved: /proc/self/root is / by another name.
func isFilesystemRoot(dir string) bool {
	// Resolve the path as given: cleaning first would turn a path like
	// /proc/self/root/.. into /proc/self, which is not where it leads.
	clean := filepath.Clean(dir)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		clean = resolved
	}
	return filepath.Dir(clean) == clean
}

// samePath reports whether two paths name the same folder: the same file on
// disk, whatever the path's spelling, symlinks or letter case (a Mac's
// filesystem ignores case). Paths that can't be read compare as cleaned text.
func samePath(a, b string) bool {
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return os.SameFile(ia, ib)
}
