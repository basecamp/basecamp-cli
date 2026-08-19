package commands

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/stdinarg"
)

// This file is the single home for "-" (read from stdin) handling.
//
// Tier 1: commands that accept content register where "-" is honored via
// allowDash and resolve it with resolveContentArg / resolveContentValue.
// Tier 2: every other exact "-" — positional or flag value — is caught by the
// dash guard installed over the whole command tree: when stdin is piped, a
// stray "-" is ambiguous (the caller almost certainly meant "read the pipe"),
// so it fails as a usage error instead of landing as literal content. On a
// TTY, a literal "-" stays legal everywhere.

// allowDash marks where cmd accepts "-" as "read from stdin", merging with any
// tokens already registered. Tokens: "arg:0" (exact positional index),
// "arg:1+" (that index and beyond), "flag:data" (a flag value).
func allowDash(cmd *cobra.Command, tokens ...string) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	merged := strings.Join(tokens, " ")
	if existing := cmd.Annotations[stdinarg.AnnotationAllowDash]; existing != "" {
		merged = existing + " " + merged
	}
	cmd.Annotations[stdinarg.AnnotationAllowDash] = merged
}

// readStdinContent reads content for a "-" placeholder from piped stdin.
//
// Nothing piped (a TTY) is a usage error rather than a silent read: waiting on
// an interactive terminal looks like a hang, so the error teaches the escape
// hatches instead. A piped-but-blank stdin is also refused — blank content is
// never an intentional write, and for update-style commands it would be an
// implicit clear.
//
// Trailing newlines — LF and CRLF alike — are trimmed: Markdown bodies don't
// care, but titles and boosts (16-rune limit) do, and virtually every pipe
// ends with one. Interior line breaks are untouched.
func readStdinContent(cmd *cobra.Command, what string) (string, error) {
	if !stdinarg.IsPiped(cmd.InOrStdin()) {
		return "", output.ErrUsageHint(
			fmt.Sprintf(`%s is "-" (read from stdin) but nothing is piped`, what),
			stdinEscapeHint(cmd, what),
		)
	}
	data, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return "", output.ErrUsage(fmt.Sprintf("failed to read %s from stdin: %v", what, err))
	}
	content := strings.TrimRight(string(data), "\r\n")
	if strings.TrimSpace(content) == "" {
		return "", output.ErrUsage(fmt.Sprintf("stdin for %s is empty", what))
	}
	return content, nil
}

// stdinEscapeHint lists the ways to satisfy a "-" from an interactive
// terminal, mentioning --edit only where the command has it.
//
// The examples repeat the input that actually carried the "-": suggesting a
// bare trailing "-" for a flag would exceed the command's positional arity.
func stdinEscapeHint(cmd *cobra.Command, what string) string {
	path := cmd.CommandPath()
	source := "-"
	if strings.HasPrefix(what, "--") {
		source = what + " -"
	}
	hint := fmt.Sprintf(
		"Pipe the content (printf '...' | %[1]s ... %[2]s), use a heredoc (%[1]s ... %[2]s <<'EOF'), or run cat | %[1]s ... %[2]s and type the content, ending with Ctrl-D",
		path, source)
	if cmd.Flags().Lookup("edit") != nil {
		hint += "; or compose with --edit"
	}
	return hint
}

// resolveContentArg resolves the join-all positional content pattern: exactly
// ["-"] reads stdin, any other args join with spaces. A "-" mixed in with
// other tokens is a usage error — it can't be both stdin and part of the
// joined text, and pre-guard versions silently posted the literal join.
// Every join-all site names its positional <content>, so errors do too.
//
// argsOffset is the index of args[0] in the command's full positional list,
// so a "-" placed at or after the "--" separator stays literal.
func resolveContentArg(cmd *cobra.Command, args []string, argsOffset int) (string, error) {
	dashes := 0
	for i, a := range args {
		if a == "-" && !afterDashSeparator(cmd, argsOffset+i) {
			dashes++
		}
	}
	switch {
	case dashes == 0:
		return strings.Join(args, " "), nil
	case len(args) == 1:
		return readStdinContent(cmd, "<content>")
	default:
		return "", output.ErrUsage(`"-" (stdin) must be the only <content> argument`)
	}
}

// resolveContentValue resolves a single content value — an exact positional
// (pass its index) or a flag value (pass argIndex -1). Exactly "-" reads
// stdin; a positional "-" at or after the "--" separator stays literal.
func resolveContentValue(cmd *cobra.Command, value string, argIndex int, what string) (string, error) {
	if value != "-" || (argIndex >= 0 && afterDashSeparator(cmd, argIndex)) {
		return value, nil
	}
	return readStdinContent(cmd, what)
}

// afterDashSeparator reports whether the positional at index came after the
// "--" separator, making it literal by definition.
func afterDashSeparator(cmd *cobra.Command, index int) bool {
	lenAtDash := cmd.ArgsLenAtDash()
	return lenAtDash >= 0 && index >= lenAtDash
}

