package commands

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/completion"
	"github.com/basecamp/basecamp-cli/internal/dateparse"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// MeOutput represents the output for the me command
type MeOutput struct {
	Identity basecamp.Identity `json:"identity"`
	Accounts []AccountInfo     `json:"accounts"`
}

// AccountInfo represents an account in the me command output
type AccountInfo struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Href    string `json:"href"`
	AppHref string `json:"app_href"`
	Current bool   `json:"current,omitempty"`
}

func NewMeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "me",
		Short: "Show current user profile",
		Long:  "Display information about the currently authenticated user.",
		RunE:  runMe,
	}
	return cmd
}

func runMe(cmd *cobra.Command, args []string) error {
	app := appctx.FromContext(cmd.Context())

	if !app.Auth.IsAuthenticated() {
		return output.ErrAuth("Not authenticated. Run: basecamp auth login")
	}

	endpoint, err := app.Auth.AuthorizationEndpoint(cmd.Context())
	if err != nil {
		return err
	}

	// Fetch identity and accounts using SDK
	authInfo, err := app.SDK.Authorization().GetInfo(cmd.Context(), &basecamp.GetInfoOptions{
		Endpoint:      endpoint,
		FilterProduct: "bc3",
	})
	if err != nil {
		return convertSDKError(err)
	}

	// Store user email for display purposes (non-fatal if fails).
	_ = app.Auth.SetUserEmail(authInfo.Identity.EmailAddress)

	// Build account output (already filtered to bc3 by SDK)
	var accounts []AccountInfo
	currentAccountID := app.Config.AccountID
	for _, acct := range authInfo.Accounts {
		info := AccountInfo{
			ID:      acct.ID,
			Name:    acct.Name,
			Href:    acct.HREF,
			AppHref: acct.AppHREF,
		}
		// Mark current account if configured (compare string to int64)
		if currentAccountID != "" && fmt.Sprintf("%d", acct.ID) == currentAccountID {
			info.Current = true
		}
		accounts = append(accounts, info)
	}

	result := MeOutput{
		Identity: authInfo.Identity,
		Accounts: accounts,
	}

	// Build summary
	name := authInfo.Identity.FirstName
	if authInfo.Identity.LastName != "" {
		name += " " + authInfo.Identity.LastName
	}
	summary := fmt.Sprintf("%s <%s>", name, authInfo.Identity.EmailAddress)
	if len(accounts) > 0 {
		summary += fmt.Sprintf(" - %d Basecamp account(s)", len(accounts))
	}

	// Build breadcrumbs based on configuration state
	var breadcrumbs []output.Breadcrumb
	if currentAccountID == "" && len(accounts) > 0 {
		// No account configured yet - suggest setup
		breadcrumbs = append(breadcrumbs, output.Breadcrumb{
			Action:      "setup",
			Cmd:         fmt.Sprintf("basecamp config set account_id %d", accounts[0].ID),
			Description: "Configure your Basecamp account",
		})
	} else {
		// Account configured - show next steps
		breadcrumbs = append(breadcrumbs,
			output.Breadcrumb{Action: "projects", Cmd: "basecamp projects list", Description: "List your projects"},
			output.Breadcrumb{Action: "todos", Cmd: "basecamp todos list --assignee me", Description: "Your assigned todos"},
		)
	}
	breadcrumbs = append(breadcrumbs, output.Breadcrumb{Action: "auth", Cmd: "basecamp auth status", Description: "Auth status"})

	// Opportunistically update accounts cache for tab completion.
	// Done synchronously to ensure write completes before process exits.
	updateAccountsCache(accounts, app.Config.CacheDir)

	return app.OK(result,
		output.WithSummary(summary),
		output.WithBreadcrumbs(breadcrumbs...),
	)
}

// updateAccountsCache updates the completion cache with account data.
// Runs synchronously; errors are ignored (best-effort).
func updateAccountsCache(accounts []AccountInfo, cacheDir string) {
	store := completion.NewStore(cacheDir)
	cached := make([]completion.CachedAccount, len(accounts))
	for i, a := range accounts {
		cached[i] = completion.CachedAccount{
			ID:   a.ID,
			Name: a.Name,
		}
	}
	_ = store.UpdateAccounts(cached) // Ignore errors - this is best-effort
}

func NewPeopleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "people [action]",
		Short:       "Manage people",
		Long:        "List, show, and manage people in your Basecamp account.",
		Annotations: map[string]string{"agent_notes": "--assignee me resolves to the current user's ID automatically\nPerson IDs are needed for --participants, --people, assign --to\nbasecamp people pingable lists people who can be @mentioned"},
	}

	cmd.AddCommand(newPeopleListCmd())
	cmd.AddCommand(newPeopleShowCmd())
	cmd.AddCommand(newPeopleProfileCmd())
	cmd.AddCommand(newPeoplePreferencesCmd())
	cmd.AddCommand(newPeopleOutOfOfficeCmd())
	cmd.AddCommand(newPeoplePingableCmd())
	cmd.AddCommand(newPeopleAddCmd())
	cmd.AddCommand(newPeopleRemoveCmd())

	return cmd
}

func newPeopleListCmd() *cobra.Command {
	var projectID string
	var limit, page int
	var all bool
	var sortField string
	var reverse bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List people",
		Long:  "List all people in your Basecamp account, or in a specific project.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if projectID == "" {
				projectID = appctx.FromContext(cmd.Context()).Flags.Project
			}
			return runPeopleList(cmd, projectID, limit, page, all, sortField, reverse)
		},
	}

	cmd.Flags().StringVarP(&projectID, "project", "p", "", "List people in a specific project")
	cmd.Flags().StringVar(&projectID, "in", "", "List people in a specific project (alias for --project)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of people to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all people (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")
	cmd.Flags().StringVar(&sortField, "sort", "", "Sort by field (name)")
	cmd.Flags().BoolVar(&reverse, "reverse", false, "Reverse sort order")

	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("project", completer.ProjectNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("in", completer.ProjectNameCompletion())

	return cmd
}

func runPeopleList(cmd *cobra.Command, projectID string, limit, page int, all bool, sortField string, reverse bool) error {
	app := appctx.FromContext(cmd.Context())

	// Validate flag combinations
	if all && limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if page > 0 && (all || limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
	}
	if page > 1 {
		return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
	}
	if sortField != "" {
		if err := validateSortField(sortField, []string{"name"}); err != nil {
			return err
		}
	}

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Build pagination options
	opts := &basecamp.PeopleListOptions{}
	if all {
		opts.Limit = 0 // SDK treats 0 as "fetch all"
	} else if limit > 0 {
		opts.Limit = limit
	}
	if page > 0 {
		opts.Page = page
	}

	var peopleResult *basecamp.PeopleListResult
	var err error
	if projectID != "" {
		// Resolve project name to ID if needed
		resolvedID, _, resolveErr := app.Names.ResolveProject(cmd.Context(), projectID)
		if resolveErr != nil {
			return resolveErr
		}
		bucketID, parseErr := strconv.ParseInt(resolvedID, 10, 64)
		if parseErr != nil {
			return output.ErrUsage("Invalid project ID")
		}
		peopleResult, err = app.Account().People().ListProjectPeople(cmd.Context(), bucketID, opts)
	} else {
		peopleResult, err = app.Account().People().List(cmd.Context(), opts)
	}

	if err != nil {
		return convertSDKError(err)
	}
	people := peopleResult.People

	// Opportunistic cache refresh: update completion cache as a side-effect.
	// Only cache account-wide people list without pagination, not project-specific lists.
	// Done synchronously to ensure write completes before process exits.
	if projectID == "" && page == 0 && (limit == 0 || all) {
		updatePeopleCache(people, app.Config.CacheDir)
	}

	// Sort raw people before slimming (sort functions need full SDK type)
	if sortField != "" {
		sortPeople(people, sortField, reverse)
	} else {
		sort.Slice(people, func(i, j int) bool {
			return strings.ToLower(people[i].Name) < strings.ToLower(people[j].Name)
		})
		if reverse {
			slices.Reverse(people)
		}
	}

	// Slim output
	type personListItem struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Title    string `json:"title"`
		Employee bool   `json:"employee"`
		Admin    bool   `json:"admin"`
	}
	items := make([]personListItem, len(people))
	for i, p := range people {
		items[i] = personListItem{
			ID:       p.ID,
			Name:     p.Name,
			Title:    p.Title,
			Employee: p.Employee,
			Admin:    p.Admin,
		}
	}

	summary := fmt.Sprintf("%d people", len(items))
	breadcrumbs := []output.Breadcrumb{
		{Action: "show", Cmd: "basecamp people show <id>", Description: "Show person details"},
	}

	respOpts := []output.ResponseOption{
		output.WithSummary(summary),
		output.WithBreadcrumbs(breadcrumbs...),
	}

	// Add truncation notice if results may be limited
	if notice := output.TruncationNotice(len(items), 0, all, limit); notice != "" {
		respOpts = append(respOpts, output.WithNotice(notice))
	}

	return app.OK(items, respOpts...)
}

