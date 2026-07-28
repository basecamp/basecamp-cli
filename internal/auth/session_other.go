//go:build !darwin && !windows

package auth

import "os"

// guiSessionAvailable reports a display server the keyring's unlock prompt
// could use even with every standard stream detached.
func guiSessionAvailable() bool {
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}
