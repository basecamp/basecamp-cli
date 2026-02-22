package data

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// Hub is the central data coordinator providing typed, realm-scoped pool access.
//
// Hub manages three realm tiers:
//   - Global: app lifetime (identity, account list)
//   - Account: active account session (projects, people)
//   - Project: active project context (schedule, campfire, messages, etc.)
//
// Typed pool accessors return realm-scoped pools whose lifecycle is automatic:
// project pools are torn down on LeaveProject/EnsureProject(different),
// account pools on SwitchAccount, global pools on Shutdown.
type Hub struct {
	mu        sync.RWMutex
	global    *Realm
	account   *Realm // nil when no account selected
	project   *Realm // nil when not in a project
	accountID string // tracks which account the realm belongs to
	projectID int64  // tracks which project the realm belongs to
	multi     *MultiStore
	poller    *Poller
	metrics   *PoolMetrics
}

// NewHub creates a Hub with a global realm and the given dependencies.
func NewHub(multi *MultiStore, poller *Poller) *Hub {
	return &Hub{
		global:  NewRealm("global", context.Background()),
		multi:   multi,
		poller:  poller,
		metrics: NewPoolMetrics(),
	}
}

// Metrics returns the pool metrics collector.
func (h *Hub) Metrics() *PoolMetrics { return h.metrics }

// Global returns the app-lifetime realm.
func (h *Hub) Global() *Realm {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.global
}

// Account returns the active account realm, or nil.
func (h *Hub) Account() *Realm {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.account
}

// Project returns the active project realm, or nil.
func (h *Hub) Project() *Realm {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.project
}

// MultiStore returns the cross-account SDK access layer.
func (h *Hub) MultiStore() *MultiStore { return h.multi }

// Poller returns the shared polling coordinator.
func (h *Hub) Poller() *Poller { return h.poller }

// EnsureAccount returns the account realm, creating one if needed.
// If called with a different accountID than the current realm, the old
// realm is torn down (along with any project realm) and a fresh one created.
func (h *Hub) EnsureAccount(accountID string) *Realm {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.account != nil && h.accountID == accountID {
		return h.account
	}
	// Different ID (or first call) — teardown old realms and create fresh.
	if h.project != nil {
		h.project.Teardown()
		h.project = nil
		h.projectID = 0
	}
	if h.account != nil {
		h.account.Teardown()
	}
	h.accountID = accountID
	h.account = NewRealm("account:"+accountID, h.global.Context())
	return h.account
}

// SwitchAccount tears down the project and account realms, then creates
// a fresh account realm. Replaces the store.Clear() + router.Reset()
// sledgehammer with targeted realm teardown.
func (h *Hub) SwitchAccount(accountID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.project != nil {
		h.project.Teardown()
		h.project = nil
		h.projectID = 0
	}
	if h.account != nil {
		h.account.Teardown()
	}
	h.accountID = accountID
	h.account = NewRealm("account:"+accountID, h.global.Context())
}

// EnsureProject returns the project realm, creating one if needed.
// If called with a different projectID than the current realm, the old
// realm is torn down and a fresh one created.
func (h *Hub) EnsureProject(projectID int64) *Realm {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.project != nil && h.projectID == projectID {
		return h.project
	}
	if h.project != nil {
		h.project.Teardown()
	}
	h.projectID = projectID
	parent := h.global.Context()
	if h.account != nil {
		parent = h.account.Context()
	}
	h.project = NewRealm(fmt.Sprintf("project:%d", projectID), parent)
	return h.project
}

// LeaveProject tears down the project realm.
func (h *Hub) LeaveProject() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.project != nil {
		h.project.Teardown()
		h.project = nil
		h.projectID = 0
	}
}

// Shutdown tears down all realms. Call on program exit.
func (h *Hub) Shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.project != nil {
		h.project.Teardown()
		h.project = nil
		h.projectID = 0
	}
	if h.account != nil {
		h.account.Teardown()
		h.account = nil
		h.accountID = ""
	}
	h.global.Teardown()
}

// -- Context helpers

