package commands

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// NewDoorsCmd creates the doors command for managing a project's external
// links. Basecamp's web app calls them external links; its API, URLs and
// recording type call them doors.
func NewDoorsCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:     "doors",
		Aliases: []string{"external-links"},
		Short:   "Manage a project's external links (doors)",
		Long: `Manage a project's external links: the links to outside services (a Figma
file, a GitHub repository, a Google Drive folder) shown in the project's
External links section. Basecamp's API calls them doors.

  basecamp doors list --in MyProject
  basecamp doors create --url https://www.figma.com/file/abc/Design --title "Design" --in MyProject
  basecamp doors update 12345 --title "Design system"
  basecamp doors delete 12345`,
		Annotations: map[string]string{
			"agent_notes": "Doors are external links in a project's dock (the web calls them External links).\n" +
				"list and create need --in <project>; show, update and delete take the door ID or its URL.\n" +
				"create infers --service from the URL's host (figma, github, dropbox, ...) and falls back to other; pass --service to choose.\n" +
				"A new door is private to the team unless --visible-to-clients is passed.\n" +
				"update is partial: only the flags given change.\n" +
				"delete does not use the trash: Basecamp destroys the door about five days later, and until then it can be restored in the web app.",
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID or name (alias for --project)")

	cmd.AddCommand(
		newDoorsListCmd(&project),
		newDoorsShowCmd(&project),
		newDoorsCreateCmd(&project),
		newDoorsUpdateCmd(&project),
		newDoorsDeleteCmd(&project),
	)

	return cmd
}

// doorServices are the service codes Basecamp knows for an external link, as
// its cloud-file service registry names them. They drive --service
// completion only: the server is the authority on what it accepts.
var doorServices = []string{
	"adobe_creative_cloud", "airtable", "basecamp", "box", "calendly", "customer_io",
	"dropbox", "figma", "fizzy", "github", "google_doc", "google_drive", "google_sheet",
	"google_slide", "help_scout", "highrise", "hubspot", "icloud", "invision", "jira",
	"microsoft", "miro", "notion", "one_drive", "other", "salesforce", "sketch", "slack",
	"trello", "xero", "zendesk", "zoho", "zoom",
}

// doorServiceHosts maps a host, or the domain a host ends in, to the service
// Basecamp validates that host against. Only hosts whose service is
// unambiguous are listed, and every entry is one the service's own URL
// pattern accepts, so an inferred service never makes Basecamp refuse the URL.
var doorServiceHosts = []struct {
	domain  string
	service string
}{
	{"airtable.com", "airtable"},
	{"box.com", "box"},
	{"dropbox.com", "dropbox"},
	{"figma.com", "figma"},
	{"github.com", "github"},
	{"github.io", "github"},
	{"miro.com", "miro"},
	{"notion.so", "notion"},
	{"notion.site", "notion"},
	{"notion.com", "notion"},
	{"slack.com", "slack"},
	{"trello.com", "trello"},
	{"zoom.us", "zoom"},
}

// doorGooglePaths maps docs.google.com path prefixes to their services; the
// host alone does not say which one it is.
var doorGooglePaths = []struct {
	prefix  string
	service string
}{
	{"/document/", "google_doc"},
	{"/spreadsheets/", "google_sheet"},
	{"/presentation/", "google_slide"},
}

// inferDoorService guesses the service code for an external link from its
// URL. It returns "" when nothing specific matches. Basecamp accepts a URL
// without a scheme (it prefixes https://), so one is assumed here too.
func inferDoorService(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		u, err = url.Parse("https://" + raw)
		if err != nil {
			return ""
		}
	}
	host := strings.ToLower(u.Hostname())

	switch host {
	case "drive.google.com":
		return "google_drive"
	case "docs.google.com":
		for _, p := range doorGooglePaths {
			if strings.HasPrefix(u.Path, p.prefix) {
				return p.service
			}
		}
		return ""
	}

	for _, h := range doorServiceHosts {
		if host == h.domain || strings.HasSuffix(host, "."+h.domain) {
			return h.service
		}
	}
	return ""
}

