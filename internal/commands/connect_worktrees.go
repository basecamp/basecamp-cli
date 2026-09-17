package commands

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// connectWorktreesDir is where a connector's task worktrees live, under its
// state directory.
const connectWorktreesDir = "worktrees"

func newConnectWorktreesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktrees",
		Short: "List and prune the git worktrees the connector kept",
		Long: `With worktrees on (connect setup --worktrees), each task works in a git
worktree of its own, on a basecamp-connect/ branch. When the task ends the
worktree is removed only if nothing in it could be lost: nothing on its disk
but the files git tracks, unchanged, no merge or rebase in progress, not
locked, and every commit it reaches pushed or merged. Otherwise it is kept,
and listed here.

A Codex worker cannot commit — a worktree's git data is outside the directory
its sandbox may write — so with Codex every task that edits anything leaves a
kept worktree for you.`,
	}
	cmd.AddCommand(newConnectWorktreesListCmd(), newConnectWorktreesPruneCmd())
	return cmd
}

func newConnectWorktreesListCmd() *cobra.Command {
	var shadow bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the worktrees kept for you to deal with",
		Long: `List the worktrees the connector kept, with why: dirty (uncommitted work),
unpushed (commits nothing else holds), locked, moved (no longer where the
connector left it), or unverified (their state could not be read).`,
		Example: `  basecamp connect worktrees list -P agent`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app := appctx.FromContext(cmd.Context())
			wt, closeLedger, err := openConnectWorktrees(app, shadow)
			if err != nil {
				return err
			}
			defer closeLedger()
			retained, err := wt.Retained(cmd.Context())
			if err != nil {
				return err
			}
			out := make([]worktreeView, 0, len(retained))
			for _, r := range retained {
				out = append(out, viewWorktree(r))
			}
			return app.OK(out, output.WithSummary(fmt.Sprintf("%d worktree(s) kept", len(out))))
		},
	}
	cmd.Flags().BoolVar(&shadow, "shadow", false, "Read the shadow connector's state instead")
	return cmd
}

func newConnectWorktreesPruneCmd() *cobra.Command {
	var (
		force  []string
		shadow bool
	)
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove the kept worktrees you have dealt with",
		Long: `Remove every kept worktree that no longer holds work: now clean, with its
commits pushed or merged, or whose directory you removed yourself. A worktree
that still holds work is kept and listed with why.

--force <path> removes that worktree even with work in it; name each one.
Every commit it reaches that nothing else holds is first kept under
refs/basecamp-connect/retained/ (retained_refs), so a force discards files,
never commits. A worktree holding a submodule's own git data, or a lock, is
never forced; neither is one that is no longer where it was (reason "moved"):
move it back, or remove it yourself and prune again. A force that could not go
through is reported as kept with force_refused. Worktrees of tasks still
running are never touched.`,
		Example: `  basecamp connect worktrees prune -P agent
  basecamp connect worktrees prune -P agent --force ~/.local/state/basecamp/connect/2914079-52007412/worktrees/app-1a2b3c4d/17-a1b2c3`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app := appctx.FromContext(cmd.Context())
			for i, p := range force {
				if !filepath.IsAbs(p) {
					return output.ErrUsage(fmt.Sprintf("--force %q: name the worktree by its absolute path, as worktrees list shows it", p))
				}
				force[i] = filepath.Clean(p)
			}
			wt, closeLedger, err := openConnectWorktrees(app, shadow)
			if err != nil {
				return err
			}
			defer closeLedger()
			results, err := wt.Prune(cmd.Context(), force)
			if errors.Is(err, connector.ErrNotRetained) {
				return output.ErrUsageHint("Nothing was pruned: "+err.Error(), "--force takes a path from `basecamp connect worktrees list`.")
			}
			if err != nil {
				return err
			}
			out := make([]pruneView, 0, len(results))
			removed, kept := 0, 0
			for _, r := range results {
				out = append(out, pruneView{worktreeView: viewWorktree(r.Worktree), Action: string(r.Action), ForceRefused: r.ForceRefused, RetainedRefs: r.RetainedRefs})
				if r.Action == connector.PruneKept {
					kept++
				} else {
					removed++
				}
			}
			return app.OK(out, output.WithSummary(fmt.Sprintf("%d removed, %d kept", removed, kept)))
		},
	}
	cmd.Flags().StringArrayVar(&force, "force", nil, "Remove this kept worktree even with work in it (repeatable; an absolute path from worktrees list)")
	cmd.Flags().BoolVar(&shadow, "shadow", false, "Read the shadow connector's state instead")
	return cmd
}

