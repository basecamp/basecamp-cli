package fakebasecamp

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// The recording reads admission makes: the typed read a recording summary
// is made from, the agent's subscription, and a recording's own history.
// Each is served only to a member of the recording's project; anyone else
// is answered 404, as Basecamp answers a recording they cannot see.

const (
	campfireType   = "Chat::Transcript"
	chatLinePrefix = "Chat::Lines::"
	// recordingEventsPage is how many history entries a page carries.
	recordingEventsPage = 15
)

// recordingRead serves the typed read for one recording type. The read
// answers 404 for a recording of another type, as Basecamp's does.
func recordingRead(typ string) func(c *call) answer {
	return func(c *call) answer {
		r, ok := c.visibleRecording("id")
		if !ok || r.Type != typ {
			return answer{status: http.StatusNotFound}
		}
		return c.jsonAnswer(http.StatusOK, c.renderRecording(r))
	}
}

// visibleRecording is the recording a path value names, when the caller is
// on its project.
func (c *call) visibleRecording(name string) (*Recording, bool) {
	id, ok := c.pathID(name)
	if !ok {
		return nil, false
	}
	r, ok := c.s.world.Recordings[id]
	if !ok || !c.s.world.member(r.BucketID, c.caller) {
		return nil, false
	}
	return r, true
}

// renderRecording is a recording as its typed read renders it. The members
// each type keeps its rich text in differ, and are the ones that matter: a
// to-do's title is plain and its description rich, a card's content is.
func (c *call) renderRecording(r *Recording) map[string]any {
	status := r.Status
	if status == "" {
		status = "active"
	}
	out := map[string]any{
		"id":                 r.ID,
		"status":             status,
		"visible_to_clients": false,
		"created_at":         epoch,
		"updated_at":         epoch,
		"title":              r.Title,
		"inherits_status":    true,
		"type":               r.Type,
		"url":                c.recordingURL(r) + ".json",
		"app_url":            c.recordingURL(r),
		"bookmark_url":       c.accountURL("my/bookmarks/%d.json", r.ID),
		"bucket":             c.renderBucket(r.BucketID),
	}
	if creator, ok := c.s.world.People[r.CreatorID]; ok {
		out["creator"] = c.renderPerson(creator)
	}
	if parent, ok := c.s.world.Recordings[r.ParentID]; ok {
		out["parent"] = map[string]any{
			"id": parent.ID, "title": parent.Title, "type": parent.Type,
			"url": c.recordingURL(parent) + ".json", "app_url": c.recordingURL(parent),
		}
	}
	switch {
	case r.Type == "Todo":
		out["content"] = r.Title
		out["description"] = r.Content
		out["completed"] = r.Completed
		out["assignees"] = c.renderPeople(r.AssigneeIDs)
		out["completion_subscribers"] = []personJSON{}
	case r.Type == "Kanban::Card":
		out["content"] = r.Content
		out["completed"] = r.Completed
		out["assignees"] = c.renderPeople(r.AssigneeIDs)
	case r.Type == "Message":
		out["subject"] = r.Title
		out["content"] = r.Content
	case r.Type == "Comment", strings.HasPrefix(r.Type, chatLinePrefix):
		out["content"] = r.Content
	}
	return out
}

func (c *call) renderBucket(id int64) map[string]any {
	name := ""
	if p, ok := c.s.world.Projects[id]; ok {
		name = p.Name
	}
	return map[string]any{"id": id, "name": name, "type": "Project"}
}

func (c *call) renderPeople(ids []int64) []personJSON {
	out := []personJSON{}
	for _, id := range ids {
		if p, ok := c.s.world.People[id]; ok {
			out = append(out, c.renderPerson(p))
		}
	}
	return out
}

// recordingURL is where a recording lives, under its type's route.
func (c *call) recordingURL(r *Recording) string {
	switch {
	case r.Type == "Comment":
		return c.accountURL("comments/%d", r.ID)
	case r.Type == "Message":
		return c.accountURL("messages/%d", r.ID)
	case r.Type == "Todo":
		return c.accountURL("todos/%d", r.ID)
	case r.Type == "Kanban::Card":
		return c.accountURL("card_tables/cards/%d", r.ID)
	case r.Type == campfireType:
		return c.accountURL("chats/%d", r.ID)
	case strings.HasPrefix(r.Type, chatLinePrefix):
		return c.accountURL("chats/%d/lines/%d", r.ParentID, r.ID)
	default:
		return c.accountURL("recordings/%d", r.ID)
	}
}

// campfires is every Campfire the caller can see: where a line is looked
// for when its project's dock does not hold it.
func (c *call) campfires() answer {
	out := []map[string]any{}
	for _, r := range c.s.sortedRecordings() {
		if r.Type == campfireType && c.s.world.member(r.BucketID, c.caller) {
			out = append(out, c.renderRecording(r))
		}
	}
	return c.jsonAnswer(http.StatusOK, out)
}

// chatLine is a line, read under the Campfire it belongs to.
func (c *call) chatLine() answer {
	campfire, ok := c.visibleRecording("campfire")
	if !ok || campfire.Type != campfireType {
		return answer{status: http.StatusNotFound}
	}
	line, ok := c.visibleRecording("id")
	if !ok || !strings.HasPrefix(line.Type, chatLinePrefix) || line.ParentID != campfire.ID {
		return answer{status: http.StatusNotFound}
	}
	return c.jsonAnswer(http.StatusOK, c.renderRecording(line))
}

// subscription is whether the caller is subscribed to a recording.
func (c *call) subscription() answer {
	r, ok := c.visibleRecording("id")
	if !ok {
		return answer{status: http.StatusNotFound}
	}
	return c.jsonAnswer(http.StatusOK, map[string]any{
		"subscribed":  slices.Contains(r.SubscriberIDs, c.caller),
		"count":       len(r.SubscriberIDs),
		"url":         c.accountURL("recordings/%d/subscription.json", r.ID),
		"subscribers": c.renderPeople(r.SubscriberIDs),
	})
}

// recordingEvents is a recording's history, newest first, a page at a time.
func (c *call) recordingEvents() answer {
	r, ok := c.visibleRecording("id")
	if !ok {
		return answer{status: http.StatusNotFound}
	}
	page := 1
	if raw := c.req.URL.Query().Get("page"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return answer{status: http.StatusBadRequest}
		}
		page = n
	}
	newest := slices.Clone(r.Events)
	slices.Reverse(newest)
	out := []map[string]any{}
	for i := (page - 1) * recordingEventsPage; i < len(newest) && i < page*recordingEventsPage; i++ {
		ev := newest[i]
		createdAt := ev.CreatedAt
		if createdAt.IsZero() {
			createdAt = epoch
		}
		row := map[string]any{
			"id":           ev.ID,
			"recording_id": r.ID,
			"action":       ev.Action,
			"created_at":   createdAt,
		}
		if ev.AddedPersonIDs != nil {
			row["details"] = map[string]any{"added_person_ids": ev.AddedPersonIDs}
		}
		if creator, ok := c.s.world.People[ev.CreatorID]; ok {
			row["creator"] = c.renderPerson(creator)
		}
		if performer, ok := c.s.world.People[ev.PerformedByID]; ok {
			row["performed_by"] = c.renderPerson(performer)
		}
		out = append(out, row)
	}
	return c.jsonAnswer(http.StatusOK, out)
}
