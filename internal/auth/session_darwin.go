package auth

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// guiSessionAvailable reports whether the process runs inside a macOS Aqua
// (GUI) session, where the keychain unlock prompt appears as a WindowServer
// dialog even with every standard stream detached — GUI-launched apps and
// IDE task runners. `launchctl managername` prints "Aqua" there; ssh and CI
// run under StandardIO or Background. launchctl only inspects the launchd
// session — it cannot touch the keychain — but is bounded anyway, and any
// failure counts as no GUI so genuinely headless sessions keep the bounded
// probe.
func guiSessionAvailable() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/bin/launchctl", "managername").Output()
	return err == nil && strings.TrimSpace(string(out)) == "Aqua"
}
