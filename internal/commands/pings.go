package commands

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
	"github.com/basecamp/basecamp-cli/internal/names"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// NewPingsCmd creates the pings command for private conversations.
func NewPingsCmd() *cobra.Command {
	var contentType string

	cmd := &cobra.Command{
		Use:     "pings",
		Aliases: []string{"ping"},
		Short:   "Talk to people directly",
		Long: `Pings are private conversations between people on an account, outside any project.

A ping with one other person is a direct ping; with two or more it is a group
ping. Each ping has one chat, and its messages are the chat's lines.

A <ping> is a ping ID, a ping URL (https://app.basecamp.com/<account>/circles/<id>),
or a person: their name, email address or person ID names your direct ping with
them. A bare number is always a ping ID, never a person ID.

Use 'basecamp pings list' to see your pings.
Use 'basecamp pings start <person>' to open one, or find the one you already have.
Use 'basecamp pings messages <ping>' and 'basecamp pings post <ping> "message"' to talk.`,
		Annotations: map[string]string{"agent_notes": "A ping appears in 'pings list' once it has a message; one just started is not listed yet, but 'pings start' prints it\n'pings start' reuses an existing direct ping (and an existing group with the same people unless --new-group), so it also opens your ping with someone\nA bare number is a ping ID; pass a person by name or email\n@mentions in a ping post resolve among the people in that ping; use --content-type text/plain to bypass\nPings are not project chats: 'chat list --all' does not show them\nIn the API a ping is a circle (/circles, bucket type Circle)"},
	}

	cmd.AddCommand(
		newPingsListCmd(),
		newPingsShowCmd(),
		newPingsStartCmd(),
		newPingsMessagesCmd(),
		newPingsPostCmd(&contentType),
		newPingsUploadCmd(),
	)

	return cmd
}

// pingView is a Ping with the id of its Campfire, which the chat commands take.
type pingView struct {
	basecamp.Ping
	CampfireID int64 `json:"campfire_id,omitempty"`
}

func newPingView(p *basecamp.Ping) pingView {
	v := pingView{Ping: *p}
	if id, ok := p.CampfireID(); ok {
		v.CampfireID = id
	}
	return v
}

func newPingViews(pings []basecamp.Ping) []pingView {
	views := make([]pingView, len(pings))
	for i := range pings {
		views[i] = newPingView(&pings[i])
	}
	return views
}

func newPingsListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your pings",
		Long: `List your active pings, most recently updated first.

A ping appears here once someone has posted in it. One you just started, with
nothing said yet, is not listed; 'basecamp pings start' prints it. Archived
pings are not listed either.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			pings, err := app.Account().Pings().List(cmd.Context())
			if err != nil {
				return pingsError(err)
			}

			views := newPingViews(pings)
			opts := []output.ResponseOption{
				output.WithSummary(fmt.Sprintf("%d pings", len(pings))),
				output.WithEntity("ping"),
			}
			if len(pings) == 0 {
				opts = append(opts,
					output.WithNotice("No pings yet. A ping appears here once it has a message."),
					output.WithBreadcrumbs(output.Breadcrumb{
						Action:      "start",
						Cmd:         "basecamp pings start <person>",
						Description: "Start a ping",
					}),
				)
			} else {
				opts = append(opts, output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "messages",
						Cmd:         fmt.Sprintf("basecamp pings messages %d", pings[0].ID),
						Description: "Read the latest ping",
					},
					output.Breadcrumb{
						Action:      "start",
						Cmd:         "basecamp pings start <person>",
						Description: "Start or open a ping",
					},
				))
			}
			return app.OK(views, opts...)
		},
	}
	return cmd
}

func newPingsShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <ping>",
		Short: "Show a ping",
		Long: `Show a ping: who is in it and its chat.

