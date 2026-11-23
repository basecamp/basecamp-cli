// Package skills embeds the skill files in the binary.
package skills

import "embed"

//go:embed basecamp
var FS embed.FS

// Connect is the basecamp-connect skill's two files: the orchestrator's
// workflow and the worker's. The connector gives them, as they are, to the
// agents it starts when it runs the work itself.
//
//go:embed basecamp-connect/SKILL.md basecamp-connect/subagent.md
var Connect embed.FS
