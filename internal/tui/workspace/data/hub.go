package data

import (
	"context"
	"fmt"
	"sync"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// Hub is the central data coordinator. It replaces Store + MultiStore.Cache()
// + ad-hoc view fetch functions with typed, realm-scoped pool access.
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
}

// NewHub creates a Hub with a global realm and the given dependencies.
func NewHub(multi *MultiStore, poller *Poller) *Hub {
	return &Hub{
		global: NewRealm("global", context.Background()),
		multi:  multi,
		poller: poller,
	}
}

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
	return RealmPool(realm, key, func() *Pool[[]ScheduleEntryInfo] {
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
}

// Checkins returns a project-scoped pool of check-in questions.
func (h *Hub) Checkins(projectID, questionnaireID int64) *Pool[[]CheckinQuestionInfo] {
	realm := h.EnsureProject(projectID)
	key := fmt.Sprintf("checkins:%d:%d", projectID, questionnaireID)
	return RealmPool(realm, key, func() *Pool[[]CheckinQuestionInfo] {
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
}

// DocsFiles returns a project-scoped pool of vault items (folders, documents, uploads).
func (h *Hub) DocsFiles(projectID, vaultID int64) *Pool[[]DocsFilesItemInfo] {
	realm := h.EnsureProject(projectID)
	key := fmt.Sprintf("docsfiles:%d:%d", projectID, vaultID)
	return RealmPool(realm, key, func() *Pool[[]DocsFilesItemInfo] {
		return NewPool(key, PoolConfig{}, func(ctx context.Context) ([]DocsFilesItemInfo, error) {
			client := h.accountClient()
			var allItems []DocsFilesItemInfo

			// Fetch folders (sub-vaults)
			foldersResult, err := client.Vaults().List(ctx, projectID, vaultID, nil)
			if err == nil {
				for _, f := range foldersResult.Vaults {
					creator := ""
					if f.Creator != nil {
						creator = f.Creator.Name
					}
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
					creator := ""
					if d.Creator != nil {
						creator = d.Creator.Name
					}
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
					creator := ""
					if u.Creator != nil {
						creator = u.Creator.Name
					}
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
}

// People returns an account-scoped pool of people in the current account.
func (h *Hub) People() *Pool[[]PersonInfo] {
	realm := h.Account()
	return RealmPool(realm, "people", func() *Pool[[]PersonInfo] {
		return NewPool("people", PoolConfig{}, func(ctx context.Context) ([]PersonInfo, error) {
			client := h.accountClient()
			result, err := client.People().List(ctx, &basecamp.PeopleListOptions{})
			if err != nil {
				return nil, err
			}
			infos := make([]PersonInfo, 0, len(result.People))
			for _, p := range result.People {
				var company string
				if p.Company != nil {
					company = p.Company.Name
				}
				infos = append(infos, PersonInfo{
					ID:         p.ID,
					Name:       p.Name,
					Email:      p.EmailAddress,
					Title:      p.Title,
					Admin:      p.Admin,
					Owner:      p.Owner,
					Client:     p.Client,
					PersonType: p.PersonableType,
					Company:    company,
				})
			}
			return infos, nil
		})
	})
}

// Forwards returns a project-scoped pool of email forwards.
func (h *Hub) Forwards(projectID, inboxID int64) *Pool[[]ForwardInfo] {
	realm := h.EnsureProject(projectID)
	key := fmt.Sprintf("forwards:%d:%d", projectID, inboxID)
	return RealmPool(realm, key, func() *Pool[[]ForwardInfo] {
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
}