// ProjectContext returns the project realm's context, or the account/global
// context as fallback. Views should pass this to pool Fetch calls for
// project-scoped data so that LeaveProject cancels in-flight fetches.
func (h *Hub) ProjectContext() context.Context {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.project != nil {
		return h.project.Context()
	}
	if h.account != nil {
		return h.account.Context()
	}
	return h.global.Context()
}

// AccountContext returns the account realm's context, or the global context
// as fallback. Views should pass this to pool Fetch calls for account-scoped
// data so that SwitchAccount cancels in-flight fetches.
func (h *Hub) AccountContext() context.Context {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.account != nil {
		return h.account.Context()
	}
	return h.global.Context()
}

// -- Typed pool accessors

// accountClient returns the SDK client for the Hub's current account.
// Safe to call from FetchFunc goroutines.
func (h *Hub) accountClient() *basecamp.AccountClient {
	h.mu.RLock()
	id := h.accountID
	h.mu.RUnlock()
	return h.multi.ClientFor(id)
}

// ScheduleEntries returns a project-scoped pool of schedule entries.
func (h *Hub) ScheduleEntries(projectID, scheduleID int64) *Pool[[]ScheduleEntryInfo] {
	realm := h.EnsureProject(projectID)
	key := fmt.Sprintf("schedule-entries:%d:%d", projectID, scheduleID)
	p := RealmPool(realm, key, func() *Pool[[]ScheduleEntryInfo] {
		return NewPool(key, PoolConfig{}, func(ctx context.Context) ([]ScheduleEntryInfo, error) {
			client := h.accountClient()
			result, err := client.Schedules().ListEntries(ctx, projectID, scheduleID, &basecamp.ScheduleEntryListOptions{})
			if err != nil {
				return nil, err
			}
			infos := make([]ScheduleEntryInfo, 0, len(result.Entries))
			for _, e := range result.Entries {
				title := e.Summary
				if title == "" {
					title = e.Title
				}
				names := make([]string, 0, len(e.Participants))
				for _, p := range e.Participants {
					names = append(names, p.Name)
				}
				startsAt := e.StartsAt.Format("Jan 2, 2006")
				endsAt := e.EndsAt.Format("Jan 2, 2006")
				if !e.AllDay {
					startsAt = e.StartsAt.Format("Jan 2 3:04pm")
					endsAt = e.EndsAt.Format("Jan 2 3:04pm")
				}
				infos = append(infos, ScheduleEntryInfo{
					ID:           e.ID,
					Summary:      title,
					StartsAt:     startsAt,
					EndsAt:       endsAt,
					AllDay:       e.AllDay,
					Participants: names,
				})
			}
			return infos, nil
		})
	})
	p.SetMetrics(h.metrics)
	return p
}

// Checkins returns a project-scoped pool of check-in questions.
func (h *Hub) Checkins(projectID, questionnaireID int64) *Pool[[]CheckinQuestionInfo] {
	realm := h.EnsureProject(projectID)
	key := fmt.Sprintf("checkins:%d:%d", projectID, questionnaireID)
	p := RealmPool(realm, key, func() *Pool[[]CheckinQuestionInfo] {
		return NewPool(key, PoolConfig{}, func(ctx context.Context) ([]CheckinQuestionInfo, error) {
			client := h.accountClient()
			result, err := client.Checkins().ListQuestions(ctx, projectID, questionnaireID, &basecamp.QuestionListOptions{})
			if err != nil {
				return nil, err
			}
			infos := make([]CheckinQuestionInfo, 0, len(result.Questions))
			for _, q := range result.Questions {
				freq := ""
				if q.Schedule != nil {
					freq = q.Schedule.Frequency
				}
				infos = append(infos, CheckinQuestionInfo{
					ID:           q.ID,
					Title:        q.Title,
					Paused:       q.Paused,
					AnswersCount: q.AnswersCount,
					Frequency:    freq,
				})
			}
			return infos, nil
		})
	})
	p.SetMetrics(h.metrics)
	return p
}

