package views

import (
	"fmt"
	"time"

	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// syncTimelineEntries builds time-bucketed list items from timeline events.
// Returns a fresh entryMeta map — caller replaces their map (not merge).
// Pass accounts=nil to suppress account badges (project-scoped views).
func syncTimelineEntries(
	entries []workspace.TimelineEventInfo,
	list *widget.List,
	accounts []data.AccountInfo,
) map[string]workspace.TimelineEventInfo {
	entryMeta := make(map[string]workspace.TimelineEventInfo, len(entries))
	items := make([]widget.ListItem, 0, len(entries)+4) // room for time headers

	// Group by time bucket
	now := time.Now()
	var justNow, hourAgo, today, yesterday, older []workspace.TimelineEventInfo

	for _, e := range entries {
		age := now.Unix() - e.CreatedAtTS
		switch {
		case age < 600: // 10 min
			justNow = append(justNow, e)
		case age < 3600: // 1 hour
			hourAgo = append(hourAgo, e)
		case age < 86400 && now.Day() == time.Unix(e.CreatedAtTS, 0).Day():
			today = append(today, e)
		case age < 172800:
			yesterday = append(yesterday, e)
		default:
			older = append(older, e)
		}
	}

	addGroup := func(label string, group []workspace.TimelineEventInfo) {
		if len(group) == 0 {
			return
		}
		items = append(items, widget.ListItem{Title: label, Header: true})
		for _, e := range group {
			// Key by account+event ID (globally unique) — NOT recording ID,
			// since multiple events can reference the same recording and
			// the same recording ID can appear across accounts.
			id := e.AccountID + ":" + fmt.Sprintf("%d", e.ID)
			entryMeta[id] = e

			// Title: "Action Target: Title" e.g. "completed Todo: Ship feature"
			title := e.Action + " " + e.Target
			if e.Title != "" {
				title += ": " + e.Title
			}

			// Description: "Creator · Project · Time"
			desc := e.Creator
			if e.Project != "" {
				desc += " · " + e.Project
			}
			desc += " · " + e.CreatedAt

			extra := ""
			if e.SummaryExcerpt != "" {
				extra = e.SummaryExcerpt
				if len(extra) > 50 {
					extra = extra[:47] + "..."
				}
			}

			items = append(items, widget.ListItem{
				ID:          id,
				Title:       title,
				Description: desc,
				Extra:       accountExtra(accounts, e.AccountID, extra),
			})
		}
	}

	addGroup("Just Now", justNow)
	addGroup("1 Hour Ago", hourAgo)
	addGroup("Today", today)
	addGroup("Yesterday", yesterday)
	addGroup("Older", older)

	list.SetItems(items)
	return entryMeta
}
