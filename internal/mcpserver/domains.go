package mcpserver

import "github.com/basecamp/mcp/catalog"

// DomainSpecs curates which slice of the basecamp-sdk surface each domain
// gateway tool exposes, in tool display order. Tags are basecamp-sdk's
// OpenAPI tags (each operation carries exactly one); a spec may merge
// several tags into one tool. This mapping is the only hand-maintained part
// of the catalog — everything else derives from the SDK model via the
// toolkit.
//
// The grouping mirrors basecamp-mcp-server's domain curation (projects,
// todos, cards, messages, campfires, schedules, files, people, account)
// where the SDK's tags allow, and follows the tags where they don't: the
// server's checkins domain lives inside the SDK's Automation tag alongside
// templates, webhooks, lineup, dock tools, and search, so it is served as
// the automation domain; the server's admin grab-bag lands across reports,
// everything, and automation. Every tag is claimed — Catalog.Unmapped is
// pinned empty by tests, so an SDK tag nobody has decided about fails the
// build.
var DomainSpecs = []catalog.DomainSpec{
	{
		Key:   "projects",
		Title: "Basecamp Projects",
		Tags:  []string{"Projects"},
		Blurb: "Basecamp projects: list, get, create, update, archive, unarchive, and trash.",
	},
	{
		Key:   "todos",
		Title: "Basecamp To-dos",
		Tags:  []string{"Todos", "Subtasks"},
		Blurb: "Todos, todolists, todolist groups, and todosets — plus each todolist's hill chart, and the subtasks on to-dos and cards.",
	},
	{
		Key:   "cards",
		Title: "Basecamp Card Tables",
		Tags:  []string{"Card Tables"},
		Blurb: "Card tables (kanban): cards, columns, steps, and wormholes, with moves, repositioning, and on-hold state.",
	},
	{
		Key:   "messages",
		Title: "Basecamp Message Boards",
		Tags:  []string{"Messages"},
		Blurb: "Message boards: messages, comments, message types (categories), and pinning.",
	},
	{
		Key:   "campfires",
		Title: "Basecamp Campfires & Pings",
		Tags:  []string{"Campfire", "Pings"},
		Blurb: "Campfire chat: rooms, chat lines, uploads, and chatbots, and Pings: private conversations, direct or group, each held in its own Campfire.",
	},
	{
		Key:   "boosts",
		Title: "Basecamp Boosts",
		Tags:  []string{"Boosts"},
		Blurb: "Boosts (emoji reactions) on recordings and their events: list, get, create, update, and delete.",
	},
	{
		Key:   "schedules",
		Title: "Basecamp Schedules",
		Tags:  []string{"Schedule", "Calendars"},
		Blurb: "Schedules and schedule entries (calendar events), timesheets and time tracking, and per-account calendars.",
	},
	{
		Key:   "files",
		Title: "Basecamp Docs & Files",
		Tags:  []string{"Files", "Folders", "Embeds"},
		Blurb: "Docs & Files: vaults (folders), documents, uploads, attachments, embeds for rich text (videos, charts), cloud file links, Google documents, and home-screen project folders.",
	},
	{
		Key:   "people",
		Title: "Basecamp People",
		Tags:  []string{"People"},
		Blurb: "People and access: profiles, pingable and assignable people, project access, out-of-office, preferences, and notification subscriptions.",
	},
	{
		Key:   "automation",
		Title: "Basecamp Automation",
		Tags:  []string{"Checkins", "Templates", "Webhooks", "Lineup", "Dock", "Recordings", "Search"},
		Blurb: "Automatic check-ins (questionnaires, questions, answers, reminders), project templates and the template library, webhooks, lineup markers, dock tools and external links (doors), recording lifecycle (archive/trash), change events, and search.",
	},
	{
		Key:   "reports",
		Title: "Basecamp Reports",
		Tags:  []string{"Reports", "Timeline"},
		Blurb: "Reports and timelines: progress, assigned and overdue todos, upcoming schedule, per-person progress, and project timelines.",
	},
	{
		Key:   "everything",
		Title: "Basecamp Everything",
		Tags:  []string{"Everything"},
		Blurb: "Account-wide feeds: every checkin, comment, file, forward, and message, and cards and todos filtered by state (open, completed, overdue, unassigned, no due date, not now).",
	},
	{
		Key:   "eventfeed",
		Title: "Basecamp Event Feed",
		Tags:  []string{"EventFeed"},
		Blurb: "The account event feed: poll events from a resumable position, and poll the agent inbox of addressed items. Pages carry a position and a continuation URL rather than page numbers; deduplicate events by id and inbox items by addressing_id, and refetch the referenced recording before acting on it. Minting a live-stream ticket is not served here — the ticket is a bearer this surface cannot use, and 'basecamp events ticket' is where it lives.",
	},
	{
		Key:   "clientside",
		Title: "Basecamp Clientside",
		Tags:  []string{"ClientFeatures"},
		Blurb: "The Clientside: client approvals, correspondences, replies, and client visibility of recordings.",
	},
	{
		Key:   "forwards",
		Title: "Basecamp Email Forwards",
		Tags:  []string{"Forwards"},
		Blurb: "Email forwards: project inboxes, forwarded emails, and their replies.",
	},
	{
		Key:   "account",
		Title: "Basecamp Account",
		Tags:  []string{"Account", "Gauges", "MyAssignments", "MyNotes", "MyNotifications", "Bookmarks", "BubbleUps", "Drafts"},
		Blurb: "Account info and your personal surface: gauges and needles, my assignments and priorities, notifications and bubble-ups, bookmarks, bubbling recordings up, personal note, and drafts.",
	},
}