// DocsFiles returns a project-scoped pool of vault items (folders, documents, uploads).
func (h *Hub) DocsFiles(projectID, vaultID int64) *Pool[[]DocsFilesItemInfo] {
	realm := h.EnsureProject(projectID)
	key := fmt.Sprintf("docsfiles:%d:%d", projectID, vaultID)
	p := RealmPool(realm, key, func() *Pool[[]DocsFilesItemInfo] {
		return NewPool(key, PoolConfig{}, func(ctx context.Context) ([]DocsFilesItemInfo, error) {
			client := h.accountClient()
			var allItems []DocsFilesItemInfo

			// Fetch folders (sub-vaults)
			foldersResult, err := client.Vaults().List(ctx, projectID, vaultID, nil)
			if err == nil {
				for _, f := range foldersResult.Vaults {
					creator := personName(f.Creator)
					allItems = append(allItems, DocsFilesItemInfo{
						ID:        f.ID,
						Title:     f.Title,
						Type:      "Folder",
						CreatedAt: f.CreatedAt.Format("Jan 2, 2006"),
						Creator:   creator,
					})
				}
			}

			// Fetch documents
			docsResult, docErr := client.Documents().List(ctx, projectID, vaultID, nil)
			if docErr == nil {
				for _, d := range docsResult.Documents {
					creator := personName(d.Creator)
					allItems = append(allItems, DocsFilesItemInfo{
						ID:        d.ID,
						Title:     d.Title,
						Type:      "Document",
						CreatedAt: d.CreatedAt.Format("Jan 2, 2006"),
						Creator:   creator,
					})
				}
			}

			// Fetch uploads
			uploadsResult, uploadErr := client.Uploads().List(ctx, projectID, vaultID, nil)
			if uploadErr == nil {
				for _, u := range uploadsResult.Uploads {
					creator := personName(u.Creator)
					title := u.Filename
					if title == "" {
						title = u.Title
					}
					allItems = append(allItems, DocsFilesItemInfo{
						ID:        u.ID,
						Title:     title,
						Type:      "Upload",
						CreatedAt: u.CreatedAt.Format("Jan 2, 2006"),
						Creator:   creator,
					})
				}
			}

			// If all three failed, report the last error encountered
			if len(allItems) == 0 {
				if uploadErr != nil {
					return nil, uploadErr
				}
				if docErr != nil {
					return nil, docErr
				}
				if err != nil {
					return nil, err
				}
			}

			return allItems, nil
		})
	})
	p.SetMetrics(h.metrics)
	return p
}

// People returns an account-scoped pool of people in the current account.
// Panics if no account realm is active — callers must EnsureAccount first.
func (h *Hub) People() *Pool[[]PersonInfo] {
	h.mu.RLock()
	realm := h.account
	id := h.accountID
	h.mu.RUnlock()
	if realm == nil {
		panic(fmt.Sprintf("Hub.People() called without active account realm (accountID=%q); call EnsureAccount first", id))
	}
	p := RealmPool(realm, "people", func() *Pool[[]PersonInfo] {
		return NewPool("people", PoolConfig{}, func(ctx context.Context) ([]PersonInfo, error) {
			client := h.accountClient()
			result, err := client.People().List(ctx, &basecamp.PeopleListOptions{})
			if err != nil {
				return nil, err
			}
			infos := make([]PersonInfo, 0, len(result.People))
			for _, pp := range result.People {
				var company string
				if pp.Company != nil {
					company = pp.Company.Name
				}
				infos = append(infos, PersonInfo{
					ID:         pp.ID,
					Name:       pp.Name,
					Email:      pp.EmailAddress,
					Title:      pp.Title,
					Admin:      pp.Admin,
					Owner:      pp.Owner,
					Client:     pp.Client,
					PersonType: pp.PersonableType,
					Company:    company,
				})
			}
			return infos, nil
		})
	})
	p.SetMetrics(h.metrics)
	return p
}

