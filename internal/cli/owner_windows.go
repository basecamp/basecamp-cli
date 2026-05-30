//go:build windows

package cli

import "os"

// isForeignOwnerWritable is a no-op on Windows, where the Unix owner model and
// this TOCTOU vector do not apply; the group/world-writable check still runs.
func isForeignOwnerWritable(os.FileInfo) bool { return false }
