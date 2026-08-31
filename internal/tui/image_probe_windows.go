//go:build !unix

package tui

import "os"

// DetectImageSupport does nothing where the probe cannot run: the answer stays no,
// and a picture is its filename. BASECAMP_IMAGE_PROTOCOL=kitty draws them anyway.
func DetectImageSupport(in, out *os.File) {}
