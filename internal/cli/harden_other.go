//go:build !unix

package cli

// hardenConfigDir is a no-op on non-Unix platforms (Windows, plan9, js/wasm),
// where the Unix permission and ownership model does not apply.
func hardenConfigDir(string) {}