<ping> is a ping ID, a ping URL, or a person (your direct ping with them).`,
		Example: `  basecamp pings show 49282878
  basecamp pings show https://app.basecamp.com/123/circles/49282878
  basecamp pings show "Jason Fried"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if len(args) == 0 {
				return missingArg(cmd, "<ping>")
			}
			target, err := parsePingTarget(app, args[0])
			if err != nil {
				return err
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			ping, err := resolvePing(cmd.Context(), app, target, pingLookupOnly)
			if err != nil {
				return err
			}
			return showPing(app, ping)
		},
	}
	return cmd
}

func showPing(app *appctx.App, ping *basecamp.Ping) error {
	view := newPingView(ping)
	return app.OK(view,
		output.WithSummary(pingSummary(ping)),
		output.WithEntity("ping"),
		output.WithBreadcrumbs(pingNextSteps(ping)...),
	)
}

func newPingsStartCmd() *cobra.Command {
	var newGroup bool

	cmd := &cobra.Command{
		Use:     "start <person>...",
		Aliases: []string{"with"},
		Short:   "Start a ping, or open the one you have",
		Long: `Start a ping with one or more people, or open the ping you already have with them.

One person makes a direct ping, two or more a group. Starting a ping that
already exists returns it: you have one direct ping with each person, and one
group per set of people unless --new-group asks for another. So this is also
how you find your ping with someone.

People are names, email addresses or person IDs among the people you can ping
(see 'basecamp people pingable').`,
		Example: `  basecamp pings start Jason
  basecamp pings with "Jason Fried" "Jane Smith"
  basecamp pings start jane@example.com john@example.com --new-group`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if newGroup && len(args) < 2 {
				return newGroupNeedsPeople()
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			people, err := resolvePingPeople(cmd.Context(), app, args)
			if err != nil {
				return err
			}
			if newGroup && len(people) < 2 {
				return newGroupNeedsPeople()
			}
			ping, err := startPing(cmd.Context(), app, people, newGroup)
			if err != nil {
				return err
			}

			opts := []output.ResponseOption{
				output.WithSummary(pingSummary(ping)),
				output.WithEntity("ping"),
				output.WithBreadcrumbs(pingNextSteps(ping)...),
			}
			if left := peopleLeftOut(people, ping); len(left) > 0 {
				opts = append(opts, output.WithDiagnostic("Left out because they cannot be pinged: "+strings.Join(left, ", ")))
			}
			return app.OK(newPingView(ping), opts...)
		},
	}

	cmd.Flags().BoolVar(&newGroup, "new-group", false, "Start a new group even when you already have one with these people")

	return cmd
}

func newGroupNeedsPeople() error {
	return output.ErrUsageHint("--new-group needs two or more people",
		"A direct ping is always the one you have with that person")
}

func newPingsMessagesCmd() *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "messages <ping>",
		Short: "View recent messages in a ping",
		Long: `View recent messages in a ping, oldest first.

<ping> is a ping ID, a ping URL, or a person (your direct ping with them).`,
		Example: `  basecamp pings messages 49282878
  basecamp pings messages Jason --limit 50`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if len(args) == 0 {
				return missingArg(cmd, "<ping>")
			}
			target, err := parsePingTarget(app, args[0])
			if err != nil {
				return err
			}
			if limit < 0 {
				return output.ErrUsage("--limit must not be negative")
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			ping, campfireID, err := resolvePingCampfire(cmd.Context(), app, target, pingLookupOnly)
			if err != nil {
				return err
			}
			return showChatMessages(cmd, app, campfireID, limit, pingNav(ping.ID))
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 25, "Number of messages to show")

	return cmd
}

