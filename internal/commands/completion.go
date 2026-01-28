package commands

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/completion"
	"github.com/basecamp/bcq/internal/output"
)

// NewCompletionCmd creates the completion command group.
func NewCompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion [shell]",
		Short: "Generate shell completion scripts",
		Long: `Generate shell completion scripts for bcq.

To load completions:

Bash:
  $ source <(bcq completion bash)

  # To load completions for each session, execute once:
  # Linux:
  $ bcq completion bash > /etc/bash_completion.d/bcq
  # macOS:
  $ bcq completion bash > $(brew --prefix)/etc/bash_completion.d/bcq

Zsh:
  # If shell completion is not already enabled in your environment,
  # you will need to enable it. You can execute the following once:
  $ echo "autoload -U compinit; compinit" >> ~/.zshrc

  # To load completions for each session, execute once:
  $ bcq completion zsh > "${fpath[1]}/_bcq"

  # You will need to start a new shell for this setup to take effect.

Fish:
  $ bcq completion fish | source

  # To load completions for each session, execute once:
  $ bcq completion fish > ~/.config/fish/completions/bcq.fish

PowerShell:
  PS> bcq completion powershell | Out-String | Invoke-Expression

  # To load completions for every new session, run:
  PS> bcq completion powershell > bcq.ps1
  # and source this file from your PowerShell profile.
`,
		DisableFlagsInUseLine: true,
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCompletion(cmd.Root(), args[0])
		},
	}

	// Add shell-specific subcommands for convenience
	cmd.AddCommand(newCompletionBashCmd())
	cmd.AddCommand(newCompletionZshCmd())
	cmd.AddCommand(newCompletionFishCmd())
	cmd.AddCommand(newCompletionPowershellCmd())

	// Add cache management subcommands
	cmd.AddCommand(newCompletionRefreshCmd())
	cmd.AddCommand(newCompletionStatusCmd())

	return cmd
}

func runCompletion(rootCmd *cobra.Command, shell string) error {
	switch shell {
	case "bash":
		return rootCmd.GenBashCompletionV2(os.Stdout, true)
	case "zsh":
		return rootCmd.GenZshCompletion(os.Stdout)
	case "fish":
		return rootCmd.GenFishCompletion(os.Stdout, true)
	case "powershell":
		return rootCmd.GenPowerShellCompletionWithDesc(os.Stdout)
	default:
		return fmt.Errorf("unknown shell: %s", shell)
	}
}

func newCompletionBashCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "bash",
		Short: "Generate bash completion script",
		Long: `Generate the autocompletion script for bash.

This script depends on the 'bash-completion' package.
If it is not installed already, you can install it via your OS's package manager.

To load completions in your current shell session:
  $ source <(bcq completion bash)

To load completions for every new session, execute once:

Linux:
  $ bcq completion bash > /etc/bash_completion.d/bcq

macOS:
  $ bcq completion bash > $(brew --prefix)/etc/bash_completion.d/bcq
`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Root().GenBashCompletionV2(os.Stdout, true)
		},
	}
}

func newCompletionZshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "zsh",
		Short: "Generate zsh completion script",
		Long: `Generate the autocompletion script for zsh.

If shell completion is not already enabled in your environment you will need
to enable it. You can execute the following once:
  $ echo "autoload -U compinit; compinit" >> ~/.zshrc

To load completions in your current shell session:
  $ source <(bcq completion zsh)

To load completions for every new session, execute once:
  $ bcq completion zsh > "${fpath[1]}/_bcq"

You will need to start a new shell for this setup to take effect.
`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Root().GenZshCompletion(os.Stdout)
		},
	}
}

func newCompletionFishCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fish",
		Short: "Generate fish completion script",
		Long: `Generate the autocompletion script for fish.

To load completions in your current shell session:
  $ bcq completion fish | source

To load completions for every new session, execute once:
  $ bcq completion fish > ~/.config/fish/completions/bcq.fish

You will need to start a new shell for this setup to take effect.
`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Root().GenFishCompletion(os.Stdout, true)
		},
	}
}

func newCompletionPowershellCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "powershell",
		Short: "Generate powershell completion script",
		Long: `Generate the autocompletion script for powershell.

To load completions in your current shell session:
  PS> bcq completion powershell | Out-String | Invoke-Expression

To load completions for every new session, add the output of the above command
to your powershell profile.
`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Root().GenPowerShellCompletionWithDesc(os.Stdout)
		},
	}
}

func newCompletionRefreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh",
		Short: "Refresh the completion cache",
		Long: `Refresh the completion cache by fetching fresh data from Basecamp.

This command fetches the current list of projects and people from Basecamp
and updates the local cache used for tab completion. Requires authentication.

The cache is also updated automatically when you run commands like:
  bcq projects
  bcq people list
  bcq me

Note: If you set cache_dir in a config file, completions won't find it.
Set BCQ_CACHE_DIR or BASECAMP_CACHE_DIR in your environment instead.
`,
		RunE: runCompletionRefresh,
	}
}

