package commands

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/richtext"
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
// It names the command rather than carrying the full text: a URL source may
// need a fetch, which a hook with a 5-second budget can't wait on, and the
// command reads the profile the post goes out as. A pointer alone is often
// skipped, or read through `head`, so when every source of the active profile
// can be read without the network (a file, or a URL's cached copy, however
// old) the notice also carries their brief: one line per rule, enough to
// write by and short enough to sit in every session's context. No
// instructions set says nothing.
func instructionsNotice(cmd *cobra.Command) string {
	app := appctx.FromContext(cmd.Context())
	if app == nil || app.Config == nil || !anyInstructionsConfigured(app) {
		return ""
	}
	pointer := "Your operator set instructions for anything an agent writes to Basecamp on this computer. " +
		"Before you write to Basecamp (a comment, card, message, document, to-do or chat line), run " +
		"`basecamp instructions`, with `-P <profile>` when the post goes out as a profile other than the default, " +
		"and follow what it prints. This holds when you run the basecamp CLI yourself and when you hand your " +
		"text to a script or tool that posts it. If the command fails, say so in what you post."
	if gist := instructionsGist(app); gist != "" {
		return pointer + "\n\n" + gist
	}
	return pointer
}

// instructionsGist is the brief of the active profile's sources, read
// without the network, or "" when some source has no copy yet or none has a
// list to cut down. A source with no list is left out of it; the header
// sends the reader to the command for the full text either way.
func instructionsGist(app *appctx.App) string {
	result, ok := loadCachedInstructions(instructionsHookApp(app))
	if !ok || len(result.Documents) == 0 {
		return ""
	}
	var lines []string
	for _, doc := range result.Documents {
		if len(doc.Brief) == 0 {
			continue
		}
		if len(result.Documents) > 1 {
			lines = append(lines, "From "+richtext.SanitizeSingleLine(doc.Scope)+":")
		}
		lines = append(lines, doc.Brief...)
	}
	if len(lines) == 0 {
		return ""
	}
	return "The gist (run the command for the full text):\n" + strings.Join(lines, "\n")
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

// instructionsHookApp is app with the cache dir the command itself would use.
// The command keeps a profile's cache under profiles/<name> (root's
// PersistentPreRunE scopes it), but the hook's app keeps the unscoped dir for
// its own state, so the instructions cache would be looked for in the wrong
// place. Only the config is copied: the hook's own state stays where it is.
func instructionsHookApp(app *appctx.App) *appctx.App {
	cfg := *app.Config
	name := cfg.ActiveProfile
	if name != "" && name != "." && filepath.IsLocal(name) && filepath.Base(name) == name && os.Getenv("BASECAMP_CACHE_DIR") == "" {
		cfg.CacheDir = filepath.Join(cfg.CacheDir, "profiles", name)
	}
	return &appctx.App{Config: &cfg}
}