func newPingsPostCmd(contentType *string) *cobra.Command {
	var content string
	var attachFiles []string

	cmd := &cobra.Command{
		Use:   "post <ping> <message>",
		Short: "Post a message to a ping",
		Long: `Post a message to a ping.

<ping> is a ping ID, a ping URL, or a person. A person names your direct ping
with them, which is started if you do not have one yet.

By default, messages are sent as plain text. Use --content-type text/html
for rich text (HTML) messages.

@mentions (@Name or @First.Last) resolve among the people in the ping and the
content type is promoted to text/html when mentions are present.

Use - as the message argument to read the message from stdin:
  printf 'Build is green' | basecamp pings post Jason -`,
		Example: `  basecamp pings post 49282878 "Lunch?"
  basecamp pings post Jason "Lunch?"`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if len(args) == 0 {
				return missingArg(cmd, "<ping>")
			}
			target, err := parsePingTarget(app, args[0])
			if err != nil {
				return err
			}

			messageContent, err := readPostInput(cmd, args[1:], 1, content, *contentType, attachFiles, func() error { return nil })
			if err != nil || (messageContent == "" && len(attachFiles) == 0) {
				return err
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			ping, campfireID, err := resolvePingCampfire(cmd.Context(), app, target, pingStartIfMissing)
			if err != nil {
				return err
			}
			return postToChat(cmd, app, campfireID, messageContent, *contentType, attachFiles, pingMentionResolver(ping), pingNav(ping.ID))
		},
	}

	cmd.Flags().StringVar(&content, "content", "", "Message content; use - to read from stdin")
	cmd.Flags().StringVar(contentType, "content-type", "", "Content type (text/html for rich text)")
	cmd.Flags().StringArrayVar(&attachFiles, "attach", nil, "Attach file (repeatable)")

	allowDash(cmd, "arg:1", "flag:content")

	return cmd
}

func newPingsUploadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upload <ping> <file>",
		Short: "Upload a file to a ping",
		Long: `Upload a file to a ping as a message with an attachment.

<ping> is a ping ID, a ping URL, or a person. A person names your direct ping
with them, which is started if you do not have one yet.`,
		Example: `  basecamp pings upload 49282878 ./screenshot.png`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			target, err := parsePingTarget(app, args[0])
			if err != nil {
				return err
			}
			filePath := richtext.NormalizeDragPath(args[1])
			if err := richtext.ValidateFile(filePath); err != nil {
				return fmt.Errorf("%s: %w", filePath, err)
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			ping, campfireID, err := resolvePingCampfire(cmd.Context(), app, target, pingStartIfMissing)
			if err != nil {
				return err
			}
			return uploadToChat(cmd, app, campfireID, filePath, pingNav(ping.ID))
		},
	}
	return cmd
}

// pingNav continues a Ping through `pings`.
func pingNav(pingID int64) chatNav {
	return chatNav{
		messages: func(limit int) string {
			if limit > 0 {
				return fmt.Sprintf("basecamp pings messages %d --limit %d", pingID, limit)
			}
			return fmt.Sprintf("basecamp pings messages %d", pingID)
		},
		post: func(text string) string {
			return fmt.Sprintf("basecamp pings post %d %q", pingID, text)
		},
		unresolved: func(names []string) string {
			if len(names) == 0 {
				return ""
			}
			return "Unresolved mentions left as text: " + strings.Join(names, ", ") +
				" (only people in this ping can be mentioned; [@Name](person:ID) names one by ID)"
		},
	}
}

func pingNextSteps(ping *basecamp.Ping) []output.Breadcrumb {
	nav := pingNav(ping.ID)
	return []output.Breadcrumb{
		{Action: "messages", Cmd: nav.messages(0), Description: "View messages"},
		{Action: "post", Cmd: nav.post("message"), Description: "Post message"},
	}
}

func pingSummary(ping *basecamp.Ping) string {
	return fmt.Sprintf("Ping #%d: %s", ping.ID, pingTitle(ping))
}

func pingTitle(ping *basecamp.Ping) string {
	if ping.Title != "" {
		return ping.Title
	}
	return "Ping"
}

// pingsError turns the API's refusals of a Ping request into the CLI's
// vocabulary: Pings turned off for the company, and a Ping Basecamp would not
// start.
func pingsError(err error) error {
	converted := convertSDKError(err)
	var e *output.Error
	if !errors.As(converted, &e) {
		return converted
	}
	switch e.HTTPStatus {
	case 403:
		if strings.Contains(strings.ToLower(e.Message), "pings have been disabled") {
			e.Hint = "Your company has turned Pings off, so no ping command works for you"
		}
	}
	return e
}

