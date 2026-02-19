package views

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// Hey is the activity feed view showing recently updated recordings across
// all accounts. It replaces the empty notifications stub with real data
// from Recordings().List() fan-out.
type Hey struct {
	session *workspace.Session
	store   *data.Store
	styles  *tui.Styles

	list    *widget.List
	spinner spinner.Model
	loading bool

	// Entries metadata for navigation
	entryMeta map[string]workspace.ActivityEntryInfo

	// Polling
	poller *data.Poller

	width, height int
}

const heyPollTag = "hey"

// NewHey creates the Hey! activity feed view.
func NewHey(session *workspace.Session, store *data.Store) *Hey {
	styles := session.Styles()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyText("No recent activity.")
	list.SetFocused(true)

	poller := data.NewPoller()
	poller.Add(data.PollConfig{
		Tag:        heyPollTag,
		Base:       30 * time.Second,
		Background: 2 * time.Minute,
		Max:        5 * time.Minute,
	})

	return &Hey{
		session:   session,
		store:     store,
		styles:    styles,
		list:      list,
		spinner:   s,
		loading:   true,
		entryMeta: make(map[string]workspace.ActivityEntryInfo),
		poller:    poller,
	}
}

func (v *Hey) Title() string { return "Hey!" }

func (v *Hey) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	}
}

func (v *Hey) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// StartFilter implements workspace.Filterable.
func (v *Hey) StartFilter() { v.list.StartFilter() }

// InputActive implements workspace.InputCapturer.
func (v *Hey) InputActive() bool { return v.list.Filtering() }

func (v *Hey) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.list.SetSize(w, h)
}

func (v *Hey) Init() tea.Cmd {
	return tea.Batch(v.spinner.Tick, v.fetchActivity(), v.poller.Start())
}

func (v *Hey) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case workspace.ActivityEntriesLoadedMsg:
		wasLoading := v.loading
		v.loading = false
		if msg.Err != nil {
			return v, workspace.ReportError(msg.Err, "loading activity")
		}
		hadEntries := v.list.Len() > 0
		v.syncEntries(msg.Entries)
		if hadEntries && !wasLoading {
			v.poller.RecordHit(heyPollTag)
		} else if !wasLoading {
			v.poller.RecordMiss(heyPollTag)
		}
		return v, nil

	case workspace.RefreshMsg:
		v.loading = true
		return v, tea.Batch(v.spinner.Tick, v.fetchActivity())

	case data.PollMsg:
		if msg.Tag == heyPollTag {
			return v, tea.Batch(v.fetchActivity(), v.poller.Schedule(heyPollTag))
		}

	case workspace.FocusMsg:
		v.poller.SetFocused(heyPollTag, true)

	case workspace.BlurMsg:
		v.poller.SetFocused(heyPollTag, false)

	case spinner.TickMsg:
		if v.loading {
			var cmd tea.Cmd
			v.spinner, cmd = v.spinner.Update(msg)
			return v, cmd
		}

	case tea.KeyMsg:
		if v.loading {
			return v, nil
		}
		keys := workspace.DefaultListKeyMap()
		switch {
		case key.Matches(msg, keys.Open):
			return v, v.openSelected()
		default:
			return v, v.list.Update(msg)
		}
	}
	return v, nil
}

func (v *Hey) View() string {
	if v.loading && v.list.Len() == 0 {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading activity...")
	}
	return v.list.View()
}

func (v *Hey) syncEntries(entries []workspace.ActivityEntryInfo) {
	v.entryMeta = make(map[string]workspace.ActivityEntryInfo, len(entries))
	items := make([]widget.ListItem, 0, len(entries)+4) // room for time headers

	// Group by time bucket
	now := time.Now()
	var justNow, hourAgo, today, yesterday, older []workspace.ActivityEntryInfo

	for _, e := range entries {
		age := now.Unix() - e.UpdatedAtTS
		switch {
		case age < 600: // 10 min
			justNow = append(justNow, e)
		case age < 3600: // 1 hour
			hourAgo = append(hourAgo, e)
		case age < 86400 && now.Day() == time.Unix(e.UpdatedAtTS, 0).Day():
			today = append(today, e)
		case age < 172800:
			yesterday = append(yesterday, e)
		default:
			older = append(older, e)
		}
	}

	addGroup := func(label string, group []workspace.ActivityEntryInfo) {
		if len(group) == 0 {
			return
		}
		items = append(items, widget.ListItem{Title: label, Header: true})
		for _, e := range group {
			id := fmt.Sprintf("%s:%d", e.AccountID, e.ID)
			v.entryMeta[id] = e
			desc := e.Account
			if e.Project != "" {
				desc += " > " + e.Project
			}
			items = append(items, widget.ListItem{
				ID:          id,
				Title:       e.Title,
				Description: desc,
				Extra:       e.Type,
			})
		}
	}

	addGroup("JUST NOW", justNow)
	addGroup("1 HOUR AGO", hourAgo)
	addGroup("TODAY", today)
	addGroup("YESTERDAY", yesterday)
	addGroup("OLDER", older)

	v.list.SetItems(items)
}