// updatePeopleCache updates the completion cache with fresh people data.
// Runs synchronously; errors are ignored (best-effort).
func updatePeopleCache(people []basecamp.Person, cacheDir string) {
	store := completion.NewStore(cacheDir)
	cached := make([]completion.CachedPerson, len(people))
	for i, p := range people {
		cached[i] = completion.CachedPerson{
			ID:           p.ID,
			Name:         p.Name,
			EmailAddress: p.EmailAddress,
		}
	}
	_ = store.UpdatePeople(cached) // Ignore errors - this is best-effort
}

func newPeopleShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id|name>",
		Short: "Show person details",
		Long:  "Display detailed information about a specific person.",
		Args:  cobra.ExactArgs(1),
		RunE:  runPeopleShow,
	}
	return cmd
}

func runPeopleShow(cmd *cobra.Command, args []string) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// "me" can be answered directly by /my/profile.json — no need to
	// resolve to an ID and then re-fetch the same person.
	if strings.EqualFold(args[0], "me") {
		person, err := app.Account().People().Me(cmd.Context())
		if err != nil {
			return convertSDKError(err)
		}
		return app.OK(person, output.WithSummary(person.Name))
	}

	// Resolve person name/ID
	personIDStr, _, err := app.Names.ResolvePerson(cmd.Context(), args[0])
	if err != nil {
		return err
	}

	personID, err := strconv.ParseInt(personIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid person ID")
	}

	person, err := app.Account().People().Get(cmd.Context(), personID)
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(person, output.WithSummary(person.Name))
}

func newPeopleProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage your profile",
		Long:  "Show or update the current authenticated person's Basecamp profile.",
	}

	cmd.AddCommand(
		newPeopleProfileShowCmd(),
		newPeopleProfileUpdateCmd(),
	)

	return cmd
}

func newPeopleProfileShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show your profile",
		Long:  "Show the current authenticated person's Basecamp profile.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			person, err := app.Account().People().Me(cmd.Context())
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(person,
				output.WithSummary(person.Name),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         "basecamp people profile update --name <name>",
						Description: "Update your profile",
					},
					output.Breadcrumb{
						Action:      "preferences",
						Cmd:         "basecamp people preferences show",
						Description: "Show your preferences",
					},
				),
			)
		},
	}
}

func newPeopleProfileUpdateCmd() *cobra.Command {
	var name string
	var email string
	var title string
	var bio string
	var location string
	var timeZone string
	var firstWeekDay string
	var timeFormat string

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update your profile",
		Long:  "Update the current authenticated person's Basecamp profile fields.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("name") &&
				!cmd.Flags().Changed("email") &&
				!cmd.Flags().Changed("title") &&
				!cmd.Flags().Changed("bio") &&
				!cmd.Flags().Changed("location") &&
				!cmd.Flags().Changed("time-zone") &&
				!cmd.Flags().Changed("first-week-day") &&
				!cmd.Flags().Changed("time-format") {
				return noChanges(cmd)
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			req := &basecamp.UpdateMyProfileRequest{}
			if cmd.Flags().Changed("name") {
				req.Name = &name
			}
			if cmd.Flags().Changed("email") {
				req.EmailAddress = &email
			}
			if cmd.Flags().Changed("title") {
				req.Title = &title
			}
			if cmd.Flags().Changed("bio") {
				req.Bio = &bio
			}
			if cmd.Flags().Changed("location") {
				req.Location = &location
			}
			if cmd.Flags().Changed("time-zone") {
				req.TimeZoneName = &timeZone
			}
			if cmd.Flags().Changed("first-week-day") {
				day, err := parseFirstWeekDay(firstWeekDay)
				if err != nil {
					return err
				}
				req.FirstWeekDay = &day
			}
			if cmd.Flags().Changed("time-format") {
				req.TimeFormat = &timeFormat
			}

			if err := app.Account().People().UpdateMyProfile(cmd.Context(), req); err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"updated": true},
				output.WithSummary("Updated your profile"),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp people profile show",
						Description: "Show your profile",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Display name")
	cmd.Flags().StringVar(&email, "email", "", "Email address")
	cmd.Flags().StringVar(&title, "title", "", "Job title")
	cmd.Flags().StringVar(&bio, "bio", "", "Short biography")
	cmd.Flags().StringVar(&location, "location", "", "Location")
	cmd.Flags().StringVar(&timeZone, "time-zone", "", "Rails time zone name (for example America/Chicago)")
	cmd.Flags().StringVar(&firstWeekDay, "first-week-day", "", "First day of the week (Sunday through Saturday)")
	cmd.Flags().StringVar(&timeFormat, "time-format", "", "Time display format (twelve_hour or twenty_four_hour)")

	return cmd
}

func newPeoplePreferencesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preferences",
		Short: "Manage your preferences",
		Long:  "Show or update the current authenticated person's Basecamp preferences.",
	}

	cmd.AddCommand(
		newPeoplePreferencesShowCmd(),
		newPeoplePreferencesUpdateCmd(),
	)

	return cmd
}

func newPeoplePreferencesShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show your preferences",
		Long:  "Show the current authenticated person's Basecamp preferences.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			prefs, err := app.Account().People().GetMyPreferences(cmd.Context())
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(prefs,
				output.WithSummary("Your preferences"),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         "basecamp people preferences update --time-format twenty_four_hour",
						Description: "Update your preferences",
					},
				),
			)
		},
	}
}

func newPeoplePreferencesUpdateCmd() *cobra.Command {
	var firstWeekDay string
	var timeFormat string
	var timeZone string

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update your preferences",
		Long:  "Update the current authenticated person's Basecamp preferences.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("first-week-day") &&
				!cmd.Flags().Changed("time-format") &&
				!cmd.Flags().Changed("time-zone") {
				return noChanges(cmd)
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			req := &basecamp.UpdateMyPreferencesRequest{}
			if cmd.Flags().Changed("first-week-day") {
				day, err := parseFirstWeekDay(firstWeekDay)
				if err != nil {
					return err
				}
				req.FirstWeekDay = string(day)
			}
			if cmd.Flags().Changed("time-format") {
				req.TimeFormat = timeFormat
			}
			if cmd.Flags().Changed("time-zone") {
				req.TimeZoneName = timeZone
			}

			if err := app.Account().People().UpdateMyPreferences(cmd.Context(), req); err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"updated": true},
				output.WithSummary("Updated your preferences"),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp people preferences show",
						Description: "Show your preferences",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&firstWeekDay, "first-week-day", "", "First day of the week (Sunday through Saturday)")
	cmd.Flags().StringVar(&timeFormat, "time-format", "", "Time display format (twelve_hour or twenty_four_hour)")
	cmd.Flags().StringVar(&timeZone, "time-zone", "", "Rails time zone name (for example America/Chicago)")

	return cmd
}

func newPeopleOutOfOfficeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "out-of-office",
		Aliases: []string{"ooo"},
		Short:   "Manage out-of-office status",
		Long:    "Show, enable, or disable out-of-office status for yourself or another visible person.",
	}

	cmd.AddCommand(
		newPeopleOutOfOfficeShowCmd(),
		newPeopleOutOfOfficeEnableCmd(),
		newPeopleOutOfOfficeDisableCmd(),
	)

	return cmd
}

func newPeopleOutOfOfficeShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show [person]",
		Short: "Show out-of-office status",
		Long:  "Show out-of-office status for a person. Defaults to the current user.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			personID, displayName, err := resolvePersonArg(cmd, app, args)
			if err != nil {
				return err
			}

			ooo, err := app.Account().People().GetOutOfOffice(cmd.Context(), personID)
			if err != nil {
				return convertSDKError(err)
			}

			summary := fmt.Sprintf("Out of office for %s", displayName)
			if ooo.Enabled {
				summary = fmt.Sprintf("%s: %s to %s", summary, ooo.StartDate, ooo.EndDate)
			}

			return app.OK(ooo,
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "enable",
						Cmd:         "basecamp people out-of-office enable --from <date> --to <date>",
						Description: "Enable out-of-office",
					},
					output.Breadcrumb{
						Action:      "disable",
						Cmd:         "basecamp people out-of-office disable",
						Description: "Disable out-of-office",
					},
				),
			)
		},
	}
}

func newPeopleOutOfOfficeEnableCmd() *cobra.Command {
	var startDate string
	var endDate string

	cmd := &cobra.Command{
		Use:   "enable [person]",
		Short: "Enable out-of-office status",
		Long:  "Enable out-of-office status for a person. Defaults to the current user.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if startDate == "" || endDate == "" {
				return output.ErrUsage("--from and --to are required")
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			personID, displayName, err := resolvePersonArg(cmd, app, args)
			if err != nil {
				return err
			}

			ooo, err := app.Account().People().EnableOutOfOffice(cmd.Context(), personID, &basecamp.EnableOutOfOfficeRequest{
				StartDate: dateparse.Parse(startDate),
				EndDate:   dateparse.Parse(endDate),
			})
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(ooo,
				output.WithSummary(fmt.Sprintf("Enabled out-of-office for %s", displayName)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp people out-of-office show",
						Description: "Show out-of-office status",
					},
					output.Breadcrumb{
						Action:      "disable",
						Cmd:         "basecamp people out-of-office disable",
						Description: "Disable out-of-office",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&startDate, "from", "", "Start date (natural language or YYYY-MM-DD)")
	cmd.Flags().StringVar(&endDate, "to", "", "End date (natural language or YYYY-MM-DD)")

	return cmd
}

func newPeopleOutOfOfficeDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable [person]",
		Short: "Disable out-of-office status",
		Long:  "Disable out-of-office status for a person. Defaults to the current user.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			personID, displayName, err := resolvePersonArg(cmd, app, args)
			if err != nil {
				return err
			}

			if err := app.Account().People().DisableOutOfOffice(cmd.Context(), personID); err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"disabled": true},
				output.WithSummary(fmt.Sprintf("Disabled out-of-office for %s", displayName)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp people out-of-office show",
						Description: "Show out-of-office status",
					},
				),
			)
		},
	}
}

func newPeoplePingableCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pingable",
		Short: "List pingable people",
		Long:  "List people who can be @mentioned (pinged) in comments and messages.",
		RunE:  runPeoplePingable,
	}
	return cmd
}

func runPeoplePingable(cmd *cobra.Command, args []string) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	result, err := app.Account().People().Pingable(cmd.Context(), nil)
	if err != nil {
		return convertSDKError(err)
	}

	summary := fmt.Sprintf("%d pingable people", len(result.People))

	return app.OK(result.People, output.WithSummary(summary))
}

func resolvePersonArg(cmd *cobra.Command, app *appctx.App, args []string) (int64, string, error) {
	person := "me"
	if len(args) > 0 {
		person = args[0]
	}

	personIDStr, personName, err := app.Names.ResolvePerson(cmd.Context(), person)
	if err != nil {
		return 0, "", err
	}
	personID, err := strconv.ParseInt(personIDStr, 10, 64)
	if err != nil {
		return 0, "", output.ErrUsage("Invalid person ID")
	}
	if personName == "" {
		personName = personIDStr
	}
	return personID, personName, nil
}

func parseFirstWeekDay(value string) (basecamp.FirstWeekDay, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "sunday":
		return basecamp.FirstWeekDaySunday, nil
	case "monday":
		return basecamp.FirstWeekDayMonday, nil
	case "tuesday":
		return basecamp.FirstWeekDayTuesday, nil
	case "wednesday":
		return basecamp.FirstWeekDayWednesday, nil
	case "thursday":
		return basecamp.FirstWeekDayThursday, nil
	case "friday":
		return basecamp.FirstWeekDayFriday, nil
	case "saturday":
		return basecamp.FirstWeekDaySaturday, nil
	default:
		return "", output.ErrUsage("first week day must be Sunday, Monday, Tuesday, Wednesday, Thursday, Friday, or Saturday")
	}
}

