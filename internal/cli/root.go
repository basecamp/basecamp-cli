package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/itchyny/gojq"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/commands"
	"github.com/basecamp/basecamp-cli/internal/completion"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/harness"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// NewRootCmd creates the root cobra command.
func NewRootCmd() *cobra.Command {
	var flags appctx.GlobalFlags

	cmd := &cobra.Command{
		Use:                        "basecamp",
		Short:                      "Command-line interface for Basecamp",
		Long:                       "basecamp is a CLI tool for interacting with Basecamp projects, todos, messages, and more.",
		Version:                    version.Version,
		SilenceUsage:               true,
		SilenceErrors:              true,
		SuggestionsMinimumDistance: 2,                             // Enable typo suggestions
		RunE:                       commands.RunQuickStartDefault, // Run quick-start when no args
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Skip setup for help and version commands
			if cmd.Name() == "help" || cmd.Name() == "version" {
				return nil
			}

			// Bare root (no subcommand, no args) will show help or
			// quickstart depending on mode. Tolerate config/profile
			// errors so bad config doesn't block help, while still
			// loading the app so wizard detection and IsMachineOutput
			// work correctly.
			bareRoot := cmd.Parent() == nil && len(args) == 0

			// initBareRootApp creates a minimal app from the given config
			// (or a default config if nil) so wizard detection, machine-
			// output checks, and preference resolution still work when
			// the bare-root path tolerates an error.
			initBareRootApp := func(cfg *config.Config) {
				if cfg == nil {
					cfg = config.Default()
				}
				resolvePreferences(cmd, cfg, &flags)
				app := appctx.NewApp(cfg)
				app.Flags = flags
				app.ApplyFlags()
				cmd.SetContext(appctx.WithApp(cmd.Context(), app))
			}

			// Load configuration (without profile-specific overrides first)
			cfg, err := config.Load(config.FlagOverrides{
				Account:  flags.Account,
				Project:  flags.Project,
				Todolist: flags.Todolist,
				CacheDir: flags.CacheDir,
			})
			if err != nil {
				if bareRoot {
					initBareRootApp(nil)
					return nil
				}
				return err
			}

			// Resolve profile
			profileName, err := resolveProfile(cfg, flags)
			if err != nil {
				if bareRoot {
					initBareRootApp(cfg)
					return nil
				}
				return err
			}
			if profileName != "" {
				if err := cfg.ApplyProfile(profileName); err != nil {
					return err
				}
				// Re-apply env and flag overrides (they take precedence over profile values)
				config.LoadFromEnv(cfg)
				config.ApplyOverrides(cfg, config.FlagOverrides{
					Account:  flags.Account,
					Project:  flags.Project,
					Todolist: flags.Todolist,
					CacheDir: flags.CacheDir,
				})
				// Profile-scoped cache (only if cache dir was not explicitly set via flag or env)
				if flags.CacheDir == "" && os.Getenv("BASECAMP_CACHE_DIR") == "" {
					cfg.CacheDir = filepath.Join(cfg.CacheDir, "profiles", profileName)
				}
			}

			// Enforce HTTPS for non-localhost base_url.
			// Skip for "config" subcommands so users can fix a bad base_url
			// without being locked out by the validation they need to repair.
			if !isConfigCmd(cmd) {
				if err := hostutil.RequireSecureURL(cfg.BaseURL); err != nil {
					if bareRoot {
						initBareRootApp(cfg)
						return nil
					}
					source := cfg.Sources["base_url"]
					if source == "" {
						source = "unknown"
					}
					return fmt.Errorf("base_url (%s): %w\nFix with: basecamp config unset base_url", source, err)
				}
			}

			// Resolve behavior preferences: explicit flag > config > version.IsDev()
			resolvePreferences(cmd, cfg, &flags)

			// Create app and store in context
			app := appctx.NewApp(cfg)
			app.Flags = flags
			app.ApplyFlags()

			// Early jq validation: parse + compile before RunE so invalid
			// expressions are rejected with no side effects.
			if flags.JQFilter != "" {
				q, err := gojq.Parse(flags.JQFilter)
				if err != nil {
					return output.ErrJQValidation(err)
				}
				if _, err := gojq.Compile(q, gojq.WithEnvironLoader(os.Environ)); err != nil {
					return output.ErrJQValidation(err)
				}
				if flags.IDsOnly {
					return output.ErrJQConflict("--ids-only")
				}
				if flags.Count {
					return output.ErrJQConflict("--count")
				}
			}

			cmd.SetContext(appctx.WithApp(cmd.Context(), app))
			return nil
		},
	}

	cmd.PersistentPostRunE = func(cmd *cobra.Command, args []string) error {
		app := appctx.FromContext(cmd.Context())
		if app != nil {
			app.Close()
		}
		if commands.RefreshSkillsIfVersionChanged() {
			if app == nil || !app.IsMachineOutput() {
				fmt.Fprintf(os.Stderr, "Agent skill updated to match CLI %s\n", version.Version)

				// One-time hint: if plugin/CLI version mismatch after upgrade, nudge the user
				if pv := harness.InstalledPluginVersion(); pv != "" && pv != version.Version && !version.IsDev() {
					fmt.Fprintf(os.Stderr, "Basecamp plugin version mismatch (plugin %s, CLI %s) — %s\n", pv, version.Version, harness.AutoUpdateHint)
				}
			}
		}
		return nil
	}

	// Allow flags anywhere in the command line
	cmd.Flags().SetInterspersed(true)
	cmd.PersistentFlags().SetInterspersed(true)

	// Output format flags
	cmd.PersistentFlags().BoolVarP(&flags.JSON, "json", "j", false, "Output as JSON")
	cmd.PersistentFlags().BoolVarP(&flags.Quiet, "quiet", "q", false, "Output data only, no envelope")
	cmd.PersistentFlags().BoolVarP(&flags.MD, "md", "m", false, "Output as Markdown (portable)")
	cmd.PersistentFlags().BoolVar(&flags.MD, "markdown", false, "Output as Markdown (portable)")
	cmd.PersistentFlags().BoolVar(&flags.Styled, "styled", false, "Force styled output (ANSI colors)")
	cmd.PersistentFlags().BoolVar(&flags.IDsOnly, "ids-only", false, "Output only IDs")
	cmd.PersistentFlags().BoolVar(&flags.Count, "count", false, "Output only count")
	cmd.PersistentFlags().BoolVar(&flags.Agent, "agent", false, "Agent mode (JSON + quiet)")
	cmd.PersistentFlags().StringVar(&flags.JQFilter, "jq", "", "Apply jq filter to JSON output (built-in, no external jq required; implies --json)")

	// Context flags
	cmd.PersistentFlags().StringVarP(&flags.Project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&flags.Project, "in", "", "Project ID or name (alias for --project)")
	cmd.PersistentFlags().StringVarP(&flags.Account, "account", "a", "", "Account ID")
	cmd.PersistentFlags().StringVar(&flags.Todolist, "todolist", "", "Todolist ID or name")
	cmd.PersistentFlags().StringVarP(&flags.Profile, "profile", "P", "", "Named profile")

	// Behavior flags
	cmd.PersistentFlags().CountVarP(&flags.Verbose, "verbose", "v", "Verbose output (-v for ops, -vv for requests)")
	cmd.PersistentFlags().BoolVar(&flags.Stats, "stats", false, "Show session statistics (persisted via: basecamp config set stats true)")
	cmd.PersistentFlags().BoolVar(&flags.NoStats, "no-stats", false, "Disable session statistics")
	cmd.MarkFlagsMutuallyExclusive("stats", "no-stats")
	cmd.PersistentFlags().BoolVar(&flags.Hints, "hints", false, "Show follow-up hints (persisted via: basecamp config set hints true)")
	cmd.PersistentFlags().BoolVar(&flags.NoHints, "no-hints", false, "Disable follow-up hints")
	cmd.MarkFlagsMutuallyExclusive("hints", "no-hints")
	cmd.PersistentFlags().StringVar(&flags.CacheDir, "cache-dir", "", "Cache directory")

	// Register tab completion for flags.
	// DefaultCacheDirFunc checks --cache-dir flag, then app context, then env vars.
	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("project", completer.ProjectNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("in", completer.ProjectNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("account", completer.AccountCompletion())
	_ = cmd.RegisterFlagCompletionFunc("profile", completer.ProfileCompletion())

	// Styled help: curated categories for root, agent JSON for --agent,
	// renderCommandHelp for all subcommands.
	cmd.SetHelpFunc(rootHelpFunc())

	// Compact usage for the root command only — prevents cobra from dumping
	// all 55 commands on error. Subcommands inherit cobra's default.
	defaultUsageFunc := cmd.UsageFunc()
	cmd.SetUsageFunc(func(c *cobra.Command) error {
		if c == cmd {
			fmt.Fprintf(c.OutOrStderr(), "Usage: basecamp <command> [flags]\n\nRun 'basecamp --help' for a list of commands.\n")
			return nil
		}
		return defaultUsageFunc(c)
	})

	return cmd
}

// Execute runs the root command.
func Execute() {
	cmd := NewRootCmd()

	// Add subcommands
	cmd.AddCommand(commands.NewAccountsCmd())
	cmd.AddCommand(commands.NewAuthCmd())
	cmd.AddCommand(commands.NewProjectsCmd())
	cmd.AddCommand(commands.NewTodosCmd())
	cmd.AddCommand(commands.NewTodoCmd())
	cmd.AddCommand(commands.NewDoneCmd())
	cmd.AddCommand(commands.NewReopenCmd())
	cmd.AddCommand(commands.NewMeCmd())
	cmd.AddCommand(commands.NewPeopleCmd())
	cmd.AddCommand(commands.NewQuickStartCmd())
	cmd.AddCommand(commands.NewAPICmd())
	cmd.AddCommand(commands.NewShowCmd())
	cmd.AddCommand(commands.NewTodolistsCmd())
	cmd.AddCommand(commands.NewCommentsCmd())
	cmd.AddCommand(commands.NewCommentCmd())
	cmd.AddCommand(commands.NewAssignCmd())
	cmd.AddCommand(commands.NewUnassignCmd())
	cmd.AddCommand(commands.NewMessagesCmd())
	cmd.AddCommand(commands.NewMessageCmd())
	cmd.AddCommand(commands.NewCardsCmd())
	cmd.AddCommand(commands.NewCardCmd())
	cmd.AddCommand(commands.NewURLCmd())
	cmd.AddCommand(commands.NewSearchCmd())
	cmd.AddCommand(commands.NewRecordingsCmd())
	cmd.AddCommand(commands.NewChatCmd())
	cmd.AddCommand(commands.NewScheduleCmd())
	cmd.AddCommand(commands.NewFilesCmd())
	cmd.AddCommand(commands.NewVaultsCmd())
	cmd.AddCommand(commands.NewDocsCmd())
	cmd.AddCommand(commands.NewUploadsCmd())
	cmd.AddCommand(commands.NewCheckinsCmd())
	cmd.AddCommand(commands.NewWebhooksCmd())
	cmd.AddCommand(commands.NewEventsCmd())
	cmd.AddCommand(commands.NewSubscriptionsCmd())
	cmd.AddCommand(commands.NewForwardsCmd())
	cmd.AddCommand(commands.NewMessageboardsCmd())
	cmd.AddCommand(commands.NewMessagetypesCmd())
	cmd.AddCommand(commands.NewTemplatesCmd())
	cmd.AddCommand(commands.NewLineupCmd())
	cmd.AddCommand(commands.NewTimesheetCmd())
	cmd.AddCommand(commands.NewBoostsCmd())
	cmd.AddCommand(commands.NewBoostShortcutCmd())
	cmd.AddCommand(commands.NewTodosetsCmd())
	cmd.AddCommand(commands.NewHillchartsCmd())
	cmd.AddCommand(commands.NewToolsCmd())
	cmd.AddCommand(commands.NewConfigCmd())
	cmd.AddCommand(commands.NewTodolistgroupsCmd())
	cmd.AddCommand(commands.NewMCPCmd())
	cmd.AddCommand(commands.NewCommandsCmd())
	cmd.AddCommand(commands.NewVersionCmd())
	cmd.AddCommand(commands.NewTimelineCmd())
	cmd.AddCommand(commands.NewReportsCmd())
	cmd.AddCommand(commands.NewCompletionCmd())
	cmd.AddCommand(commands.NewSetupCmd())
	cmd.AddCommand(commands.NewLoginCmd())
	cmd.AddCommand(commands.NewLogoutCmd())
	cmd.AddCommand(commands.NewDoctorCmd())
	cmd.AddCommand(commands.NewUpgradeCmd())
	cmd.AddCommand(commands.NewMigrateCmd())
	cmd.AddCommand(commands.NewProfileCmd())
	cmd.AddCommand(commands.NewSkillCmd())
	cmd.AddCommand(commands.NewAttachmentsCmd())
	cmd.AddCommand(commands.NewAttachCmd())
	cmd.AddCommand(commands.NewUploadCmd())
	cmd.AddCommand(commands.NewTUICmd())
	cmd.AddCommand(commands.NewBonfireCmd())

	// Use ExecuteC to get the executed command (for correct context access)
	executedCmd, err := cmd.ExecuteC()

	// Bare group command with explicit flags (e.g. "cards --in X"): the help
	// function suppressed output. Convert to a usage error.
	if err == nil && executedCmd != cmd && isBareGroupWithFlags(executedCmd) {
		err = output.ErrUsageHint(
			"subcommand required",
			"Usage: "+executedCmd.CommandPath()+" <command> [flags]",
		)
	}

	if err != nil {
		// When a command receives zero args but requires some, show help instead of an error —
		// but only for interactive human users. Machine consumers (--agent, --json, piped stdout)
		// need the structured error to flow through transformCobraError.
		if isMissingArgsError(err) || isBareRequiredFlagError(err, executedCmd) {
			if !isMachineConsumer(cmd) {
				_ = executedCmd.Help()
				os.Exit(0)
			}
		}

		// Transform Cobra errors to match Bash CLI error format
		err = transformCobraError(err)

		// Convert error to structured output
		apiErr := output.AsError(err)

		// jq-related errors (validation failures, unsupported commands, conflicts)
		// must never be fed through the jq filter. Skip app.Err() entirely and
		// render with a plain writer.
		disableJQ := output.IsJQError(err)
		if !disableJQ {
			if app := appctx.FromContext(executedCmd.Context()); app != nil {
				if writeErr := app.Err(err); writeErr == nil {
					os.Exit(apiErr.ExitCode())
				}
				// app.Err() write failed (e.g. jq runtime error on the error
				// envelope, or broken pipe). Disable jq in the fallback writer
				// to avoid replaying the same failure.
				disableJQ = true
			}
		}

		// Fallback: output error directly (app not available, or jq bypass needed)
		pf := cmd.PersistentFlags()
		format := output.FormatAuto // Default to auto (TTY → styled, non-TTY → JSON)
		agent, _ := pf.GetBool("agent")
		quiet, _ := pf.GetBool("quiet")
		idsOnly, _ := pf.GetBool("ids-only")
		count, _ := pf.GetBool("count")
		styled, _ := pf.GetBool("styled")
		md, _ := pf.GetBool("md")
		jsonFlag, _ := pf.GetBool("json")
		jqFilter, _ := pf.GetString("jq")
		hadJQ := jqFilter != ""

		// Strip jq filter when disabled (jq-about-jq errors OR app.Err() write failure).
		// hadJQ preserves the "--jq implies --json" format decision even after zeroing.
		if disableJQ {
			jqFilter = ""
		}

		if agent || quiet {
			format = output.FormatQuiet
		} else if idsOnly {
			format = output.FormatIDs
		} else if count {
			format = output.FormatCount
		} else if styled {
			format = output.FormatStyled
		} else if md {
			format = output.FormatMarkdown
		} else if jsonFlag || hadJQ {
			format = output.FormatJSON
		}

		writer := output.New(output.Options{
			Format:   format,
			Writer:   os.Stdout,
			JQFilter: jqFilter,
		})
		_ = writer.Err(err)

		os.Exit(apiErr.ExitCode())
	}
}

// resolveProfile determines which profile to use.
// Resolution order:
// 1. --profile / -P flag
// 2. BASECAMP_PROFILE env var
// 3. default_profile in config
// 4. Single profile → auto-use
// 5. Multiple profiles → interactive picker (if TTY)
// 6. No profiles → empty string (use top-level config values)
func resolveProfile(cfg *config.Config, flags appctx.GlobalFlags) (string, error) {
	// 1. --profile flag
	if flags.Profile != "" {
		if len(cfg.Profiles) == 0 {
			return "", fmt.Errorf("profile %q specified via --profile but no profiles are configured; create one with: basecamp profile create", flags.Profile)
		}
		if _, ok := cfg.Profiles[flags.Profile]; !ok {
			return "", fmt.Errorf("unknown profile %q (available: %s)", flags.Profile, profileNames(cfg))
		}
		return flags.Profile, nil
	}

	// 2. BASECAMP_PROFILE env var
	if profile := os.Getenv("BASECAMP_PROFILE"); profile != "" {
		if len(cfg.Profiles) == 0 {
			return "", fmt.Errorf("profile %q specified via BASECAMP_PROFILE but no profiles are configured; create one with: basecamp profile create", profile)
		}
		if _, ok := cfg.Profiles[profile]; !ok {
			return "", fmt.Errorf("unknown profile %q from BASECAMP_PROFILE (available: %s)", profile, profileNames(cfg))
		}
		return profile, nil
	}

	// No profiles configured - use top-level config
	if len(cfg.Profiles) == 0 {
		return "", nil
	}

	// 3. default_profile in config
	if cfg.DefaultProfile != "" {
		if _, ok := cfg.Profiles[cfg.DefaultProfile]; ok {
			return cfg.DefaultProfile, nil
		}
		fmt.Fprintf(os.Stderr, "Warning: default_profile %q not found in configured profiles\n", cfg.DefaultProfile)
	}

	// 4. Single profile → auto-use
	if len(cfg.Profiles) == 1 {
		for name := range cfg.Profiles {
			return name, nil
		}
	}

	// 5. Multiple profiles → interactive picker (if TTY)
	if isInteractiveTTY(flags) {
		if name, err := promptForProfile(cfg); err == nil {
			return name, nil
		}
	}

	// 6. Multiple profiles but non-interactive — require explicit selection
	return "", fmt.Errorf("multiple profiles configured but none selected; use --profile or set default_profile (available: %s)", profileNames(cfg))
}

// profileNames returns a sorted comma-separated list of profile names.
func profileNames(cfg *config.Config) string {
	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// isInteractiveTTY returns true if stdout is a terminal and no machine-output mode is set.
func isInteractiveTTY(flags appctx.GlobalFlags) bool {
	// Not interactive if any machine-output mode is set
	if flags.Agent || flags.JSON || flags.Quiet || flags.IDsOnly || flags.Count {
		return false
	}

	// Check if stdout is a terminal
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// promptForProfile shows an interactive picker for profile selection.
func promptForProfile(cfg *config.Config) (string, error) {
	// Build picker items from configured profiles
	items := make([]tui.PickerItem, 0, len(cfg.Profiles))

	// Sort profile names for consistent ordering
	profileNames := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		profileNames = append(profileNames, name)
	}
	sort.Strings(profileNames)

	for _, name := range profileNames {
		p := cfg.Profiles[name]
		items = append(items, tui.PickerItem{
			ID:          name,
			Title:       name,
			Description: p.BaseURL,
		})
	}

	selected, err := tui.PickHost(items) // Reuse the host picker UI
	if err != nil {
		return "", err
	}
	if selected == nil {
		return "", output.ErrUsage("profile selection canceled")
	}

	return selected.ID, nil
}

// isConfigCmd returns true if cmd is "config" or any of its subcommands.
// Used to skip HTTPS enforcement so users can repair a bad base_url.
func isConfigCmd(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() == "config" {
			return true
		}
	}
	return false
}

// isMissingArgsError returns true when cobra rejects a command because zero
// positional arguments were supplied but the command requires at least one.
func isMissingArgsError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "arg(s), received 0") ||
		(strings.Contains(msg, "requires at least") && strings.Contains(msg, "received 0"))
}

