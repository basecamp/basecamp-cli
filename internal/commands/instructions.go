package commands

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// instructionsMaxBytes caps one instructions file. The text is read into an
// agent's context before every write to Basecamp, so a file this size is
// already a long style guide; anything past it is a mistake (the wrong path,
// a generated file), and reading it would push the agent's own rules out.
const instructionsMaxBytes = 64 * 1024

// instructionsPreface frames the files for the agent reading them. The text
// comes from a file on this computer that only the person's own global
// config can name, so it is the operator's direction on how to write, unlike
// anything the agent reads in Basecamp. It still never overrides the
// operator's own words or the skill's rules.
const instructionsPreface = "Follow these instructions when you write anything to Basecamp. " +
	"They come from a file on this computer that your operator chose, not from Basecamp. " +
	"They are defaults, and where several are listed, a later one wins where they differ. " +
	"A project's own instructions for agents (its AGENTS.md document), if it has them, win on how posts look in that project. " +
	"On voice (tone, length, emoji), the person you work for wins: " +
	"their own instructions to you, and the profile's file listed here, over the shared one. " +
	"An instruction they give you for a single post wins over all of these. " +
	"None of this overrides your skill's rules."

// resolveInstructionsFile turns the path given to `config set` into the
// absolute path that is saved, and checks it names a readable file now, so
// a typo fails at set time rather than on an agent's next write.
func resolveInstructionsFile(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	usage := func(msg string) error {
		return output.ErrUsageHint(msg, "Give the path of a Markdown file on this computer, e.g. ~/notes/basecamp-style.md")
	}
	if path == "" {
		return "", usage("instructions_file needs the path of a file")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", usage(fmt.Sprintf("cannot expand ~ in %q: %v", path, err))
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", usage(fmt.Sprintf("cannot resolve %q: %v", raw, err))
	}
	if _, err := readInstructionsFile(abs); err != nil {
		return "", err
	}
	return abs, nil
}

// instructionsDocument is one instructions file as `basecamp instructions`
// reports it. Files are listed baseline first: the top-level
// instructions_file, then the active profile's own, so an agent's voice sits
// on top of the shared basics.
type instructionsDocument struct {
	Scope      string    `json:"scope"` // where the path was set: "global", "system", or "profile:<name>"
	Path       string    `json:"path"`
	Content    string    `json:"content"` // Markdown, as the file holds it
	ModifiedAt time.Time `json:"modified_at"`
}

type instructionsResult struct {
	// Configured says an instructions_file is set at all.
	Configured bool                   `json:"configured"`
	Guidance   string                 `json:"guidance,omitempty"`
	Documents  []instructionsDocument `json:"documents"`
}