// doorIDArg reads a door ID or the URL of one door. The URL router is
// host-agnostic, so a URL must be on a trusted Basecamp host and name a door:
// a look-alike or another kind of recording never supplies an ID. Its account
// is adopted when none is configured and refused when it differs, so a
// same-numbered door in the configured account is never touched. A URL that
// names its project supplies the project for breadcrumbs when --in was not
// given.
func doorIDArg(app *appctx.App, arg, project string) (int64, string, error) {
	idStr := arg
	if urlarg.IsURL(arg) {
		if !hostutil.IsTrustedBasecampHost(arg, app.Config.BaseURL) {
			return 0, "", output.ErrUsage("refusing untrusted host in URL — expected a Basecamp URL")
		}
		parsed := urlarg.Parse(arg)
		if parsed == nil || parsed.IsCollection || parsed.Type != "doors" || parsed.RecordingID == "" {
			return 0, "", output.ErrUsageHint("The URL does not name one external link",
				"Pass the door's ID, or its URL (…/buckets/<project>/dock/doors/<id>); basecamp doors list --in <project> lists them")
		}
		if parsed.AccountID != "" {
			switch {
			case app.Config.AccountID == "":
				app.Config.AccountID = parsed.AccountID
			case !accountIDsEqual(parsed.AccountID, app.Config.AccountID):
				return 0, "", output.ErrUsage(fmt.Sprintf("URL account %s does not match the configured account %s", parsed.AccountID, app.Config.AccountID))
			}
		}
		idStr = parsed.RecordingID
		if project == "" {
			project = parsed.ProjectID
		}
	}
	doorID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, "", output.ErrUsage("Invalid door ID")
	}
	return doorID, project, nil
}

func doorSummary(door *basecamp.Door) string {
	visibility := "private to the team"
	if door.VisibleToClients {
		visibility = "visible to clients"
	}
	return fmt.Sprintf("%s → %s (%s)", door.Title, door.URL, visibility)
}

func newDoorsListCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List a project's external links",
		Long: `List a project's external links, in the order they appear in its External
links section.

  basecamp doors list --in MyProject`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			scope := newFollowUpScope(app)

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			bucketID, resolvedProjectID, err := resolveToolBucketID(cmd, app, *project)
			if err != nil {
				return err
			}

			result, err := app.Account().Doors().List(cmd.Context(), bucketID)
			if err != nil {
				return convertSDKError(err)
			}
			doors := result.Doors

			return app.OK(doors,
				output.WithSummary(fmt.Sprintf("%d external links", len(doors))),
				output.WithBreadcrumbs(doorCrumbs(scope.args(app.Config.AccountID),
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp doors show <id> --in %s", resolvedProjectID),
						Description: "View external link",
					},
					output.Breadcrumb{
						Action:      "create",
						Cmd:         fmt.Sprintf("basecamp doors create --url <url> --title \"Title\" --in %s", resolvedProjectID),
						Description: "Add external link",
					},
				)...),
			)
		},
	}
}

func newDoorsShowCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show an external link",
		Long: `Display an external link: its title, the address it points to, its service,
description and client visibility.

  basecamp doors show 12345`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			scope := newFollowUpScope(app)

			doorID, projectArg, err := doorIDArg(app, args[0], *project)
			if err != nil {
				return err
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			resolvedProjectID, err := resolveToolsProject(cmd, app, projectArg)
			if err != nil {
				return err
			}
			inFlag := toolBreadcrumbFlag(resolvedProjectID)

			door, err := app.Account().Doors().Get(cmd.Context(), doorID)
			if err != nil {
				return convertSDKError(err)
			}

			crumbs := []output.Breadcrumb{
				{
					Action:      "update",
					Cmd:         fmt.Sprintf("basecamp doors update %d --title \"New title\"%s", doorID, inFlag),
					Description: "Update external link",
				},
				{
					Action:      "delete",
					Cmd:         fmt.Sprintf("basecamp doors delete %d%s", doorID, inFlag),
					Description: "Delete external link",
				},
			}
			if resolvedProjectID != "" {
				crumbs = append(crumbs, output.Breadcrumb{
					Action:      "list",
					Cmd:         fmt.Sprintf("basecamp doors list --in %s", resolvedProjectID),
					Description: "List external links",
				})
			}

			return app.OK(door,
				output.WithSummary(doorSummary(door)),
				output.WithBreadcrumbs(doorCrumbs(scope.args(app.Config.AccountID), crumbs...)...),
			)
		},
	}
}

