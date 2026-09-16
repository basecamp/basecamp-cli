package admission

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// TrustMode names who, besides the operator, may drive the agent.
type TrustMode string

const (
	// TrustOperator trusts the operator alone. The default.
	TrustOperator TrustMode = "operator"
	// TrustAllowlist trusts the operator and a list of Person ids.
	TrustAllowlist TrustMode = "allowlist"
	// TrustProject trusts the operator and any non-client member of the
	// event's project.
	TrustProject TrustMode = "project"
)

// Trust is the trust section of connect.json.
type Trust struct {
	Mode TrustMode `json:"mode"`
	// OperatorID is the Person id of the person who approved the connection.
	OperatorID int64 `json:"operator_id"`
	// AllowlistIDs are the Person ids trusted in allowlist mode.
	AllowlistIDs []int64 `json:"allowlist_ids,omitempty"`
}

// Route is one project's entry in connect.json: where its work runs and how
// admission treats it. Routes are local: nothing read from Basecamp can add or
// change one.
type Route struct {
	// Path is the approved working directory the project maps to.
	Path string `json:"path"`
	// Class is the project's classification, carried on the record.
	Class string `json:"class,omitempty"`
	// WatchCompletions makes the agent a driver of the project: every trusted
	// completion in it is admitted as trigger completed, without the agent
	// being assigned or subscribed.
	WatchCompletions bool `json:"watch_completions,omitempty"`
}

// Policy is what admission reads from connect.json, plus the two facts that
// never come from that file: the agent's own Person id (from the profile's
// verified identity) and the --project scope.
//
// connect.json as a whole is written by `basecamp connect setup` (plan step
// 16), which also carries the driver, concurrency, deadline and worktrees;
// this is only the part admission decides from.
type Policy struct {
	// AgentID is the agent's own Person id. Required.
	AgentID int64 `json:"-"`
	// Buckets is the --project scope. Empty means every project the agent can
	// see.
	Buckets []int64 `json:"-"`

	Trust Trust `json:"trust"`
	// Projects maps a bucket id to its route.
	Projects map[int64]Route `json:"projects,omitempty"`
}

// ParsePolicy decodes the admission part of connect.json. Unknown keys are
// ignored: the file carries settings that belong to other steps. The caller
// sets AgentID and Buckets, then calls Validate.
func ParsePolicy(data []byte) (Policy, error) {
	var p Policy
	if err := json.Unmarshal(data, &p); err != nil {
		return Policy{}, fmt.Errorf("admission: parse connect.json: %w", err)
	}
	if p.Trust.Mode == "" {
		p.Trust.Mode = TrustOperator
	}
	return p, nil
}

// Validate refuses a policy under which trust could be misread. Every check
// here fails closed: a policy that does not validate admits nothing.
func (p Policy) Validate() error {
	switch {
	case p.AgentID <= 0:
		return errors.New("admission: policy needs the agent's Person id")
	case p.Trust.OperatorID <= 0:
		return errors.New("admission: policy needs the operator's Person id")
	case p.Trust.OperatorID == p.AgentID:
		return errors.New("admission: the operator cannot be the agent itself")
	}
	switch p.Trust.Mode {
	case TrustOperator, TrustProject:
		if len(p.Trust.AllowlistIDs) > 0 {
			return fmt.Errorf("admission: allowlist_ids is set but trust mode is %q", p.Trust.Mode)
		}
	case TrustAllowlist:
		// The agent in its own allowlist is a configuration that reads as
		// "the agent may drive itself". The gate refuses the agent regardless;
		// the policy says so here too, where the operator can see it.
		for _, id := range p.Trust.AllowlistIDs {
			if id <= 0 {
				return fmt.Errorf("admission: allowlist id %d is not a Person id", id)
			}
			if id == p.AgentID {
				return errors.New("admission: the agent's own Person id is in the allowlist")
			}
		}
	default:
		return fmt.Errorf("admission: unknown trust mode %q", p.Trust.Mode)
	}
	for bucket, route := range p.Projects {
		if bucket <= 0 {
			return fmt.Errorf("admission: route for bucket %d: not a bucket id", bucket)
		}
		if route.Path == "" {
			return fmt.Errorf("admission: route for bucket %d has no path", bucket)
		}
	}
	return nil
}

// inScope reports whether bucket is in the --project scope.
func (p Policy) inScope(bucket int64) bool {
	return len(p.Buckets) == 0 || slices.Contains(p.Buckets, bucket)
}

// route returns the bucket's route, if connect.json has one.
func (p Policy) route(bucket int64) (Route, bool) {
	r, ok := p.Projects[bucket]
	return r, ok
}
