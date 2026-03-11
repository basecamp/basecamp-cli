package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// BonfireLayout stores a saved multiplexer layout.
type BonfireLayout struct {
	Panes             []BonfirePane `json:"panes"`
	MultiplexerLayout string        `json:"multiplexer_layout"`
}

// BonfirePane represents a single pane in a layout.
type BonfirePane struct {
	URL string `json:"url"`
}

// NewBonfireCmd creates the bonfire command group.
func NewBonfireCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bonfire",
		Short: "Multi-campfire orchestration (experimental)",
		Long: `Orchestrate multiple campfire views using terminal multiplexers (tmux/zellij).

Requires an active tmux or zellij session.

This is an experimental feature. Enable it with:
  basecamp config set experimental.bonfire true --global`,
		Annotations: map[string]string{"agent_notes": "bonfire split opens a new pane with a campfire\nbonfire layout saves/restores pane arrangements"},
	}

	cmd.AddCommand(
		newBonfireSplitCmd(),
		newBonfireLayoutCmd(),
	)

	return cmd
}

func newBonfireSplitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "split <room-url>",
		Short: "Open a campfire in a new multiplexer pane",
		Long:  "Split the current terminal and open a campfire room in the new pane.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := requireBonfireExperimental(app); err != nil {
				return err
			}
			mux := hostutil.DetectMultiplexer()
			if mux == hostutil.MultiplexerNone {
				return output.ErrUsage("bonfire split requires tmux or zellij (not detected)")
			}

			roomURL := args[0]
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("cannot find executable: %w", err)
			}

			if err := hostutil.SplitPane(cmd.Context(), mux, exe, "tui", roomURL); err != nil {
				return fmt.Errorf("failed to split pane: %w", err)
			}

			return app.OK(map[string]any{
				"url":         roomURL,
				"multiplexer": string(mux),
				"status":      "opened",
			}, output.WithSummary(fmt.Sprintf("Opened campfire in new %s pane", mux)))
		},
	}
}

func newBonfireLayoutCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "layout",
		Short: "Manage bonfire layouts",
		Long:  "Save and restore multi-campfire layouts.",
	}

	cmd.AddCommand(
		newBonfireLayoutSaveCmd(),
		newBonfireLayoutLoadCmd(),
		newBonfireLayoutListCmd(),
	)

	return cmd
}

func newBonfireLayoutSaveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "save <name> <url>...",
		Short: "Save a bonfire layout",
		Long:  "Save a named layout with the given campfire URLs.",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := requireBonfireExperimental(app); err != nil {
				return err
			}
			name := args[0]
			urls := args[1:]

			panes := make([]BonfirePane, len(urls))
			for i, u := range urls {
				panes[i] = BonfirePane{URL: u}
			}

			layout := BonfireLayout{
				Panes:             panes,
				MultiplexerLayout: "tiled",
			}

			if app.Config.CacheDir == "" {
				return fmt.Errorf("cache_dir not configured; run: basecamp config set cache_dir <path> --global")
			}

			layouts, err := loadBonfireLayouts(app.Config.CacheDir)
			if err != nil {
				layouts = make(map[string]BonfireLayout)
			}
			layouts[name] = layout

			if err := saveBonfireLayouts(app.Config.CacheDir, layouts); err != nil {
				return fmt.Errorf("failed to save layout: %w", err)
			}

			return app.OK(map[string]any{
				"name":   name,
				"panes":  len(panes),
				"status": "saved",
			}, output.WithSummary(fmt.Sprintf("Saved layout %q with %d panes", name, len(panes))))
		},
	}
}

func newBonfireLayoutLoadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "load <name>",
		Short: "Restore a saved bonfire layout",
		Long:  "Open multiplexer panes for each campfire in the named layout.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := requireBonfireExperimental(app); err != nil {
				return err
			}
			name := args[0]

			mux := hostutil.DetectMultiplexer()
			if mux == hostutil.MultiplexerNone {
				return output.ErrUsage("bonfire layout load requires tmux or zellij (not detected)")
			}

			if app.Config.CacheDir == "" {
				return fmt.Errorf("cache_dir not configured; run: basecamp config set cache_dir <path> --global")
			}

			layouts, err := loadBonfireLayouts(app.Config.CacheDir)
			if err != nil {
				return fmt.Errorf("no saved layouts found: %w", err)
			}

			layout, ok := layouts[name]
			if !ok {
				return output.ErrNotFound("layout", name)
			}

			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("cannot find executable: %w", err)
			}

			for _, pane := range layout.Panes {
				if err := hostutil.SplitPane(cmd.Context(), mux, exe, "tui", pane.URL); err != nil {
					return fmt.Errorf("failed to create pane for %s: %w", pane.URL, err)
				}
			}

			if layout.MultiplexerLayout != "" {
				_ = hostutil.ApplyLayout(cmd.Context(), mux, layout.MultiplexerLayout)
			}

			return app.OK(map[string]any{
				"name":   name,
				"panes":  len(layout.Panes),
				"status": "loaded",
			}, output.WithSummary(fmt.Sprintf("Loaded layout %q with %d panes", name, len(layout.Panes))))
		},
	}
}

func newBonfireLayoutListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List saved bonfire layouts",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := requireBonfireExperimental(app); err != nil {
				return err
			}

			if app.Config.CacheDir == "" {
				return app.OK([]any{}, output.WithSummary("No saved layouts (cache_dir not configured)"))
			}

			layouts, err := loadBonfireLayouts(app.Config.CacheDir)
			if err != nil || len(layouts) == 0 {
				return app.OK([]any{}, output.WithSummary("No saved layouts"))
			}

			result := make([]map[string]any, 0, len(layouts))
			for name, layout := range layouts {
				result = append(result, map[string]any{
					"name":  name,
					"panes": len(layout.Panes),
				})
			}

			return app.OK(result, output.WithSummary(fmt.Sprintf("%d saved layout(s)", len(layouts))))
		},
	}
}

func requireBonfireExperimental(app *appctx.App) error {
	if !devBuild {
		return output.ErrUsageHint(
			"bonfire is only available in development builds",
			"build with: make build (or go build -tags dev ./cmd/basecamp)",
		)
	}
	if !app.Config.IsExperimental("bonfire") {
		return output.ErrUsage(
			"experimental feature \"bonfire\" is not enabled; run: basecamp config set experimental.bonfire true --global")
	}
	return nil
}

func layoutsPath(cacheDir string) string {
	return filepath.Join(cacheDir, "bonfire-layouts.json")
}

func loadBonfireLayouts(cacheDir string) (map[string]BonfireLayout, error) {
	data, err := os.ReadFile(layoutsPath(cacheDir)) //nolint:gosec // path from config
	if err != nil {
		return nil, err
	}
	var layouts map[string]BonfireLayout
	if err := json.Unmarshal(data, &layouts); err != nil {
		return nil, err
	}
	return layouts, nil
}

func saveBonfireLayouts(cacheDir string, layouts map[string]BonfireLayout) error {
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(layouts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(layoutsPath(cacheDir), append(data, '\n'), 0600)
}
