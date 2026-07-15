package commands

import (
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
)

const (
	maxCodexHookInput           = 1 << 20
	codexAlternateCanceledState = "cancel" + "led"
)

var basecampReferencePattern = regexp.MustCompile(`(?i)\b(BC|todo|basecamp)-([0-9]+)\b`)

type codexHookInput struct {
	CWD          string          `json:"cwd"`
	ToolName     string          `json:"tool_name"`
	ToolInput    json.RawMessage `json:"tool_input"`
	ToolOutput   json.RawMessage `json:"tool_output"`
	ToolResponse json.RawMessage `json:"tool_response"`
}

// NewCodexHookCmd creates the hidden command group used by Codex plugin hooks.
func NewCodexHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "codex-hook",
		Short:  "Run Codex plugin hooks",
		Hidden: true,
	}
	cmd.AddCommand(newCodexSessionStartCmd(), newCodexPostCommitCheckCmd())
	return cmd
}

func newCodexSessionStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "session-start",
		Short: "Report Basecamp integration status to Codex",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			contextMessage := "Basecamp is active. OAuth is not ready; run `basecamp auth login` before using Basecamp commands."
			if app != nil && app.Auth.IsAuthenticated() {
				contextMessage = "Basecamp is active and OAuth is ready. Use the Basecamp skills for project work."
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
				"hookSpecificOutput": map[string]string{
					"hookEventName":     "SessionStart",
					"additionalContext": contextMessage,
				},
			})
		},
	}
}

func newCodexPostCommitCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "post-commit-check",
		Short: "Suggest Basecamp follow-up after referenced commits",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			input, ok := readCodexHookInput(cmd.InOrStdin())
			if !ok || input.ToolName != "Bash" || !codexHookRanCommit(input.ToolInput) || !codexHookSucceeded(input) {
				return nil
			}

			cwd := input.CWD
			if cwd == "" {
				cwd = "."
			}

			branch, subject, revision, ok := codexCommitDetails(cmd.Context(), cwd)
			if !ok {
				return nil
			}
			match := basecampReferencePattern.FindStringSubmatch(subject)
			if match == nil {
				match = basecampReferencePattern.FindStringSubmatch(branch)
			}
			if match == nil {
				return nil
			}

			message := "Basecamp reference " + match[0] + " detected after commit " + revision + ". " +
				"Consider linking it with: basecamp comments create " + match[2] + " \"Commit " + revision + " linked from Git\". " +
				"When the todo is complete: basecamp todos complete " + match[2] + ". Nothing was posted or completed automatically."
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"systemMessage": message})
		},
	}
}

func readCodexHookInput(r io.Reader) (codexHookInput, bool) {
	var input codexHookInput
	decoder := json.NewDecoder(io.LimitReader(r, maxCodexHookInput))
	if err := decoder.Decode(&input); err != nil {
		return codexHookInput{}, false
	}
	return input, true
}

func codexHookRanCommit(raw json.RawMessage) bool {
	var input struct {
		Command string `json:"command"`
		Cmd     string `json:"cmd"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return false
	}
	command := input.Command
	if command == "" {
		command = input.Cmd
	}
	fields := strings.Fields(command)
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == "git" && strings.Trim(fields[i+1], ";|&") == "commit" {
			return true
		}
	}
	return false
}

func codexHookSucceeded(input codexHookInput) bool {
	raw := input.ToolResponse
	if len(raw) == 0 || string(raw) == "null" {
		raw = input.ToolOutput
	}
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}

	var nested string
	if err := json.Unmarshal(raw, &nested); err == nil {
		if json.Valid([]byte(nested)) {
			raw = []byte(nested)
		} else {
			return nested != ""
		}
	}

	var result struct {
		ExitCode *int   `json:"exit_code"`
		Success  *bool  `json:"success"`
		IsError  bool   `json:"is_error"`
		Error    string `json:"error"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return false
	}
	if result.ExitCode != nil && *result.ExitCode != 0 {
		return false
	}
	if result.Success != nil && !*result.Success {
		return false
	}
	if result.IsError || result.Error != "" {
		return false
	}
	switch strings.ToLower(result.Status) {
	case "error", "failed", "failure", "canceled", codexAlternateCanceledState, "timed-out", "timeout":
		return false
	default:
		return true
	}
}

func codexCommitDetails(parent context.Context, cwd string) (string, string, string, bool) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()

	branch, err := gitOutput(ctx, cwd, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", "", "", false
	}
	subject, err := gitOutput(ctx, cwd, "log", "-1", "--format=%s")
	if err != nil {
		return "", "", "", false
	}
	revision, err := gitOutput(ctx, cwd, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", "", "", false
	}
	return branch, subject, revision, true
}

func gitOutput(ctx context.Context, cwd string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", cwd}, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...) //nolint:gosec // executable is fixed and arguments are passed without a shell
	output, err := cmd.Output()
	return strings.TrimSpace(string(output)), err
}
