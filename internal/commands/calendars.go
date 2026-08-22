package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// calendarColors are the colors a calendar accepts.
//
// Validated client-side because the SDK cannot report what the server says: at
// v0.12.0 its error parser reads only error/error_description, so a 422 whose
// body is {"errors":{"color":[...]}} degrades to a bare "validation error" with
// no mention of the field, the value, or the alternatives. Rejecting here turns
// that into an answer the caller can act on.
var calendarColors = []string{
	"white", "red", "orange", "yellow", "green", "blue",
	"aqua", "purple", "gray", "pink", "brown",
}

// NewCalendarsCmd creates the calendars command.
//
// There is no index endpoint, so this group cannot list calendars — the way a
// caller names one is by pasting its URL, which extractID turns into the id.
func NewCalendarsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "calendars",
		Short: "View and recolor calendars",
		Long: `View and recolor calendars.

Calendars have no listing endpoint, so name one by id or by pasting its
Basecamp URL.

  basecamp calendars show 12345
  basecamp calendars show https://3.basecamp.com/1234567/calendars/12345
  basecamp calendars update 12345 --color blue`,
		Annotations: map[string]string{
			"agent_notes": "No index endpoint — there is no 'calendars list'.\n" +
				"Address a calendar by id or by pasting its URL.",
		},
	}

	cmd.AddCommand(
		newCalendarsShowCmd(),
		newCalendarsUpdateCmd(),
	)

	return cmd
}

func newCalendarsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show a calendar",
		Long: `Show a calendar's name, color, and linked schedule.

  basecamp calendars show 12345
  basecamp calendars show https://3.basecamp.com/1234567/calendars/12345`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			calendarID, err := calendarIDFromArg(args[0])
			if err != nil {
				return err
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			calendar, err := app.Account().Calendars().Get(cmd.Context(), calendarID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(calendar,
				output.WithSummary(calendarSummary(calendar)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("basecamp calendars update %d --color <color>", calendarID),
						Description: "Change the calendar color",
					},
				),
			)
		},
	}
}

func newCalendarsUpdateCmd() *cobra.Command {
	var color string

	cmd := &cobra.Command{
		Use:   "update <id|url> --color <color>",
		Short: "Change a calendar's color",
		Long: `Change a calendar's display color.

Colors: ` + strings.Join(calendarColors, ", ") + `

  basecamp calendars update 12345 --color blue`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			calendarID, err := calendarIDFromArg(args[0])
			if err != nil {
				return err
			}
			if err := validateCalendarColor(color); err != nil {
				return err
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			calendar, err := app.Account().Calendars().Update(cmd.Context(), calendarID, color)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(calendar,
				output.WithSummary(fmt.Sprintf("Calendar color set to %s", color)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp calendars show %d", calendarID),
						Description: "View the calendar",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&color, "color", "", "Calendar color ("+strings.Join(calendarColors, ", ")+")")

	return cmd
}

// validateCalendarColor rejects an unknown color before the request, naming the
// alternatives. The server would reject it too, but the SDK cannot carry its
// message back, so an unchecked value fails as a bare "validation error".
func validateCalendarColor(color string) error {
	if color == "" {
		return output.ErrUsageHint(
			"--color is required",
			"Pick one of: "+strings.Join(calendarColors, ", "),
		)
	}
	for _, valid := range calendarColors {
		if color == valid {
			return nil
		}
	}
	return output.ErrUsageHint(
		fmt.Sprintf("%q is not a valid calendar color", color),
		"Pick one of: "+strings.Join(calendarColors, ", "),
	)
}

// calendarIDFromArg resolves the <id|url> positional. With no index endpoint, a
// pasted URL is the realistic way to name a calendar.
func calendarIDFromArg(arg string) (int64, error) {
	id, err := strconv.ParseInt(extractID(arg), 10, 64)
	if err != nil {
		return 0, output.ErrUsageHint(
			fmt.Sprintf("%q is not a calendar id or Basecamp URL", arg),
			"Pass a numeric calendar id, or paste the calendar's Basecamp URL",
		)
	}
	return id, nil
}

func calendarSummary(calendar *basecamp.Calendar) string {
	if calendar == nil {
		return "Calendar"
	}
	if calendar.Name == "" {
		return fmt.Sprintf("Calendar %d (%s)", calendar.ID, calendar.Color)
	}
	return fmt.Sprintf("%s (%s)", calendar.Name, calendar.Color)
}
