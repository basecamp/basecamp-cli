package completion

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
)

// CacheDirFunc returns the cache directory to use for completion.
// Takes the command to allow checking both context and flags at completion time.
type CacheDirFunc func(cmd *cobra.Command) string

// DefaultCacheDirFunc returns the cache directory by checking (in order):
// 1. --cache-dir flag on the root command
// 2. App config from context (set by PersistentPreRunE)
// 3. BCQ_CACHE_DIR environment variable (takes precedence)
// 4. BASECAMP_CACHE_DIR environment variable
// 5. Default cache directory
//
// This is the standard CacheDirFunc that all commands should use.
//
// Limitation: During __complete, PersistentPreRunE doesn't run, so appctx is not
// set and config files are not loaded. This means cache_dir set in config files
// is NOT honored during completion—only --cache-dir flag and env vars work.
// This is intentional: loading config files adds latency that defeats fast completions.
// Users who set cache_dir in config should also set BCQ_CACHE_DIR or BASECAMP_CACHE_DIR.
func DefaultCacheDirFunc(cmd *cobra.Command) string {
	// Check --cache-dir flag on root command
	if root := cmd.Root(); root != nil {
		if flag := root.PersistentFlags().Lookup("cache-dir"); flag != nil && flag.Changed {
			return flag.Value.String()
		}
	}
	// Check app context (populated by PersistentPreRunE)
	if app := appctx.FromContext(cmd.Context()); app != nil {
		return app.Config.CacheDir
	}
	// Check env vars (for completions where appctx isn't set)
	// BCQ_CACHE_DIR takes precedence over BASECAMP_CACHE_DIR, matching config.go
	if v := os.Getenv("BCQ_CACHE_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("BASECAMP_CACHE_DIR"); v != "" {
		return v
	}
	// Fall back to default
	return ""
}

// Completer provides tab completion functions for bcq CLI.
// It reads from a file-based cache and does NOT initialize the full App or SDK.
type Completer struct {
	getCacheDir CacheDirFunc
}

// NewCompleter creates a new Completer.
// The getCacheDir function is called at completion time to determine the cache directory.
// If nil, DefaultCacheDirFunc is used.
func NewCompleter(getCacheDir CacheDirFunc) *Completer {
	if getCacheDir == nil {
		getCacheDir = DefaultCacheDirFunc
	}
	return &Completer{getCacheDir: getCacheDir}
}

// store returns the Store to use for completion, resolving the cache dir at call time.
func (c *Completer) store(cmd *cobra.Command) *Store {
	return NewStore(c.getCacheDir(cmd))
}

// ProjectCompletion returns a Cobra completion function for project arguments.
// Projects are ranked: HQ > Bookmarked > Recent > Alphabetical.
func (c *Completer) ProjectCompletion() cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		projects := c.store(cmd).Projects()
		if len(projects) == 0 {
			// No cache - suggest no completions but allow any input
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		// Rank and sort projects
		ranked := rankProjects(projects)

		// Filter by prefix
		toCompleteLower := strings.ToLower(toComplete)
		var completions []cobra.Completion
		for _, p := range ranked {
			nameLower := strings.ToLower(p.Name)
			if strings.HasPrefix(nameLower, toCompleteLower) ||
				strings.Contains(nameLower, toCompleteLower) {
				// Use ID as completion value with name as description
				completion := cobra.CompletionWithDesc(
					fmt.Sprintf("%d", p.ID),
					p.Name,
				)
				completions = append(completions, completion)
			}
		}

		return completions, cobra.ShellCompDirectiveNoFileComp
	}
}

// ProjectNameCompletion returns a Cobra completion function for project name arguments.
// Unlike ProjectCompletion, this returns names instead of IDs for commands that accept names.
func (c *Completer) ProjectNameCompletion() cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		projects := c.store(cmd).Projects()
		if len(projects) == 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		ranked := rankProjects(projects)
		toCompleteLower := strings.ToLower(toComplete)
		var completions []cobra.Completion
		for _, p := range ranked {
			nameLower := strings.ToLower(p.Name)
			if strings.HasPrefix(nameLower, toCompleteLower) ||
				strings.Contains(nameLower, toCompleteLower) {
				// Return name as-is; Cobra's completion scripts handle escaping
				completions = append(completions, cobra.Completion(p.Name))
			}
		}

		return completions, cobra.ShellCompDirectiveNoFileComp
	}
}

