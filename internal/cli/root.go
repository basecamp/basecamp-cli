package cli

import (
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/commands"
	"github.com/basecamp/bcq/internal/completion"
	"github.com/basecamp/bcq/internal/config"
	"github.com/basecamp/bcq/internal/hostutil"
	"github.com/basecamp/bcq/internal/output"
	"github.com/basecamp/bcq/internal/tui"
	"github.com/basecamp/bcq/internal/version"
)

// NewRootCmd creates the root cobra command.
func NewRootCmd() *cobra.Command {
	var flags appctx.GlobalFlags

	cmd := &cobra.Command{
		Use:           "bcq",
		Short:         "Command-line interface for Basecamp",
		Long:          "bcq is a CLI tool for interacting with Basecamp projects, todos, messages, and more.",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          commands.RunQuickStartDefault, // Run quick-start when no args
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Skip setup for help and version commands
			if cmd.Name() == "help" || cmd.Name() == "version" {
				return nil
			}

			// Load configuration first (without host override to access hosts map)
			cfg, err := config.Load(config.FlagOverrides{
				Account:  flags.Account,
				Project:  flags.Project,
				Todolist: flags.Todolist,
				CacheDir: flags.CacheDir,
			})
			if err != nil {
				return err
			}

			// Resolve host: check if --host matches a configured host name first,
			// then fall back to URL normalization
			baseURL := resolveHostFlag(flags.Host, cfg)
			if baseURL != "" {
				cfg.BaseURL = baseURL
				cfg.Sources["base_url"] = string(config.SourceFlag)
			} else if flags.Host == "" {
				// No explicit host - try interactive resolution if multiple hosts configured
				resolvedURL, err := resolveHost(cfg, flags)
				if err != nil {
					return err
				}
				if resolvedURL != "" {
					cfg.BaseURL = resolvedURL
					cfg.Sources["base_url"] = string(config.SourcePrompt)
				}
			}

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
	cmd.PersistentFlags().StringVar(&flags.Host, "host", "", "Basecamp host (e.g., localhost:3000, staging.example.com)")
	cmd.PersistentFlags().StringVar(&flags.Host, "base-url", "", "Basecamp API base URL (deprecated: use --host)")
	_ = cmd.PersistentFlags().MarkHidden("base-url")

	// Behavior flags
	cmd.PersistentFlags().CountVarP(&flags.Verbose, "verbose", "v", "Verbose output (-v for ops, -vv for requests)")
	cmd.PersistentFlags().BoolVar(&flags.Stats, "stats", false, "Show session statistics")
	cmd.PersistentFlags().StringVar(&flags.CacheDir, "cache-dir", "", "Cache directory")

	// Register tab completion for flags.
	// DefaultCacheDirFunc checks --cache-dir flag, then app context, then env vars.
	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("project", completer.ProjectNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("account", completer.AccountCompletion())
	_ = cmd.RegisterFlagCompletionFunc("host", completer.HostCompletion())

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
	cmd.AddCommand(commands.NewTodosetsCmd())
	cmd.AddCommand(commands.NewToolsCmd())
	cmd.AddCommand(commands.NewConfigCmd())
	cmd.AddCommand(commands.NewTodolistgroupsCmd())
	cmd.AddCommand(commands.NewMCPCmd())
	cmd.AddCommand(commands.NewCommandsCmd())
	cmd.AddCommand(commands.NewTimelineCmd())
	cmd.AddCommand(commands.NewReportsCmd())
	cmd.AddCommand(commands.NewCompletionCmd())

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

// resolveHostFlag resolves a --host flag value to a base URL.
// First checks if the value matches a configured host name (e.g., "production"),
// then falls back to URL normalization for hostnames/URLs.
func resolveHostFlag(host string, cfg *config.Config) string {
	if host == "" {
		return ""
	}

	// Check if host matches a configured host name (case-insensitive)
	if cfg != nil && len(cfg.Hosts) > 0 {
		hostLower := strings.ToLower(host)
		for name, hostConfig := range cfg.Hosts {
			if strings.ToLower(name) == hostLower {
				return hostConfig.BaseURL
			}
		}
	}

	// Not a configured host name - treat as hostname/URL
	return hostutil.Normalize(host)
}

// transformCobraError transforms Cobra's default error messages to match the
// Bash CLI format for consistency with existing tests and user expectations.
func transformCobraError(err error) error {
	msg := err.Error()

	// Transform "flag needs an argument: --FLAG" → "--FLAG requires a value"
	// This matches the Bash CLI's error format
	if strings.HasPrefix(msg, "flag needs an argument: ") {
		flag := strings.TrimPrefix(msg, "flag needs an argument: ")
		// Special cases for flags with custom error messages
		if flag == "--on" {
			return output.ErrUsage("--on requires a recording ID")
		}
		return output.ErrUsage(flag + " requires a value")
	}

	// Transform "unknown flag: --FLAG" → "Unknown option: --FLAG"
	if strings.HasPrefix(msg, "unknown flag: ") {
		flag := strings.TrimPrefix(msg, "unknown flag: ")
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

// resolveHost determines the base URL to use based on configuration.
// Resolution order:
// 1. BCQ_HOST environment variable
// 2. Config default_host (lookup in hosts map)
// 3. If single host configured → use it
// 4. If multiple hosts configured → show interactive picker (if TTY)
// 5. Fall back to config base_url
func resolveHost(cfg *config.Config, flags appctx.GlobalFlags) (string, error) {
	// 1. Check BCQ_HOST environment variable
	if host := os.Getenv("BCQ_HOST"); host != "" {
		return hostutil.Normalize(host), nil
	}

	// No hosts configured - use existing base_url
	if len(cfg.Hosts) == 0 {
		return "", nil
	}

	// 2. Check default_host in config
	if cfg.DefaultHost != "" {
		if hostConfig, ok := cfg.Hosts[cfg.DefaultHost]; ok {
			return hostConfig.BaseURL, nil
		}
	}

	// 3. If only one host configured, use it
	if len(cfg.Hosts) == 1 {
		for _, hostConfig := range cfg.Hosts {
			return hostConfig.BaseURL, nil
		}
	}

	// 4. Multiple hosts - try interactive picker if TTY
	if !isInteractiveTTY(flags) {
		return "", output.ErrUsage("Multiple hosts configured. Use --host flag or set default_host in config.")
	}

	return promptForHost(cfg)
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

// promptForHost shows an interactive picker for host selection.
func promptForHost(cfg *config.Config) (string, error) {
	// Build picker items from configured hosts
	items := make([]tui.PickerItem, 0, len(cfg.Hosts))

	// Sort host names for consistent ordering
	hostNames := make([]string, 0, len(cfg.Hosts))
	for name := range cfg.Hosts {
		hostNames = append(hostNames, name)
	}
	sort.Strings(hostNames)

	for _, name := range hostNames {
		hostConfig := cfg.Hosts[name]
		items = append(items, tui.PickerItem{
			ID:          hostConfig.BaseURL,
			Title:       name,
			Description: hostConfig.BaseURL,
		})
	}

	selected, err := tui.PickHost(items)
	if err != nil {
		return "", err
	}
	if selected == nil {
		return "", output.ErrUsage("host selection canceled")
	}

	return selected.ID, nil
}
