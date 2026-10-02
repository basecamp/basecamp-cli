package fakebasecamp

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"time"
)

// The default world's facts. The ids are the shapes Basecamp uses; none of
// them, and none of the secrets, names anything real.
const (
	AccountID   int64 = 999
	AccountName       = "Acme"

	OperatorID   int64 = 26909558
	OperatorName       = "Operator"
	AgentID      int64 = 52007412
	AgentName          = "Marie Chef"
	ProjectID    int64 = 48699913
	ProjectName        = "Connector"

	// AgentClientID and AgentSecret are the agent's OAuth client as the
	// default world starts. The connection ceremony rotates the secret, as
	// Basecamp does, so read the current one from the World after it.
	AgentClientID = "agent-client"
	AgentSecret   = "not-a-real-secret" //nolint:gosec // G101: the fake's placeholder, not a credential

	// OperatorToken is a bearer token for the operator, as a completed
	// person login would have stored.
	OperatorToken = "operator-token"
)

// Scopes a credential can carry.
const (
	ScopeFull = "full"
	ScopeRead = "read"
)

// World is everything the fake knows. The maps are keyed by the id of the
// value they hold. A World passed to Start, and every pointer in it, belongs
// to the Server from then on: change it only inside Update.
type World struct {
	Account Account

	// People is everyone in the account, agents included.
	People map[int64]*Person
	// Projects are the account's projects.
	Projects map[int64]*Project
	// Recordings are the recordings in the projects: comments, messages,
	// to-dos, cards, Campfires and their lines.
	Recordings map[int64]*Recording

	// Agents are the OAuth clients that mint client_credentials tokens,
	// keyed by client id.
	Agents map[string]*AgentClient
	// Tokens are the bearer tokens the fake accepts, keyed by the token.
	// Minting adds to it; a test adds a person's own login here.
	Tokens map[string]*Token

	// Connection is what the operator approves on the agent connection
	// page, for every ceremony that reaches it.
	Connection Connection
}

// Account is the one Basecamp account the fake serves.
type Account struct {
	ID   int64
	Name string
}

// Person is someone in the account.
type Person struct {
	ID   int64
	Name string
	// Agent makes the person an Agent principal; otherwise a User.
	Agent bool
	// Client marks a client of the account.
	Client bool
	// BossID is who the person works for, as their own profile names them:
	// a personal agent's owner. Zero names nobody.
	BossID int64
	// IdentityID is the Launchpad identity authorization.json answers for
	// the person's token. Zero answers 401, as it does for an agent.
	IdentityID int64
	// ReadableBy, when not nil, is everyone whose read of this person
	// succeeds; anyone else is refused with 403.
	ReadableBy []int64
}

// Project is a project, and who is on it.
type Project struct {
	ID      int64
	Name    string
	Members []int64
}

// Recording is one recording in a project.
type Recording struct {
	ID int64
	// Type is Basecamp's recording type: Comment, Message, Todo,
	// Kanban::Card, Chat::Transcript (a Campfire), or a Chat::Lines:: line.
	Type     string
	BucketID int64
	// ParentID is the recording this one belongs to: a comment's parent,
	// a line's Campfire.
	ParentID  int64
	CreatorID int64
	Title     string
	// Content is the rich text mentions live in: a to-do's description, a
	// card's content, a comment's or a line's body.
	Content       string
	AssigneeIDs   []int64
	SubscriberIDs []int64
	Completed     bool
	// Status is "active" when empty.
	Status string
	// Events is the recording's own history, oldest first. Emit appends an
	// event about the recording to it.
	Events []RecordingEvent
}

// RecordingEvent is one entry in a recording's history.
type RecordingEvent struct {
	ID            int64
	Action        string
	CreatorID     int64
	PerformedByID int64
	// CreatedAt is the fake's epoch when zero.
	CreatedAt time.Time
	// AddedPersonIDs is an assignment change's delta; nil carries no
	// details at all.
	AddedPersonIDs []int64
}

