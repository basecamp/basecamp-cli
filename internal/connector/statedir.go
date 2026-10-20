package connector

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

func StateDirName(accountID string, agentID int64, profile string) string {
	if account, err := strconv.ParseUint(accountID, 10, 64); err == nil {
		accountID = strconv.FormatUint(account, 10)
	}
	return LegacyStateDirName(accountID, agentID) + "-" + profile
}

// LegacyStateDirName preserves the saved spelling: older builds used it
// verbatim. Only the new profile directory canonicalizes account aliases.
func LegacyStateDirName(accountID string, agentID int64) string {
	return accountID + "-" + strconv.FormatInt(agentID, 10)
}

func AdoptLegacyStateDir(parent, accountID string, agentID int64, profile string) (bool, error) {
	if _, err := setup.Path("", profile); err != nil {
		return false, err
	}
	if account, err := strconv.ParseUint(accountID, 10, 64); err != nil || account == 0 || agentID <= 0 {
		return false, errors.New("connector: state needs a numeric account id and an agent person id")
	}
	dir := filepath.Join(parent, StateDirName(accountID, agentID, profile))
	legacy := filepath.Join(parent, LegacyStateDirName(accountID, agentID))
	if present, err := pathPresent(dir); err != nil || present {
		return false, err
	}
	if present, err := pathPresent(legacy); err != nil || !present {
		return false, err
	}
	lock, err := AcquireLegacyInstanceLock(legacy, accountID, agentID)
	if err != nil {
		return false, err
	}
	defer func() { _ = lock.Release() }()
	if present, err := pathPresent(dir); err != nil || present {
		return false, err
	}
	if err := os.Rename(legacy, dir); err != nil {
		return false, fmt.Errorf("connector: move %s to %s: %w", legacy, dir, err)
	}
	_ = os.Remove(filepath.Join(dir, filepath.Base(lock.Path())+".json"))
	return true, syncDirectory(parent)
}

// AcquireLegacyInstanceLock briefly waits for another verified adopter. A
// persistently held legacy lock belongs to an older connector, not necessarily
// this profile. Older builds must be stopped before migration.
func AcquireLegacyInstanceLock(dir, accountID string, agentID int64) (*InstanceLock, error) {
	for attempt := 0; ; attempt++ {
		lock, err := AcquireInstanceLock(dir, accountID, agentID, time.Now())
		if !errors.Is(err, ErrAlreadyRunning) {
			return lock, err
		}
		if attempt == 3 {
			return nil, LegacyLockError(dir, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func LegacyLockError(dir string, err error) error {
	return fmt.Errorf("connector: an older connector for this agent holds %s; stop it and restart its existing profile on this build before starting another profile (%w)", dir, err)
}

func pathPresent(path string) (bool, error) {
	switch _, err := os.Lstat(path); {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("connector: inspect %s: %w", path, err)
	}
}