// Todolists returns a project-scoped pool of todolists.
func (h *Hub) Todolists(projectID, todosetID int64) *Pool[[]TodolistInfo] {
	realm := h.EnsureProject(projectID)
	key := fmt.Sprintf("todolists:%d:%d", projectID, todosetID)
	p := RealmPool(realm, key, func() *Pool[[]TodolistInfo] {
		return NewPool(key, PoolConfig{}, func(ctx context.Context) ([]TodolistInfo, error) {
			client := h.accountClient()
			result, err := client.Todolists().List(ctx, projectID, todosetID, &basecamp.TodolistListOptions{})
			if err != nil {
				return nil, err
			}
			infos := make([]TodolistInfo, 0, len(result.Todolists))
			for _, tl := range result.Todolists {
				infos = append(infos, TodolistInfo{
					ID:             tl.ID,
					Title:          tl.Title,
					CompletedRatio: tl.CompletedRatio,
				})
			}
			return infos, nil
		})
	})
	p.SetMetrics(h.metrics)
	return p
}

// Todos returns a project-scoped MutatingPool of todos for a specific todolist.
// The MutatingPool supports optimistic todo completion via TodoCompleteMutation.
func (h *Hub) Todos(projectID, todolistID int64) *MutatingPool[[]TodoInfo] {
	realm := h.EnsureProject(projectID)
	key := fmt.Sprintf("todos:%d:%d", projectID, todolistID)
	mp := RealmPool(realm, key, func() *MutatingPool[[]TodoInfo] {
		return NewMutatingPool(key, PoolConfig{}, func(ctx context.Context) ([]TodoInfo, error) {
			client := h.accountClient()
			result, err := client.Todos().List(ctx, projectID, todolistID, &basecamp.TodoListOptions{})
			if err != nil {
				return nil, err
			}
			infos := make([]TodoInfo, 0, len(result.Todos))
			for _, t := range result.Todos {
				names := make([]string, 0, len(t.Assignees))
				for _, a := range t.Assignees {
					names = append(names, a.Name)
				}
				infos = append(infos, TodoInfo{
					ID:          t.ID,
					Content:     t.Content,
					Description: t.Description,
					Completed:   t.Completed,
					DueOn:       t.DueOn,
					Assignees:   names,
					Position:    t.Position,
					BoostEmbed: BoostEmbed{
						BoostsSummary: BoostSummary{Count: t.BoostsCount},
					},
				})
			}
			return infos, nil
		})
	})
	mp.SetMetrics(h.metrics)
	return mp
}

// Cards returns a project-scoped MutatingPool of card columns with their cards.
// The MutatingPool supports optimistic card moves via CardMoveMutation.
// Done and Not Now columns are deferred: metadata only, no card fetching.
func (h *Hub) Cards(projectID, tableID int64) *MutatingPool[[]CardColumnInfo] {
	realm := h.EnsureProject(projectID)
	key := fmt.Sprintf("cards:%d:%d", projectID, tableID)
	mp := RealmPool(realm, key, func() *MutatingPool[[]CardColumnInfo] {
		return NewMutatingPool(key, PoolConfig{}, func(ctx context.Context) ([]CardColumnInfo, error) {
			client := h.accountClient()
			cardTable, err := client.CardTables().Get(ctx, projectID, tableID)
			if err != nil {
				return nil, err
			}

			columns, jobs := buildCardColumns(cardTable.Lists)

			// Fetch cards in parallel for non-deferred columns
			type fetchResult struct {
				idx   int
				cards []CardInfo
				err   error
			}
			results := make(chan fetchResult, len(jobs))
			for _, job := range jobs {
				go func(j cardFetchJob) {
					listResult, err := client.Cards().List(ctx, projectID, j.colID, &basecamp.CardListOptions{})
					if err != nil {
						results <- fetchResult{idx: j.idx, err: fmt.Errorf("loading cards for %q: %w", j.title, err)}
						return
					}
					cards := make([]CardInfo, 0, len(listResult.Cards))
					for _, c := range listResult.Cards {
						cards = append(cards, mapCardInfo(c))
					}
					results <- fetchResult{idx: j.idx, cards: cards}
				}(job)
			}
			for range jobs {
				r := <-results
				if r.err != nil {
					return nil, r.err
				}
				columns[r.idx].Cards = r.cards
				columns[r.idx].CardsCount = len(r.cards)
			}

			return columns, nil
		})
	})
	mp.SetMetrics(h.metrics)
	return mp
}