func (v *Hey) openSelected() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}

	meta, ok := v.entryMeta[item.ID]
	if !ok {
		return nil
	}

	if r := v.session.Recents(); r != nil {
		r.Add(recents.Item{
			ID:          item.ID,
			Title:       meta.Title,
			Description: meta.Type,
			Type:        recents.TypeRecording,
			AccountID:   meta.AccountID,
			ProjectID:   fmt.Sprintf("%d", meta.ProjectID),
		})
	}

	scope := v.session.Scope()
	scope.AccountID = meta.AccountID
	scope.ProjectID = meta.ProjectID
	scope.RecordingID = meta.ID
	scope.RecordingType = meta.Type
	return workspace.Navigate(workspace.ViewDetail, scope)
}

// fetchActivity serves cached data immediately when available, firing a
// background revalidation when the entry is stale.
func (v *Hey) fetchActivity() tea.Cmd {
	cache := v.session.MultiStore().Cache()
	entry := cache.Get("hey:activity")

	if entry != nil {
		entries, _ := entry.Value.([]workspace.ActivityEntryInfo) //nolint:errcheck
		if entry.IsFresh() {
			return func() tea.Msg {
				return workspace.ActivityEntriesLoadedMsg{Entries: entries}
			}
		}
		// Stale — serve immediately, refresh in background
		return tea.Batch(
			func() tea.Msg {
				return workspace.ActivityEntriesLoadedMsg{Entries: entries}
			},
			v.fetchActivityFromAPI(),
		)
	}

	return v.fetchActivityFromAPI()
}

// fetchActivityFromAPI fans out Recordings().List() across all accounts and
// stores the result in the response cache.
func (v *Hey) fetchActivityFromAPI() tea.Cmd {
	ms := v.session.MultiStore()
	ctx := v.session.Context()
	client := v.session.AccountClient()
	scope := v.session.Scope()
	return func() tea.Msg {
		accounts := ms.Accounts()
		if len(accounts) == 0 {
			// Fall back to single-account if discovery hasn't completed
			if scope.AccountID == "" {
				return workspace.ActivityEntriesLoadedMsg{}
			}
			return v.fetchSingleAccountActivity(ctx, client, scope)
		}

		// Fan out across all accounts, fetching recent Messages and Todos
		recordingTypes := []basecamp.RecordingType{
			basecamp.RecordingTypeMessage,
			basecamp.RecordingTypeTodo,
			basecamp.RecordingTypeDocument,
		}

		var allEntries []workspace.ActivityEntryInfo
		results := ms.FanOut(ctx, func(acct data.AccountInfo, client *basecamp.AccountClient) (any, error) {
			var entries []workspace.ActivityEntryInfo
			for _, rt := range recordingTypes {
				result, err := client.Recordings().List(ctx, rt, &basecamp.RecordingsListOptions{
					Sort:      "updated_at",
					Direction: "desc",
					Limit:     10,
					Page:      1,
				})
				if err != nil {
					continue // skip this type, try next
				}
				for _, rec := range result.Recordings {
					entries = append(entries, recordingToEntry(rec, acct))
				}
			}
			return entries, nil
		})

		for _, r := range results {
			if r.Err != nil {
				continue
			}
			if entries, ok := r.Data.([]workspace.ActivityEntryInfo); ok {
				allEntries = append(allEntries, entries...)
			}
		}

		// Sort by UpdatedAt descending
		sort.Slice(allEntries, func(i, j int) bool {
			return allEntries[i].UpdatedAtTS > allEntries[j].UpdatedAtTS
		})

		// Cap at 50 entries
		if len(allEntries) > 50 {
			allEntries = allEntries[:50]
		}

		ms.Cache().Set("hey:activity", allEntries, 30*time.Second, 5*time.Minute)
		return workspace.ActivityEntriesLoadedMsg{Entries: allEntries}
	}
}

func (v *Hey) fetchSingleAccountActivity(ctx context.Context, client *basecamp.AccountClient, scope workspace.Scope) workspace.ActivityEntriesLoadedMsg {
	var entries []workspace.ActivityEntryInfo
	for _, rt := range []basecamp.RecordingType{
		basecamp.RecordingTypeMessage,
		basecamp.RecordingTypeTodo,
	} {
		result, err := client.Recordings().List(ctx, rt, &basecamp.RecordingsListOptions{
			Sort:      "updated_at",
			Direction: "desc",
			Limit:     15,
			Page:      1,
		})
		if err != nil {
			continue
		}
		acct := data.AccountInfo{ID: scope.AccountID, Name: scope.AccountName}
		for _, rec := range result.Recordings {
			entries = append(entries, recordingToEntry(rec, acct))
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].UpdatedAtTS > entries[j].UpdatedAtTS
	})
	if len(entries) > 30 {
		entries = entries[:30]
	}

	return workspace.ActivityEntriesLoadedMsg{Entries: entries}
}

func recordingToEntry(rec basecamp.Recording, acct data.AccountInfo) workspace.ActivityEntryInfo {
	creator := ""
	if rec.Creator != nil {
		creator = rec.Creator.Name
	}
	project := ""
	var projectID int64
	if rec.Bucket != nil {
		project = rec.Bucket.Name
		projectID = rec.Bucket.ID
	}
	return workspace.ActivityEntryInfo{
		ID:          rec.ID,
		Title:       rec.Title,
		Type:        rec.Type,
		Creator:     creator,
		Account:     acct.Name,
		AccountID:   acct.ID,
		Project:     project,
		ProjectID:   projectID,
		UpdatedAt:   rec.UpdatedAt.Format("Jan 2 3:04pm"),
		UpdatedAtTS: rec.UpdatedAt.Unix(),
	}
}