// NewInstructionsCmd creates the instructions command.
func NewInstructionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "instructions",
		Short: "Print the instructions agents follow when writing to Basecamp",
		Long: `Print the local file set as instructions_file, as Markdown.

Set the shared baseline with:

  basecamp config set instructions_file <path> --global

and, on top of it, a profile's own (an agent's voice, say) with:

  basecamp config set instructions_file <path> --global --for-profile <name>

Agents run this before they write to Basecamp and follow what it prints:
the baseline first, then the active profile's file, which wins where the
two differ. These are defaults: a project's AGENTS.md wins on how posts
look in that project, the operator's own instructions win on voice, and
an instruction for a single post wins over everything.

The file is read from disk every time, so an edit applies to the next
write. Keep it somewhere changes are reviewed, like a git repository:
whoever can change the file steers every agent on this computer. Only
the global config can set the path, never a repo's.

Prints nothing when no file is set.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			result, err := loadInstructions(app)
			if err != nil {
				return err
			}
			return renderInstructions(cmd, app, result)
		},
	}
}

// instructionsLayer is one place a file can be set, in the order the layers
// are read: the shared baseline first, then the active profile's own, which
// is more specific and wins where the two differ.
type instructionsLayer struct {
	scope string
	path  string
}

func instructionsLayers(app *appctx.App) []instructionsLayer {
	var layers []instructionsLayer
	if path := app.Config.InstructionsFile; path != "" {
		scope := app.Config.Sources["instructions_file"]
		if scope == "" {
			scope = "global"
		}
		layers = append(layers, instructionsLayer{scope: scope, path: path})
	}
	if path := app.Config.ProfileInstructionsFile; path != "" {
		layers = append(layers, instructionsLayer{scope: "profile:" + app.Config.ActiveProfile, path: path})
	}
	return layers
}

// loadInstructions reads every configured file, baseline first. A file that
// is set but can't be read is an error, not silence: the person chose it,
// and an agent writing without it should say so rather than carry on as if
// nothing were set.
func loadInstructions(app *appctx.App) (instructionsResult, error) {
	layers := instructionsLayers(app)
	result := instructionsResult{Configured: len(layers) > 0, Documents: []instructionsDocument{}}
	for _, layer := range layers {
		doc, err := readInstructionsFile(layer.path)
		if err != nil {
			return instructionsResult{}, err
		}
		doc.Scope = layer.scope
		result.Documents = append(result.Documents, doc)
	}
	if result.Configured {
		result.Guidance = instructionsPreface
	}
	return result, nil
}

// readInstructionsFile reads one file, refusing what can't be an
// instructions file: a relative path (which would follow the working
// directory, and with it whichever repo the agent stands in), a directory,
// a file over instructionsMaxBytes, or one that isn't UTF-8 text.
func readInstructionsFile(path string) (instructionsDocument, error) {
	fix := "Point it at another file with `basecamp config set instructions_file <path> --global`, or unset it with `basecamp config unset instructions_file --global`"
	if !filepath.IsAbs(path) {
		return instructionsDocument{}, output.ErrUsageHint(fmt.Sprintf("instructions_file %q is not an absolute path", path), fix)
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return instructionsDocument{}, &output.Error{
				Code:    output.CodeNotFound,
				Message: fmt.Sprintf("the instructions file %s does not exist", path),
				Hint:    fix,
			}
		}
		return instructionsDocument{}, output.ErrUsageHint(fmt.Sprintf("cannot read the instructions file %s: %v", path, err), fix)
	}
	if !info.Mode().IsRegular() {
		return instructionsDocument{}, output.ErrUsageHint(fmt.Sprintf("the instructions file %s is not a regular file", path), fix)
	}
	if info.Size() > instructionsMaxBytes {
		return instructionsDocument{}, output.ErrUsageHint(
			fmt.Sprintf("the instructions file %s is %d bytes, over the %d-byte limit", path, info.Size(), instructionsMaxBytes), fix)
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path comes from the person's own global config
	if err != nil {
		return instructionsDocument{}, output.ErrUsageHint(fmt.Sprintf("cannot read the instructions file %s: %v", path, err), fix)
	}
	if len(data) > instructionsMaxBytes {
		return instructionsDocument{}, output.ErrUsageHint(
			fmt.Sprintf("the instructions file %s is %d bytes, over the %d-byte limit", path, len(data), instructionsMaxBytes), fix)
	}
	if !utf8.Valid(data) {
		return instructionsDocument{}, output.ErrUsageHint(fmt.Sprintf("the instructions file %s is not UTF-8 text", path), fix)
	}
	return instructionsDocument{
		Path:       path,
		Content:    strings.TrimSpace(string(data)),
		ModifiedAt: info.ModTime().UTC(),
	}, nil
}

// renderInstructions prints the envelope in machine modes and, otherwise, the
// files themselves as Markdown, the form an agent reads most directly. With
// nothing set it prints nothing on stdout and says so on stderr, so "run
// basecamp instructions before writing" is safe for every agent to do.
func renderInstructions(cmd *cobra.Command, app *appctx.App, result instructionsResult) error {
	if app.IsMachineOutput() {
		summary := "No instructions to follow: no instructions_file is set"
		if result.Configured {
			summary = instructionsPreface
		}
		return app.OK(result, output.WithSummary(summary))
	}

	if !result.Configured {
		fmt.Fprintln(cmd.ErrOrStderr(), "No instructions to follow: no instructions_file is set.")
		return nil
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%s\n", instructionsPreface)
	for _, doc := range result.Documents {
		// Printed to a terminal: strip the controls that could move the
		// cursor or rewrite the screen.
		fmt.Fprintf(out, "\n---\n\nSource: %s (%s)\n\n%s\n",
			richtext.SanitizeSingleLine(doc.Path), doc.Scope, richtext.SanitizeTerminal(doc.Content))
	}
	return nil
}