// isBareRequiredFlagError returns true when a required flag is missing AND the
// user supplied zero positional arguments — they invoked the command bare.
func isBareRequiredFlagError(err error, cmd *cobra.Command) bool {
	if !strings.HasPrefix(err.Error(), "required flag(s)") {
		return false
	}
	// ArgsLenAtDash returns -1 when no dash separator is used.
	// Flags().NArg() gives the number of non-flag args cobra parsed.
	return cmd.Flags().NArg() == 0
}

// isMachineConsumer returns true when the root command's flags indicate a
// non-interactive consumer: --agent, --json, --quiet, etc., or stdout piped to a non-TTY.
func isMachineConsumer(root *cobra.Command) bool {
	pf := root.PersistentFlags()
	for _, flag := range []string{"agent", "json", "quiet", "ids-only", "count"} {
		if v, _ := pf.GetBool(flag); v {
			return true
		}
	}
	if jq, _ := pf.GetString("jq"); jq != "" {
		return true
	}
	fi, err := os.Stdout.Stat()
	if err == nil && (fi.Mode()&os.ModeCharDevice) == 0 {
		return true
	}
	return false
}

// transformCobraError transforms Cobra's default error messages to match the
// Bash CLI format for consistency with existing tests and user expectations.
func transformCobraError(err error) error {
	msg := err.Error()

	// Transform "flag needs an argument: --FLAG" → "--FLAG requires a value"
	// This matches the Bash CLI's error format
	if after, ok := strings.CutPrefix(msg, "flag needs an argument: "); ok {
		flag := after
		// Special cases for flags with custom error messages
		if flag == "--on" {
			return output.ErrUsage("--on requires an ID")
		}
		return output.ErrUsage(flag + " requires a value")
	}

	// Transform "unknown flag: --FLAG" → "Unknown option: --FLAG"
	if after, ok := strings.CutPrefix(msg, "unknown flag: "); ok {
		flag := after
		return output.ErrUsage("Unknown option: " + flag)
	}

	// Transform "unknown shorthand flag: 'X' in -X" → "Unknown option: -X"
	if strings.HasPrefix(msg, "unknown shorthand flag: ") {
		re := regexp.MustCompile(`unknown shorthand flag: '.' in (-\w)`)
		if matches := re.FindStringSubmatch(msg); len(matches) > 1 {
			return output.ErrUsage("Unknown option: " + matches[1])
		}
	}

	// Transform "invalid argument" errors to usage errors
	if strings.Contains(msg, "invalid argument") {
		return output.ErrUsage(msg)
	}

	// Transform "requires at least N arg(s)" → "ID(s) required"
	if strings.Contains(msg, "requires at least") && strings.Contains(msg, "arg(s)") {
		return output.ErrUsage("Todo ID(s) required")
	}

	// Transform "accepts N arg(s), received 0" → "ID required"
	if strings.Contains(msg, "arg(s), received 0") {
		return output.ErrUsage("ID required")
	}

	// Transform "required flag(s) X not set" → more specific message
	if strings.HasPrefix(msg, "required flag(s) ") {
		re := regexp.MustCompile(`required flag\(s\) "(\w+)" not set`)
		if matches := re.FindStringSubmatch(msg); len(matches) > 1 {
			flag := matches[1]
			switch flag {
			case "content":
				return output.ErrUsage("Content required")
			case "subject":
				return output.ErrUsage("Message subject required")
			case "to":
				return output.ErrUsage("Position required")
			case "on":
				return output.ErrUsage("ID required")
			default:
				return output.ErrUsage(flag + " required")
			}
		}
	}

	return err
}