// cardFetchJob identifies a column whose cards need fetching.
type cardFetchJob struct {
	idx   int
	colID int64
	title string
}

// isColumnDeferred returns true for column types that should not have
// their cards fetched (Done and Not Now columns, which can contain
// hundreds of cards that are never displayed by default).
func isColumnDeferred(colType string) bool {
	return colType == "Kanban::DoneColumn" || colType == "Kanban::NotNowColumn"
}

// buildCardColumns classifies SDK columns into CardColumnInfo entries and
// returns fetch jobs for non-deferred columns. Pure function, no I/O.
func buildCardColumns(lists []basecamp.CardColumn) ([]CardColumnInfo, []cardFetchJob) {
	columns := make([]CardColumnInfo, len(lists))
	var jobs []cardFetchJob
	for i, col := range lists {
		columns[i] = CardColumnInfo{
			ID:         col.ID,
			Title:      col.Title,
			Color:      col.Color,
			Type:       col.Type,
			CardsCount: col.CardsCount,
		}
		if isColumnDeferred(col.Type) {
			columns[i].Deferred = true
		} else {
			jobs = append(jobs, cardFetchJob{idx: i, colID: col.ID, title: col.Title})
		}
	}
	return columns, jobs
}

// mapCardInfo converts an SDK Card to a CardInfo, enriching with step
// progress, completion status, and comment counts.
func mapCardInfo(c basecamp.Card) CardInfo {
	names := make([]string, 0, len(c.Assignees))
	for _, a := range c.Assignees {
		names = append(names, a.Name)
	}
	stepsDone := 0
	for _, s := range c.Steps {
		if s.Completed {
			stepsDone++
		}
	}
	return CardInfo{
		ID:            c.ID,
		Title:         c.Title,
		Assignees:     names,
		DueOn:         c.DueOn,
		Position:      c.Position,
		Completed:     c.Completed,
		StepsTotal:    len(c.Steps),
		StepsDone:     stepsDone,
		CommentsCount: c.CommentsCount,
		BoostEmbed: BoostEmbed{
			BoostsSummary: BoostSummary{Count: c.BoostsCount},
		},
	}
}

// CampfireLines returns a project-scoped pool of campfire lines with polling config.
// The pool stores CampfireLinesResult (lines + TotalCount) for pagination support.
// Pagination (fetchOlderLines) and writes (sendLine) remain view-owned.
func (h *Hub) CampfireLines(projectID, campfireID int64) *Pool[CampfireLinesResult] {
	realm := h.EnsureProject(projectID)
	key := fmt.Sprintf("campfire-lines:%d:%d", projectID, campfireID)
	p := RealmPool(realm, key, func() *Pool[CampfireLinesResult] {
		return NewPool(key, PoolConfig{
			FreshTTL: 4 * time.Second, // expire before PollBase fires
			StaleTTL: 5 * time.Minute, // serve stale during re-fetch
			PollBase: 5 * time.Second,
			PollBg:   30 * time.Second,
			PollMax:  2 * time.Minute,
		}, func(ctx context.Context) (CampfireLinesResult, error) {
			client := h.accountClient()
			result, err := client.Campfires().ListLines(ctx, projectID, campfireID)
			if err != nil {
				return CampfireLinesResult{}, err
			}
			infos := make([]CampfireLineInfo, 0, len(result.Lines))
			for _, line := range result.Lines {
				creator := personName(line.Creator)
				infos = append(infos, CampfireLineInfo{
					ID:        line.ID,
					Body:      line.Content,
					Creator:   creator,
					CreatedAt: line.CreatedAt.Format("3:04pm"),
					BoostEmbed: BoostEmbed{
						BoostsSummary: BoostSummary{Count: line.BoostsCount},
					},
				})
			}
			// API returns newest-first; reverse for chronological display
			for i, j := 0, len(infos)-1; i < j; i, j = i+1, j-1 {
				infos[i], infos[j] = infos[j], infos[i]
			}
			return CampfireLinesResult{
				Lines:      infos,
				TotalCount: result.Meta.TotalCount,
			}, nil
		})
	})
	p.SetMetrics(h.metrics)
	return p
}

