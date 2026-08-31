package workspace

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// The web's own cap, from My::Sidebar::ReadingsController: the sidebar shows two
// bubble-ups and offers the rest behind a "view more".
const sidebarBubbleUps = 2

// reading is one row of the sidebar — a notification, flattened to what a column
// this narrow can draw. The timestamp arrives already worded so rendering stays
// a pure function of what was read.
type reading struct {
	title   string
	excerpt string
	who     string
	where   string
	when    string
	unread  int
	ping    bool
}

// readings is what the sidebar shows, in the three groups the web shows them in.
type readings struct {
	bubbleUps     []reading
	unreads       []reading
	reads         []reading
	moreBubbleUps int
}

// pings reports whether any of the unread items was aimed at the reader rather
// than merely landing near them.
func (r readings) pings() bool {
	for _, item := range r.unreads {
		if item.ping {
			return true
		}
	}
	return false
}

// readingsLoadedMsg is the answer to a read of the notifications.
type readingsLoadedMsg struct {
	readings readings
	err      error
}

// loadReadings reads the notifications the sidebar shows. It asks for the same
// capped bubble-up list the web sidebar asks for, so the count behind "N more"
// is the server's rather than something counted here.
func loadReadings(ctx context.Context, app *appctx.App, now time.Time) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return readingsLoadedMsg{err: err}
		}

		result, err := app.Account().MyNotifications().
			GetWithOptions(ctx, 0, basecamp.WithLimitBubbleUps())
		if err != nil {
			return readingsLoadedMsg{err: err}
		}

		bubbleUps := toReadings(result.BubbleUps, now)
		if len(bubbleUps) > sidebarBubbleUps {
			bubbleUps = bubbleUps[:sidebarBubbleUps]
		}
		return readingsLoadedMsg{readings: readings{
			bubbleUps:     bubbleUps,
			unreads:       toReadings(result.Unreads, now),
			reads:         toReadings(result.Reads, now),
			moreBubbleUps: max(int(result.BubbleUpsCount)-len(bubbleUps), 0),
		}}
	}
}

func toReadings(notifications []basecamp.Notification, now time.Time) []reading {
	rows := make([]reading, 0, len(notifications))
	for _, n := range notifications {
		rows = append(rows, toReading(n, now))
	}
	return rows
}

func toReading(n basecamp.Notification, now time.Time) reading {
	who := ""
	if n.Creator != nil {
		who = n.Creator.Name
	}
	return reading{
		title:   richtext.SanitizeSingleLine(n.Title),
		excerpt: richtext.SanitizeSingleLine(n.ContentExcerpt),
		who:     richtext.SanitizeSingleLine(who),
		where:   richtext.SanitizeSingleLine(n.BucketName),
		when:    stamp(readingTime(n), now),
		unread:  int(n.UnreadCount),
		ping:    n.Section == pingsSection,
	}
}

// pingsSection is the section a reading carries when it was addressed to the
// reader. Basecamp's own enum: inbox, chats, pings, bubbles, mentions.
const pingsSection = "pings"

// readingTime is when the row happened, by whichever of the transition times the
// notification carries — the same field each list is ordered by.
func readingTime(n basecamp.Notification) time.Time {
	switch {
	case n.UnreadAt != nil:
		return *n.UnreadAt
	case n.ReadAt != nil:
		return *n.ReadAt
	default:
		return n.CreatedAt
	}
}

// stamp words a time the way the web sidebar does: the clock for something from
// today, the date for anything older.
func stamp(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	at = at.Local()
	if now.Local().YearDay() == at.YearDay() && now.Local().Year() == at.Year() {
		return at.Format("15:04")
	}
	return at.Format("Jan 2")
}
