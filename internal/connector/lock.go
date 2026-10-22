package connector

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

var ErrAlreadyRunning = errors.New("connector: another connector already runs for this profile")

// The kernel drops a flock when the holding descriptor closes, process death
// included, so a crashed connector cannot wedge the lock and there is no
// stale-lock reaping to get wrong. The metadata written beside it is
// diagnostic only: the lock is the lock.
type InstanceLock struct {
	unlock func() error
	path   string
	holder instanceHolder
}

// instanceHolder is what a running connector writes beside its lock so the
// refusal can say who is holding it.
type instanceHolder struct {
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
	AccountID string `json:"account_id"`
	AgentID   int64  `json:"agent_person_id"`
	// Pings is whether the holder hears the agent's Pings: true only for a
	// connector that answers them (NotePings). Every holder this build writes
	// says, so nil is a holder an older build wrote, which does not.
	Pings *bool `json:"pings,omitempty"`
	// PingsBecause is why, in the run's own words for its reason.
	PingsBecause string `json:"pings_because,omitempty"`
}

// AcquireInstanceLock takes the lock in a profile's state directory, or refuses.
// The account and agent name the file; the containing directory isolates profiles.
// The holder it describes hears no Pings until NotePings says otherwise: a
// command that only needs the connector stopped answers nothing.
func AcquireInstanceLock(dir, accountID string, agentPersonID int64, now time.Time) (*InstanceLock, error) {
	account, err := strconv.ParseUint(accountID, 10, 64)
	if err != nil || account == 0 || agentPersonID <= 0 {
		return nil, errors.New("connector: the instance lock needs a numeric account id and an agent person id")
	}
	// The file is named from the NUMBER, not the spelling: "02914079" and
	// "2914079" are one account and must meet one lock, or the refusal of a
	// second connector is a matter of how the id was typed.
	accountID = strconv.FormatUint(account, 10)

	path := filepath.Join(dir, "instance-"+accountID+"-"+strconv.FormatInt(agentPersonID, 10)+".lock")
	// The lock is only a lock if nobody else can reach it. A directory that
	// merely EXISTS at 0700 is not enough — MkdirAll leaves a group-writable
	// one exactly as it found it — so the directory, everything above it and
	// the lock file itself are vetted here, by the same check the connector's
	// trust file gets. Someone who can write the directory can point two
	// connectors at two inodes, and then neither excludes the other: one
	// profile's mentions dispatched twice, which is the failure this lock
	// exists to prevent.
	unlock, err := setup.TryLockPrivate(path)
	switch {
	case errors.Is(err, setup.ErrLockHeld):
		return nil, fmt.Errorf("%w: %s", ErrAlreadyRunning, describeHolder(path))
	case err != nil:
		// No lock means no connector. There is no degraded mode here.
		return nil, fmt.Errorf("connector: take the instance lock: %w", err)
	}

	hears := false
	lock := &InstanceLock{unlock: unlock, path: path, holder: instanceHolder{
		PID:       os.Getpid(),
		StartedAt: now.UTC().Format(time.RFC3339),
		AccountID: accountID,
		AgentID:   agentPersonID,
		Pings:     &hears,
	}}
	// Best effort, and deliberately after the lock is held: a connector
	// that cannot describe itself still must not be a second connector.
	lock.describe()
	return lock, nil
}

// NotePings says beside the lock whether the connector holding it hears the
// agent's Pings, and why. Another connector for the agent on this computer
// reads it to warn that two answer them (InstanceHolder). The connector
// decides it while it holds the lock, so a start of the same profile can't
// change the choice between the decision and the run.
func (l *InstanceLock) NotePings(hears bool, because string) {
	l.holder.Pings, l.holder.PingsBecause = &hears, because
	l.describe()
}

func (l *InstanceLock) describe() {
	if holder, err := json.Marshal(l.holder); err == nil {
		_ = os.WriteFile(l.path+".json", append(holder, '\n'), 0o600)
	}
}

// Release drops the lock.
func (l *InstanceLock) Release() error {
	_ = os.Remove(l.path + ".json")
	return l.unlock()
}

// Path is the lock file, for diagnostics.
func (l *InstanceLock) Path() string { return l.path }

func describeHolder(path string) string {
	raw, err := os.ReadFile(path + ".json")
	if err != nil {
		return "held by another process"
	}
	var holder instanceHolder
	if err := json.Unmarshal(raw, &holder); err != nil {
		return "held by another process"
	}
	return fmt.Sprintf("held by pid %d since %s", holder.PID, holder.StartedAt)
}

// InstanceHolderInfo is what a running connector wrote beside its lock.
type InstanceHolderInfo struct {
	PID       int
	StartedAt string
	// Pings is whether it hears the agent's Pings; nil when an older build
	// wrote it, which does not say. PingsBecause is why.
	Pings        *bool
	PingsBecause string
}

// InstanceHolder reads what a connector holding the lock in dir wrote about
// itself, without taking the lock: status must not make a starting connector
// find its own lock held. It is diagnostic, and can be stale after a crash.
func InstanceHolder(dir, accountID string, agentPersonID int64) (InstanceHolderInfo, bool) {
	account, err := strconv.ParseUint(accountID, 10, 64)
	if err != nil || account == 0 || agentPersonID <= 0 {
		return InstanceHolderInfo{}, false
	}
	path := filepath.Join(dir, "instance-"+strconv.FormatUint(account, 10)+"-"+strconv.FormatInt(agentPersonID, 10)+".lock.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return InstanceHolderInfo{}, false
	}
	var holder instanceHolder
	if err := json.Unmarshal(raw, &holder); err != nil || holder.PID <= 0 {
		return InstanceHolderInfo{}, false
	}
	return InstanceHolderInfo{PID: holder.PID, StartedAt: holder.StartedAt, Pings: holder.Pings, PingsBecause: holder.PingsBecause}, true
}