type pingLookup int

const (
	// pingLookupOnly reads a Ping that exists: a person names a direct ping
	// the caller already has, never one started for the occasion.
	pingLookupOnly pingLookup = iota
	// pingStartIfMissing starts the direct ping a person names when there is
	// none, for commands that write to it.
	pingStartIfMissing
)

// pingTarget is what a <ping> argument names: a Ping by id, or the caller's
// direct Ping with a person.
type pingTarget struct {
	id     int64
	person string
	// accountID is the account a URL named, to hold against the resolved one.
	accountID string
}

// parsePingTarget reads a <ping> argument without touching the network. A URL
// must be on a Basecamp host and in the configured account; a bare number is a
// Ping id; anything else names a person.
func parsePingTarget(app *appctx.App, arg string) (pingTarget, error) {
	if urlarg.IsURL(arg) {
		if !hostutil.IsTrustedBasecampHost(arg, app.Config.BaseURL) {
			return pingTarget{}, output.ErrUsage("refusing untrusted host in URL — expected a Basecamp URL")
		}
		parsed := urlarg.Parse(arg)
		if parsed == nil || parsed.PingID == "" {
			return pingTarget{}, output.ErrUsageHint("expected a ping URL",
				"Ping URLs look like https://app.basecamp.com/<account>/circles/<ping>")
		}
		if parsed.AccountID != "" && app.Config.AccountID != "" && parsed.AccountID != app.Config.AccountID {
			return pingTarget{}, output.ErrUsage(fmt.Sprintf("URL account %s does not match the configured account %s", parsed.AccountID, app.Config.AccountID))
		}
		id, err := strconv.ParseInt(parsed.PingID, 10, 64)
		if err != nil {
			return pingTarget{}, output.ErrUsage("Invalid ping ID")
		}
		return pingTarget{id: id, accountID: parsed.AccountID}, nil
	}
	if isNumeric(arg) {
		id, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			return pingTarget{}, output.ErrUsage("Invalid ping ID")
		}
		return pingTarget{id: id}, nil
	}
	if strings.TrimSpace(arg) == "" {
		return pingTarget{}, output.ErrUsage("<ping> must not be empty")
	}
	return pingTarget{person: arg}, nil
}

// resolvePing finds the Ping a target names.
func resolvePing(ctx context.Context, app *appctx.App, target pingTarget, mode pingLookup) (*basecamp.Ping, error) {
	if target.accountID != "" && target.accountID != app.Config.AccountID {
		return nil, output.ErrUsage(fmt.Sprintf("URL account %s does not match the configured account %s", target.accountID, app.Config.AccountID))
	}
	if target.id != 0 {
		ping, err := app.Account().Pings().Get(ctx, target.id)
		if err != nil {
			return nil, pingsGetError(err, target.id)
		}
		return ping, nil
	}

	people, err := resolvePingPeople(ctx, app, []string{target.person})
	if err != nil {
		return nil, err
	}
	if mode == pingStartIfMissing {
		return startPing(ctx, app, people, false)
	}

	pings, err := app.Account().Pings().List(ctx)
	if err != nil {
		return nil, pingsError(err)
	}
	for i := range pings {
		if isDirectPingWith(&pings[i], people[0].ID) {
			return &pings[i], nil
		}
	}
	return nil, output.ErrNotFoundHint("Ping", target.person,
		fmt.Sprintf("No ping with %s has a message yet. Start one: basecamp pings start %s", people[0].Name, richtext.ShellQuote(target.person)))
}

func isDirectPingWith(ping *basecamp.Ping, personID int64) bool {
	return !ping.Named && len(ping.People) == 1 && ping.People[0].ID == personID
}

