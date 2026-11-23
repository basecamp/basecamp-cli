package console

import (
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/runner"
)

// Snapshot is everything the console shows that is read from this computer:
// ids, states, names and timestamps, as `basecamp connect status` shows,
// and for the runner's workers the title of what each was asked on and what
// the worker reported. It holds no feed position and no credential, and not
// the body of any request.
type Snapshot struct {
	At        time.Time
	Profile   string
	AccountID string
	Agent     Person
	Connector Connector
	Projects  []Project
	Pings     Pings
	Trust     Trust
	// Runner is nil when the connector hands requests to the session that
	// started it and no runner has been set up or has run.
	Runner *Runner
	// Now and Done are the runner's workers: those running or waiting, and
	// those that ended, each newest first.
	Now  []runner.WorkerActivity
	Done []runner.WorkerActivity
	// Requests are the latest requests the connector let in, from its
	// ledger, and Held those waiting for a person.
	Requests []Request
	Held     []connector.HeldStatus
	// Notes are the parts that could not be read, and why.
	Notes []string
}

// Person is someone connect.json names: by id, and by name once Basecamp
// has been asked.
type Person struct {
	ID   int64
	Name string
}

// Connector is the connector's process and its own record of itself.
type Connector struct {
	// Running says a connector holds this profile's lock.
	Running bool
	PID     int
	// StartedAt is when the running connector started, as it wrote.
	StartedAt string
	// Hold is the standing hold, under which nothing is handed off.
	Hold *connector.HoldStatus
	// LastChange is the last run's own record of itself.
	LastChange *connector.ConnectionStatus
}

// Project is a project the connector serves.
type Project struct {
	ID               int64
	Name             string
	URL              string
	WatchCompletions bool
	// Campfires counts the chats it answers without being mentioned.
	Campfires int
}

// Pings is whether this profile's connector answers the agent's Pings.
type Pings struct {
	// Hears is nil when it cannot be told.
	Hears   *bool
	Because string
	// Twice says another connector on this computer answers them too.
	Twice bool
	// Guests are the people let into one Ping each, as participants.
	Guests []PingGuests
	Error  string
}

// PingGuests are the people let into one Ping.
type PingGuests struct {
	PingID int64
	People []Person
}

// Trust is who may give the agent work.
type Trust struct {
	Mode             string
	Operator         Person
	Allowlist        []Person
	AllowAssignments bool
	Agents           []Person
}

// Runner is the connector doing the work itself: its settings as
// runner.json has them and its orchestrator.
type Runner struct {
	// Live says a connector is running with an orchestrator now.
	Live         bool
	Orchestrator runner.Spec
	Worker       runner.Spec
	Permissions  string
	// MaxWorkers is the cap on workers running at once, zero for none: the
	// one a running connector started with, and otherwise runner.json's.
	// KeptMaxWorkers is runner.json's, which a running connector takes up
	// at its next start.
	MaxWorkers     int
	KeptMaxWorkers int
	// Rules are runner.json's, in the order they are tried.
	Rules []Rule
	// Turns, Waiting and SetAside are the orchestrator's: the turns it has
	// finished, the messages it has not handled, and those it gave up on.
	Turns    int
	Waiting  int
	SetAside int
	// Process is the process group the orchestrator's session still names.
	Process int
	// ConfigError is why runner.json cannot be read as written. No agent
	// is started until it can.
	ConfigError string
}

// Rule is one runner.json rule: the conditions a request must meet, in
// words, and the worker it then gets.
type Rule struct {
	When   []string
	Worker runner.Spec
}

// Request is one request the connector let in and what became of it.
type Request struct {
	EventID     int64
	Trigger     string
	EventType   string
	ProjectID   int64
	ProjectName string
	URL         string
	State       string
	Reason      string
	At          time.Time
}

// Check statuses, as `basecamp connect doctor` reports them.
const (
	StatusPass = "pass"
	StatusWarn = "warn"
	StatusFail = "fail"
	StatusSkip = "skip"
)

// Check is one of doctor's checks.
type Check struct {
	Name    string
	Status  string
	Message string
	Hint    string
}

// Health is what reading it costs a request to Basecamp or a process
// started: doctor's checks and the coding agents installed. It is read when
// the console opens and when the person asks again, not with every
// snapshot.
type Health struct {
	At     time.Time
	Checks []Check
	Agents []runner.Installed
}
