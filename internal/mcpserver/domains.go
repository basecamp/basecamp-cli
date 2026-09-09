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
		Tags:  []string{"Projects"},
		Blurb: "Basecamp projects: list, get, create, update, archive, unarchive, and trash.",
	},
	{
		Key:   "todos",
		Tags:  []string{"Todos"},
		Blurb: "Todos, todolists, todolist groups, and todosets — plus each todolist's hill chart.",
	},
	{
		Key:   "cards",
		Tags:  []string{"Card Tables"},
		Blurb: "Card tables (kanban): cards, columns, steps, and wormholes, with moves, repositioning, and on-hold state.",
	},
	{
		Key:   "messages",
		Tags:  []string{"Messages"},
		Blurb: "Message boards: messages, comments, message types (categories), and pinning.",
	},
	{
		Key:   "campfires",
		Tags:  []string{"Campfire"},
		Blurb: "Campfire chat: rooms, chat lines, uploads, and chatbots.",
	},
	{
		Key:   "boosts",
		Tags:  []string{"Boosts"},
		Blurb: "Boosts (emoji reactions) on recordings and their events: list, get, create, and delete.",
	},
	{
		Key:   "schedules",
		Tags:  []string{"Schedule", "Calendars"},
		Blurb: "Schedules and schedule entries (calendar events), timesheets and time tracking, and per-account calendars.",
	},
	{
		Key:   "files",
		Tags:  []string{"Files", "Folders"},
		Blurb: "Docs & Files: vaults (folders), documents, uploads, attachments, cloud file links, Google documents, and home-screen project folders.",
	},
	{
		Key:   "people",
		Tags:  []string{"People"},
		Blurb: "People and access: profiles, pingable people, project access, out-of-office, preferences, and notification subscriptions.",
	},
	{
		Key:   "automation",
		Tags:  []string{"Automation"},
		Blurb: "Automatic check-ins (questionnaires, questions, answers, reminders), project templates, webhooks, lineup markers, dock tools, recording lifecycle (archive/trash), change events, and search.",
	},
	{
		Key:   "reports",
		Tags:  []string{"Reports"},
		Blurb: "Reports and timelines: progress, assigned and overdue todos, upcoming schedule, per-person progress, and project timelines.",
	},
	{
		Key:   "everything",
		Tags:  []string{"Everything"},
		Blurb: "Account-wide feeds: every checkin, comment, file, forward, and message, and cards and todos filtered by state (open, completed, overdue, unassigned, no due date, not now).",
	},
	{
		Key:   "clientside",
		Tags:  []string{"ClientFeatures"},
		Blurb: "The Clientside: client approvals, correspondences, replies, and client visibility of recordings.",
	},
	{
		Key:   "forwards",
		Tags:  []string{"Forwards"},
		Blurb: "Email forwards: project inboxes, forwarded emails, and their replies.",
	},
	{
		Key:   "account",
		Tags:  []string{"Account", "Gauges", "MyAssignments", "MyNotes", "MyNotifications", "Bookmarks", "BubbleUps", "Drafts"},
		Blurb: "Account info and your personal surface: gauges and needles, my assignments and priorities, notifications and bubble-ups, bookmarks, bubbling recordings up, personal note, and drafts.",
	},
}
