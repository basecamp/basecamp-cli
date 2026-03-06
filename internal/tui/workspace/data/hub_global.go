package data

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// -- Helpers

// personName returns the person's name, or "" if the person is nil.
func personName(p *basecamp.Person) string {
	if p != nil {
		return p.Name
	}
	return ""
}

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
	p := RealmPool(h.Global(), "hey:activity", func() *Pool[[]ActivityEntryInfo] {
		return NewPool("hey:activity", PoolConfig{
			FreshTTL: 30 * time.Second,
			StaleTTL: 5 * time.Minute,
			PollBase: 45 * time.Second,
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
	p.SetMetrics(h.metrics)
	p.SetCache(h.cache)
	return p
}

// Pulse returns a global-scope pool of cross-account recent activity.
// Like HeyActivity but includes more recording types and groups by account.
func (h *Hub) Pulse() *Pool[[]ActivityEntryInfo] {
	p := RealmPool(h.Global(), "pulse", func() *Pool[[]ActivityEntryInfo] {
		return NewPool("pulse", PoolConfig{
			FreshTTL: 30 * time.Second,
			StaleTTL: 5 * time.Minute,
			PollBase: 60 * time.Second,
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
	p.SetMetrics(h.metrics)
	p.SetCache(h.cache)
	return p
}

// Assignments returns a global-scope pool of cross-account todo assignments.
func (h *Hub) Assignments() *Pool[[]AssignmentInfo] {
	p := RealmPool(h.Global(), "assignments", func() *Pool[[]AssignmentInfo] {
		return NewPool("assignments", PoolConfig{
			FreshTTL: 30 * time.Second,
			StaleTTL: 5 * time.Minute,
			PollBase: 60 * time.Second,
		}, func(ctx context.Context) ([]AssignmentInfo, error) {
			identity := h.multi.Identity()
			if identity == nil {
				// Identity not discovered yet — return empty rather than
				// error so consumers don't get stuck in permanent loading.
				// FreshTTL will expire and the next FetchIfStale will retry.
				return nil, nil
			}
			personID := identity.ID

			accounts := h.multi.Accounts()
			if len(accounts) == 0 {
				acct := h.currentAccountInfo()
				if acct.ID == "" {
					return nil, nil
				}
				client := h.multi.ClientFor(acct.ID)
				return fetchAccountAssignments(ctx, client, acct, personID), nil
			}

			results := FanOut[[]AssignmentInfo](ctx, h.multi,
				func(acct AccountInfo, client *basecamp.AccountClient) ([]AssignmentInfo, error) {
					return fetchAccountAssignments(ctx, client, acct, personID), nil
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
	p.SetMetrics(h.metrics)
	p.SetCache(h.cache)
	return p
}

// PingRooms returns a global-scope pool of 1:1 campfire threads.
func (h *Hub) PingRooms() *Pool[[]PingRoomInfo] {
	p := RealmPool(h.Global(), "ping-rooms", func() *Pool[[]PingRoomInfo] {
		return NewPool("ping-rooms", PoolConfig{
			FreshTTL: 1 * time.Minute,
			StaleTTL: 5 * time.Minute,
			PollBase: 60 * time.Second,
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
						lines, err := client.Campfires().ListLines(ctx, cf.ID)
						if err == nil && len(lines.Lines) > 0 {
							last := lines.Lines[len(lines.Lines)-1]
							if last.Creator != nil {
								lastMsg = last.Creator.Name + ": "
							}
							content := last.Content
							if r := []rune(content); len(r) > 40 {
								content = string(r[:37]) + "…"
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
	p.SetMetrics(h.metrics)
	return p
}

// BonfireRooms returns a global-scope pool of campfire rooms from bookmarked
// and recently visited projects. Uses FanOut like PingRooms but filters for
// project campfires (not 1:1 pings). For accounts with no bookmarks, falls back
// to recently visited projects (from recents store). The RoomStore (if configured
// via SetRoomStore) can further narrow or widen via explicit includes/excludes.
func (h *Hub) BonfireRooms() *Pool[[]BonfireRoomConfig] {
	p := RealmPool(h.Global(), "bonfire-rooms", func() *Pool[[]BonfireRoomConfig] {
		return NewPool("bonfire-rooms", PoolConfig{
			FreshTTL: 2 * time.Minute,
			StaleTTL: 5 * time.Minute,
			PollBase: 120 * time.Second,
		}, func(ctx context.Context) ([]BonfireRoomConfig, error) {
			// Fetch all project campfires and bookmarked project IDs per account.
			// We need both: the full set (for explicit-include overrides to widen)
			// and the bookmarked subset (for the default when no overrides exist).
			type accountRooms struct {
				all       []BonfireRoomConfig // every project campfire
				bookmarks map[string]struct{} // keys of bookmarked-project rooms
			}
			fetchAccountRooms := func(acct AccountInfo, client *basecamp.AccountClient) (accountRooms, error) {
				projResult, projErr := client.Projects().List(ctx, &basecamp.ProjectListOptions{})
				if projErr != nil {
					return accountRooms{}, projErr
				}
				bookmarked := make(map[int64]string) // project ID -> name
				for _, p := range projResult.Projects {
					if p.Bookmarked {
						bookmarked[p.ID] = p.Name
					}
				}

				campfires, err := client.Campfires().List(ctx)
				if err != nil {
					return accountRooms{}, err
				}
				var rooms []BonfireRoomConfig
				bmKeys := make(map[string]struct{})
				for _, cf := range campfires.Campfires {
					if cf.Bucket == nil || cf.Bucket.Type != "Project" {
						continue
					}
					projectName := cf.Bucket.Name
					if name, ok := bookmarked[cf.Bucket.ID]; ok && name != "" {
						projectName = name
					}
					rc := BonfireRoomConfig{
						RoomID: RoomID{
							AccountID:  acct.ID,
							ProjectID:  cf.Bucket.ID,
							CampfireID: cf.ID,
						},
						RoomName:    cf.Title,
						ProjectName: projectName,
					}
					rooms = append(rooms, rc)
					if _, ok := bookmarked[cf.Bucket.ID]; ok {
						bmKeys[rc.Key()] = struct{}{}
					}
				}
				// If no bookmarks, fall back to recently visited projects
				// scoped to this account to avoid cross-account ID collisions.
				if len(bookmarked) == 0 {
					h.mu.RLock()
					recentFn := h.recentProjects
					h.mu.RUnlock()
					if recentFn != nil {
						recentIDs := make(map[int64]struct{})
						for _, id := range recentFn(acct.ID) {
							recentIDs[id] = struct{}{}
						}
						for _, r := range rooms {
							if _, ok := recentIDs[r.ProjectID]; ok {
								bmKeys[r.Key()] = struct{}{}
							}
						}
					}
					// If still empty (fresh account, no recents), include all rooms
					// so the user sees something rather than an empty Bonfire.
					if len(bmKeys) == 0 {
						for _, r := range rooms {
							bmKeys[r.Key()] = struct{}{}
						}
					}
				}
				return accountRooms{all: rooms, bookmarks: bmKeys}, nil
			}

			var allRooms []BonfireRoomConfig
			bookmarkSet := make(map[string]struct{})

			accounts := h.multi.Accounts()
			if len(accounts) == 0 {
				// Pre-discovery: fall back to the configured single account
				// so rooms appear immediately. After AccountsDiscoveredMsg
				// the pool will go stale and re-fetch with all accounts.
				acct := h.currentAccountInfo()
				if acct.ID == "" {
					return nil, nil
				}
				client := h.multi.ClientFor(acct.ID)
				if client == nil {
					return nil, nil
				}
				result, err := fetchAccountRooms(acct, client)
				if err == nil {
					allRooms = append(allRooms, result.all...)
					for k := range result.bookmarks {
						bookmarkSet[k] = struct{}{}
					}
				}
			} else {
				results := FanOut[accountRooms](ctx, h.multi, fetchAccountRooms)
				for _, r := range results {
					if r.Err == nil {
						allRooms = append(allRooms, r.Data.all...)
						for k := range r.Data.bookmarks {
							bookmarkSet[k] = struct{}{}
						}
					}
				}
			}

			// Apply room selection overrides.
			// Contract: (bookmarked ∪ recents ∪ explicit-includes) − explicit-excludes.
			h.mu.RLock()
			rs := h.roomStore
			h.mu.RUnlock()

			wantSet := make(map[string]struct{})
			for k := range bookmarkSet {
				wantSet[k] = struct{}{}
			}
			if rs != nil {
				if overrides, err := rs.Load(ctx); err == nil {
					for k := range overrides.Includes {
						wantSet[k] = struct{}{}
					}
					for k := range overrides.Excludes {
						delete(wantSet, k)
					}
				}
			}

			var selected []BonfireRoomConfig
			for _, r := range allRooms {
				if _, ok := wantSet[r.Key()]; ok {
					selected = append(selected, r)
				}
			}

			sort.Slice(selected, func(i, j int) bool {
				return selected[i].ProjectName < selected[j].ProjectName
			})
			return selected, nil
		})
	})
	p.SetMetrics(h.metrics)
	return p
}

// BonfireLines returns a global-scope pool of campfire lines for a specific room.
// Uses h.multi.ClientFor(room.AccountID) — no EnsureProject needed.
// Keyed as "bonfire-lines:{accountID}:{projectID}:{campfireID}".
func (h *Hub) BonfireLines(room RoomID) *Pool[CampfireLinesResult] {
	key := fmt.Sprintf("bonfire-lines:%s", room.Key())
	p := RealmPool(h.Global(), key, func() *Pool[CampfireLinesResult] {
		return NewPool(key, PoolConfig{
			FreshTTL: 5 * time.Second,
			StaleTTL: 30 * time.Second,
			PollBase: 15 * time.Second,
			PollMax:  2 * time.Minute,
		}, func(ctx context.Context) (CampfireLinesResult, error) {
			client := h.multi.ClientFor(room.AccountID)
			if client == nil {
				return CampfireLinesResult{}, fmt.Errorf("no client for account %s", room.AccountID)
			}
			result, err := client.Campfires().ListLines(ctx, room.CampfireID)
			if err != nil {
				return CampfireLinesResult{}, err
			}
			infos := mapCampfireLines(result.Lines)
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

// BonfireDigest returns a global-scope pool of last-message-per-room summaries.
// Self-sufficient: if BonfireRooms hasn't been populated yet, triggers a room
// fetch inline before reading. This ensures the ticker works on fresh sessions
// without depending on another view having fetched rooms first.
func (h *Hub) BonfireDigest() *Pool[[]BonfireDigestEntry] {
	p := RealmPool(h.Global(), "bonfire-digest", func() *Pool[[]BonfireDigestEntry] {
		return NewPool("bonfire-digest", PoolConfig{
			FreshTTL: 10 * time.Second,
			StaleTTL: 1 * time.Minute,
			PollBase: 15 * time.Second,
			PollMax:  2 * time.Minute,
		}, func(ctx context.Context) ([]BonfireDigestEntry, error) {
			roomPool := h.BonfireRooms()
			snap := roomPool.Get()
			if !snap.HasData {
				// Rooms not yet populated — run the room fetch inline.
				// Pool.Fetch returns a Cmd (func() tea.Msg); execute it
				// directly since we're already in a goroutine.
				if cmd := roomPool.Fetch(ctx); cmd != nil {
					cmd() // blocks until room fetch completes
				}
				snap = roomPool.Get()
			}
			rooms := CapRoomsRoundRobin(snap.Data, 8)
			if len(rooms) == 0 {
				return nil, nil
			}

			ch := make(chan BonfireDigestEntry, len(rooms))
			sem := make(chan struct{}, 3) // limit 3 concurrent

			for _, room := range rooms {
				go func(rc BonfireRoomConfig) {
					sem <- struct{}{}
					defer func() { <-sem }()

					entry := BonfireDigestEntry{
						RoomID:   rc.RoomID,
						RoomName: rc.RoomName,
					}

					client := h.multi.ClientFor(rc.AccountID)
					if client == nil {
						ch <- entry
						return
					}
					lines, err := client.Campfires().ListLines(ctx, rc.CampfireID)
					if err != nil || len(lines.Lines) == 0 {
						ch <- entry
						return
					}
					last := lines.Lines[0] // newest first from API
					entry.LastAuthor = personName(last.Creator)
					content := StripTags(last.Content)
					if runes := []rune(content); len(runes) > 80 {
						content = string(runes[:77]) + "…"
					}
					entry.LastMessage = content
					entry.LastAt = last.CreatedAt.Format("Jan 2 3:04pm")
					entry.LastAtTS = last.CreatedAt.Unix()
					ch <- entry
				}(room)
			}

			entries := make([]BonfireDigestEntry, 0, len(rooms))
			for range rooms {
				entry := <-ch
				if entry.LastMessage == "" && entry.LastAtTS == 0 {
					continue // skip rooms with no messages
				}
				entries = append(entries, entry)
			}
			sort.Slice(entries, func(i, j int) bool {
				return entries[i].LastAtTS > entries[j].LastAtTS
			})
			return entries, nil
		})
	})
	p.SetMetrics(h.metrics)
	p.SetCache(h.cache)
	return p
}

// Timeline returns a global-scope pool of cross-account timeline events.
// Uses the richer Timeline.Progress() API instead of Recordings.List.
func (h *Hub) Timeline() *Pool[[]TimelineEventInfo] {
	p := RealmPool(h.Global(), "timeline", func() *Pool[[]TimelineEventInfo] {
		return NewPool("timeline", PoolConfig{
			FreshTTL: 30 * time.Second,
			StaleTTL: 5 * time.Minute,
			PollBase: 45 * time.Second,
			PollBg:   2 * time.Minute,
			PollMax:  5 * time.Minute,
		}, func(ctx context.Context) ([]TimelineEventInfo, error) {
			accounts := h.multi.Accounts()
			if len(accounts) == 0 {
				acct := h.currentAccountInfo()
				if acct.ID == "" {
					return nil, nil
				}
				client := h.multi.ClientFor(acct.ID)
				return fetchTimelineEvents(ctx, client, acct)
			}

			results := FanOut[[]TimelineEventInfo](ctx, h.multi,
				func(acct AccountInfo, client *basecamp.AccountClient) ([]TimelineEventInfo, error) {
					return fetchTimelineEvents(ctx, client, acct)
				})

			var all []TimelineEventInfo
			for _, r := range results {
				if r.Err == nil {
					all = append(all, r.Data...)
				}
			}
			sort.Slice(all, func(i, j int) bool {
				return all[i].CreatedAtTS > all[j].CreatedAtTS
			})
			if len(all) > 80 {
				all = all[:80]
			}
			return all, nil
		})
	})
	p.SetMetrics(h.metrics)
	p.SetCache(h.cache)
	return p
}

// -- Shared fetch helpers

// fetchTimelineEvents fetches timeline events from a single account.
func fetchTimelineEvents(ctx context.Context, client *basecamp.AccountClient, acct AccountInfo) ([]TimelineEventInfo, error) {
	events, err := client.Timeline().Progress(ctx)
	if err != nil {
		return nil, err
	}
	infos := make([]TimelineEventInfo, 0, len(events))
	for _, e := range events {
		project := ""
		var projectID int64
		if e.Bucket != nil {
			project = e.Bucket.Name
			projectID = e.Bucket.ID
		}
		excerpt := e.SummaryExcerpt
		if r := []rune(excerpt); len(r) > 100 {
			excerpt = string(r[:97]) + "…"
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
			ProjectID:      projectID,
			Account:        acct.Name,
			AccountID:      acct.ID,
		})
	}
	return infos, nil
}

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
			creator := personName(rec.Creator)
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

// fetchAccountAssignments fetches todos assigned to the current user via the
// Reports API which returns only the user's assignments with richer data
// (DueOn, Completed, Parent todolist, Assignees).
func fetchAccountAssignments(ctx context.Context, client *basecamp.AccountClient,
	acct AccountInfo, personID int64,
) []AssignmentInfo {
	result, err := client.Reports().AssignedTodos(ctx, personID, nil)
	if err != nil {
		return nil
	}

	var assignments []AssignmentInfo
	for _, todo := range result.Todos {
		project := ""
		var projectID int64
		if todo.Bucket != nil {
			project = todo.Bucket.Name
			projectID = todo.Bucket.ID
		}
		todolist := ""
		if todo.Parent != nil {
			todolist = todo.Parent.Title
		}
		assignments = append(assignments, AssignmentInfo{
			ID:        todo.ID,
			Content:   todo.Content,
			DueOn:     todo.DueOn,
			Completed: todo.Completed,
			Account:   acct.Name,
			AccountID: acct.ID,
			Project:   project,
			ProjectID: projectID,
			Todolist:  todolist,
		})
	}
	return assignments
}

// Projects returns a global-scope pool of all projects across accounts.
// Each project carries account attribution for cross-account navigation.
// Used by Home (bookmarks), Projects view, and Dock.
func (h *Hub) Projects() *Pool[[]ProjectInfo] {
	p := RealmPool(h.Global(), "projects", func() *Pool[[]ProjectInfo] {
		return NewPool("projects", PoolConfig{
			FreshTTL: 30 * time.Second,
			StaleTTL: 5 * time.Minute,
			PollBase: 120 * time.Second,
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
	p.SetMetrics(h.metrics)
	p.SetCache(h.cache)
	return p
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
