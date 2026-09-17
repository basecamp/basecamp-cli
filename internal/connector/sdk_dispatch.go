package connector

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// AdoptionScanLimit bounds a reply listing: the adopted-reply rule needs the
// replies after an acknowledgement, not a conversation's whole history, and a
// settlement must not page a busy Campfire from its beginning.
const AdoptionScanLimit = 500

// AdoptionScanTimeout bounds the listing in time as well, since settlement
// runs on a context a shutdown does not cancel.
const AdoptionScanTimeout = 30 * time.Second

// ErrRepliesTruncated is a listing the scan limit cut short. The adopted-reply
// rule needs to know there is exactly one candidate, and a cut listing cannot
// say that, so nothing is adopted.
var ErrRepliesTruncated = errors.New("the reply listing was truncated")

// SanitizeWorkerServerEnv is what a connector-started MCP server does to its
// own environment before it authenticates or starts anything: it keeps the
// variables the connector declared for it and unsets the rest.
//
// The connector hands each MCP server an explicit environment, but an agent
// may add its own to that — Claude Code hands its MCP servers the agent's
// whole environment, which carries the agent's own credentials (the ACP spike
// measured 63 variables, a messaging token among them). What the connector
// cannot control on the way in, its own server drops on arrival, so an
// agent's key never reaches this process's children or its credential
// helpers. It reports the names it removed, for the log.
func SanitizeWorkerServerEnv() []string {
	keep := map[string]bool{}
	for _, name := range append(append([]string{}, driver.BaseEnv...), MCPServerEnv...) {
		keep[name] = true
	}
	var removed []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == "" || keep[name] {
			continue
		}
		if err := os.Unsetenv(name); err == nil {
			removed = append(removed, name)
		}
	}
	slices.Sort(removed)
	return removed
}

// SDKReplies lists the agent's replies at a destination through the SDK, for
// the adopted-reply rule.
type SDKReplies struct {
	Client  *basecamp.AccountClient
	AgentID int64
}

var _ ReplyLister = SDKReplies{}

// AgentReplies implements ReplyLister. The listing is exhaustive: the rule
// adopts only when exactly one reply matches, and a page left unread could
// hold the second.
func (r SDKReplies) AgentReplies(ctx context.Context, _ int64, kind string, recordingID int64, since time.Time) ([]AgentReply, error) {
	ctx, cancel := context.WithTimeout(ctx, AdoptionScanTimeout)
	defer cancel()
	var out []AgentReply
	keep := func(id int64, creator *basecamp.Person, created time.Time) {
		if creator != nil && creator.ID == r.AgentID && created.After(since) {
			out = append(out, AgentReply{ID: id, CreatedAt: created})
		}
	}
	switch admission.ReplyKind(kind) {
	case admission.ReplyComment:
		result, err := r.Client.Comments().List(ctx, recordingID, &basecamp.CommentListOptions{Limit: AdoptionScanLimit})
		if err != nil {
			return nil, err
		}
		if result.Meta.Truncated {
			return nil, fmt.Errorf("connector: %w: %d comments on recording %d", ErrRepliesTruncated, AdoptionScanLimit, recordingID)
		}
		for _, c := range result.Comments {
			keep(c.ID, c.Creator, c.CreatedAt)
		}
	case admission.ReplyChatLine:
		// Newest first: the replies the rule cares about are the ones after
		// the acknowledgement, not the beginning of the room.
		result, err := r.Client.Campfires().ListLines(ctx, recordingID, &basecamp.CampfireLineListOptions{
			Limit: AdoptionScanLimit, Sort: "created_at", Direction: "desc",
		})
		if err != nil {
			return nil, err
		}
		if result.Meta.Truncated {
			return nil, fmt.Errorf("connector: %w: %d lines in campfire %d", ErrRepliesTruncated, AdoptionScanLimit, recordingID)
		}
		for _, l := range result.Lines {
			keep(l.ID, l.Creator, l.CreatedAt)
		}
	default:
		return nil, fmt.Errorf("connector: no reply listing for %q", kind)
	}
	return out, nil
}

// SDKMembership lists the buckets the agent can see, for intake's reconnect.
type SDKMembership struct {
	Client *basecamp.AccountClient
}

var _ MembershipSource = SDKMembership{}

// Buckets implements MembershipSource.
func (m SDKMembership) Buckets(ctx context.Context) ([]int64, error) {
	result, err := m.Client.Projects().List(ctx, nil)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(result.Projects))
	for _, p := range result.Projects {
		ids = append(ids, p.ID)
	}
	return ids, nil
}
