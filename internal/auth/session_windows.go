package auth

// guiSessionAvailable is always false on Windows: Credential Manager
// operates without prompting, so a bounded probe cannot cut off an
// interaction, and misclassifying a GUI-launched session as headless is
// harmless here.
func guiSessionAvailable() bool {
	return false
}
