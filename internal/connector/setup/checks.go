package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// PersonableAgent is the personable type Basecamp gives an Agent person.
const PersonableAgent = "Agent"

// Check statuses, the doctor command's vocabulary.
const (
	StatusPass = "pass"
	StatusFail = "fail"
	StatusWarn = "warn"
	StatusSkip = "skip"
)

// Check is one setup check's result.
type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// Person is the part of a Basecamp person the checks look at.
type Person struct {
	ID             int64
	Name           string
	PersonableType string
	Client         bool
}

// Reader is what the checks read, as the agent, in the agent's account.
type Reader interface {
	// Me is the person the credential authenticates as.
	Me(ctx context.Context) (Person, error)
	// Person reads someone else in the account.
	Person(ctx context.Context, id int64) (Person, error)
	// MintStreamTicket mints a stream ticket and throws it away. The ticket
	// is a bearer credential: an implementation must neither return, log nor
	// keep it.
	MintStreamTicket(ctx context.Context) error
	// Project reads a project.
	Project(ctx context.Context, id int64) error
	// ProjectPeople reads a project's people.
	ProjectPeople(ctx context.Context, id int64) error
}

// ErrMalformedTicket reports a mint that answered without a ticket or URL.
var ErrMalformedTicket = errors.New("the stream ticket response carries no ticket or no URL")

// SDKReader is Reader over an SDK account client. Build the client without
// request hooks or a debug logger: they log request URLs, and a feed URL can
// carry a position or a ticket.
type SDKReader struct {
	Client *basecamp.AccountClient
}

// Me implements Reader.
func (r SDKReader) Me(ctx context.Context) (Person, error) {
	p, err := r.Client.People().Me(ctx)
	if err != nil {
		return Person{}, err
	}
	return fromSDK(p), nil
}

// Person implements Reader.
func (r SDKReader) Person(ctx context.Context, id int64) (Person, error) {
	p, err := r.Client.People().Get(ctx, id)
	if err != nil {
		return Person{}, err
	}
	return fromSDK(p), nil
}

// streamTicketPath is the account feed's ticket mint (bc3 13049).
const streamTicketPath = "/events/stream_ticket.json"