// worktreeView is a kept worktree as the commands show it.
type worktreeView struct {
	Path       string `json:"path"`
	WorkDir    string `json:"work_dir"`
	Branch     string `json:"branch"`
	Route      string `json:"route"`
	Reason     string `json:"reason,omitempty"`
	EventID    int64  `json:"event_id"`
	TaskID     int64  `json:"task_id,omitempty"`
	RetainedAt string `json:"retained_at,omitempty"`
}

type pruneView struct {
	worktreeView
	Action       string   `json:"action"`
	ForceRefused bool     `json:"force_refused,omitempty"`
	RetainedRefs []string `json:"retained_refs,omitempty"`
}

func viewWorktree(w connector.Worktree) worktreeView {
	v := worktreeView{
		Path: w.Path, WorkDir: w.WorkDir, Branch: w.Branch, Route: w.Route,
		Reason: string(w.RetainedReason), EventID: w.OriginatingEventID, TaskID: w.TaskID,
	}
	if !w.RetainedAt.IsZero() {
		v.RetainedAt = w.RetainedAt.UTC().Format(time.RFC3339)
	}
	return v
}

// openConnectWorktrees opens the ledger of the connector the active profile
// is set up as: the one it has, never a new one, and never a schema this
// binary would migrate under a connector that is running.
func openConnectWorktrees(app *appctx.App, shadow bool) (*connector.Worktrees, func(), error) {
	if app == nil {
		return nil, nil, errors.New("app not initialized")
	}
	name := app.Config.ActiveProfile
	if name == "" {
		return nil, nil, output.ErrUsageHint("Worktrees belong to a connector's profile", "Pass -P/--profile <name>, a profile set up with `basecamp connect setup`.")
	}
	path, err := setup.Path(config.GlobalConfigDir(), name)
	if err != nil {
		return nil, nil, output.ErrUsage(err.Error())
	}
	file, err := setup.Load(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, nil, output.ErrUsageHint(fmt.Sprintf("Profile %q is not set up as a connector", name), "Run: basecamp connect setup -P "+strconv.Quote(name))
	case err != nil:
		return nil, nil, output.ErrUsage("connect.json cannot be used: " + err.Error())
	}
	// Named, not created: reading what a connector left must not make a
	// state directory for a connector that never ran.
	stateDir, err := connectStateDirPath(file, shadow)
	if err != nil {
		return nil, nil, output.ErrUsage("The connector's state directory cannot be used: " + err.Error())
	}
	ledgerPath := filepath.Join(stateDir, connector.LedgerFile)
	if _, err := os.Lstat(ledgerPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, output.ErrUsageHint("This connector has not run yet: there is no ledger in "+stateDir, "Run: basecamp connect -P "+strconv.Quote(name))
		}
		return nil, nil, err
	}
	ledger, err := connector.OpenExistingLedger(context.Background(), ledgerPath)
	if err != nil {
		return nil, nil, err
	}
	wt, err := connector.NewWorktrees(connector.WorktreesOptions{
		Ledger: ledger, Root: filepath.Join(stateDir, connectWorktreesDir),
		// What a removal refuses is said, not swallowed.
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if err != nil {
		_ = ledger.Close()
		return nil, nil, err
	}
	return wt, func() { _ = ledger.Close() }, nil
}
