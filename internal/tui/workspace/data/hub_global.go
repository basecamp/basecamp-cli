package data

import (
	"context"
	"sort"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// -- Global-realm pool accessors (cross-account fan-out)

// currentAccountInfo returns the Hub's active account as an AccountInfo,
// looking up the name from the discovered account list.
func (h *Hub) currentAccountInfo() AccountInfo {
	h.mu.RLock()
	id := h.accountID
	h.mu.RUnlock()
	for _, a := range h.multi.Accounts() {
		if a.ID == id {
			return a
		}
	}
	return AccountInfo{ID: id}
}

// HeyActivity returns a global-scope pool of cross-account activity entries.
// The pool fans out Recordings.List across all accounts, caches for 30s (fresh)
// / 5m (stale), and polls at 30s/2m intervals.
func (h *Hub) HeyActivity() *Pool[[]ActivityEntryInfo] {
	return RealmPool(h.Global(), "hey:activity", func() *Pool[[]ActivityEntryInfo] {
		return NewPool("hey:activity", PoolConfig{
			FreshTTL: 30 * time.Second,
			StaleTTL: 5 * time.Minute,
			PollBase: 30 * time.Second,
			PollBg:   2 * time.Minute,
			PollMax:  5 * time.Minute,
		}, func(ctx context.Context) ([]ActivityEntryInfo, error) {
			types := []basecamp.RecordingType{
				basecamp.RecordingTypeMessage,
				basecamp.RecordingTypeTodo,
				basecamp.RecordingTypeDocument,
			}
			accounts := h.multi.Accounts()
			if len(accounts) == 0 {
				acct := h.currentAccountInfo()
				if acct.ID == "" {
					return nil, nil
				}
				client := h.multi.ClientFor(acct.ID)
				entries := fetchRecordingsAsActivity(ctx, client, acct, types, 15)
				if len(entries) > 30 {
					entries = entries[:30]
				}
				return entries, nil
			}

			results := FanOut[[]ActivityEntryInfo](ctx, h.multi,
				func(acct AccountInfo, client *basecamp.AccountClient) ([]ActivityEntryInfo, error) {
					return fetchRecordingsAsActivity(ctx, client, acct, types, 10), nil
				})

			var all []ActivityEntryInfo
			for _, r := range results {
				if r.Err == nil {
					all = append(all, r.Data...)
				}
			}
			sort.Slice(all, func(i, j int) bool {
				return all[i].UpdatedAtTS > all[j].UpdatedAtTS
			})
			if len(all) > 50 {
				all = all[:50]
			}
			return all, nil
		})
	})
}

// Pulse returns a global-scope pool of cross-account recent activity.
// Like HeyActivity but includes more recording types and groups by account.
func (h *Hub) Pulse() *Pool[[]ActivityEntryInfo] {
	return RealmPool(h.Global(), "pulse", func() *Pool[[]ActivityEntryInfo] {
		return NewPool("pulse", PoolConfig{
			FreshTTL: 30 * time.Second,
			StaleTTL: 5 * time.Minute,
		}, func(ctx context.Context) ([]ActivityEntryInfo, error) {
			types := []basecamp.RecordingType{
				basecamp.RecordingTypeMessage,
				basecamp.RecordingTypeTodo,
				basecamp.RecordingTypeDocument,
				basecamp.RecordingTypeKanbanCard,
			}
			accounts := h.multi.Accounts()
			if len(accounts) == 0 {
				return nil, nil
			}

			results := FanOut[[]ActivityEntryInfo](ctx, h.multi,
				func(acct AccountInfo, client *basecamp.AccountClient) ([]ActivityEntryInfo, error) {
					return fetchRecordingsAsActivity(ctx, client, acct, types, 5), nil
				})

			var all []ActivityEntryInfo
			for _, r := range results {
				if r.Err == nil {
					all = append(all, r.Data...)
				}
			}
			sort.Slice(all, func(i, j int) bool {
				return all[i].UpdatedAtTS > all[j].UpdatedAtTS
			})
			if len(all) > 60 {
				all = all[:60]
			}
			return all, nil
		})
	})
}

// Assignments returns a global-scope pool of cross-account todo assignments.
func (h *Hub) Assignments() *Pool[[]AssignmentInfo] {
	return RealmPool(h.Global(), "assignments", func() *Pool[[]AssignmentInfo] {
		return NewPool("assignments", PoolConfig{
			FreshTTL: 30 * time.Second,
			StaleTTL: 5 * time.Minute,
		}, func(ctx context.Context) ([]AssignmentInfo, error) {
			identity := h.multi.Identity()
			if identity == nil {
				// Identity not discovered yet — return empty rather than
				// error so consumers don't get stuck in permanent loading.
				// FreshTTL will expire and the next FetchIfStale will retry.
				return nil, nil
			}
			myName := identity.FirstName + " " + identity.LastName

			accounts := h.multi.Accounts()
			if len(accounts) == 0 {
				acct := h.currentAccountInfo()
				if acct.ID == "" {
					return nil, nil
				}
				client := h.multi.ClientFor(acct.ID)
				return fetchAccountAssignments(ctx, client, acct, myName), nil
			}

			results := FanOut[[]AssignmentInfo](ctx, h.multi,
				func(acct AccountInfo, client *basecamp.AccountClient) ([]AssignmentInfo, error) {
					return fetchAccountAssignments(ctx, client, acct, myName), nil
				})

			var all []AssignmentInfo
			for _, r := range results {
				if r.Err == nil {
					all = append(all, r.Data...)
				}
			}
			sort.Slice(all, func(i, j int) bool {
				if all[i].DueOn == "" {
					return false
				}
				if all[j].DueOn == "" {
					return true
				}
				return all[i].DueOn < all[j].DueOn
			})
			return all, nil
		})
	})
}

// PingRooms returns a global-scope pool of 1:1 campfire threads.
func (h *Hub) PingRooms() *Pool[[]PingRoomInfo] {
	return RealmPool(h.Global(), "ping-rooms", func() *Pool[[]PingRoomInfo] {
		return NewPool("ping-rooms", PoolConfig{
			FreshTTL: 1 * time.Minute,
			StaleTTL: 5 * time.Minute,
		}, func(ctx context.Context) ([]PingRoomInfo, error) {
			accounts := h.multi.Accounts()
			if len(accounts) == 0 {
				return nil, nil
			}

			results := FanOut[[]PingRoomInfo](ctx, h.multi,
				func(acct AccountInfo, client *basecamp.AccountClient) ([]PingRoomInfo, error) {
					campfires, err := client.Campfires().List(ctx)
					if err != nil {
						return nil, err
					}
					var rooms []PingRoomInfo
					for _, cf := range campfires.Campfires {
						if cf.Bucket != nil && cf.Bucket.Type == "Project" {
							continue
						}
						var lastMsg, lastAt string
						var lastAtTS int64
						lines, err := client.Campfires().ListLines(ctx, 0, cf.ID)
						if err == nil && len(lines.Lines) > 0 {
							last := lines.Lines[len(lines.Lines)-1]
							if last.Creator != nil {
								lastMsg = last.Creator.Name + ": "
							}
							content := last.Content
							if len(content) > 40 {
								content = content[:37] + "..."
							}
							lastMsg += content
							lastAt = last.CreatedAt.Format("Jan 2 3:04pm")
							lastAtTS = last.CreatedAt.Unix()
						}
						var projectID int64
						if cf.Bucket != nil {
							projectID = cf.Bucket.ID
						}
						rooms = append(rooms, PingRoomInfo{
							CampfireID:  cf.ID,
							ProjectID:   projectID,
							PersonName:  cf.Title,
							Account:     acct.Name,
							AccountID:   acct.ID,
							LastMessage: lastMsg,
							LastAt:      lastAt,
							LastAtTS:    lastAtTS,
						})
					}
					return rooms, nil
				})

			var all []PingRoomInfo
			for _, r := range results {
				if r.Err == nil {
					all = append(all, r.Data...)
				}
			}
			sort.Slice(all, func(i, j int) bool {
				return all[i].LastAtTS > all[j].LastAtTS
			})
			return all, nil
		})
	})
}

// -- Shared fetch helpers

// fetchRecordingsAsActivity fetches recordings of the given types from a single
// account and maps them to ActivityEntryInfo. Shared by HeyActivity and Pulse.
func fetchRecordingsAsActivity(ctx context.Context, client *basecamp.AccountClient,
	acct AccountInfo, types []basecamp.RecordingType, limit int,
) []ActivityEntryInfo {
	var entries []ActivityEntryInfo
	for _, rt := range types {
		result, err := client.Recordings().List(ctx, rt, &basecamp.RecordingsListOptions{
			Sort:      "updated_at",
			Direction: "desc",
			Limit:     limit,
			Page:      1,
		})
		if err != nil {
			continue
		}
		for _, rec := range result.Recordings {
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
			entries = append(entries, ActivityEntryInfo{
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
			})
		}
	}
	return entries
}

// fetchAccountAssignments fetches active todos from a single account.
func fetchAccountAssignments(ctx context.Context, client *basecamp.AccountClient,
	acct AccountInfo, myName string,
) []AssignmentInfo {
	result, err := client.Recordings().List(ctx, basecamp.RecordingTypeTodo, &basecamp.RecordingsListOptions{
		Status:    "active",
		Sort:      "updated_at",
		Direction: "desc",
		Limit:     50,
		Page:      1,
	})
	if err != nil {
		return nil
	}

	var assignments []AssignmentInfo
	for _, rec := range result.Recordings {
		project := ""
		var projectID int64
		if rec.Bucket != nil {
			project = rec.Bucket.Name
			projectID = rec.Bucket.ID
		}
		_ = myName // TODO: filter by assignee when SDK supports it
		assignments = append(assignments, AssignmentInfo{
			ID:        rec.ID,
			Content:   rec.Title,
			Account:   acct.Name,
			AccountID: acct.ID,
			Project:   project,
			ProjectID: projectID,
		})
	}
	return assignments
}

// Projects returns a global-scope pool of all projects across accounts.
// Each project carries account attribution for cross-account navigation.
// Used by Home (bookmarks), Projects view, and Dock.
func (h *Hub) Projects() *Pool[[]ProjectInfo] {
	return RealmPool(h.Global(), "projects", func() *Pool[[]ProjectInfo] {
		return NewPool("projects", PoolConfig{
			FreshTTL: 30 * time.Second,
			StaleTTL: 5 * time.Minute,
		}, func(ctx context.Context) ([]ProjectInfo, error) {
			accounts := h.multi.Accounts()
			if len(accounts) == 0 {
				acct := h.currentAccountInfo()
				if acct.ID == "" {
					return nil, nil
				}
				client := h.multi.ClientFor(acct.ID)
				result, err := client.Projects().List(ctx, &basecamp.ProjectListOptions{})
				if err != nil {
					return nil, err
				}
				return projectsToInfos(result.Projects, acct), nil
			}

			results := FanOut[[]ProjectInfo](ctx, h.multi,
				func(acct AccountInfo, client *basecamp.AccountClient) ([]ProjectInfo, error) {
					result, err := client.Projects().List(ctx, &basecamp.ProjectListOptions{})
					if err != nil {
						return nil, err
					}
					return projectsToInfos(result.Projects, acct), nil
				})

			var all []ProjectInfo
			for _, r := range results {
				if r.Err == nil {
					all = append(all, r.Data...)
				}
			}
			return all, nil
		})
	})
}

// projectsToInfos maps SDK projects to ProjectInfo with account attribution.
func projectsToInfos(projects []basecamp.Project, acct AccountInfo) []ProjectInfo {
	infos := make([]ProjectInfo, 0, len(projects))
	for _, p := range projects {
		dock := make([]DockToolInfo, 0, len(p.Dock))
		for _, d := range p.Dock {
			dock = append(dock, DockToolInfo{
				ID:      d.ID,
				Name:    d.Name,
				Title:   d.Title,
				Enabled: d.Enabled,
			})
		}
		infos = append(infos, ProjectInfo{
			ID:          p.ID,
			Name:        p.Name,
			Description: p.Description,
			Purpose:     p.Purpose,
			Bookmarked:  p.Bookmarked,
			AccountID:   acct.ID,
			AccountName: acct.Name,
			Dock:        dock,
		})
	}
	return infos
}