// resolvePingCampfire finds the Ping a target names and its Campfire.
func resolvePingCampfire(ctx context.Context, app *appctx.App, target pingTarget, mode pingLookup) (*basecamp.Ping, int64, error) {
	ping, err := resolvePing(ctx, app, target, mode)
	if err != nil {
		return nil, 0, err
	}
	campfireID, ok := ping.CampfireID()
	if !ok {
		return nil, 0, output.ErrAPI(0, fmt.Sprintf("ping #%d did not name its chat", ping.ID))
	}
	return ping, campfireID, nil
}

func pingsGetError(err error, id int64) error {
	converted := pingsError(err)
	var e *output.Error
	if errors.As(converted, &e) && e.HTTPStatus == 404 {
		return output.ErrNotFoundHint("Ping", strconv.FormatInt(id, 10),
			"A bare number is a ping ID, not a person ID; list yours with: basecamp pings list")
	}
	return converted
}

// resolvePingPeople resolves the people to ping from their names, email
// addresses or ids among the people the caller can ping, once each.
func resolvePingPeople(ctx context.Context, app *appctx.App, args []string) ([]names.Person, error) {
	var people []names.Person
	for _, arg := range args {
		if strings.EqualFold(strings.TrimSpace(arg), "me") {
			return nil, output.ErrUsageHint("You are always in your own pings", "Name the people to ping with you")
		}
		person, err := app.Names.ResolvePingable(ctx, arg)
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(people, func(p names.Person) bool { return p.ID == person.ID }) {
			people = append(people, *person)
		}
	}
	return people, nil
}

// startPing opens the Ping with people: the existing one when there is one.
func startPing(ctx context.Context, app *appctx.App, people []names.Person, newGroup bool) (*basecamp.Ping, error) {
	ids := make([]int64, len(people))
	for i, p := range people {
		ids[i] = p.ID
	}
	ping, err := app.Account().Pings().Create(ctx, &basecamp.CreatePingRequest{UserIDs: ids, NewGroup: newGroup})
	if err != nil {
		return nil, pingsStartError(err)
	}
	if len(peopleLeftOut(people, ping)) == len(people) {
		return nil, output.ErrUsageHint("None of those people can be pinged",
			"See who you can ping: basecamp people pingable")
	}
	return ping, nil
}

func pingsStartError(err error) error {
	converted := pingsError(err)
	var e *output.Error
	if errors.As(converted, &e) && e.HTTPStatus == 422 {
		return &output.Error{
			Code:       e.Code,
			Message:    "Basecamp would not start this ping",
			Hint:       "An agent can only start a ping that includes someone who can use Pings",
			HTTPStatus: e.HTTPStatus,
			Cause:      e.Cause,
		}
	}
	return converted
}

// peopleLeftOut names the requested people the Ping does not hold. Basecamp
// drops people the caller cannot reach rather than refusing them.
func peopleLeftOut(requested []names.Person, ping *basecamp.Ping) []string {
	var left []string
	for _, p := range requested {
		if !slices.ContainsFunc(ping.People, func(m basecamp.Person) bool { return m.ID == p.ID }) {
			left = append(left, p.Name)
		}
	}
	return left
}

// pingMentionResolver resolves @mentions among the people in the Ping: only
// they can read what is said there.
func pingMentionResolver(ping *basecamp.Ping) mentionResolver {
	people := make([]names.Person, len(ping.People))
	for i, p := range ping.People {
		people[i] = names.Person{
			ID:             p.ID,
			AttachableSGID: p.AttachableSGID,
			Name:           p.Name,
			Email:          p.EmailAddress,
			PersonableType: p.PersonableType,
		}
	}
	return func(_ context.Context, html string) (richtext.MentionResult, error) {
		return resolveMentionsWith(html,
			func(name string) (*names.Person, error) { return names.ResolveAmong(name, people) },
			func(id int64) (*names.Person, error) {
				for i := range people {
					if people[i].ID == id {
						return &people[i], nil
					}
				}
				return nil, output.ErrNotFoundHint("Person", strconv.FormatInt(id, 10), "Not in this ping")
			},
		)
	}
}