// PeopleCompletion returns a Cobra completion function for person arguments.
// People are sorted alphabetically by name, with "me" always first if it matches the filter.
func (c *Completer) PeopleCompletion() cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		toCompleteLower := strings.ToLower(toComplete)
		var completions []cobra.Completion

		// Add "me" as a special completion option (always available, even with empty cache)
		if strings.HasPrefix("me", toCompleteLower) {
			completions = append(completions, cobra.CompletionWithDesc("me", "Current authenticated user"))
		}

		people := c.store(cmd).People()
		if len(people) == 0 {
			return completions, cobra.ShellCompDirectiveNoFileComp
		}

		// Sort alphabetically by name
		sorted := make([]CachedPerson, len(people))
		copy(sorted, people)
		sort.Slice(sorted, func(i, j int) bool {
			return strings.ToLower(sorted[i].Name) < strings.ToLower(sorted[j].Name)
		})

		// Filter by prefix
		for _, p := range sorted {
			nameLower := strings.ToLower(p.Name)
			emailLower := strings.ToLower(p.EmailAddress)
			if strings.HasPrefix(nameLower, toCompleteLower) ||
				strings.Contains(nameLower, toCompleteLower) ||
				strings.HasPrefix(emailLower, toCompleteLower) {
				// Use ID as completion value with name as description
				desc := p.Name
				if p.EmailAddress != "" {
					desc = fmt.Sprintf("%s <%s>", p.Name, p.EmailAddress)
				}
				completion := cobra.CompletionWithDesc(
					fmt.Sprintf("%d", p.ID),
					desc,
				)
				completions = append(completions, completion)
			}
		}

		return completions, cobra.ShellCompDirectiveNoFileComp
	}
}

// PeopleNameCompletion returns a Cobra completion function for person name arguments.
// People are sorted alphabetically by name, with "me" always first if it matches the filter.
func (c *Completer) PeopleNameCompletion() cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		toCompleteLower := strings.ToLower(toComplete)
		var completions []cobra.Completion

		// Add "me" as a special completion option (always available, even with empty cache)
		if strings.HasPrefix("me", toCompleteLower) {
			completions = append(completions, "me")
		}

		people := c.store(cmd).People()
		if len(people) == 0 {
			return completions, cobra.ShellCompDirectiveNoFileComp
		}

		// Sort alphabetically
		sorted := make([]CachedPerson, len(people))
		copy(sorted, people)
		sort.Slice(sorted, func(i, j int) bool {
			return strings.ToLower(sorted[i].Name) < strings.ToLower(sorted[j].Name)
		})

		for _, p := range sorted {
			nameLower := strings.ToLower(p.Name)
			if strings.HasPrefix(nameLower, toCompleteLower) ||
				strings.Contains(nameLower, toCompleteLower) {
				// Return name as-is; Cobra's completion scripts handle escaping
				completions = append(completions, cobra.Completion(p.Name))
			}
		}

		return completions, cobra.ShellCompDirectiveNoFileComp
	}
}

// rankProjects returns projects sorted by priority:
// 1. HQ (purpose="hq")
// 2. Bookmarked
// 3. Recently updated
// 4. Alphabetical
func rankProjects(projects []CachedProject) []CachedProject {
	// Copy to avoid mutating the original
	ranked := make([]CachedProject, len(projects))
	copy(ranked, projects)

	sort.Slice(ranked, func(i, j int) bool {
		// HQ projects first
		iHQ := ranked[i].Purpose == "hq"
		jHQ := ranked[j].Purpose == "hq"
		if iHQ != jHQ {
			return iHQ
		}

		// Then bookmarked
		if ranked[i].Bookmarked != ranked[j].Bookmarked {
			return ranked[i].Bookmarked
		}

		// Then by recency (more recent first)
		if !ranked[i].UpdatedAt.IsZero() && !ranked[j].UpdatedAt.IsZero() {
			if !ranked[i].UpdatedAt.Equal(ranked[j].UpdatedAt) {
				return ranked[i].UpdatedAt.After(ranked[j].UpdatedAt)
			}
		}

		// Finally alphabetical (case-insensitive)
		return strings.ToLower(ranked[i].Name) < strings.ToLower(ranked[j].Name)
	})

	return ranked
}