// resolvePreferences resolves behavior flag values using the precedence chain:
// explicit flag > config > version.IsDev()
//
// Flags register with default=false so we can detect explicit usage via Changed().
// When no flag is passed, we check config, then fall back to version.IsDev().
func resolvePreferences(cmd *cobra.Command, cfg *config.Config, flags *appctx.GlobalFlags) {
	pf := cmd.PersistentFlags()

	if !pf.Changed("stats") && (!pf.Changed("no-stats") || !flags.NoStats) {
		if cfg.Stats != nil {
			flags.Stats = *cfg.Stats
		} else {
			flags.Stats = version.IsDev()
		}
	}

	if !pf.Changed("hints") && (!pf.Changed("no-hints") || !flags.NoHints) {
		if cfg.Hints != nil {
			flags.Hints = *cfg.Hints
		} else {
			flags.Hints = version.IsDev()
		}
	}

	if !pf.Changed("verbose") && cfg.Verbose != nil {
		flags.Verbose = *cfg.Verbose
	}
}

// agentHelpInfo is the structured help output for --help --agent.
type agentHelpInfo struct {
	Command        string            `json:"command"`
	Path           string            `json:"path"`
	Short          string            `json:"short"`
	Long           string            `json:"long,omitempty"`
	Usage          string            `json:"usage"`
	Notes          []string          `json:"notes,omitempty"`
	Args           []ArgInfo         `json:"args,omitempty"`
	Subcommands    []agentSubcommand `json:"subcommands,omitempty"`
	Flags          []agentFlag       `json:"flags,omitempty"`
	InheritedFlags []agentFlag       `json:"inherited_flags,omitempty"`
}