// Messages returns a project-scoped pool of message board posts.
func (h *Hub) Messages(projectID, boardID int64) *Pool[[]MessageInfo] {
	realm := h.EnsureProject(projectID)
	key := fmt.Sprintf("messages:%d:%d", projectID, boardID)
	p := RealmPool(realm, key, func() *Pool[[]MessageInfo] {
		return NewPool(key, PoolConfig{}, func(ctx context.Context) ([]MessageInfo, error) {
			client := h.accountClient()
			result, err := client.Messages().List(ctx, projectID, boardID, &basecamp.MessageListOptions{})
			if err != nil {
				return nil, err
			}
			infos := make([]MessageInfo, 0, len(result.Messages))
			for _, m := range result.Messages {
				creator := personName(m.Creator)
				category := ""
				if m.Category != nil {
					category = m.Category.Name
				}
				infos = append(infos, MessageInfo{
					ID:        m.ID,
					Subject:   m.Subject,
					Creator:   creator,
					CreatedAt: m.CreatedAt.Format("Jan 2, 2006"),
					Category:  category,
					BoostEmbed: BoostEmbed{
						BoostsSummary: BoostSummary{Count: m.BoostsCount},
					},
				})
			}
			return infos, nil
		})
	})
	p.SetMetrics(h.metrics)
	return p
}

// Forwards returns a project-scoped pool of email forwards.
func (h *Hub) Forwards(projectID, inboxID int64) *Pool[[]ForwardInfo] {
	realm := h.EnsureProject(projectID)
	key := fmt.Sprintf("forwards:%d:%d", projectID, inboxID)
	p := RealmPool(realm, key, func() *Pool[[]ForwardInfo] {
		return NewPool(key, PoolConfig{}, func(ctx context.Context) ([]ForwardInfo, error) {
			client := h.accountClient()
			result, err := client.Forwards().List(ctx, projectID, inboxID, &basecamp.ForwardListOptions{})
			if err != nil {
				return nil, err
			}
			infos := make([]ForwardInfo, 0, len(result.Forwards))
			for _, f := range result.Forwards {
				infos = append(infos, ForwardInfo{
					ID:      f.ID,
					Subject: f.Subject,
					From:    f.From,
				})
			}
			return infos, nil
		})
	})
	p.SetMetrics(h.metrics)
	return p
}

// ProjectTimeline returns a project-scoped pool of timeline events.
func (h *Hub) ProjectTimeline(projectID int64) *Pool[[]TimelineEventInfo] {
	realm := h.EnsureProject(projectID)
	key := fmt.Sprintf("project-timeline:%d", projectID)
	p := RealmPool(realm, key, func() *Pool[[]TimelineEventInfo] {
		return NewPool(key, PoolConfig{
			FreshTTL: 30 * time.Second,
			StaleTTL: 5 * time.Minute,
		}, func(ctx context.Context) ([]TimelineEventInfo, error) {
			client := h.accountClient()
			acct := h.currentAccountInfo()
			events, err := client.Timeline().ProjectTimeline(ctx, projectID)
			if err != nil {
				return nil, err
			}
			infos := make([]TimelineEventInfo, 0, len(events))
			for _, e := range events {
				project := ""
				var pID int64
				if e.Bucket != nil {
					project = e.Bucket.Name
					pID = e.Bucket.ID
				}
				excerpt := e.SummaryExcerpt
				if len(excerpt) > 100 {
					excerpt = excerpt[:97] + "..."
				}
				infos = append(infos, TimelineEventInfo{
					ID:             e.ID,
					RecordingID:    e.ParentRecordingID,
					CreatedAt:      e.CreatedAt.Format("Jan 2 3:04pm"),
					CreatedAtTS:    e.CreatedAt.Unix(),
					Kind:           e.Kind,
					Action:         e.Action,
					Target:         e.Target,
					Title:          e.Title,
					SummaryExcerpt: excerpt,
					Creator:        personName(e.Creator),
					Project:        project,
					ProjectID:      pID,
					Account:        acct.Name,
					AccountID:      acct.ID,
				})
			}
			return infos, nil
		})
	})
	p.SetMetrics(h.metrics)
	return p
}