func newDoorsCreateCmd(project *string) *cobra.Command {
	var linkURL, title, service, description string
	var visibleToClients bool

	cmd := &cobra.Command{
		Use:   "create [title]",
		Short: "Add an external link to a project",
		Long: `Add an external link to the end of a project's External links section.

--url is required. --service is the link's service code (figma, github,
google_drive, notion, other, ...); without it the service is inferred from the
URL's host, and a host with no known service is added as other. The service
decides the icon, and Basecamp refuses a URL that does not match it.

The link is private to the team unless --visible-to-clients is passed.
--description is Markdown, converted to rich text (- reads stdin).

  basecamp doors create --url https://github.com/basecamp/basecamp-cli --title "CLI repo" --in MyProject
  basecamp doors create "Roadmap" --url https://example.com/roadmap --visible-to-clients --in MyProject`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && title == "" {
				title = args[0]
			}
			if linkURL == "" {
				return output.ErrUsageHint("--url is required",
					"basecamp doors create --url https://example.com --title \"Title\" --in <project>")
			}
			if service == "" {
				service = inferDoorService(linkURL)
				if service == "" {
					service = "other"
				}
			}

			app := appctx.FromContext(cmd.Context())
			scope := newFollowUpScope(app)

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			desc, err := resolveContentValue(cmd, description, -1, "--description")
			if err != nil {
				return err
			}
			if desc != "" {
				desc = richTextToHTML(cmd, desc)
			}

			bucketID, resolvedProjectID, err := resolveToolBucketID(cmd, app, *project)
			if err != nil {
				return err
			}

			req := &basecamp.CreateDoorRequest{
				Service:     service,
				URL:         linkURL,
				Title:       title,
				Description: desc,
			}
			if cmd.Flags().Changed("visible-to-clients") {
				req.VisibleToClients = &visibleToClients
			}

			door, err := app.Account().Doors().Create(cmd.Context(), bucketID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(door,
				output.WithSummary("Created: "+doorSummary(door)),
				output.WithBreadcrumbs(doorCrumbs(scope.args(app.Config.AccountID),
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp doors show %d --in %s", door.ID, resolvedProjectID),
						Description: "View external link",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("basecamp doors list --in %s", resolvedProjectID),
						Description: "List external links",
					},
				)...),
			)
		},
	}

	cmd.Flags().StringVar(&linkURL, "url", "", "Address the link points to (required)")
	cmd.Flags().StringVar(&title, "title", "", "Title shown in External links (Basecamp uses \"Untitled\" without one)")
	cmd.Flags().StringVar(&service, "service", "", "Service code (figma, github, notion, other, ...); inferred from the URL when omitted")
	cmd.Flags().StringVarP(&description, "description", "d", "", "Description (Markdown); use - to read from stdin")
	cmd.Flags().BoolVar(&visibleToClients, "visible-to-clients", false, "Make the link visible to clients (omit to keep it private to the team)")
	addRichTextFormatFlag(cmd)
	allowDash(cmd, "flag:description")
	registerDoorServiceCompletion(cmd)

	return cmd
}

