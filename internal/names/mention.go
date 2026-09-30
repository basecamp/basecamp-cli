package names

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// ProjectScope yields the project whose agents an @mention may reach. The
// resolver calls it only when the pingable set cannot answer on its own, so
// a mention of a pingable person never pays for resolving the project. A nil
// scope, or one that yields 0, means no project is in scope.
type ProjectScope func(context.Context) (int64, error)

// ScopeError reports that a mention's project scope could not be resolved.
// It is distinct from a person lookup miss, which callers may downgrade.
type ScopeError struct{ Err error }

func (e *ScopeError) Error() string { return e.Err.Error() }
func (e *ScopeError) Unwrap() error { return e.Err }

// ResolveMentionByName resolves a fuzzy @Name mention.
//
// People resolve against the pingable set, exactly as ResolvePersonByName
// does. Agents cannot be pinged by design, so they are never in that set;
// when scope names a project, the agents on that project are candidates too.
// Only agents are drawn from the project: a person who is not pingable stays
// unmentionable.
//
// A name that equals an agent's (ignoring case) wins over a person whose name
// merely contains it, so "@Quincy" reaches the agent Quincy rather than a
// pingable "Quincy Jones". Otherwise people keep precedence, and an agent is
// matched by partial name only when no person matches at all.
func (r *Resolver) ResolveMentionByName(ctx context.Context, input string, scope ProjectScope) (*Person, error) {
	pingable, err := r.getPingable(ctx)
	if err != nil {
		return nil, err
	}

	match, matches := resolve(input, pingable, personIDName)
	if match != nil && strings.EqualFold(match.Name, input) {
		return match, nil
	}
	if len(matches) > 1 && strings.EqualFold(matches[0].Name, input) {
		return nil, ambiguousPeople(matches)
	}

	agents, err := r.scopedAgents(ctx, scope)
	if err != nil {
		// A scope that cannot be resolved is always an error. A failed fetch
		// of its people fails only when agents were the sole remaining
		// answer: a mention the pingable set already matches loses just the
		// exact-agent preference.
		var scopeErr *ScopeError
		if errors.As(err, &scopeErr) || (match == nil && len(matches) == 0) {
			return nil, err
		}
		agents = nil
	}

	// Agents with the same name, ignoring case, are ambiguous even when one
	// matches exactly: resolve would pick the first, and the other is as
	// much the agent meant.
	var namesakes []Person
	for _, a := range agents {
		if strings.EqualFold(a.Name, input) {
			namesakes = append(namesakes, a)
		}
	}
	if len(namesakes) > 1 {
		return nil, ambiguousPeople(namesakes)
	}

	agent, agentMatches := resolve(input, agents, personIDName)
	switch {
	case len(namesakes) == 1:
		return &namesakes[0], nil
	case match != nil:
		return match, nil
	case len(matches) > 1:
		return nil, ambiguousPeople(matches)
	case agent != nil:
		return agent, nil
	case len(agentMatches) > 1:
		return nil, ambiguousPeople(agentMatches)
	}

	candidates := append(append([]Person{}, pingable...), agents...)
	suggestions := suggest(input, candidates, func(p Person) string { return p.Name })
	if len(suggestions) > 0 {
		return nil, output.ErrNotFoundHint("Person", input, "Did you mean: "+strings.Join(suggestions, ", "))
	}
	return nil, output.ErrNotFound("Person", input)
}

func (r *Resolver) scopedAgents(ctx context.Context, scope ProjectScope) ([]Person, error) {
	if scope == nil {
		return nil, nil
	}
	projectID, err := scope(ctx)
	if err != nil {
		return nil, &ScopeError{Err: err}
	}
	if projectID == 0 {
		return nil, nil
	}
	return r.getProjectAgents(ctx, projectID)
}

// ResolveMentionByID resolves a [@Name](person:ID) mention. A pingable person
// resolves from the cached pingable set. Any other ID is looked up directly
// and accepted only if it is an agent, which is the one kind of principal the
// pingable set leaves out by design.
func (r *Resolver) ResolveMentionByID(ctx context.Context, id int64) (*Person, error) {
	person, err := r.ResolvePersonByID(ctx, id)
	var cliErr *output.Error
	if err == nil || !errors.As(err, &cliErr) || cliErr.Code != output.CodeNotFound || cliErr.HTTPStatus != 0 {
		return person, err
	}
	notFound := err

	r.mu.RLock()
	cached, ok := r.agentsByID[id]
	r.mu.RUnlock()
	if ok {
		return cached, nil
	}

	p, err := r.forAccount().People().Get(ctx, id)
	if err != nil {
		converted := convertSDKError(err)
		var e *output.Error
		if errors.As(converted, &e) && e.HTTPStatus == 404 {
			return nil, notFound
		}
		return nil, converted
	}
	if p.PersonableType != personableAgent {
		return nil, notFound
	}
	agent := &Person{
		ID:             p.ID,
		AttachableSGID: p.AttachableSGID,
		Name:           p.Name,
		Email:          p.EmailAddress,
		PersonableType: p.PersonableType,
	}

	r.mu.Lock()
	if r.agentsByID == nil {
		r.agentsByID = make(map[int64]*Person)
	}
	r.agentsByID[id] = agent
	r.mu.Unlock()
	return agent, nil
}

const personableAgent = "Agent"

func personIDName(p Person) (int64, string) { return p.ID, p.Name }

func ambiguousPeople(matches []Person) error {
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m.Name
	}
	return output.ErrAmbiguous("person", names)
}

// getProjectAgents returns the agents on a project, fetched once per project
// per run. The project's people list is the one directory that includes
// agents together with the attachable SGID a mention needs.
func (r *Resolver) getProjectAgents(ctx context.Context, projectID int64) ([]Person, error) {
	r.mu.RLock()
	agents, ok := r.agents[projectID]
	failed := r.agentErrs[projectID]
	r.mu.RUnlock()
	if ok || failed != nil {
		return agents, failed
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if agents, ok := r.agents[projectID]; ok {
		return agents, nil
	}
	if failed := r.agentErrs[projectID]; failed != nil {
		return nil, failed
	}

	result, err := r.forAccount().People().ListProjectPeople(ctx, projectID, nil)
	if err != nil {
		// Remember the failure for the run: a mention the pingable set
		// already matches tolerates it, and each such mention must not retry.
		if r.agentErrs == nil {
			r.agentErrs = make(map[int64]error)
		}
		r.agentErrs[projectID] = convertSDKError(err)
		return nil, r.agentErrs[projectID]
	}

	agents = []Person{}
	for _, p := range result.People {
		if p.PersonableType == personableAgent {
			agents = append(agents, Person{
				ID:             p.ID,
				AttachableSGID: p.AttachableSGID,
				Name:           p.Name,
				Email:          p.EmailAddress,
				PersonableType: p.PersonableType,
			})
		}
	}

	if r.agents == nil {
		r.agents = make(map[int64][]Person)
	}
	r.agents[projectID] = agents
	return agents, nil
}

// ParseProjectScope returns a scope for a project given by numeric ID, or nil
// when the value is empty or not an ID.
func ParseProjectScope(projectID string) ProjectScope {
	id, err := strconv.ParseInt(projectID, 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	return func(context.Context) (int64, error) { return id, nil }
}