// InstallDashGuard wraps every runnable command in the tree with the tier-2
// dash guard. It wraps the Args validator: cobra runs ValidateArgs after flag
// parsing (so Changed and ArgsLenAtDash are available) but before the
// persistent pre-run chain, PreRunE, and required-flag validation — so a
// stray "-" is rejected before any lifecycle side effect (config hardening,
// the update check) and before a competing usage error can shadow it. It is
// not a PersistentPreRunE hook because cobra runs only the innermost one —
// the agent hook already shadows the root's, and any future subtree would
// silently lose the guard. A nil Args means ArbitraryArgs (always nil), so
// wrapping it is behavior-preserving.
func InstallDashGuard(root *cobra.Command) {
	// The root's nil Args is load-bearing: cobra's Find() rejects unknown
	// subcommands (legacyArgs) only while Args == nil, so wrapping it would
	// turn "basecamp unknowncmd" into a quickstart run. Leaving the root
	// unguarded loses nothing: its positionals are subcommand names, and a
	// bare "basecamp -" just runs quickstart, which posts no content for a
	// literal "-" to corrupt and whose TUI paths (the first-run wizard) are
	// stdin-gated by stdinarg.InteractiveStdio — piped stdin routes to the
	// non-interactive summary instead of a wizard that would eat the pipe.
	skipRoot := root.Args == nil && !root.HasParent() && root.HasSubCommands()
	if root.Runnable() && !skipRoot {
		existing := root.Args
		root.Args = func(cmd *cobra.Command, args []string) error {
			if err := guardDashArgs(cmd, args); err != nil {
				return err
			}
			if existing != nil {
				return existing(cmd, args)
			}
			return nil
		}
	}
	for _, sub := range root.Commands() {
		InstallDashGuard(sub)
	}
}

// guardDashArgs enforces the tier-2 policy for one invocation:
//
//  1. Collect every exact "-" — positionals before the "--" separator, plus
//     changed string-ish flags whose value (or element) is exactly "-".
//  2. More than one allowed "-" can never be satisfied by one stdin, so that
//     fails regardless of pipe state.
//  3. A disallowed "-" combined with piped stdin is ambiguous — the caller
//     meant the pipe — so it fails with a hint naming the offender.
//  4. Otherwise pass through: a TTY literal "-" stays legal everywhere.
func guardDashArgs(cmd *cobra.Command, args []string) error {
	allow := stdinarg.ParseAllow(cmd.Annotations[stdinarg.AnnotationAllowDash])

	allowed := 0
	var disallowedArgs, disallowedFlags []string

	for i, a := range args {
		if a != "-" || afterDashSeparator(cmd, i) {
			continue
		}
		if allow.Arg(i) {
			allowed++
		} else {
			disallowedArgs = append(disallowedArgs, positionalName(cmd, i))
		}
	}

	// Alias flags (--description/--desc) share one backing value, and pflag
	// hands each alias the same Value instance — dedupe on it, or a value set
	// through both spellings would count as two stdin inputs.
	seen := map[pflag.Value]bool{}
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if !f.Changed || seen[f.Value] {
			return
		}
		dashes := 0
		switch f.Value.Type() {
		case "string":
			if f.Value.String() == "-" {
				dashes = 1
			}
		case "stringArray", "stringSlice":
			if sv, ok := f.Value.(pflag.SliceValue); ok {
				for _, v := range sv.GetSlice() {
					if v == "-" {
						dashes++
					}
				}
			}
		default:
			return
		}
		seen[f.Value] = true
		if dashes == 0 {
			return
		}
		if allow.Flag(f.Name) {
			allowed += dashes
		} else {
			disallowedFlags = append(disallowedFlags, "--"+f.Name)
		}
	})

	if allowed > 1 {
		return output.ErrUsage(`only one input can read from stdin ("-") at a time`)
	}
	disallowed := append(append([]string{}, disallowedArgs...), disallowedFlags...)
	if len(disallowed) > 0 && stdinarg.IsPiped(cmd.InOrStdin()) {
		msg := fmt.Sprintf(`%s does not read stdin via "-" for %s`,
			cmd.CommandPath(), strings.Join(disallowed, ", "))
		// -- only escapes positionals; a flag value has no in-line escape, so
		// the honest remedy there is an unpiped stdin. Naming a concrete
		// redirect would be wrong on Windows and on headless runners with no
		// controlling terminal, so the hint stays at the shape of the fix.
		var hints []string
		if len(disallowedArgs) > 0 {
			hints = append(hints, `For a literal "-" argument, pass it after the -- separator`)
		}
		if len(disallowedFlags) > 0 {
			hints = append(hints, `For a literal "-" flag value, run the command without piped stdin`)
		}
		if accepts := describeAllowed(cmd, allow); accepts != "" {
			hints = append(hints, "this command reads stdin when \"-\" is given as "+accepts)
		}
		return output.ErrUsageHint(msg, strings.Join(hints, "; "))
	}
	return nil
}

// positionalName names a positional for guard errors, preferring the
// placeholder from the Use string ("<name>") over a bare ordinal.
func positionalName(cmd *cobra.Command, index int) string {
	if placeholders := usePlaceholders(cmd); index < len(placeholders) {
		return placeholders[index]
	}
	return fmt.Sprintf("argument %d", index+1)
}

// describeAllowed renders the allow set for hints: placeholder names for
// positionals, --name for flags.
func describeAllowed(cmd *cobra.Command, allow stdinarg.Allow) string {
	var parts []string
	placeholders := usePlaceholders(cmd)
	for i, p := range placeholders {
		if allow.Arg(i) {
			parts = append(parts, p)
		}
	}
	for _, token := range strings.Fields(cmd.Annotations[stdinarg.AnnotationAllowDash]) {
		// --out is exempted for "-" meaning stdout, not stdin — listing it
		// under "reads stdin" would teach the wrong thing.
		if name, ok := strings.CutPrefix(token, "flag:"); ok && name != "out" {
			parts = append(parts, "--"+name)
		}
	}
	return strings.Join(parts, ", ")
}

// usePlaceholders extracts the positional placeholders ("<id|url>",
// "[content]") from the command's Use string, in order.
func usePlaceholders(cmd *cobra.Command) []string {
	fields := strings.Fields(cmd.Use)
	if len(fields) == 0 {
		return nil
	}
	var placeholders []string
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "<") || strings.HasPrefix(f, "[") {
			placeholders = append(placeholders, f)
		}
	}
	return placeholders
}
