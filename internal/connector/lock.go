package connector

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gofrs/flock"
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
	flock *flock.Flock
	path  string
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("connector: create state directory: %w", err)
	}

	path := filepath.Join(dir, "instance-"+accountID+"-"+strconv.FormatInt(agentPersonID, 10)+".lock")
	lock := flock.New(path)
	held, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("connector: take the instance lock: %w", err)
	}
	if !held {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyRunning, describeHolder(path))
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

	return &InstanceLock{flock: lock, path: path}, nil
}

// Release drops the lock.
func (l *InstanceLock) Release() error {
	_ = os.Remove(l.path + ".json")
	return l.flock.Unlock()
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
