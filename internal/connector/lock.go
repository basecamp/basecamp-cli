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

// ErrAlreadyRunning reports a second connector for the same agent.
var ErrAlreadyRunning = errors.New("connector: another connector already holds this account and agent")

// InstanceLock is the refusal of a second connector on one agent identity.
//
// It is keyed on the ACCOUNT and the agent's Person id, not on the profile
// name. Two profiles can hold credentials for the same agent, and the failure
// this prevents is not "the same configuration twice" — it is one agent's
// mentions being dispatched twice, which is a property of the identity, not of
// the file that names it.
//
// The kernel drops an flock when the holding descriptor closes, process death
// included, so a crashed connector cannot wedge the lock and there is no
// stale-lock reaping to get wrong. The metadata written beside it is
// diagnostic only: the lock is the lock.
type InstanceLock struct {
	unlock func() error
	path   string
}

// instanceHolder is what a running connector writes beside its lock so the
// refusal can say who is holding it.
type instanceHolder struct {
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
	AccountID string `json:"account_id"`
	AgentID   int64  `json:"agent_person_id"`
}

// AcquireInstanceLock takes the lock for one account and agent, or refuses.
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
	// agent's mentions dispatched twice, which is the failure this lock
	// exists to prevent.
	unlock, err := setup.TryLockPrivate(path)
	switch {
	case errors.Is(err, setup.ErrLockHeld):
		return nil, fmt.Errorf("%w: %s", ErrAlreadyRunning, describeHolder(path))
	case err != nil:
		// No lock means no connector. There is no degraded mode here.
		return nil, fmt.Errorf("connector: take the instance lock: %w", err)
	}

	holder, err := json.Marshal(instanceHolder{
		PID:       os.Getpid(),
		StartedAt: now.UTC().Format(time.RFC3339),
		AccountID: accountID,
		AgentID:   agentPersonID,
	})
	if err == nil {
		// Best effort, and deliberately after the lock is held: a connector
		// that cannot describe itself still must not be a second connector.
		_ = os.WriteFile(path+".json", append(holder, '\n'), 0o600)
	}

	return &InstanceLock{unlock: unlock, path: path}, nil
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
