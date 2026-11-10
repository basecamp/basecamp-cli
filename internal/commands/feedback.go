package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/feedback"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/resilience"
)

// feedbackStore is a seam so tests can keep detection state in a temp dir.
var feedbackStore = feedback.DefaultStore

// FeedbackReceipt is what Basecamp answers a submitted report with.
type FeedbackReceipt struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

// errFeedbackNotAvailable is the whole answer while the feature is off: no
// hint, so nothing points anywhere else.
func errFeedbackNotAvailable() *output.Error {
	return &output.Error{Code: output.CodeNotFound, Message: feedback.NotAvailable}
}

// NewFeedbackCmd creates the feedback command. It starts hidden; Execute
// shows it only while Basecamp advertises the endpoint or BASECAMP_FEEDBACK
// forces it on (see internal/feedback).
func NewFeedbackCmd() *cobra.Command {
	var category, subject, errorCode string

	cmd := &cobra.Command{
		Use:    "feedback <details>",
		Short:  "Report a problem or missing capability to the Basecamp team",
		Hidden: true,
		Long: `Send a report about Basecamp itself to the Basecamp team: a bug, a missing
capability, confusing behavior, praise, or anything else.

--category is required: ` + strings.Join(feedback.Categories, ", ") + `.
--subject names the command or tool the report is about (e.g. "todos create");
--error-code the error code it returned (e.g. api_error). Details are plain
text, up to ` + fmt.Sprint(feedback.MaxDetails) + ` characters ("-" reads stdin). Leave out personal or
customer content: the report is read by the Basecamp team.

The report is sent with your own login and answered with a receipt ID.`,
		Example: `  basecamp feedback --category bug --subject "todos create" --error-code api_error "Creating a to-do with --due tomorrow returns a 500"
  basecamp feedback --category missing_capability "No way to list a card table's on-hold cards"`,
		Args: cobra.ArbitraryArgs,
		Annotations: map[string]string{
			"agent_notes": "Reports a problem with Basecamp, a missing capability or confusing behavior to the Basecamp team.\n" +
				"--category is one of " + strings.Join(feedback.Categories, ", ") + "; details are at most " + fmt.Sprint(feedback.MaxDetails) + " characters.\n" +
				"Leave out personal or customer content.\n" +
				"Prints \"" + feedback.NotAvailable + "\" and exits non-zero where Basecamp does not offer feedback.",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			store := feedbackStore()

			// Availability comes before anything else, argument checks
			// included: while the feature is off, the one thing this
			// command says is that it is not available.
			forced, set := feedback.Override()
			if set && !forced {
				return errFeedbackNotAvailable()
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			key := feedbackStoreKey(app)
			account := app.Account()

			// Ask afresh unless forced on: a person may have been switched on
			// or off since the cached answer, and this is the one command
			// where a detection request is the point.
			if !set {
				advertised, err := store.Detect(cmd.Context(), account, key)
				if err != nil {
					return convertSDKError(err)
				}
				if !advertised {
					return errFeedbackNotAvailable()
				}
			}

			if !feedback.ValidCategory(category) {
				hint := "One of: " + strings.Join(feedback.Categories, ", ")
				if category == "" {
					return output.ErrUsageHint("--category is required", hint)
				}
				return output.ErrUsageHint(fmt.Sprintf("unknown category %q", category), hint)
			}
			if len(args) == 0 {
				return output.ErrUsageHint("<details> required",
					`basecamp feedback --category <category> "details"`)
			}
			details, err := resolveContentArg(cmd, args, 0)
			if err != nil {
				return err
			}
			details = strings.TrimSpace(details)
			if details == "" {
				return output.ErrUsage("<details> is empty")
			}
			if n := utf8.RuneCountInString(details); n > feedback.MaxDetails {
				return output.ErrUsage(fmt.Sprintf("<details> is %d characters; the limit is %d", n, feedback.MaxDetails))
			}

			body := map[string]string{
				"surface":  feedback.Surface,
				"category": category,
				"details":  details,
			}
			if s := strings.TrimSpace(subject); s != "" {
				body["subject"] = s
			}
			if c := strings.TrimSpace(errorCode); c != "" {
				body["error_code"] = c
			}

			// The 429 here is the person's daily report quota, not
			// throttling: it must not block every later command.
			resp, err := account.Post(resilience.WithEndpointQuota(cmd.Context()), feedback.Path, body)
			if err != nil {
				return feedbackError(store, key, err)
			}
			store.Record(key, true)

			var receipt FeedbackReceipt
			if err := json.Unmarshal(resp.Data, &receipt); err != nil {
				return fmt.Errorf("failed to parse feedback receipt: %w", err)
			}

			return app.OK(receipt,
				output.WithSummary(fmt.Sprintf("Feedback received (receipt %d)", receipt.ID)),
			)
		},
	}

	cmd.Flags().StringVar(&category, "category", "", "Kind of report: "+strings.Join(feedback.Categories, ", ")+" (required)")
	cmd.Flags().StringVar(&subject, "subject", "", "Command or tool the report is about")
	cmd.Flags().StringVar(&errorCode, "error-code", "", "Error code the command returned")
	_ = cmd.RegisterFlagCompletionFunc("category", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return feedback.Categories, cobra.ShellCompDirectiveNoFileComp
	})
	allowDash(cmd, "arg:0")

	// While the command is hidden, a malformed flag gets the same answer as
	// a well-formed invocation, so no usage error describes a feature that
	// is off.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		if c.Hidden {
			return errFeedbackNotAvailable()
		}
		return err
	})

	// While the command is hidden its help describes nothing. `feedback
	// --help` prints nothing here and fails as running it does
	// (UnavailableFeedbackHelp); `help feedback` prints the one line.
	cmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		if c.Hidden {
			if help, _ := c.Flags().GetBool("help"); !help {
				fmt.Fprintln(c.OutOrStdout(), feedback.NotAvailable)
			}
			return
		}
		if parent := c.Parent(); parent != nil {
			parent.HelpFunc()(c, args)
			return
		}
		fmt.Fprint(c.OutOrStdout(), c.Long+"\n\n"+c.UsageString())
	})

	return cmd
}

