package commands

import (
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
)

// instructionsNotice points a session at `basecamp instructions` when the
// operator set some. The basecamp skill and the connector's subagent brief
// say to run it before writing, but an agent reads either only when it loads
// it: one that runs the CLI from its shell without loading the skill, or
// hands its text to a script that posts it, never sees that line and writes
// as if nothing were set. The hook reaches every session the plugin runs in,
// at its start and again after each compaction, so the instructions reach the
// agent before it drafts, whichever way its text gets to Basecamp.
//
// It names the command rather than carrying the text: a URL source may need a
// fetch, which a hook with a 5-second budget can't wait on, and the command
// reads the profile the post goes out as. No instructions set says nothing.
func instructionsNotice(cmd *cobra.Command) string {
	app := appctx.FromContext(cmd.Context())
	if app == nil || app.Config == nil || !anyInstructionsConfigured(app) {
		return ""
	}
	return "Your operator set instructions for anything an agent writes to Basecamp on this computer. " +
		"Before you write to Basecamp (a comment, card, message, document, to-do or chat line), run " +
		"`basecamp instructions`, with `-P <profile>` when the post goes out as a profile other than the default, " +
		"and follow what it prints. This holds when you run the basecamp CLI yourself and when you hand your " +
		"text to a script or tool that posts it. If the command fails, say so in what you post."
}

// anyInstructionsConfigured reports whether any post could go out under
// instructions: the baseline, the active profile's, or another profile's. The
// hook can't know which profile a session will post as, and a session that
// starts on a profile with none can still post with `-P` as one that has
// some, so a source set on any profile counts. It reads only the configured
// sources, never their contents.
func anyInstructionsConfigured(app *appctx.App) bool {
	if len(instructionsLayers(app)) > 0 {
		return true
	}
	for _, profile := range app.Config.Profiles {
		if profile != nil && profile.Instructions != "" {
			return true
		}
	}
	return false
}