// Boosts returns a project-scoped pool of boosts for a recording.
// The pool stores BoostSummary (count + preview) for list item display.
func (h *Hub) Boosts(projectID, recordingID int64) *Pool[BoostSummary] {
	realm := h.EnsureProject(projectID)
	key := fmt.Sprintf("boosts:%d:%d", projectID, recordingID)
	p := RealmPool(realm, key, func() *Pool[BoostSummary] {
		return NewPool(key, PoolConfig{}, func(ctx context.Context) (BoostSummary, error) {
			client := h.accountClient()
			result, err := client.Boosts().ListRecording(ctx, projectID, recordingID)
			if err != nil {
				return BoostSummary{}, err
			}
			return mapBoostSummary(result.Boosts), nil
		})
	})
	p.SetMetrics(h.metrics)
	return p
}

// CreateBoost creates a new boost on a recording.
// Returns the created BoostInfo or an error.
func (h *Hub) CreateBoost(ctx context.Context, projectID, recordingID int64, content string) (BoostInfo, error) {
	client := h.accountClient()
	boost, err := client.Boosts().CreateRecording(ctx, projectID, recordingID, content)
	if err != nil {
		return BoostInfo{}, err
	}
	return mapBoostInfo(*boost), nil
}

// DeleteBoost deletes a boost by ID.
func (h *Hub) DeleteBoost(ctx context.Context, projectID, boostID int64) error {
	client := h.accountClient()
	return client.Boosts().Delete(ctx, projectID, boostID)
}

// mapBoostSummary converts SDK boosts to a BoostSummary for list display.
func mapBoostSummary(boosts []basecamp.Boost) BoostSummary {
	summary := BoostSummary{
		Count:   len(boosts),
		Preview: make([]BoostPreview, 0, min(len(boosts), 3)), // max 3 preview items
	}
	// Take up to 3 most recent boosts for preview
	start := 0
	if len(boosts) > 3 {
		start = len(boosts) - 3
	}
	for i := start; i < len(boosts); i++ {
		b := boosts[i]
		boosterID := int64(0)
		if b.Booster != nil {
			boosterID = b.Booster.ID
		}
		summary.Preview = append(summary.Preview, BoostPreview{
			Content:   b.Content,
			BoosterID: boosterID,
		})
	}
	return summary
}

// CompleteTodo marks a todo as completed. Uses explicit accountID for
// cross-account mutations from aggregate views (Assignments, Hey).
func (h *Hub) CompleteTodo(ctx context.Context, accountID string, projectID, todoID int64) error {
	client := h.multi.ClientFor(accountID)
	if client == nil {
		return fmt.Errorf("no client for account %s", accountID)
	}
	return client.Todos().Complete(ctx, projectID, todoID)
}

// UncompleteTodo reopens a completed todo.
func (h *Hub) UncompleteTodo(ctx context.Context, accountID string, projectID, todoID int64) error {
	client := h.multi.ClientFor(accountID)
	if client == nil {
		return fmt.Errorf("no client for account %s", accountID)
	}
	return client.Todos().Uncomplete(ctx, projectID, todoID)
}

// TrashRecording moves a recording to the trash.
func (h *Hub) TrashRecording(ctx context.Context, accountID string, projectID, recordingID int64) error {
	client := h.multi.ClientFor(accountID)
	if client == nil {
		return fmt.Errorf("no client for account %s", accountID)
	}
	return client.Recordings().Trash(ctx, projectID, recordingID)
}

// UpdateTodo updates a todo's fields.
func (h *Hub) UpdateTodo(ctx context.Context, accountID string, projectID, todoID int64, req *basecamp.UpdateTodoRequest) error {
	client := h.multi.ClientFor(accountID)
	if client == nil {
		return fmt.Errorf("no client for account %s", accountID)
	}
	_, err := client.Todos().Update(ctx, projectID, todoID, req)
	return err
}