// AgentClient is an agent's confidential OAuth client.
type AgentClient struct {
	ID     string
	Secret string
	// PersonID is the Agent its tokens act as.
	PersonID int64
	// Scope is what the client was approved for.
	Scope string
	// TokenLifetime is how long the tokens it mints say they last, in
	// whole seconds of at least one; zero is Basecamp's hour. The fake
	// does not retire a token when it runs out: the client renews by what
	// it was told, and a test can wait for that to come due.
	TokenLifetime time.Duration
}

// Token is what a bearer token authenticates as.
type Token struct {
	PersonID int64
	Scope    string
	// ClientID is the agent client that minted it; empty for a person's
	// own login.
	ClientID string
}

// Connection is the operator's approval on the agent connection page.
type Connection struct {
	// ClientID is the agent the ceremony hands over.
	ClientID string
	// Scope is the access the operator approves; empty approves full.
	Scope string
}

// DefaultWorld is an account with an operator, an agent connected to its
// OAuth client, and one project they are both on.
func DefaultWorld() *World {
	return &World{
		Account: Account{ID: AccountID, Name: AccountName},
		People: map[int64]*Person{
			OperatorID: {ID: OperatorID, Name: OperatorName},
			AgentID:    {ID: AgentID, Name: AgentName, Agent: true},
		},
		Projects: map[int64]*Project{
			ProjectID: {ID: ProjectID, Name: ProjectName, Members: []int64{OperatorID, AgentID}},
		},
		Recordings: map[int64]*Recording{},
		Agents: map[string]*AgentClient{
			AgentClientID: {ID: AgentClientID, Secret: AgentSecret, PersonID: AgentID, Scope: ScopeFull},
		},
		Tokens: map[string]*Token{
			OperatorToken: {PersonID: OperatorID, Scope: ScopeFull},
		},
		Connection: Connection{ClientID: AgentClientID},
	}
}

// Mention is the rich-text attachment that mentions a person, as Basecamp
// renders one: the person is named only inside the attachment's sgid.
func Mention(personID int64) string {
	return `<bc-attachment sgid="` + attachableSGID(personID) + `" content-type="application/vnd.basecamp.mention"></bc-attachment>`
}

func attachableSGID(personID int64) string {
	payload := `{"_rails":{"data":"gid://bc3/Person/` + strconv.FormatInt(personID, 10) + `","pur":"attachable"}}`
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// check reports the first value filed under a key that is not its own id.
func (w *World) check() error {
	if w.Account.ID <= 0 {
		return fmt.Errorf("the account id %d is not an id", w.Account.ID)
	}
	for id, p := range w.People {
		if p == nil || p.ID != id {
			return fmt.Errorf("people[%d] does not hold person %d", id, id)
		}
	}
	for id, p := range w.Projects {
		if p == nil || p.ID != id {
			return fmt.Errorf("projects[%d] does not hold project %d", id, id)
		}
	}
	for id, r := range w.Recordings {
		if r == nil || r.ID != id {
			return fmt.Errorf("recordings[%d] does not hold recording %d", id, id)
		}
	}
	for id, c := range w.Agents {
		if c == nil || c.ID != id {
			return fmt.Errorf("agents[%q] does not hold client %q", id, id)
		}
	}
	for token, t := range w.Tokens {
		if t == nil {
			return fmt.Errorf("tokens[%q] holds nothing", token)
		}
	}
	return nil
}

// member reports whether a person is on a project. A project that does not
// exist has no members.
func (w *World) member(projectID, personID int64) bool {
	p, ok := w.Projects[projectID]
	if !ok {
		return false
	}
	for _, id := range p.Members {
		if id == personID {
			return true
		}
	}
	return false
}

// isAgent reports whether a person is an Agent principal.
func (w *World) isAgent(personID int64) bool {
	p, ok := w.People[personID]
	return ok && p.Agent
}
