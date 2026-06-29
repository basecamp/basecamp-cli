// Package skills embeds the skill files in the binary.
package skills

import "embed"

//go:embed basecamp basecamp-import
var FS embed.FS
