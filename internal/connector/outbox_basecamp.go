package connector

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// BasecampPoster posts lifecycle messages through the SDK as the agent: the
// account client must be the agent's own, so every message is the agent's.
//
// A create is not idempotent, and the SDK's generated create path makes one
// attempt at it whatever its retry settings, so Post is one request. (The SDK
// does replay a mutation once after a 401 refreshes the token, which creates
// nothing.) The client given should carry no retries of its own around that.
type BasecampPoster struct {
	account *basecamp.AccountClient
	agentID int64
}

// NewBasecampPoster builds a poster over the agent's account client. agentID
// is the agent's Person id: List answers only its messages.
func NewBasecampPoster(account *basecamp.AccountClient, agentID int64) (*BasecampPoster, error) {
	if account == nil {
		return nil, errors.New("connector: the poster needs the agent's account client")
	}
	if agentID <= 0 {
		return nil, errors.New("connector: the poster needs the agent's Person id")
	}
	return &BasecampPoster{account: account, agentID: agentID}, nil
}

var _ Poster = (*BasecampPoster)(nil)

// Post creates the message.
func (p *BasecampPoster) Post(ctx context.Context, dest Destination, body string) (int64, error) {
	switch dest.Kind {
	case MessageBoost:
		boost, err := p.account.Boosts().CreateRecording(ctx, dest.RecordingID, body)
		if err != nil {
			return 0, err
		}
		return boost.ID, nil
	case MessageComment:
		comment, err := p.account.Comments().Create(ctx, dest.RecordingID, &basecamp.CreateCommentRequest{Content: body})
		if err != nil {
			return 0, err
		}
		return comment.ID, nil
	case MessageChatLine:
		line, err := p.account.Campfires().CreateLine(ctx, dest.RecordingID, body)
		if err != nil {
			return 0, err
		}
		return line.ID, nil
	}
	return 0, fmt.Errorf("connector: %q is not a message kind", dest.Kind)
}

// linePageLimit bounds how far back a chat listing pages, for both callers:
// reconciliation, which reaches back to a send made minutes ago, and the
// adopted-reply rule, which reaches back to an acknowledgement a task-length
// ago. A Campfire busier than this leaves the intent unreconciled and the
// reply unadopted — an error, not a shorter answer that would read as
// "nothing was posted".
const linePageLimit = 200

// List answers the agent's messages at the destination since the time given.
// Boosts and comments are listed whole; chat lines newest first, page by page,
// until a page reaches back past since.
func (p *BasecampPoster) List(ctx context.Context, dest Destination, since time.Time) ([]PostedMessage, error) {
	out, err := p.list(ctx, dest, since)
	if err == nil {
		return out, nil
	}
	if e := basecamp.AsError(err); e != nil && (e.Code == basecamp.CodeNotFound || e.Code == basecamp.CodeForbidden) {
		return nil, fmt.Errorf("connector: list %s at %d: %w: %w", dest.Kind, dest.RecordingID, ErrUnlistable, err)
	}
	return out, err
}

func (p *BasecampPoster) list(ctx context.Context, dest Destination, since time.Time) ([]PostedMessage, error) {
	var out []PostedMessage
	// Newest-first paging shifts lines across pages as new ones arrive, so
	// one line can be served twice; it is one message.
	seen := map[int64]bool{}
	keep := func(creator *basecamp.Person, id int64, created time.Time, content string) {
		if creator != nil && creator.ID == p.agentID && !created.Before(since) && !seen[id] {
			seen[id] = true
			out = append(out, PostedMessage{ID: id, CreatedAt: created, Content: content})
		}
	}
	switch dest.Kind {
	case MessageBoost:
		result, err := p.account.Boosts().ListRecording(ctx, dest.RecordingID, &basecamp.BoostListOptions{Limit: -1})
		if err != nil {
			return nil, err
		}
		if result.Meta.Truncated {
			return nil, errors.New("connector: the boost listing was truncated")
		}
		for _, b := range result.Boosts {
			keep(b.Booster, b.ID, b.CreatedAt, b.Content)
		}
		return out, nil
	case MessageComment:
		result, err := p.account.Comments().List(ctx, dest.RecordingID, &basecamp.CommentListOptions{Limit: -1})
		if err != nil {
			return nil, err
		}
		if result.Meta.Truncated {
			return nil, errors.New("connector: the comment listing was truncated")
		}
		for _, c := range result.Comments {
			keep(c.Creator, c.ID, c.CreatedAt, c.Content)
		}
		return out, nil
	case MessageChatLine:
		for page := 1; page <= linePageLimit; page++ {
			result, err := p.account.Campfires().ListLines(ctx, dest.RecordingID, &basecamp.CampfireLineListOptions{
				Sort: "created_at", Direction: "desc", Page: page,
			})
			if err != nil {
				return nil, err
			}
			if len(result.Lines) == 0 {
				return out, nil
			}
			reachedBack := false
			for _, l := range result.Lines {
				keep(l.Creator, l.ID, l.CreatedAt, l.Content)
				if l.CreatedAt.Before(since) {
					reachedBack = true
				}
			}
			if reachedBack {
				return out, nil
			}
		}
		return nil, fmt.Errorf("connector: the Campfire listing did not reach back to %s within %d pages: %w", since.UTC().Format(time.RFC3339), linePageLimit, ErrUnlistable)
	}
	return nil, fmt.Errorf("connector: %q is not a message kind", dest.Kind)
}
