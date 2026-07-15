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
	segments, ok := shellCommandSegments(command)
	if !ok {
		return false
	}
	for _, segment := range segments {
		if codexGitSubcommand(strings.Fields(segment)) == "commit" {
			return true
		}
	}
	return false
}

func shellCommandSegments(command string) ([]string, bool) {
	segments := make([]string, 0, 2)
	start := 0
	var quote byte
	escaped := false
	for index := 0; index < len(command); index++ {
		char := command[index]
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			}
			continue
		}
		if char == '\'' || char == '"' {
			quote = char
			continue
		}
		if char == ';' || char == '\n' || char == '&' || char == '|' {
			if segment := strings.TrimSpace(command[start:index]); segment != "" {
				segments = append(segments, segment)
			}
			start = index + 1
		}
	}
	if quote != 0 || escaped {
		return nil, false
	}
	if segment := strings.TrimSpace(command[start:]); segment != "" {
		segments = append(segments, segment)
	}
	return segments, true
}

func codexGitSubcommand(fields []string) string {
	if len(fields) < 2 || !isGitExecutable(fields[0]) {
		return ""
	}
	for index := 1; index < len(fields); {
		argument := strings.Trim(fields[index], `"'`)
		switch argument {
		case "-C", "-c", "--git-dir", "--work-tree", "--namespace", "--config-env", "--exec-path":
			if index+1 >= len(fields) {
				return ""
			}
			index += 2
		case "--no-pager", "--paginate", "-p", "-P", "--bare", "--literal-pathspecs", "--glob-pathspecs", "--noglob-pathspecs", "--icase-pathspecs":
			index++
		case "--":
			if index+1 >= len(fields) {
				return ""
			}
			return strings.Trim(fields[index+1], `"'`)
		default:
			if strings.HasPrefix(argument, "-C") || strings.HasPrefix(argument, "-c") ||
				strings.HasPrefix(argument, "--git-dir=") || strings.HasPrefix(argument, "--work-tree=") ||
				strings.HasPrefix(argument, "--namespace=") || strings.HasPrefix(argument, "--config-env=") ||
				strings.HasPrefix(argument, "--exec-path=") {
				index++
				continue
			}
			if strings.HasPrefix(argument, "-") {
				return ""
			}
			return argument
		}
	}
	return ""
}

func isGitExecutable(field string) bool {
	executable := strings.Trim(field, `"'`)
	if separator := strings.LastIndexAny(executable, `/\`); separator >= 0 {
		executable = executable[separator+1:]
	}
	return executable == "git" || strings.EqualFold(executable, "git.exe")
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
			return false
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
	if result.IsError || result.Error != "" {
		return false
	}
	if result.Success != nil && !*result.Success {
		return false
	}
	switch strings.ToLower(result.Status) {
	case "error", "failed", "failure", "canceled", codexAlternateCanceledState, "timed-out", "timeout":
		return false
	}
	if result.ExitCode != nil {
		return *result.ExitCode == 0
	}
	if result.Success != nil {
		return *result.Success
	}
	switch strings.ToLower(result.Status) {
	case "completed", "success", "succeeded":
		return true
	default:
		return false
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
