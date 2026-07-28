//go:build !darwin

package auth

import "os"

// guiSessionAvailable reports a display server the keyring's unlock prompt
// could use even with every standard stream detached. On Windows this is
// always false: Credential Manager operates without prompting, so a bounded
// probe cannot cut off an interaction there.
func guiSessionAvailable() bool {
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}
