package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/commands"
	"github.com/basecamp/basecamp-cli/internal/completion"
	"github.com/basecamp/basecamp-cli/internal/config"
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

			// Load configuration (without profile-specific overrides first)
			cfg, err := config.Load(config.FlagOverrides{
				Account:  flags.Account,
				Project:  flags.Project,
				Todolist: flags.Todolist,
				CacheDir: flags.CacheDir,
			})
			if err != nil {
				return err
			}

			// Resolve profile
			profileName, err := resolveProfile(cfg, flags)
			if err != nil {
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

			// Resolve behavior preferences: explicit flag > config > version.IsDev()
			resolvePreferences(cmd, cfg, &flags)

			// Create app and store in context
			app := appctx.NewApp(cfg)
			app.Flags = flags
			app.ApplyFlags()

			cmd.SetContext(appctx.WithApp(cmd.Context(), app))
			return nil
		},
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

	// Context flags
	cmd.PersistentFlags().StringVarP(&flags.Project, "project", "p", "", "Project ID or name")
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
	_ = cmd.RegisterFlagCompletionFunc("account", completer.AccountCompletion())
	_ = cmd.RegisterFlagCompletionFunc("profile", completer.ProfileCompletion())

	return cmd
}

// Execute runs the root command.
func Execute() {
	cmd := NewRootCmd()

	// Add subcommands
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
	cmd.AddCommand(commands.NewCampfireCmd())
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
	cmd.AddCommand(commands.NewClockCmd())
	cmd.AddCommand(commands.NewBoostsCmd())
	cmd.AddCommand(commands.NewBoostShortcutCmd())
	cmd.AddCommand(commands.NewTodosetsCmd())
	cmd.AddCommand(commands.NewToolsCmd())
	cmd.AddCommand(commands.NewConfigCmd())
	cmd.AddCommand(commands.NewTodolistgroupsCmd())
	cmd.AddCommand(commands.NewMCPCmd())
	cmd.AddCommand(commands.NewCommandsCmd())
	cmd.AddCommand(commands.NewTimelineCmd())
	cmd.AddCommand(commands.NewReportsCmd())
	cmd.AddCommand(commands.NewCompletionCmd())
	cmd.AddCommand(commands.NewSetupCmd())
	cmd.AddCommand(commands.NewDoctorCmd())
	cmd.AddCommand(commands.NewUpgradeCmd())
	cmd.AddCommand(commands.NewMigrateCmd())
	cmd.AddCommand(commands.NewProfileCmd())
	cmd.AddCommand(commands.NewTUICmd())

	// Use ExecuteC to get the executed command (for correct context access)
	executedCmd, err := cmd.ExecuteC()
	if err != nil {
		// Transform Cobra errors to match Bash CLI error format
		err = transformCobraError(err)

		// Convert error to structured output
		apiErr := output.AsError(err)

		// Try to use app.Err() if app is available (for --stats support)
		if app := appctx.FromContext(executedCmd.Context()); app != nil {
			_ = app.Err(err)
			os.Exit(apiErr.ExitCode())
		}

		// Fallback: output error directly (app not available, e.g., during setup)
		pf := cmd.PersistentFlags()
		format := output.FormatAuto // Default to auto (TTY → styled, non-TTY → JSON)
		agent, _ := pf.GetBool("agent")
		quiet, _ := pf.GetBool("quiet")
		idsOnly, _ := pf.GetBool("ids-only")
		count, _ := pf.GetBool("count")
		styled, _ := pf.GetBool("styled")
		md, _ := pf.GetBool("md")
		jsonFlag, _ := pf.GetBool("json")

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
		} else if jsonFlag {
			format = output.FormatJSON
		}

		writer := output.New(output.Options{
			Format: format,
			Writer: os.Stdout,
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
			return output.ErrUsage("--on requires a recording ID")
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
				return output.ErrUsage("Recording ID required")
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
