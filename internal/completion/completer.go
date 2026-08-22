package completion

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// CacheDirFunc returns the cache directory to use for completion.
// Takes the command to allow checking both context and flags at completion time.
type CacheDirFunc func(cmd *cobra.Command) string

// DefaultCacheDirFunc returns the cache directory by checking (in order):
// 1. --cache-dir flag on the root command
// 2. App config from context (set by PersistentPreRunE)
// 3. BASECAMP_CACHE_DIR environment variable
// 4. Default cache directory
//
// This is the standard CacheDirFunc that all commands should use.
//
// Limitation: During __complete, PersistentPreRunE doesn't run, so appctx is not
// set and config files are not loaded. This means cache_dir set in config files
// is NOT honored during completion—only --cache-dir flag and env vars work.
// This is intentional: loading config files adds latency that defeats fast completions.
// Users who set cache_dir in config should also set BASECAMP_CACHE_DIR.
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
	// Check env var (for completions where appctx isn't set)
	if v := os.Getenv("BASECAMP_CACHE_DIR"); v != "" {
		return v
	}
	// Fall back to default
	return ""
}

// Completer provides tab completion functions for the basecamp CLI.
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
					sanitizeCompletionDesc(p.Name),
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
			// Skip names carrying control characters: the value must round-trip
			// verbatim for name resolution, so it can't be sanitized. The
			// project stays reachable by ID.
			if hasControlChars(p.Name) {
				continue
			}
			nameLower := strings.ToLower(p.Name)
			if strings.HasPrefix(nameLower, toCompleteLower) ||
				strings.Contains(nameLower, toCompleteLower) {
				// Return name as-is; Cobra's completion scripts handle escaping
				completions = append(completions, p.Name)
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
					sanitizeCompletionDesc(desc),
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
			// Skip names carrying control characters: the value must round-trip
			// verbatim for name resolution, so it can't be sanitized. The
			// person stays reachable by ID.
			if hasControlChars(p.Name) {
				continue
			}
			nameLower := strings.ToLower(p.Name)
			if strings.HasPrefix(nameLower, toCompleteLower) ||
				strings.Contains(nameLower, toCompleteLower) {
				// Return name as-is; Cobra's completion scripts handle escaping
				completions = append(completions, p.Name)
			}
		}

		return completions, cobra.ShellCompDirectiveNoFileComp
	}
}

// AccountCompletion returns a Cobra completion function for account arguments.
// Accounts are sorted alphabetically by name. The --account flag takes an ID.
func (c *Completer) AccountCompletion() cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		accounts := c.store(cmd).Accounts()
		if len(accounts) == 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		// Sort alphabetically by name
		sorted := make([]CachedAccount, len(accounts))
		copy(sorted, accounts)
		sort.Slice(sorted, func(i, j int) bool {
			return strings.ToLower(sorted[i].Name) < strings.ToLower(sorted[j].Name)
		})

		// Filter by prefix (match on ID or name)
		toCompleteLower := strings.ToLower(toComplete)
		var completions []cobra.Completion
		for _, a := range sorted {
			idStr := fmt.Sprintf("%d", a.ID)
			nameLower := strings.ToLower(a.Name)
			if strings.HasPrefix(idStr, toComplete) ||
				strings.HasPrefix(nameLower, toCompleteLower) ||
				strings.Contains(nameLower, toCompleteLower) {
				// Use ID as completion value with name as description
				completions = append(completions, cobra.CompletionWithDesc(idStr, sanitizeCompletionDesc(a.Name)))
			}
		}

		return completions, cobra.ShellCompDirectiveNoFileComp
	}
}

// ProfileCompletion returns a Cobra completion function for profile arguments.
// Profiles come from the config file's profiles map. Since completion runs before
// PersistentPreRunE, we need to load the config directly here.
func (c *Completer) ProfileCompletion() cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		profiles := c.store(cmd).Profiles()
		if len(profiles) == 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		// Sort alphabetically by name
		sorted := make([]CachedProfile, len(profiles))
		copy(sorted, profiles)
		sort.Slice(sorted, func(i, j int) bool {
			return strings.ToLower(sorted[i].Name) < strings.ToLower(sorted[j].Name)
		})

		// Filter by prefix
		toCompleteLower := strings.ToLower(toComplete)
		var completions []cobra.Completion
		for _, p := range sorted {
			// Skip names carrying control characters: the profile name is the
			// completion value and must round-trip verbatim, so it can't be
			// sanitized.
			if hasControlChars(p.Name) {
				continue
			}
			nameLower := strings.ToLower(p.Name)
			if strings.HasPrefix(nameLower, toCompleteLower) ||
				strings.Contains(nameLower, toCompleteLower) {
				// Use name as completion value with base URL as description
				completions = append(completions, cobra.CompletionWithDesc(p.Name, sanitizeCompletionDesc(p.BaseURL)))
			}
		}

		return completions, cobra.ShellCompDirectiveNoFileComp
	}
}

// sanitizeCompletionDesc strips terminal escape sequences and control
// characters from a completion description. Descriptions can carry API- or
// config-controlled strings (project/person/account names, profile base_url)
// which the shell renders to the terminal; stripping them prevents terminal
// injection.
//
// It delegates to richtext.SanitizeTerminal for the ANSI-aware pass so a full
// escape sequence like ESC[31m is removed whole (rather than leaving "[31m"
// litter after only the ESC byte is dropped), then drops any newlines, carriage
// returns, and tabs that SanitizeTerminal preserves so descriptions stay on a
// single line. Ordinary spaces and other printable text are left untouched, so
// hasControlChars only flags genuine control/escape content.
func sanitizeCompletionDesc(s string) string {
	s = richtext.SanitizeTerminal(s)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, s)
}

// hasControlChars reports whether s would be altered by sanitizeCompletionDesc:
// it contains control characters (C0 including ESC, DEL, or C1) or invalid
// UTF-8 (strings.Map rewrites invalid bytes to U+FFFD, so a raw C1 byte like
// \x9b also differs). Used to SKIP name-valued completion candidates outright —
// sanitizing a completion VALUE would break resolution, which matches by the
// exact name string.
func hasControlChars(s string) bool {
	return s != sanitizeCompletionDesc(s)
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
