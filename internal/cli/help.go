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
		// Bare group with explicit flags (e.g. "cards --in X"): suppress
		// help output so Execute() can emit a usage error with non-zero exit.
		// Must run before machine-help branches to avoid mixed output.
		if isBareGroupWithFlags(cmd) {
			return
		}

		pf := cmd.Root().PersistentFlags()
		if agent, _ := pf.GetBool("agent"); agent {
			emitAgentHelp(cmd)
			return
		}
		if jsonFlag, _ := pf.GetBool("json"); jsonFlag {
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

// hasExplicitLocalFlags reports whether any flag declared directly on cmd
// (not inherited from parents) was explicitly set, excluding --help.
func hasExplicitLocalFlags(cmd *cobra.Command) bool {
	found := false
	cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
		if f.Changed && f.Name != "help" {
			found = true
		}
	})
	return found
}

// isBareGroupWithFlags reports whether cmd is a non-runnable group command
// that was invoked bare (no subcommand) with explicitly-set local flags.
// Explicit --help always renders help regardless of other flags.
func isBareGroupWithFlags(cmd *cobra.Command) bool {
	if cmd.Runnable() || !cmd.HasSubCommands() {
		return false
	}
	if helpFlag := cmd.Flags().Lookup("help"); helpFlag != nil && helpFlag.Changed {
		return false
	}
	return hasExplicitLocalFlags(cmd)
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
		include:  map[string]bool{"projects": true, "todos": true, "todolists": true, "messages": true, "chat": true, "cards": true},
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
		{"", "--jq", "Filter JSON with jq expression"},
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

	// FLAGS — local flags plus parent-scoped persistent flags (e.g. --chat,
	// --project defined on a parent command). This promotes parent-scoped
	// flags into the primary FLAGS section where they're immediately visible,
	// rather than burying them in INHERITED FLAGS alongside root globals.
	merged := pflag.NewFlagSet("flags", pflag.ContinueOnError)
	cmd.LocalFlags().VisitAll(func(f *pflag.Flag) { merged.AddFlag(f) })
	parentScopedFlags(cmd).VisitAll(func(f *pflag.Flag) {
		if merged.Lookup(f.Name) == nil {
			merged.AddFlag(f)
		}
	})
	flagsUsage := strings.TrimRight(merged.FlagUsages(), "\n")
	if flagsUsage != "" {
		b.WriteString("\n")
		b.WriteString(r.Header.Render("FLAGS"))
		b.WriteString("\n")
		b.WriteString(flagsUsage)
		b.WriteString("\n")
	}

	// INHERITED FLAGS — root-level globals only. Parent-scoped persistent
	// flags (--project, --chat, etc.) are promoted into FLAGS above.
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
// INHERITED FLAGS. Non-root parent-scoped persistent flags are promoted
// into the FLAGS section by parentScopedFlags and never appear here.
var salientRootFlags = map[string]bool{
	"account": true,
	"json":    true,
	"md":      true,
	"project": true,
	"quiet":   true,
}

// parentScopedFlags returns inherited flags that originate from a non-root
// parent command. These are promoted into the FLAGS section so they're
// immediately visible on leaf commands, rather than buried in INHERITED FLAGS.
// Provenance is determined by pointer identity against root's PersistentFlags.
func parentScopedFlags(cmd *cobra.Command) *pflag.FlagSet {
	root := cmd.Root()
	ps := pflag.NewFlagSet("parent-scoped", pflag.ContinueOnError)
	cmd.InheritedFlags().VisitAll(func(f *pflag.Flag) {
		rootFlag := root.PersistentFlags().Lookup(f.Name)
		if rootFlag != nil && rootFlag == f {
			return // root-level — stays in INHERITED FLAGS
		}
		ps.AddFlag(f)
	})
	return ps
}

// filterInheritedFlags returns formatted flag usages for INHERITED FLAGS,
// containing only the curated subset of root-level globals. Parent-scoped
// persistent flags (--chat, --project on messages, etc.) are excluded here
// because parentScopedFlags promotes them into FLAGS.
func filterInheritedFlags(cmd *cobra.Command) string {
	root := cmd.Root()
	filtered := pflag.NewFlagSet("inherited", pflag.ContinueOnError)

	// Commands that accept <id|url> resolve the project from the ID
	// automatically, so --project is noise in their help output.
	acceptsID := strings.Contains(cmd.Use, "<id|url>")

	cmd.InheritedFlags().VisitAll(func(f *pflag.Flag) {
		rootFlag := root.PersistentFlags().Lookup(f.Name)
		if rootFlag == nil || rootFlag != f {
			return // parent-scoped — already promoted to FLAGS
		}
		if !salientRootFlags[f.Name] {
			return
		}
		if acceptsID && f.Name == "project" {
			return
		}
		filtered.AddFlag(f)
	})

	return strings.TrimRight(filtered.FlagUsages(), "\n")
}
