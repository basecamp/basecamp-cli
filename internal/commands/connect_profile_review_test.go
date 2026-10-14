//go:build unix

package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/output"
)

func TestAVerifiedWrongPersonCannotAdoptLegacyState(t *testing.T) {
	f := newOperatorFixture(t)
	legacy := seedLegacyLedger(t, f.s, f.file)
	f.s.agentID = setupPeerAgent // working bearer, but Me proves a different agent
	r := startProfileConnector(t, f.s, "agent")
	err := r.awaitExit(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connect.json")
	assertLegacyLedgerKept(t, legacy, f.s, f.file)
	_, err = os.Lstat(filepath.Join(connectStateRoot(t), connector.StateDirName(f.file.AccountID, f.file.Agent.PersonID, "agent")))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestAShadowRunAdoptsAndPromotesItsHeldLegacyState(t *testing.T) {
	f := newOperatorFixture(t)
	legacy := seedLegacyLedger(t, f.s, f.file, true)
	r := startProfileConnector(t, f.s, "agent", "--shadow")
	r.awaitRunning(t)
	require.Eventually(t, func() bool { return strings.Contains(r.out.String(), "resuming the feed from the stored position") }, 10*time.Second, 20*time.Millisecond)
	assert.Contains(t, r.out.String(), "adopted legacy state")
	r.cancel()
	_ = r.awaitExit(t)
	shadow := filepath.Join(filepath.Dir(connectStateRoot(t)), "connect-shadow", connector.StateDirName(f.file.AccountID, f.file.Agent.PersonID, "agent"))
	assertLegacyLedgerKept(t, shadow, f.s, f.file, true)
	_, err := os.Lstat(legacy)
	assert.ErrorIs(t, err, os.ErrNotExist)
	out, err := f.run(t, output.FormatJSON, "shadow", "promote")
	require.NoError(t, err, out)
	dir := filepath.Join(connectStateRoot(t), connector.StateDirName(f.file.AccountID, f.file.Agent.PersonID, "agent"))
	assertLegacyLedgerKept(t, dir, f.s, f.file, true)
}

func TestAFreshSiblingNamesWhereTheEarlierHeldStateLives(t *testing.T) {
	f := newOperatorFixture(t)
	setUpSecondProfile(t, f.s, "agent-b")
	seedLegacyLedger(t, f.s, f.file)
	a := startProfileConnector(t, f.s, "agent")
	a.awaitRunning(t)
	b := startProfileConnector(t, f.s, "agent-b")
	b.awaitRunning(t)
	assert.Contains(t, b.out.String(), "this profile has no ledger")
	assert.Contains(t, b.out.String(), "--since")
	assert.Contains(t, b.out.String(), "sibling="+filepath.Join(connectStateRoot(t), connector.StateDirName(f.file.AccountID, f.file.Agent.PersonID, "agent")))
}

func TestStatePathsRefuseInvalidProfilesBeforeCreatingAnything(t *testing.T) {
	f := newOperatorFixture(t)
	for _, profile := range []string{"", "../escape", "a/b", "a\\b", ".", "profile name"} {
		_, err := connectStatePath(profile, f.file, false)
		require.Error(t, err, profile)
		_, err = connectStateDir(profile, f.file, false)
		require.Error(t, err, profile)
	}
	_, err := os.Lstat(connectStateRoot(t))
	assert.ErrorIs(t, err, os.ErrNotExist)
}
