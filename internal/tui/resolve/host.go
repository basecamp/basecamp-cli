package resolve

import (
	"context"
	"os"
	"sort"

	"github.com/basecamp/bcq/internal/hostutil"
	"github.com/basecamp/bcq/internal/output"
	"github.com/basecamp/bcq/internal/tui"
)

// Host resolves the host/environment using the following precedence:
// 1. CLI flag (--host)
// 2. Environment variable (BCQ_HOST)
// 3. Config file (default_host -> hosts map)
// 4. Interactive prompt (if terminal is interactive and multiple hosts configured)
// 5. Default to production URL
//
// Returns the resolved base URL and the source it came from.
func (r *Resolver) Host(ctx context.Context) (*ResolvedValue, error) {
	// 1. Check CLI flag
	if r.flags.Host != "" {
		return &ResolvedValue{
			Value:  hostutil.Normalize(r.flags.Host),
			Source: SourceFlag,
		}, nil
	}

	// 2. Check environment variable
	if host := os.Getenv("BCQ_HOST"); host != "" {
		return &ResolvedValue{
			Value:  hostutil.Normalize(host),
			Source: SourceConfig, // Treat env var as config-level
		}, nil
	}

	// 3. Check config for hosts map and default_host
	if len(r.config.Hosts) > 0 {
		// If default_host is set, use it
		if r.config.DefaultHost != "" {
			if hostConfig, ok := r.config.Hosts[r.config.DefaultHost]; ok {
				return &ResolvedValue{
					Value:  hostConfig.BaseURL,
					Source: SourceConfig,
				}, nil
			}
		}

		// If only one host configured, use it automatically
		if len(r.config.Hosts) == 1 {
			for _, hostConfig := range r.config.Hosts {
				return &ResolvedValue{
					Value:  hostConfig.BaseURL,
					Source: SourceDefault,
				}, nil
			}
		}

		// Multiple hosts configured - try interactive prompt
		if r.IsInteractive() {
			return r.promptForHost()
		}
	}

	// 4. Fall back to base_url from config if set
	if r.config.BaseURL != "" {
		return &ResolvedValue{
			Value:  r.config.BaseURL,
			Source: SourceConfig,
		}, nil
	}

	// 5. Default to production URL
	return &ResolvedValue{
		Value:  "https://3.basecampapi.com",
		Source: SourceDefault,
	}, nil
}

// promptForHost shows an interactive picker for host selection.
func (r *Resolver) promptForHost() (*ResolvedValue, error) {
	if len(r.config.Hosts) == 0 {
		return nil, output.ErrUsage("no hosts configured")
	}

	// Build picker items from configured hosts
	items := make([]tui.PickerItem, 0, len(r.config.Hosts))

	// Sort host names for consistent ordering
	hostNames := make([]string, 0, len(r.config.Hosts))
	for name := range r.config.Hosts {
		hostNames = append(hostNames, name)
	}
	sort.Strings(hostNames)

	for _, name := range hostNames {
		hostConfig := r.config.Hosts[name]
		items = append(items, tui.PickerItem{
			ID:          hostConfig.BaseURL,
			Title:       name,
			Description: hostConfig.BaseURL,
		})
	}

	selected, err := tui.PickHost(items)
	if err != nil {
		return nil, err
	}
	if selected == nil {
		return nil, output.ErrUsage("host selection canceled")
	}

	return &ResolvedValue{
		Value:  selected.ID, // ID is the base URL
		Source: SourcePrompt,
	}, nil
}

// HostWithPersist resolves the host and optionally prompts to save it.
func (r *Resolver) HostWithPersist(ctx context.Context) (*ResolvedValue, error) {
	resolved, err := r.Host(ctx)
	if err != nil {
		return nil, err
	}

	// Only prompt to persist if it was selected interactively
	if resolved.Source == SourcePrompt {
		// Find the host name for this URL
		for name, hostConfig := range r.config.Hosts {
			if hostConfig.BaseURL == resolved.Value {
				_, _ = PromptAndPersistDefaultHost(name)
				break
			}
		}
	}

	return resolved, nil
}