// UnavailableFeedbackHelp returns the not-available error when cmd is the
// hidden feedback command and --help was asked of it, and nil otherwise.
// Cobra answers --help itself, with success, so the caller turns that
// success into this error: help of a feature that is off fails as running
// it does.
func UnavailableFeedbackHelp(cmd *cobra.Command) error {
	if cmd == nil || cmd.Name() != "feedback" || !cmd.Hidden {
		return nil
	}
	if help, _ := cmd.Flags().GetBool("help"); help {
		return errFeedbackNotAvailable()
	}
	return nil
}

// feedbackError maps a failed submission. A 404 means the feature is off for
// this person after all, which is recorded so help stops offering it.
func feedbackError(store *feedback.Store, key string, err error) error {
	if feedback.IsNotFound(err) {
		store.Record(key, false)
		return errFeedbackNotAvailable()
	}
	var sdkErr *basecamp.Error
	if errors.As(err, &sdkErr) && sdkErr.Code == basecamp.CodeRateLimit {
		hint := "Basecamp limits how many reports a person and an account can send each day."
		if sdkErr.RetryAfter > 0 {
			hint = fmt.Sprintf("%s Try again in %d seconds.", hint, sdkErr.RetryAfter)
		}
		return &output.Error{
			Code:       output.CodeRateLimit,
			Message:    "Feedback limit reached",
			Hint:       hint,
			HTTPStatus: sdkErr.HTTPStatus,
			Retryable:  true,
			Cause:      sdkErr,
		}
	}
	return convertSDKError(err)
}

// RefreshFeedbackDetection renews the cached detection answer for the app's
// account once it is due (feedback.TTL). It runs after a command that has
// already talked to Basecamp, and uses only the credential that command
// already read (auth.WithHeldCredentialOnly): no keyring read and no token
// renewal happen for it alone. It is bounded by feedback.RefreshTimeout. A
// command that made no request adds none, and a cache directory that cannot
// be written, where no answer could be kept, gets no probe at all.
func RefreshFeedbackDetection(cmd *cobra.Command, app *appctx.App) {
	if app == nil || app.Config == nil || app.SDK == nil || app.Collector == nil {
		return
	}
	if _, set := feedback.Override(); set {
		return
	}
	if cmd.Annotations["stdout_wire"] != "" || cmd.Name() == "feedback" || changesCredentials(cmd) {
		return
	}
	if app.Collector.Summary().TotalRequests == 0 || app.RequireAccount() != nil {
		return
	}
	store := feedbackStore()
	key := feedbackStoreKey(app)
	if !store.NeedsRefresh(key) || !store.BeginRefresh(key) {
		return
	}
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(auth.WithHeldCredentialOnly(parent), feedback.RefreshTimeout)
	defer cancel()
	_, _ = store.Detect(ctx, app.Account(), key)
}

// changesCredentials reports whether cmd sits under a command that logs in,
// out or between profiles, where a request on either credential is wrong.
func changesCredentials(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "auth", "login", "logout", "profile", "setup":
			return true
		}
	}
	return false
}

// FeedbackHint writes the feedback hint to w when the failed command's error
// is one it belongs on and feedback is on for the app's account. The error
// envelope on stdout is left exactly as it was.
func FeedbackHint(w io.Writer, cmd *cobra.Command, app *appctx.App, err error) {
	if app == nil || app.Config == nil || cmd.Name() == "feedback" || !feedback.WantsHint(err) {
		return
	}
	if app.RequireAccount() != nil {
		return
	}
	if feedbackStore().Enabled(feedbackStoreKey(app)) {
		fmt.Fprintln(w, feedback.Hint)
	}
}

// feedbackStoreKey names the cached detection answer for the app's host, account
// and the credential its requests go out with.
func feedbackStoreKey(app *appctx.App) string {
	stored := ""
	if app.Auth != nil {
		stored = app.Auth.CredentialKey()
	}
	return feedback.Key(app.Config.BaseURL, app.Config.AccountID, feedback.Credential(stored))
}