func runCompletionRefresh(cmd *cobra.Command, args []string) error {
	app := appctx.FromContext(cmd.Context())
	if app == nil {
		return fmt.Errorf("app not initialized")
	}

	// Check authentication first for friendly error message
	if !app.Auth.IsAuthenticated() {
		return output.ErrAuth("Not authenticated. Run: bcq auth login")
	}

	// Then check account is configured
	if err := app.RequireAccount(); err != nil {
		return err
	}

	// Create refresher with the account-scoped client
	store := completion.NewStore(app.Config.CacheDir)
	refresher := completion.NewRefresher(store, app.Account())

	// Perform synchronous refresh
	refreshResult := refresher.RefreshAll(cmd.Context())

	// Load actual cache to get current counts (includes preserved data on partial failure)
	cache, loadErr := store.Load()
	if loadErr != nil {
		// Cache load failed - report the error, don't pretend all is well
		return fmt.Errorf("refresh completed but failed to read cache: %w", loadErr)
	}
	projectsCount := len(cache.Projects)
	peopleCount := len(cache.People)

	// Build result with actual cached counts
	result := map[string]any{
		"projects":   projectsCount,
		"people":     peopleCount,
		"cache_path": store.Path(),
	}

	// Include refresh details on partial failure
	if refreshResult.HasError() {
		if refreshResult.ProjectsErr != nil {
			result["projects_error"] = refreshResult.ProjectsErr.Error()
			result["projects_refreshed"] = false
		} else {
			result["projects_refreshed"] = true
		}
		if refreshResult.PeopleErr != nil {
			result["people_error"] = refreshResult.PeopleErr.Error()
			result["people_refreshed"] = false
		} else {
			result["people_refreshed"] = true
		}
	}

	// Build summary
	var summary string
	if refreshResult.ProjectsErr != nil && refreshResult.PeopleErr != nil {
		return fmt.Errorf("refresh failed: %w", refreshResult.Error())
	} else if refreshResult.HasError() {
		// Report actual cached counts with warning about what failed
		summary = fmt.Sprintf("Cached %d projects, %d people (warning: %v)",
			projectsCount, peopleCount, refreshResult.Error())
	} else {
		summary = fmt.Sprintf("Cached %d projects and %d people",
			projectsCount, peopleCount)
	}

	return app.Output.OK(result, output.WithSummary(summary))
}

func newCompletionStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show completion cache status",
		Long: `Show the status of the completion cache.

Displays information about the cached completion data including:
- Number of cached projects, people, and accounts
- When the cache was last updated
- Whether the cache is stale
- Cache file location

Note: If you set cache_dir in a config file, completions won't find it.
Set BCQ_CACHE_DIR or BASECAMP_CACHE_DIR in your environment instead.
`,
		RunE: runCompletionStatus,
	}
}

func runCompletionStatus(cmd *cobra.Command, args []string) error {
	app := appctx.FromContext(cmd.Context())
	if app == nil {
		return fmt.Errorf("app not initialized")
	}

	store := completion.NewStore(app.Config.CacheDir)
	cache, err := store.Load()
	if err != nil {
		return err
	}

	// Determine staleness for projects/people (refreshed together via `completion refresh`)
	// Accounts are refreshed separately via `bcq me`, so not included in staleness check
	isStale := store.IsStale(completion.DefaultMaxAge)

	// Find oldest timestamp across projects/people for age calculation
	// (accounts are separate - refreshed via `bcq me`)
	oldest := cache.ProjectsUpdatedAt
	if !cache.PeopleUpdatedAt.IsZero() && (oldest.IsZero() || cache.PeopleUpdatedAt.Before(oldest)) {
		oldest = cache.PeopleUpdatedAt
	}

	// Determine status based on projects/people (the main completion data)
	hasProjectsOrPeople := len(cache.Projects) > 0 || len(cache.People) > 0
	var age string
	var status string
	if !hasProjectsOrPeople {
		if len(cache.Accounts) > 0 {
			// Only accounts cached - need to run `completion refresh` for projects/people
			age = "never"
			status = "needs refresh"
		} else {
			age = "never"
			status = "empty"
		}
	} else if oldest.IsZero() {
		// Has data but no timestamps (legacy cache)
		age = "unknown"
		status = "stale"
	} else {
		age = time.Since(oldest).Round(time.Second).String()
		if isStale {
			status = "stale"
		} else {
			status = "fresh"
		}
	}

	result := map[string]any{
		"projects":            len(cache.Projects),
		"people":              len(cache.People),
		"accounts":            len(cache.Accounts),
		"projects_updated_at": cache.ProjectsUpdatedAt,
		"people_updated_at":   cache.PeopleUpdatedAt,
		"accounts_updated_at": cache.AccountsUpdatedAt,
		"age":                 age,
		"status":              status,
		"stale":               isStale,
		"cache_path":          store.Path(),
	}

	summary := fmt.Sprintf("%d projects, %d people, %d accounts (%s)",
		len(cache.Projects), len(cache.People), len(cache.Accounts), status)

	return app.Output.OK(result, output.WithSummary(summary))
}
