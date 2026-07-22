package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
)

const (
	maxAgentHookInput    = 1 << 20
	agentHookSnapshotTTL = time.Hour
	agentHookGitTimeout  = 2 * time.Second
	unbornHeadSnapshot   = "EMPTY"
)

var basecampReferencePattern = regexp.MustCompile(`(?i)\b(BC|todo|basecamp)-([0-9]+)\b`)

// agentHookInput carries the fields both agents send on every hook event.
// tool_response is deliberately absent: neither agent promises an exit code
// there, so commit success is proven from repository state instead.
type agentHookInput struct {
	CWD           string          `json:"cwd"`
	SessionID     string          `json:"session_id"`
	ToolUseID     string          `json:"tool_use_id"`
	HookEventName string          `json:"hook_event_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
}

// NewAgentHookCmd creates the hidden command group used by the plugin hooks
// shared between Claude Code and Codex.
//
// The group declares its own persistent lifecycle, which replaces the root
// command's for this subtree (Cobra runs only the innermost declaration).
// The root lifecycle validates config, resolves profiles, and enforces
// base_url HTTPS — any of which can fail with a nonzero exit, breaking the
// hooks' never-block contract. Hooks tolerate every config problem instead:
// they fall back to defaults, which still supports BASECAMP_TOKEN and
// stored-credential auth detection plus the cache-directory fallback.
func NewAgentHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "agent-hook",
		Short:  "Run agent plugin hooks",
		Hidden: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if appctx.FromContext(cmd.Context()) != nil {
				return nil
			}
			cmd.SetContext(appctx.WithApp(cmd.Context(), newAgentHookApp()))
			return nil
		},
		PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
			if app := appctx.FromContext(cmd.Context()); app != nil {
				app.Close()
			}
			return nil
		},
	}
	cmd.AddCommand(newAgentHookSessionStartCmd(), newAgentHookPreCommitSnapshotCmd(), newAgentHookPostCommitCmd())
	return cmd
}

// newAgentHookApp builds the hook app from whatever config is usable. An
// insecure base_url must be neutralized before NewApp: the SDK client
// constructor panics on a non-localhost http URL, and root's HTTPS gate —
// which normally catches it first — is bypassed by the hook lifecycle.
func newAgentHookApp() *appctx.App {
	cfg, err := config.Load(config.FlagOverrides{})
	if err != nil || cfg == nil {
		cfg = config.Default()
	}
	if hostutil.RequireSecureURL(cfg.BaseURL) != nil {
		cfg.BaseURL = config.Default().BaseURL
	}
	return appctx.NewApp(cfg)
}

func newAgentHookSessionStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "session-start",
		Short: "Report Basecamp integration status at session start",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			message := "Basecamp is active. OAuth is not ready; run `basecamp auth login` before using Basecamp commands."
			if app != nil && app.Auth.IsAuthenticated() {
				message = "Basecamp is active and OAuth is ready. Use the Basecamp skills for project work."
			}
			emitAgentHookContext(cmd, "SessionStart", message)
			return nil
		},
	}
}

func newAgentHookPreCommitSnapshotCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pre-commit-snapshot",
		Short: "Record HEAD before a shell command that may commit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			input, ok := readAgentHookInput(cmd.InOrStdin())
			if !ok || !strings.Contains(strings.ToLower(agentHookCommand(input.ToolInput)), "commit") {
				return nil
			}
			head, ok := agentHookHead(cmd.Context(), input.CWD)
			if !ok {
				return nil
			}
			if dir := agentHookStateDir(cmd.Context()); dir != "" {
				_ = writeAgentHookSnapshot(dir, agentHookSnapshotName(input), head)
			}
			return nil
		},
	}
}

func newAgentHookPostCommitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "post-commit",
		Short: "Suggest Basecamp follow-up after referenced commits",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			input, ok := readAgentHookInput(cmd.InOrStdin())
			if !ok {
				return nil
			}
			dir := agentHookStateDir(cmd.Context())
			if dir == "" {
				return nil
			}
			// Claim the snapshot with an atomic rename before reading so
			// concurrent deliveries (PostToolUse and PostToolUseFailure can
			// both fire for one tool call) produce exactly one nudge.
			path := filepath.Join(dir, agentHookSnapshotName(input))
			claimed := path + ".claim"
			if err := os.Rename(path, claimed); err != nil {
				return nil //nolint:nilerr // no snapshot (or already claimed) means nothing to prove — stay silent
			}
			data, err := os.ReadFile(claimed) //nolint:gosec // G304: path is a hash inside our own state directory
			_ = os.Remove(claimed)
			if err != nil {
				return nil //nolint:nilerr // unreadable snapshot — stay silent
			}

			reference, revision, ok := agentHookCommitReference(cmd.Context(), input.CWD, strings.TrimSpace(string(data)))
			if !ok {
				return nil
			}
			event := input.HookEventName
			if event == "" {
				event = "PostToolUse"
			}
			emitAgentHookContext(cmd, event, "Basecamp reference "+reference+" detected in commit "+revision+". "+
				"Use the Basecamp skill to link the commit or complete the referenced item. "+
				"Nothing was posted to Basecamp automatically.")
			return nil
		},
	}
}

func emitAgentHookContext(cmd *cobra.Command, event, message string) {
	_ = json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":     event,
			"additionalContext": message,
		},
	})
}

func readAgentHookInput(r io.Reader) (agentHookInput, bool) {
	data, err := io.ReadAll(io.LimitReader(r, maxAgentHookInput+1))
	if err != nil || len(data) > maxAgentHookInput {
		return agentHookInput{}, false
	}
	var input agentHookInput
	if err := json.Unmarshal(data, &input); err != nil {
		return agentHookInput{}, false
	}
	return input, true
}

func agentHookCommand(raw json.RawMessage) string {
	var input struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return ""
	}
	return input.Command
}

// agentHookHead resolves HEAD in cwd, reporting unbornHeadSnapshot for a
// repository with no commits yet and false for anything that is not a work
// tree at all.
func agentHookHead(ctx context.Context, cwd string) (string, bool) {
	if cwd == "" {
		cwd = "."
	}
	if inside, err := gitOutput(ctx, cwd, "rev-parse", "--is-inside-work-tree"); err != nil || inside != "true" {
		return "", false
	}
	// ^{commit} verifies HEAD resolves to an existing commit object — a
	// detached HEAD at a missing object "resolves" textually but must not
	// be recorded as known state.
	head, err := gitOutput(ctx, cwd, "rev-parse", "--verify", "HEAD^{commit}")
	if err == nil {
		return head, true
	}
	// HEAD did not verify. Claim EMPTY only for a proven unborn branch —
	// HEAD is still a symbolic ref to a branch with no commits. Any other
	// failure (transient git error, broken HEAD) stays silent rather than
	// risk a later nudge against state the snapshot never established.
	if _, symErr := gitOutput(ctx, cwd, "symbolic-ref", "-q", "HEAD"); symErr == nil {
		return unbornHeadSnapshot, true
	}
	return "", false
}

// agentHookCommitReference proves a commit happened (HEAD moved and the last
// reflog action was a commit) and returns the first Basecamp reference found
// in the new subject or branch name, with the short revision.
func agentHookCommitReference(ctx context.Context, cwd, before string) (string, string, bool) {
	if cwd == "" {
		cwd = "."
	}
	head, err := gitOutput(ctx, cwd, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || head == before {
		return "", "", false
	}
	action, err := gitOutput(ctx, cwd, "reflog", "-1", "--format=%gs")
	if err != nil || !strings.HasPrefix(action, "commit") {
		return "", "", false
	}
	subject, err := gitOutput(ctx, cwd, "log", "-1", "--format=%s")
	if err != nil {
		return "", "", false
	}
	reference := basecampReferencePattern.FindString(subject)
	if reference == "" {
		branch, err := gitOutput(ctx, cwd, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return "", "", false
		}
		reference = basecampReferencePattern.FindString(branch)
	}
	if reference == "" {
		return "", "", false
	}
	revision, err := gitOutput(ctx, cwd, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", "", false
	}
	return reference, revision, true
}

// agentHookStateDir prefers the persistent per-plugin data directory both
// agents provide to plugin hooks, falling back to the CLI cache directory.
func agentHookStateDir(ctx context.Context) string {
	if data := os.Getenv("CLAUDE_PLUGIN_DATA"); data != "" {
		return filepath.Join(data, "commit-snapshots")
	}
	app := appctx.FromContext(ctx)
	if app == nil || app.Config == nil || app.Config.CacheDir == "" {
		return ""
	}
	return filepath.Join(app.Config.CacheDir, "agent-hook")
}

// agentHookSnapshotName keys a snapshot to one tool call without exposing the
// session or tool-use identifiers in the filename.
func agentHookSnapshotName(input agentHookInput) string {
	sum := sha256.Sum256([]byte(input.SessionID + "|" + input.ToolUseID))
	return hex.EncodeToString(sum[:])
}

func writeAgentHookSnapshot(dir, name, head string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	removeExpiredAgentHookSnapshots(dir)
	return atomicWriteAgentHookFile(filepath.Join(dir, name), []byte(head+"\n"))
}

func removeExpiredAgentHookSnapshots(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-agentHookSnapshotTTL)
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && !entry.IsDir() && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// atomicWriteAgentHookFile writes data atomically via temp+rename, mirroring
// config.atomicWriteTrustFile including its Windows remove-then-rename fallback.
func atomicWriteAgentHookFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmpFile.Chmod(0600); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	if err := os.Rename(tmpPath, path); err != nil {
		if runtime.GOOS == "windows" {
			_ = os.Remove(path)
			return os.Rename(tmpPath, path)
		}
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// gitOutput runs one git command with its own deadline so a single slow
// invocation cannot starve the calls after it; the hook-level timeout in
// hooks.json remains the overall backstop.
func gitOutput(parent context.Context, cwd string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, agentHookGitTimeout)
	defer cancel()
	commandArgs := append([]string{"-C", cwd}, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...) //nolint:gosec // executable is fixed and arguments are passed without a shell
	output, err := cmd.Output()
	return strings.TrimSpace(string(output)), err
}
