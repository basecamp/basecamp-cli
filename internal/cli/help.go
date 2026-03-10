package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/basecamp/basecamp-cli/internal/commands"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// rootHelpFunc returns a help function that renders styled help for all
// commands: agent JSON when --agent is set, curated categories for root,
// and a consistent styled layout for every subcommand.
func rootHelpFunc() func(*cobra.Command, []string) {
	return func(cmd *cobra.Command, args []string) {
		if agent, _ := cmd.Root().PersistentFlags().GetBool("agent"); agent {
			emitAgentHelp(cmd)
			return
		}
		if cmd == cmd.Root() {
			renderRootHelp(cmd.OutOrStdout(), cmd)
			return
		}
		renderCommandHelp(cmd)
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
		include:  map[string]bool{"todo": true, "done": true, "comment": true, "message": true, "card": true, "attach": true, "upload": true},
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

// renderCommandHelp renders styled help for any non-root command, reading
// structure from cobra's command tree rather than hardcoding per-command.
func renderCommandHelp(cmd *cobra.Command) {
	w := cmd.OutOrStdout()
	r := output.NewRenderer(w, false)
	var b strings.Builder

	// Description
	desc := cmd.Long
	if desc == "" {
		desc = cmd.Short
	}
	if desc != "" {
		b.WriteString(desc)
		b.WriteString("\n")
	}

	// USAGE
	b.WriteString("\n")
	b.WriteString(r.Header.Render("USAGE"))
	b.WriteString("\n")
	if cmd.HasAvailableSubCommands() && !cmd.Runnable() {
		b.WriteString("  " + cmd.CommandPath() + " <command> [flags]\n")
	} else {
		b.WriteString("  " + cmd.UseLine() + "\n")
	}

	// ALIASES
	if len(cmd.Aliases) > 0 {
		b.WriteString("\n")
		b.WriteString(r.Header.Render("ALIASES"))
		b.WriteString("\n")
		b.WriteString("  " + cmd.Name())
		for _, a := range cmd.Aliases {
			b.WriteString(", " + a)
		}
		b.WriteString("\n")
	}

	// ARGUMENTS
	if args := ParseArgs(cmd); len(args) > 0 {
		b.WriteString("\n")
		b.WriteString(r.Header.Render("ARGUMENTS"))
		b.WriteString("\n")

		// Compute max display width for alignment
		maxDisplay := 0
		displays := make([]string, len(args))
		for i, a := range args {
			displays[i] = ArgDisplay(a)
			if len(displays[i]) > maxDisplay {
				maxDisplay = len(displays[i])
			}
		}
		for i, a := range args {
			desc := a.Description
			if !a.Required {
				desc += " (optional)"
			}
			if a.Variadic {
				desc += " (one or more)"
			}
			fmt.Fprintf(&b, "  %-*s  %s\n", maxDisplay, displays[i], desc)
		}
	}

	// COMMANDS
	if cmd.HasAvailableSubCommands() {
		var entries []helpEntry
		maxName := 0
		for _, sub := range cmd.Commands() {
			if !sub.IsAvailableCommand() {
				continue
			}
			entries = append(entries, helpEntry{name: sub.Name(), desc: sub.Short})
			if len(sub.Name()) > maxName {
				maxName = len(sub.Name())
			}
		}
		b.WriteString("\n")
		b.WriteString(r.Header.Render("COMMANDS"))
		b.WriteString("\n")
		for _, e := range entries {
			fmt.Fprintf(&b, "  %-*s  %s\n", maxName, e.name, e.desc)
		}
	}

	// FLAGS (all local flags: persistent + non-persistent)
	localFlags := cmd.LocalFlags()
	localUsage := strings.TrimRight(localFlags.FlagUsages(), "\n")
	if localUsage != "" {
		b.WriteString("\n")
		b.WriteString(r.Header.Render("FLAGS"))
		b.WriteString("\n")
		b.WriteString(localUsage)
		b.WriteString("\n")
	}

	// INHERITED FLAGS
	// Parent-defined persistent flags (--project, --campfire, etc.) always
	// show — they carry required context. Root-level global flags are curated
	// to the essentials so leaf help isn't 20+ lines of noise.
	inherited := filterInheritedFlags(cmd)
	if inherited != "" {
		b.WriteString("\n")
		b.WriteString(r.Header.Render("INHERITED FLAGS"))
		b.WriteString("\n")
		b.WriteString(inherited)
		b.WriteString("\n")
	}

	// EXAMPLES
	if cmd.Example != "" {
		b.WriteString("\n")
		b.WriteString(r.Header.Render("EXAMPLES"))
		b.WriteString("\n")
		for _, line := range strings.Split(cmd.Example, "\n") {
			b.WriteString(r.Muted.Render(line) + "\n")
		}
	}

	// LEARN MORE
	b.WriteString("\n")
	b.WriteString(r.Header.Render("LEARN MORE"))
	b.WriteString("\n")
	if cmd.HasAvailableSubCommands() {
		b.WriteString("  " + cmd.CommandPath() + " <command> --help\n")
	} else if cmd.HasParent() {
		b.WriteString("  " + cmd.Parent().CommandPath() + " --help\n")
	}

	fmt.Fprint(w, b.String())
}

// salientRootFlags is the curated set of root-level global flags shown in
// inherited flag sections. Parent-defined persistent flags always appear;
// only root globals are filtered to this set.
var salientRootFlags = map[string]bool{
	"account": true,
	"json":    true,
	"md":      true,
	"project": true,
	"quiet":   true,
}

// filterInheritedFlags returns formatted flag usages for inherited flags,
// keeping all parent-defined persistent flags and curating root globals
// to the salient set. Provenance is determined by pointer identity: if the
// flag object is the same pointer as the one on root's PersistentFlags,
// it truly originates from root. A parent that redefines the same name
// (e.g. --project on messages) produces a different pointer and always
// passes through.
func filterInheritedFlags(cmd *cobra.Command) string {
	root := cmd.Root()
	filtered := pflag.NewFlagSet("inherited", pflag.ContinueOnError)

	// Commands that accept <id|url> resolve the project from the ID
	// automatically, so --project is noise in their help output.
	acceptsID := strings.Contains(cmd.Use, "<id|url>")

	cmd.InheritedFlags().VisitAll(func(f *pflag.Flag) {
		rootFlag := root.PersistentFlags().Lookup(f.Name)
		if rootFlag != nil && rootFlag == f && !salientRootFlags[f.Name] {
			return
		}
		if acceptsID && f.Name == "project" && rootFlag == f {
			return
		}
		filtered.AddFlag(f)
	})

	return strings.TrimRight(filtered.FlagUsages(), "\n")
}
