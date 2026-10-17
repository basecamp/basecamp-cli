package connector

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func StateDirName(accountID string, agentID int64, profile string) string {
	return LegacyStateDirName(accountID, agentID) + "-" + profile
}

func LegacyStateDirName(accountID string, agentID int64) string {
	return accountID + "-" + strconv.FormatInt(agentID, 10)
}

func AdoptLegacyStateDir(parent, accountID string, agentID int64, profile string) (bool, error) {
	dir := filepath.Join(parent, StateDirName(accountID, agentID, profile))
	legacy := filepath.Join(parent, LegacyStateDirName(accountID, agentID))
	if present, err := pathPresent(dir); err != nil || present {
		return false, err
	}
	if present, err := pathPresent(legacy); err != nil || !present {
		return false, err
	}
	lock, err := AcquireInstanceLock(legacy, accountID, agentID, time.Now())
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
