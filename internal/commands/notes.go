package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// NewNotesCmd creates the notes command for the current user's personal note.
//
// This is a singleton, not a collection: one note per person, addressed by no
// id at all. Hence show/set rather than list/create/update — there is nothing
// to enumerate and nothing to choose between.
func NewNotesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notes",
		Short: "Read and write your personal note",
		Long: `Read and write your personal note — a single private scratchpad.

The note is yours alone and lives outside any project. There is one per
person, so there is nothing to list and no id to pass.

  basecamp notes show
  basecamp notes set "Remember to follow up on the Q3 rollout"
  basecamp notes set --file notes.md
  cat notes.md | basecamp notes set`,
		Annotations: map[string]string{
			"agent_notes": "Account-wide and personal — no --in <project> needed.\n" +
				"Singleton: no id. 'set' replaces the whole note; it does not append.",
		},
	}

	cmd.AddCommand(
		newNotesShowCmd(),
		newNotesSetCmd(),
	)

	return cmd
}

func newNotesShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show your personal note",
		Long: `Show your personal note.

Before you have ever written to it the note does not exist server-side yet.
That is an empty note, not a missing one, so it renders as empty rather than
failing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			note, err := app.Account().MyNotes().Get(cmd.Context())
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(note,
				output.WithSummary(notesSummary(note)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "set",
						Cmd:         `basecamp notes set "<content>"`,
						Description: "Replace your note",
					},
				),
			)
		},
	}
}

// notesSummary describes a note, including the pre-first-write state.
//
// A note that has never been written has a nil id and empty content. That is a
// real, reachable state rather than an error: every account starts there, so it
// is reported plainly instead of being treated as a missing record.
func notesSummary(note *basecamp.MyNote) string {
	if note == nil || note.ID == nil {
		return "Your note is empty (nothing written yet)"
	}
	if strings.TrimSpace(note.Content) == "" {
		return "Your note is empty"
	}
	if note.UpdatedAt != nil {
		return fmt.Sprintf("Your note, last updated %s", note.UpdatedAt.Format("2006-01-02 15:04"))
	}
	return "Your note"
}

func newNotesSetCmd() *cobra.Command {
	var file string

	cmd := &cobra.Command{
		Use:   "set [content]",
		Short: "Replace your personal note",
		Long: `Replace your personal note with new content.

Content comes from a positional argument, --file, or piped stdin. Markdown is
converted to HTML, since the note is a rich text field — passing raw text
through would store escaped markup rather than formatting.

This replaces the whole note; it does not append. The first write creates the
note, so there is no separate "create" step.

  basecamp notes set "Follow up with Ann on the rollout"
  basecamp notes set --file notes.md
  cat notes.md | basecamp notes set

Attachments are out of scope: this writes the note body only.

Empty content is refused, in every form — an empty argument, an empty file, an
empty pipe. 'set' replaces everything, so an empty write is indistinguishable
from a script whose input silently produced nothing, and the note it would erase
is not recoverable from here.

The cost is that there is no way to clear the note from the CLI: do that on
Basecamp web. An explicit --clear would close that gap without weakening the
guard, and is deliberately left for its own change rather than folded in here —
a destructive verb deserves its own review, not a rider on a bump.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			content, err := notesContent(cmd, args, file)
			if err != nil {
				return err
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// The note is rich text (my_notes.md: content | HTML), so Markdown
			// is converted like every other content-writing command. Sending the
			// raw string would store escaped or malformed markup.
			note, err := app.Account().MyNotes().Update(cmd.Context(), richtext.MarkdownToHTML(content))
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(note,
				output.WithSummary("Note updated"),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp notes show",
						Description: "Read your note back",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", "Read note content from a file")

	return cmd
}

// notesContent resolves the note body from exactly one of the three inputs.
//
// Naming two sources is a usage error rather than a silent precedence rule: a
// caller who passes both an argument and --file has a wrong expectation about
// which one wins, and this command overwrites the whole note.
//
// All three sources are detected before any of them is chosen. Checking stdin
// only after an argument and --file had been ruled out made
// `generate | basecamp notes set --file fallback.md` overwrite the note from
// the file and discard the generated body without a word — the precise failure
// this function exists to prevent, in the one command that replaces everything.
func notesContent(cmd *cobra.Command, args []string, file string) (string, error) {
	positional := strings.Join(args, " ")

	piped, ok, err := readPipedStdin(cmd)
	if err != nil {
		return "", err
	}
	// An empty pipe is not a source. A redirected-but-empty stdin carries no
	// body to lose, so it must not turn a valid `--file` call into an error.
	hasPipe := ok && strings.TrimSpace(piped) != ""

	named := 0
	for _, present := range []bool{file != "", positional != "", hasPipe} {
		if present {
			named++
		}
	}
	if named > 1 {
		return "", output.ErrUsage("pass note content as an argument, with --file, or on stdin — not more than one")
	}

	switch {
	case file != "":
		data, err := os.ReadFile(file)
		if err != nil {
			return "", output.ErrUsage(fmt.Sprintf("failed to read %s: %v", file, err))
		}
		return notesRequireContent(string(data))
	case positional != "":
		return notesRequireContent(positional)
	case hasPipe:
		return notesRequireContent(piped)
	}

	return "", output.ErrUsageHint(
		"note content is required",
		`Pass it as an argument, with --file, or on stdin: basecamp notes set "..."`,
	)
}

// notesRequireContent refuses to blank the note by accident.
//
// set replaces everything, so an empty file or an empty pipe would silently
// erase the note. Clearing it is a reasonable thing to want, but it should be
// asked for on purpose rather than arrived at by a mistyped path.
func notesRequireContent(content string) (string, error) {
	if strings.TrimSpace(content) == "" {
		return "", output.ErrUsageHint(
			"note content is empty",
			"set replaces the whole note; pass content, or use --file with a non-empty file",
		)
	}
	return content, nil
}