func newDoorsUpdateCmd(project *string) *cobra.Command {
	var linkURL, title, service, description string
	var visibleToClients bool

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update an external link",
		Long: `Update an external link. Only the flags given change; everything else keeps
its value.

A new --url without --service changes the service too when the URL's host names
a known one (figma.com, github.com, ...); otherwise the service stays as it is.
--description "" clears the description. --visible-to-clients=false makes the
link private to the team.

  basecamp doors update 12345 --title "Design system"
  basecamp doors update 12345 --url https://www.figma.com/file/xyz/New
  basecamp doors update 12345 --visible-to-clients`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cmd.Flags()
			if !flags.Changed("url") && !flags.Changed("title") && !flags.Changed("service") &&
				!flags.Changed("description") && !flags.Changed("visible-to-clients") {
				return noChanges(cmd)
			}
			if flags.Changed("url") && linkURL == "" {
				return output.ErrUsage("--url cannot be empty")
			}
			if flags.Changed("title") && title == "" {
				return output.ErrUsage("--title cannot be empty")
			}
			if flags.Changed("service") && service == "" {
				return output.ErrUsage("--service cannot be empty")
			}
			if service == "" && linkURL != "" {
				service = inferDoorService(linkURL)
			}

			app := appctx.FromContext(cmd.Context())
			scope := newFollowUpScope(app)

			doorID, projectArg, err := doorIDArg(app, args[0], *project)
			if err != nil {
				return err
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if err := checkBodyUpdate(cmd, func() (time.Time, error) {
				live, err := app.Account().Doors().Get(cmd.Context(), doorID)
				if err != nil {
					return time.Time{}, err
				}
				return live.UpdatedAt, nil
			}); err != nil {
				return err
			}

			req := &basecamp.UpdateDoorRequest{
				Service: service,
				URL:     linkURL,
				Title:   title,
			}
			if flags.Changed("description") {
				desc, err := resolveContentValue(cmd, description, -1, "--description")
				if err != nil {
					return err
				}
				if desc != "" {
					desc = richTextToHTML(cmd, desc)
				}
				req.Description = &desc
			}
			if flags.Changed("visible-to-clients") {
				req.VisibleToClients = &visibleToClients
			}

			resolvedProjectID, err := resolveToolsProject(cmd, app, projectArg)
			if err != nil {
				return err
			}
			inFlag := toolBreadcrumbFlag(resolvedProjectID)

			door, err := app.Account().Doors().Update(cmd.Context(), doorID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(door,
				output.WithSummary("Updated: "+doorSummary(door)),
				output.WithBreadcrumbs(doorCrumbs(scope.args(app.Config.AccountID),
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp doors show %d%s", doorID, inFlag),
						Description: "View external link",
					},
				)...),
			)
		},
	}

	addBodyGuardFlags(cmd)
	cmd.Flags().StringVar(&linkURL, "url", "", "New address")
	cmd.Flags().StringVar(&title, "title", "", "New title")
	cmd.Flags().StringVar(&service, "service", "", "New service code (figma, github, notion, other, ...)")
	cmd.Flags().StringVarP(&description, "description", "d", "", "New description (Markdown); \"\" clears it, - reads stdin")
	cmd.Flags().BoolVar(&visibleToClients, "visible-to-clients", false, "Whether clients can see the link (=false makes it private to the team)")
	addRichTextFormatFlag(cmd)
	allowDash(cmd, "flag:description")
	registerDoorServiceCompletion(cmd)

	return cmd
}

func newDoorsDeleteCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id|url>",
		Short: "Delete an external link (not to the trash)",
		Long: `Delete an external link from its project's External links section.

The link does not go to the trash: its status becomes "deleted" and Basecamp
permanently destroys it about five days later. Until then it can be restored
in the Basecamp web app.

  basecamp doors delete 12345`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			scope := newFollowUpScope(app)

			doorID, projectArg, err := doorIDArg(app, args[0], *project)
			if err != nil {
				return err
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			resolvedProjectID, err := resolveToolsProject(cmd, app, projectArg)
			if err != nil {
				return err
			}

			if err := app.Account().Doors().Delete(cmd.Context(), doorID); err != nil {
				return convertSDKError(err)
			}

			var crumbs []output.Breadcrumb
			if resolvedProjectID != "" {
				crumbs = append(crumbs, output.Breadcrumb{
					Action:      "list",
					Cmd:         fmt.Sprintf("basecamp doors list --in %s", resolvedProjectID),
					Description: "List external links",
				})
			}

			return app.OK(map[string]any{"deleted": true, "id": doorID},
				output.WithSummary(fmt.Sprintf("Deleted external link %d (not in the trash; destroyed in about five days)", doorID)),
				output.WithBreadcrumbs(doorCrumbs(scope.args(app.Config.AccountID), crumbs...)...),
			)
		},
	}
}

// doorCrumbs appends the invocation's profile and, where it was not
// inherited, its account to each follow-up command, so a pasted command acts
// where this one did.
func doorCrumbs(scopeArgs string, crumbs ...output.Breadcrumb) []output.Breadcrumb {
	for i := range crumbs {
		crumbs[i].Cmd += scopeArgs
	}
	return crumbs
}

func registerDoorServiceCompletion(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("service",
		cobra.FixedCompletions(doorServices, cobra.ShellCompDirectiveNoFileComp))
}