func newPeopleAddCmd() *cobra.Command {
	var projectID string

	cmd := &cobra.Command{
		Use:   "add <person-id>...",
		Short: "Add people to a project",
		Long:  "Grant people access to a project.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<person-id>...")
			}
			if projectID == "" {
				projectID = appctx.FromContext(cmd.Context()).Flags.Project
			}
			if projectID == "" {
				return output.ErrUsage("--project (or --in) is required")
			}
			return runPeopleAdd(cmd, args, projectID)
		},
	}

	cmd.Flags().StringVarP(&projectID, "project", "p", "", "Project to add people to (required)")
	cmd.Flags().StringVar(&projectID, "in", "", "Project to add people to (alias for --project)")

	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("project", completer.ProjectNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("in", completer.ProjectNameCompletion())

	return cmd
}

func runPeopleAdd(cmd *cobra.Command, personIDs []string, projectID string) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Resolve project
	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	// Resolve all person IDs
	var ids []int64
	for _, pid := range personIDs {
		resolvedID, _, resolveErr := app.Names.ResolvePerson(cmd.Context(), pid)
		if resolveErr != nil {
			return resolveErr
		}
		id, parseErr := strconv.ParseInt(resolvedID, 10, 64)
		if parseErr != nil {
			return output.ErrUsage("Invalid person ID")
		}
		ids = append(ids, id)
	}

	// Build SDK request
	req := &basecamp.UpdateProjectAccessRequest{
		Grant: ids,
	}

	result, err := app.Account().People().UpdateProjectAccess(cmd.Context(), bucketID, req)
	if err != nil {
		return convertSDKError(err)
	}

	summary := fmt.Sprintf("Added %d person(s) to project #%s", len(ids), resolvedProjectID)
	breadcrumbs := []output.Breadcrumb{
		{Action: "list", Cmd: fmt.Sprintf("basecamp people list --project %s", resolvedProjectID), Description: "List project members"},
	}

	return app.OK(result,
		output.WithSummary(summary),
		output.WithBreadcrumbs(breadcrumbs...),
	)
}

func newPeopleRemoveCmd() *cobra.Command {
	var projectID string

	cmd := &cobra.Command{
		Use:   "remove <person-id>...",
		Short: "Remove people from a project",
		Long:  "Revoke people's access to a project.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<person-id>...")
			}
			if projectID == "" {
				projectID = appctx.FromContext(cmd.Context()).Flags.Project
			}
			if projectID == "" {
				return output.ErrUsage("--project (or --in) is required")
			}
			return runPeopleRemove(cmd, args, projectID)
		},
	}

	cmd.Flags().StringVarP(&projectID, "project", "p", "", "Project to remove people from (required)")
	cmd.Flags().StringVar(&projectID, "in", "", "Project to remove people from (alias for --project)")

	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("project", completer.ProjectNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("in", completer.ProjectNameCompletion())

	return cmd
}

func runPeopleRemove(cmd *cobra.Command, personIDs []string, projectID string) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Resolve project
	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	// Resolve all person IDs
	var ids []int64
	for _, pid := range personIDs {
		resolvedID, _, resolveErr := app.Names.ResolvePerson(cmd.Context(), pid)
		if resolveErr != nil {
			return resolveErr
		}
		id, parseErr := strconv.ParseInt(resolvedID, 10, 64)
		if parseErr != nil {
			return output.ErrUsage("Invalid person ID")
		}
		ids = append(ids, id)
	}

	// Build SDK request
	req := &basecamp.UpdateProjectAccessRequest{
		Revoke: ids,
	}

	result, err := app.Account().People().UpdateProjectAccess(cmd.Context(), bucketID, req)
	if err != nil {
		return convertSDKError(err)
	}

	summary := fmt.Sprintf("Removed %d person(s) from project #%s", len(ids), resolvedProjectID)
	breadcrumbs := []output.Breadcrumb{
		{Action: "list", Cmd: fmt.Sprintf("basecamp people list --project %s", resolvedProjectID), Description: "List project members"},
	}

	return app.OK(result,
		output.WithSummary(summary),
		output.WithBreadcrumbs(breadcrumbs...),
	)
}
