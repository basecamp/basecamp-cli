package setup

import (
	"encoding/json"
	"slices"
)

// Report is the outcome of one setup run: what was written and every check.
// Whether the connector is ready is never stored: it is derived from the
// checks every time it is asked for, so no code path can report ready while
// a check has failed or before any check ran.
type Report struct {
	Path          string
	Profile       string
	AccountID     string
	AgentPersonID int64
	AgentKind     string
	OperatorID    int64
	TrustMode     string
	Routes        int
	Written       bool

	checks []Check
}

// Add records checks, in order.
func (r *Report) Add(checks ...Check) { r.checks = append(r.checks, checks...) }

// Checks returns the checks recorded so far.
func (r *Report) Checks() []Check { return slices.Clone(r.checks) }

// Failed returns the checks that failed.
func (r *Report) Failed() []Check {
	var failed []Check
	for _, c := range r.checks {
		if c.Status != StatusPass && c.Status != StatusWarn && c.Status != StatusSkip {
			failed = append(failed, c)
		}
	}
	return failed
}

// Ready reports whether the connector could run as set up: connect.json was
// written, at least one check ran, and none failed. A check with a status
// outside the known ones counts as failed.
func (r *Report) Ready() bool {
	return r.Written && len(r.checks) > 0 && len(r.Failed()) == 0
}

// MarshalJSON writes the report with ready derived, never stored.
func (r *Report) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Path          string  `json:"path"`
		Profile       string  `json:"profile"`
		AccountID     string  `json:"account_id"`
		AgentPersonID int64   `json:"agent_person_id"`
		AgentKind     string  `json:"agent_kind"`
		OperatorID    int64   `json:"operator_id"`
		TrustMode     string  `json:"trust_mode"`
		Routes        int     `json:"routes"`
		Written       bool    `json:"written"`
		Ready         bool    `json:"ready"`
		Checks        []Check `json:"checks"`
	}{r.Path, r.Profile, r.AccountID, r.AgentPersonID, r.AgentKind, r.OperatorID, r.TrustMode, r.Routes, r.Written, r.Ready(), r.Checks()})
}