type agentSubcommand struct {
	Name  string `json:"name"`
	Short string `json:"short"`
	Path  string `json:"path"`
}

type agentFlag struct {
	Name      string `json:"name"`
	Shorthand string `json:"shorthand,omitempty"`
	Type      string `json:"type"`
	Default   string `json:"default"`
	Usage     string `json:"usage"`
}

// emitAgentHelp writes structured JSON help for the given command to stdout.
func emitAgentHelp(cmd *cobra.Command) {
	info := agentHelpInfo{
		Command: cmd.Name(),
		Path:    cmd.CommandPath(),
		Short:   cmd.Short,
		Long:    cmd.Long,
		Usage:   cmd.UseLine(),
	}

	// Structured positional args from Use: string
	info.Args = ParseArgs(cmd)

	// Extract notes from Annotations["agent_notes"]
	if notes, ok := cmd.Annotations["agent_notes"]; ok && notes != "" {
		for _, line := range strings.Split(notes, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				info.Notes = append(info.Notes, line)
			}
		}
	}

	// Subcommands (include aliases so the CLI surface snapshot tracks them)
	for _, sub := range cmd.Commands() {
		if sub.IsAvailableCommand() || sub.Name() == "help" {
			info.Subcommands = append(info.Subcommands, agentSubcommand{
				Name:  sub.Name(),
				Short: sub.Short,
				Path:  sub.CommandPath(),
			})
			for _, alias := range sub.Aliases {
				info.Subcommands = append(info.Subcommands, agentSubcommand{
					Name:  alias,
					Short: sub.Short,
					Path:  strings.TrimSuffix(sub.CommandPath(), sub.Name()) + alias,
				})
			}
		}
	}

	// Local flags
	cmd.NonInheritedFlags().VisitAll(func(f *pflag.Flag) {
		info.Flags = append(info.Flags, agentFlag{
			Name:      f.Name,
			Shorthand: f.Shorthand,
			Type:      f.Value.Type(),
			Default:   f.DefValue,
			Usage:     f.Usage,
		})
	})

	// Parent-scoped flags (e.g. --room on chat subcommands) — promoted into
	// flags to match text help's parentScopedFlags promotion.
	parentScopedFlags(cmd).VisitAll(func(f *pflag.Flag) {
		info.Flags = append(info.Flags, agentFlag{
			Name:      f.Name,
			Shorthand: f.Shorthand,
			Type:      f.Value.Type(),
			Default:   f.DefValue,
			Usage:     f.Usage,
		})
	})

	// Inherited flags — shared logic with filterInheritedFlags (text help)
	curatedInheritedFlags(cmd).VisitAll(func(f *pflag.Flag) {
		info.InheritedFlags = append(info.InheritedFlags, agentFlag{
			Name:      f.Name,
			Shorthand: f.Shorthand,
			Type:      f.Value.Type(),
			Default:   f.DefValue,
			Usage:     f.Usage,
		})
	})

	_ = json.NewEncoder(cmd.OutOrStdout()).Encode(info)
}
