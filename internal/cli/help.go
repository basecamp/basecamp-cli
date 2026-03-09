package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/commands"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// rootHelpFunc returns a help function that renders gh-style help for the root
// command, agent JSON when --agent is set, and falls through to cobra's default
// for subcommands.
func rootHelpFunc(defaultHelp func(*cobra.Command, []string)) func(*cobra.Command, []string) {
	return func(cmd *cobra.Command, args []string) {
		// --agent → structured JSON help
		if agent, _ := cmd.Root().PersistentFlags().GetBool("agent"); agent {
			emitAgentHelp(cmd)
			return
		}

		// Only override the root command's help
		if cmd != cmd.Root() {
			defaultHelp(cmd, args)
			return
		}

		renderRootHelp(cmd.OutOrStdout(), cmd)
	}
}

// curatedCategories defines the subset of categories and commands shown in root help.
// Commands not listed here are discoverable via `basecamp commands`.
var curatedCategories = []struct {
	heading  string
	category string
	include  map[string]bool // nil = include all from category
}{
	{
		heading:  "CORE COMMANDS",
		category: "Core Commands",
		include:  map[string]bool{"projects": true, "todos": true, "todolists": true, "messages": true, "campfire": true, "cards": true},
	},
	{
		heading:  "SHORTCUTS",
		category: "Shortcut Commands",
		include:  map[string]bool{"todo": true, "done": true, "comment": true, "message": true, "card": true},
	},
	{
		heading:  "SEARCH & BROWSE",
		category: "Search & Browse",
		include:  map[string]bool{"search": true, "show": true, "url": true},
	},
	{
		heading:  "AUTH & CONFIG",
		category: "Auth & Config",
		include:  map[string]bool{"auth": true, "config": true, "setup": true},
	},
}

// helpEntry is a command name + description for rendering.
type helpEntry struct {
	name string
	desc string
}

func renderRootHelp(w io.Writer, cmd *cobra.Command) {
	r := output.NewRenderer(w, false)
	var b strings.Builder

	b.WriteString(cmd.Short)
	b.WriteString("\n")

	// USAGE
	b.WriteString("\n")
	b.WriteString(r.Header.Render("USAGE"))
	b.WriteString("\n")
	b.WriteString("  basecamp <command> [flags]\n")

	// Build lookup from command name → registered cobra.Command
	registered := make(map[string]*cobra.Command, len(cmd.Commands()))
	for _, sub := range cmd.Commands() {
		registered[sub.Name()] = sub
	}

	// Build lookup from category name → command names (for ordering)
	allCategories := commands.CommandCategories()
	catMap := make(map[string][]string, len(allCategories))
	for _, cat := range allCategories {
		names := make([]string, len(cat.Commands))
		for i, ci := range cat.Commands {
			names[i] = ci.Name
		}
		catMap[cat.Name] = names
	}

	// Render curated categories
	for _, cur := range curatedCategories {
		names := catMap[cur.category]
		if len(names) == 0 {
			continue
		}

		// Filter to included commands, resolve descriptions from live commands
		var entries []helpEntry
		maxName := 0
		for _, name := range names {
			if cur.include != nil && !cur.include[name] {
				continue
			}
			sub := registered[name]
			if sub == nil {
				continue
			}
			entries = append(entries, helpEntry{name: name, desc: sub.Short})
			if len(name) > maxName {
				maxName = len(name)
			}
		}

		b.WriteString("\n")
		b.WriteString(r.Header.Render(cur.heading))
		b.WriteString("\n")
		for _, e := range entries {
			fmt.Fprintf(&b, "  %-*s  %s\n", maxName, e.name, e.desc)
		}
	}

	// FLAGS — curated subset of global flags
	b.WriteString("\n")
	b.WriteString(r.Header.Render("FLAGS"))
	b.WriteString("\n")

	type flagEntry struct {
		short string
		long  string
		desc  string
	}
	flags := []flagEntry{
		{"-j", "--json", "Output as JSON"},
		{"-m", "--md", "Output as Markdown"},
		{"-q", "--quiet", "Quiet output"},
		{"-p", "--project", "Project ID or name"},
		{"-v", "--verbose", "Verbose output"},
		{"", "--help", "Show help for command"},
		{"", "--version", "Show version"},
	}

	for _, f := range flags {
		if f.short != "" {
			fmt.Fprintf(&b, "  %s, %-12s %s\n", f.short, f.long, f.desc)
		} else {
			fmt.Fprintf(&b, "      %-12s %s\n", f.long, f.desc)
		}
	}

	// EXAMPLES
	b.WriteString("\n")
	b.WriteString(r.Header.Render("EXAMPLES"))
	b.WriteString("\n")
	examples := []string{
		"$ basecamp projects",
		"$ basecamp todos",
		`$ basecamp todo "Write the proposal"`,
		`$ basecamp search "quarterly review"`,
	}
	for _, ex := range examples {
		b.WriteString(r.Muted.Render("  "+ex) + "\n")
	}

	// LEARN MORE
	b.WriteString("\n")
	b.WriteString(r.Header.Render("LEARN MORE"))
	b.WriteString("\n")
	b.WriteString("  basecamp commands      List all available commands\n")
	b.WriteString("  basecamp <command> -h  Help for any command\n")

	fmt.Fprint(w, b.String())
}
