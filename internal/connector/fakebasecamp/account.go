package fakebasecamp

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
	"time"
)

// The account reads: who a token is, who someone else is, and the projects.

// epoch is when everything in the fake was created and last updated.
var epoch = time.Date(2026, time.January, 5, 9, 0, 0, 0, time.UTC)

// personJSON is a person as Basecamp renders one.
type personJSON struct {
	ID             int64     `json:"id"`
	AttachableSGID string    `json:"attachable_sgid"`
	Name           string    `json:"name"`
	EmailAddress   string    `json:"email_address,omitempty"`
	PersonableType string    `json:"personable_type"`
	Title          string    `json:"title"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Admin          bool      `json:"admin"`
	Owner          bool      `json:"owner"`
	Client         bool      `json:"client"`
	Employee       bool      `json:"employee"`
	TimeZone       string    `json:"time_zone"`
	AvatarURL      string    `json:"avatar_url"`
	// Boss is in the person's own profile only.
	Boss    *named `json:"boss,omitempty"`
	Company *named `json:"company,omitempty"`
	CanPing bool   `json:"can_ping"`
}

type named struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func (c *call) renderPerson(p *Person) personJSON {
	kind := "User"
	if p.Agent {
		kind = "Agent"
	}
	out := personJSON{
		ID:             p.ID,
		AttachableSGID: attachableSGID(p.ID),
		Name:           p.Name,
		PersonableType: kind,
		CreatedAt:      epoch,
		UpdatedAt:      epoch,
		Client:         p.Client,
		Employee:       !p.Client && !p.Agent,
		TimeZone:       "Etc/UTC",
		AvatarURL:      c.base() + "/avatars/" + slug(p.Name),
		Company:        &named{ID: 1, Name: c.s.world.Account.Name},
		CanPing:        !p.Client,
	}
	if !p.Agent {
		out.EmailAddress = slug(p.Name) + "@example.com"
	}
	return out
}

func slug(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, " ", "."))
}

// authorization is Launchpad's authorization.json: the identity behind a
// person's token, and the accounts it reaches.
func (c *call) authorization() answer {
	caller, ok := c.s.callerLocked(c.req)
	p := c.s.world.People[caller]
	if !ok || p == nil || p.IdentityID == 0 {
		return answer{status: http.StatusUnauthorized}
	}
	first, last, _ := strings.Cut(p.Name, " ")
	a := c.s.world.Account
	return c.jsonAnswer(http.StatusOK, map[string]any{
		"expires_at": epoch.Add(24 * time.Hour * 365),
		"identity": map[string]any{
			"id": p.IdentityID, "first_name": first, "last_name": last,
			"email_address": slug(p.Name) + "@example.com",
		},
		"accounts": []map[string]any{{
			"product": "bc3", "id": a.ID, "name": a.Name,
			"href": c.accountURL(""), "app_href": c.accountURL(""),
		}},
	})
}

// profile is the caller's own profile, the one read that names their boss.
func (c *call) profile() answer {
	p, ok := c.s.world.People[c.caller]
	if !ok {
		return answer{status: http.StatusUnauthorized}
	}
	out := c.renderPerson(p)
	if boss, ok := c.s.world.People[p.BossID]; ok {
		out.Boss = &named{ID: boss.ID, Name: boss.Name}
	}
	return c.jsonAnswer(http.StatusOK, out)
}

// person is anyone in the account, unless they are readable only by others.
func (c *call) person() answer {
	id, ok := c.pathID("id")
	if !ok {
		return answer{status: http.StatusNotFound}
	}
	p, ok := c.s.world.People[id]
	if !ok {
		return answer{status: http.StatusNotFound}
	}
	if p.ReadableBy != nil && !slices.Contains(p.ReadableBy, c.caller) {
		return answer{status: http.StatusForbidden}
	}
	return c.jsonAnswer(http.StatusOK, c.renderPerson(p))
}

// projectJSON is a project as Basecamp renders one, with its dock.
type projectJSON struct {
	ID             int64      `json:"id"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	Purpose        string     `json:"purpose"`
	ClientsEnabled bool       `json:"clients_enabled"`
	BookmarkURL    string     `json:"bookmark_url"`
	URL            string     `json:"url"`
	AppURL         string     `json:"app_url"`
	Dock           []dockJSON `json:"dock"`
	Bookmarked     bool       `json:"bookmarked"`
}

type dockJSON struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Position *int   `json:"position"`
	URL      string `json:"url"`
	AppURL   string `json:"app_url"`
}

func (c *call) renderProject(p *Project) projectJSON {
	out := projectJSON{
		ID:          p.ID,
		Status:      "active",
		CreatedAt:   epoch,
		UpdatedAt:   epoch,
		Name:        p.Name,
		Purpose:     "topic",
		BookmarkURL: c.accountURL("my/bookmarks/%d.json", p.ID),
		URL:         c.accountURL("projects/%d.json", p.ID),
		AppURL:      c.accountURL("projects/%d", p.ID),
		Dock:        []dockJSON{},
	}
	// A Campfire is docked as "chat": that is where a line's Campfire is
	// found first.
	for _, r := range c.s.sortedRecordings() {
		if r.BucketID == p.ID && r.Type == campfireType {
			position := len(out.Dock) + 1
			out.Dock = append(out.Dock, dockJSON{
				ID: r.ID, Title: r.Title, Name: "chat", Enabled: true, Position: &position,
				URL: c.accountURL("chats/%d.json", r.ID), AppURL: c.accountURL("chats/%d", r.ID),
			})
		}
	}
	return out
}

// projects is every project the caller is on.
func (c *call) projects() answer {
	out := []projectJSON{}
	for _, p := range c.s.sortedProjects() {
		if c.s.world.member(p.ID, c.caller) {
			out = append(out, c.renderProject(p))
		}
	}
	return c.jsonAnswer(http.StatusOK, out)
}

// visibleProject is a project the caller is on; a project they are not on
// is not found, as Basecamp answers it.
func (c *call) visibleProject() (*Project, bool) {
	id, ok := c.pathID("id")
	if !ok || !c.s.world.member(id, c.caller) {
		return nil, false
	}
	return c.s.world.Projects[id], true
}

func (c *call) project() answer {
	p, ok := c.visibleProject()
	if !ok {
		return answer{status: http.StatusNotFound}
	}
	return c.jsonAnswer(http.StatusOK, c.renderProject(p))
}

func (c *call) projectPeople() answer {
	p, ok := c.visibleProject()
	if !ok {
		return answer{status: http.StatusNotFound}
	}
	out := []personJSON{}
	for _, id := range p.Members {
		if person, ok := c.s.world.People[id]; ok {
			out = append(out, c.renderPerson(person))
		}
	}
	return c.jsonAnswer(http.StatusOK, out)
}

// sortedProjects is the projects in id order, so every listing is stable.
func (s *Server) sortedProjects() []*Project {
	out := make([]*Project, 0, len(s.world.Projects))
	for _, p := range s.world.Projects {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b *Project) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// sortedRecordings is the recordings in id order.
func (s *Server) sortedRecordings() []*Recording {
	out := make([]*Recording, 0, len(s.world.Recordings))
	for _, r := range s.world.Recordings {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b *Recording) int { return cmp.Compare(a.ID, b.ID) })
	return out
}