// MintStreamTicket implements Reader.
func (r SDKReader) MintStreamTicket(ctx context.Context) error {
	resp, err := r.Client.Post(ctx, streamTicketPath, nil)
	if err != nil {
		return err
	}
	var minted struct {
		Ticket string `json:"ticket"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(resp.Data, &minted); err != nil || minted.Ticket == "" || minted.URL == "" {
		return ErrMalformedTicket
	}
	return nil
}

// Project implements Reader.
func (r SDKReader) Project(ctx context.Context, id int64) error {
	_, err := r.Client.Projects().Get(ctx, id)
	return err
}

// ProjectPeople implements Reader.
func (r SDKReader) ProjectPeople(ctx context.Context, id int64) error {
	_, err := r.Client.People().ListProjectPeople(ctx, id, nil)
	return err
}

func fromSDK(p *basecamp.Person) Person {
	if p == nil {
		return Person{}
	}
	return Person{ID: p.ID, Name: p.Name, PersonableType: p.PersonableType, Client: p.Client}
}

// httpStatus is the status a failed read answered with, 0 when it did not
// reach an answer.
func httpStatus(err error) int {
	var apiErr *basecamp.Error
	if errors.As(err, &apiErr) {
		return apiErr.HTTPStatus
	}
	return 0
}

func refused(err error) bool {
	switch httpStatus(err) {
	case http.StatusForbidden, http.StatusNotFound:
		return true
	}
	return false
}

// agentReadsRefused is what setup says when Basecamp refuses a read admission
// makes to an Agent identity. The connector would take events off the feed
// and block every one of them on a read it can never make, which from the
// outside looks like an agent that ignores its mentions.
const agentReadsRefused = "Basecamp refuses this read to an Agent identity today, and admission makes it for every event: " +
	"the connector would see mentions and block each one on a read it cannot make"

const agentReadsRefusedHint = "Until Basecamp allows Agent identities these reads, run the connector as a bot user: " +
	"basecamp connect setup -P <bot-profile> --expect-identity <identity-id>"

// TicketCheck mints a stream ticket as the agent.
func TicketCheck(ctx context.Context, r Reader, kind string) Check {
	c := Check{Name: "Stream ticket"}
	err := r.MintStreamTicket(ctx)
	switch {
	case err == nil:
		c.Status, c.Message = StatusPass, "The agent can mint a ticket for the account event feed"
	case refused(err) || httpStatus(err) == http.StatusUnauthorized:
		c.Status = StatusFail
		c.Message = fmt.Sprintf("Basecamp refused the ticket mint (HTTP %d): the connector could not open the account feed", httpStatus(err))
		if kind == KindAgent {
			c.Hint = "The account event feed has to be enabled for this agent's account and principal."
		} else {
			c.Hint = "The account event feed has to be enabled for this account."
		}
	default:
		c.Status, c.Message = StatusFail, "The ticket mint failed: "+safeError(err)
	}
	return c
}

// Trust is who connect.json says may drive the agent, as setup is about to
// write it, and how each person in it came to be named.
type Trust struct {
	// Operator is the operator. When OperatorProfile is set, it is the person
	// that profile's own credential read back, which proves who they are.
	Operator        Person
	OperatorProfile string
	// Allowlist is every allowlisted Person id the file will hold.
	Allowlist []int64
	// Recorded is the trust connect.json already holds for this same agent
	// and account. It is taken as the owner's own record — the file is
	// private to them, and setup verified it if setup wrote it — so a person
	// in it who cannot be re-read now is a warning, not a refusal. A read
	// that answers still decides. The zero value records nobody.
	Recorded admission.Trust
}

// VerifyTrust is the one check of the trust anchor every setup goes
// through before connect.json is written. A failed result means nothing may
// be written. The properties, each checked here:
//
//  1. The operator and every allowlisted id are Person ids (positive).
//  2. None of them is the agent itself: the agent's own id never authorizes.
//  3. Each was verified as a person in the agent's account: the operator
//     by their own profile's credential, or else by reading them; every
//     allowlisted id by reading it. A read that is refused or fails refuses
//     the id, unless connect.json already records that same person in that
//     same role, which was verified when it was written.
//  4. None of them reads back as an Agent person: agents never authorize.
//  5. None of them reads back as a client of the account.
//  6. A read answers for the id that was asked for.
//
// people reads persons in the account; pass a reader built on the operator's
// own credential when there is one, since Basecamp refuses person reads to
// an Agent identity today.
func VerifyTrust(ctx context.Context, people Reader, t Trust, agentID int64) []Check {
	ids := slices.Clone(t.Allowlist)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	checks := make([]Check, 0, 1+len(ids))
	checks = append(checks, verifyPerson(ctx, people, "Operator", t.Operator, agentID, t.OperatorProfile, t.Recorded.OperatorID == t.Operator.ID && t.Operator.ID > 0))
	for _, id := range ids {
		name := fmt.Sprintf("Allowlist %d", id)
		checks = append(checks, verifyPerson(ctx, people, name, Person{ID: id}, agentID, "", slices.Contains(t.Recorded.AllowlistIDs, id)))
	}
	return checks
}

func verifyPerson(ctx context.Context, r Reader, name string, p Person, agentID int64, fromProfile string, recorded bool) Check {
	c := Check{Name: name}
	switch {
	case p.ID <= 0:
		c.Status, c.Message = StatusFail, fmt.Sprintf("%d is not a Person id", p.ID)
		return c
	case p.ID == agentID:
		c.Status, c.Message = StatusFail, fmt.Sprintf("Person %d is the agent itself; the agent's own id never authorizes", p.ID)
		return c
	}
	source := ""
	if fromProfile != "" {
		source = fmt.Sprintf(", the identity of profile %q", fromProfile)
	} else {
		read, err := r.Person(ctx, p.ID)
		switch {
		case err != nil && recorded:
			c.Status = StatusWarn
			c.Message = fmt.Sprintf("Person %d, already recorded in connect.json, could not be re-read: %s", p.ID, unreadReason(err))
			c.Hint = "It was verified when it was recorded. To verify the operator again, pass --operator-profile <profile>."
			return c
		case err != nil:
			c.Status = StatusFail
			c.Message = fmt.Sprintf("Person %d could not be read (%s), so it cannot be verified", p.ID, unreadReason(err))
			c.Hint = "Pass --operator-profile <profile>: people are read through the operator's own credential, which Basecamp does not refuse."
			return c
		case read.ID != p.ID:
			c.Status, c.Message = StatusFail, fmt.Sprintf("Reading person %d answered with person %d", p.ID, read.ID)
			return c
		}
		p = read
	}
	switch {
	case p.PersonableType == PersonableAgent:
		c.Status, c.Message = StatusFail, fmt.Sprintf("Person %d is an Agent; agents never authorize", p.ID)
	case p.Client:
		c.Status, c.Message = StatusFail, fmt.Sprintf("Person %d is a client of the account; only members are trusted", p.ID)
	default:
		c.Status = StatusPass
		c.Message = fmt.Sprintf("Person %d, %s%s", p.ID, richtext.SanitizeSingleLine(p.Name), source)
	}
	return c
}

func unreadReason(err error) string {
	if status := httpStatus(err); status != 0 {
		return fmt.Sprintf("HTTP %d", status)
	}
	return safeError(err)
}

// RouteChecks reads each routed project the way admission will, as the
// agent: the project, and its people (project trust mode's membership read).
func RouteChecks(ctx context.Context, r Reader, f File) []Check {
	if len(f.Projects) == 0 {
		return []Check{{
			Name:    "Routes",
			Status:  StatusFail,
			Message: "No project is routed: every mention would get a holding reply and no work",
			Hint:    "Add one: basecamp connect setup -P " + f.Profile + " --route <project-id>=<dir>",
		}}
	}
	ids := make([]int64, 0, len(f.Projects))
	for id := range f.Projects {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	checks := make([]Check, 0, len(ids))
	for _, id := range ids {
		checks = append(checks, routeCheck(ctx, r, f.Agent.Kind, id, f.Projects[id].Path))
	}
	return checks
}

func routeCheck(ctx context.Context, r Reader, kind string, id int64, path string) Check {
	c := Check{Name: fmt.Sprintf("Project %d", id)}
	for _, read := range []struct {
		what string
		run  func(context.Context, int64) error
	}{
		{"the project", r.Project},
		{"the project's people", r.ProjectPeople},
	} {
		err := read.run(ctx, id)
		if err == nil {
			continue
		}
		c.Status = StatusFail
		switch {
		case refused(err) && kind == KindAgent:
			c.Message = fmt.Sprintf("Reading %s was refused (HTTP %d). %s", read.what, httpStatus(err), agentReadsRefused)
			c.Hint = "If the agent is not on this project, add it there. Otherwise: " + agentReadsRefusedHint
		case refused(err):
			c.Message = fmt.Sprintf("Reading %s was refused (HTTP %d): the agent cannot see project %d", read.what, httpStatus(err), id)
			c.Hint = "Add the agent to the project in Basecamp, then run setup again."
		default:
			c.Message = fmt.Sprintf("Reading %s failed: %s", read.what, safeError(err))
		}
		return c
	}
	c.Status = StatusPass
	c.Message = "Readable by the agent, routed to " + richtext.SanitizeSingleLine(path)
	return c
}

// safeError is an error's text for a one-line terminal sink. Server-supplied
// text is reduced to one line; the checks never put a response body in an
// error, so no ticket can reach it.
func safeError(err error) string {
	return richtext.SanitizeSingleLine(err.Error())
}