// ClearTodoDueOn clears the due date on a todo. Uses a raw Put to bypass
// the SDK's omitempty on DueOn which prevents sending empty strings.
func (h *Hub) ClearTodoDueOn(ctx context.Context, accountID string, projectID, todoID int64) error {
	client := h.multi.ClientFor(accountID)
	if client == nil {
		return fmt.Errorf("no client for account %s", accountID)
	}
	path := fmt.Sprintf("/buckets/%d/todos/%d.json", projectID, todoID)
	_, err := client.Put(ctx, path, map[string]any{"due_on": nil})
	return err
}

// ClearTodoAssignees clears all assignees on a todo. Uses a raw Put to bypass
// the SDK's omitempty on AssigneeIDs which prevents sending empty slices.
func (h *Hub) ClearTodoAssignees(ctx context.Context, accountID string, projectID, todoID int64) error {
	client := h.multi.ClientFor(accountID)
	if client == nil {
		return fmt.Errorf("no client for account %s", accountID)
	}
	path := fmt.Sprintf("/buckets/%d/todos/%d.json", projectID, todoID)
	_, err := client.Put(ctx, path, map[string]any{"assignee_ids": []int64{}})
	return err
}

// UpdateCard updates a card's fields.
func (h *Hub) UpdateCard(ctx context.Context, accountID string, projectID, cardID int64, req *basecamp.UpdateCardRequest) error {
	client := h.multi.ClientFor(accountID)
	if client == nil {
		return fmt.Errorf("no client for account %s", accountID)
	}
	_, err := client.Cards().Update(ctx, projectID, cardID, req)
	return err
}

// PinMessage pins a message to the top of its board.
func (h *Hub) PinMessage(ctx context.Context, accountID string, projectID, messageID int64) error {
	client := h.multi.ClientFor(accountID)
	if client == nil {
		return fmt.Errorf("no client for account %s", accountID)
	}
	return client.Messages().Pin(ctx, projectID, messageID)
}

// UnpinMessage unpins a message from the board.
func (h *Hub) UnpinMessage(ctx context.Context, accountID string, projectID, messageID int64) error {
	client := h.multi.ClientFor(accountID)
	if client == nil {
		return fmt.Errorf("no client for account %s", accountID)
	}
	return client.Messages().Unpin(ctx, projectID, messageID)
}

// Subscribe subscribes the current user to a recording.
func (h *Hub) Subscribe(ctx context.Context, accountID string, projectID, recordingID int64) error {
	client := h.multi.ClientFor(accountID)
	if client == nil {
		return fmt.Errorf("no client for account %s", accountID)
	}
	_, err := client.Subscriptions().Subscribe(ctx, projectID, recordingID)
	return err
}

// Unsubscribe unsubscribes the current user from a recording.
func (h *Hub) Unsubscribe(ctx context.Context, accountID string, projectID, recordingID int64) error {
	client := h.multi.ClientFor(accountID)
	if client == nil {
		return fmt.Errorf("no client for account %s", accountID)
	}
	return client.Subscriptions().Unsubscribe(ctx, projectID, recordingID)
}

// UpdateComment updates a comment's content.
func (h *Hub) UpdateComment(ctx context.Context, accountID string, projectID, commentID int64, content string) error {
	client := h.multi.ClientFor(accountID)
	if client == nil {
		return fmt.Errorf("no client for account %s", accountID)
	}
	_, err := client.Comments().Update(ctx, projectID, commentID, &basecamp.UpdateCommentRequest{Content: content})
	return err
}

// TrashComment moves a comment to the trash.
func (h *Hub) TrashComment(ctx context.Context, accountID string, projectID, commentID int64) error {
	client := h.multi.ClientFor(accountID)
	if client == nil {
		return fmt.Errorf("no client for account %s", accountID)
	}
	return client.Comments().Trash(ctx, projectID, commentID)
}

// mapBoostInfo converts an SDK Boost to BoostInfo.
func mapBoostInfo(b basecamp.Boost) BoostInfo {
	booster := ""
	boosterID := int64(0)
	if b.Booster != nil {
		booster = b.Booster.Name
		boosterID = b.Booster.ID
	}
	return BoostInfo{
		ID:        b.ID,
		Content:   b.Content,
		Booster:   booster,
		BoosterID: boosterID,
		CreatedAt: b.CreatedAt.Format("Jan 2 3:04pm"),
	}
}
