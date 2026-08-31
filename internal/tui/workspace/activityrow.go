package workspace

import (
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// gutterWidth is the column down the left that the times sit in, right-aligned
// so the numbers line up rather than running ragged against the text. Wide
// enough for the longest thing that goes there: a count of weeks.
const gutterWidth = 9

// activity is one entry of the timeline: who did what, where, and when. The home
// screen's feed and the Latest Activity screen both read it, so there is one of
// these rather than one per screen.
type activity struct {
	who   string
	what  string
	where string

	// at is local time. The API answers in UTC, and both which day an event
	// belongs to and how long ago it was are questions about the reader's clock,
	// not the server's.
	//
	// The instant is kept rather than pre-formatted: how long ago something was
	// is a different answer every minute, and a screen left open all afternoon
	// would go on saying "3m ago".
	at time.Time
}

// matches reports whether a quick-find needle is anywhere in the entry. The
// project and the person are searched along with the sentence: "what did anyone
// do in Ops" is the same question as "what did Jorge do".
func (a activity) matches(needle string) bool {
	if needle == "" {
		return true
	}
	needle = strings.ToLower(needle)
	for _, field := range []string{a.what, a.who, a.where} {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}

// activityRows is one entry of a feed: what happened, then where and who, with
// the time down the left.
//
// The relative time is what the web shows and what a reader actually wants; the
// clock time under it is what a terminal has instead of a tooltip. Both feeds
// draw with this, so an event reads the same on the home screen as it does on
// the screen the home screen's button leads to.
func activityRows(styles *tui.Styles, entry activity, now time.Time, width int, selected bool) []string {
	theme := styles.Theme()

	marker := "  "
	what := lipgloss.NewStyle().Foreground(theme.Foreground)
	if selected {
		marker = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).Render("› ")
		what = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
	}

	inner := max(width-2-gutterWidth-1, 1)
	where := strings.Join(nonEmpty(entry.where, entry.who), " · ")

	return []string{
		marker + styles.Muted.Render(gutter(since(entry.at, now))) + " " +
			what.Render(truncateToWidth(entry.what, inner)),
		"  " + styles.Muted.Render(gutter(clockOf(entry.at))) + " " +
			styles.Muted.Render(truncateToWidth(where, inner)),
	}
}

// gutter right-aligns a time in the left-hand column.
func gutter(text string) string {
	pad := gutterWidth - tui.DisplayWidth(text)
	if pad <= 0 {
		return truncateToWidth(text, gutterWidth)
	}
	return strings.Repeat(" ", pad) + text
}

// --- Time ---

// sameDay is whether two instants fall on the same local day. Both sides are
// moved to local time first: an event at 01:00 UTC belongs to the day the reader
// was living in, not the one Greenwich was.
func sameDay(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return a.IsZero() && b.IsZero()
	}
	a, b = a.Local(), b.Local()
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

// dayLabel words a day the way the web's separator does — TODAY, MONDAY, AUGUST
// 31 — dropping the year while it is the current one.
func dayLabel(at, now time.Time) string {
	if at.IsZero() {
		return "SOMETIME"
	}
	at, now = at.Local(), now.Local()

	date := at.Format("Monday, January 2")
	if at.Year() != now.Year() {
		date = at.Format("Monday, January 2, 2006")
	}

	switch {
	case sameDay(at, now):
		date = "Today, " + date
	case sameDay(at, now.AddDate(0, 0, -1)):
		date = "Yesterday, " + date
	}
	return strings.ToUpper(date)
}

// since is how long ago an instant was, in the shortest words that still say it:
// 3m, 4h, 2d. Anything the same minute is "now".
func since(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}

	elapsed := now.Sub(at)
	switch {
	case elapsed < time.Minute:
		return "now"
	case elapsed < time.Hour:
		return plural(int(elapsed.Minutes()), "m") + " ago"
	case elapsed < 24*time.Hour:
		return plural(int(elapsed.Hours()), "h") + " ago"
	case elapsed < 7*24*time.Hour:
		return plural(int(elapsed.Hours()/24), "d") + " ago"
	default:
		return plural(int(elapsed.Hours()/(24*7)), "w") + " ago"
	}
}

func plural(count int, unit string) string {
	return strconv.Itoa(count) + unit
}

// clockOf is the time of day an event happened, which is the rest of the
// timestamp the day heading started.
func clockOf(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Local().Format("15:04")
}

// --- Reading ---

func toActivity(event basecamp.TimelineEvent) activity {
	who := ""
	if event.Creator != nil {
		who = event.Creator.Name
	}
	where := ""
	if event.Bucket != nil {
		where = event.Bucket.Name
	}
	at := time.Time{}
	if event.CreatedAt != nil {
		at = event.CreatedAt.Local()
	}

	// Basecamp words the event itself, and words it with the actor's name in it
	// — "Jorge M. commented on …". Putting the creator in front of that says it
	// twice, so who goes quietly beside the project instead.
	what := strings.TrimSpace(event.Title)
	if what == "" {
		what = strings.TrimSpace(event.Action)
	}
	if what == "" {
		what = event.Kind
	}

	return activity{
		who:   richtext.SanitizeSingleLine(who),
		what:  richtext.SanitizeSingleLine(what),
		where: richtext.SanitizeSingleLine(where),
		at:    at,
	}
}
