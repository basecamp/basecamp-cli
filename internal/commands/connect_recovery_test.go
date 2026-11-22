//go:build unix

package commands

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// The two ways a person breaks connect.json by typing into it, as the card
// that asked for this found them: a misspelled key, and a file that is no
// longer JSON.
var brokenConnectFiles = []struct {
	name string
	edit func(string) string
	says []string
}{
	{"a misspelled key", func(s string) string {
		return strings.Replace(s, `"watch_completions"`, `"watch_completion"`, 1)
	}, []string{`unknown field "watch_completion"`, `did you mean "watch_completions"?`, "line "}},
	{"broken JSON", func(s string) string {
		return strings.Replace(s, `"profile": "agent",`, `"profile": "agent" nope,`, 1)
	}, []string{"invalid character", "line 3, column 22"}},
}

// breakConnectFile rewrites the agent's connect.json in place, keeping its
// owner-only mode, and returns what it now holds.
func breakConnectFile(t *testing.T, edit func(string) string) []byte {
	t.Helper()
	path := connectSetupPath(t, "agent")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	broken := edit(string(data))
	require.NotEqual(t, string(data), broken, "the edit must change the file")
	require.NoError(t, os.WriteFile(path, []byte(broken), 0o600))
	_, err = loadConnectFile(path, "agent")
	require.Error(t, err, "the file must now be refused")
	return []byte(broken)
}

// watchedFile is a served project with watch_completions on, so the
// misspelled-key edit has a key to misspell.
func watchCompletions(t *testing.T, f operatorFixture) {
	t.Helper()
	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, f.s, "agent"), fmt.Sprintf("--watch-completions=%d", setupProject))
	require.NoError(t, err, out)
}

func (f operatorFixture) reportProfile(t *testing.T) connectProfile {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(appctx.WithApp(context.Background(), newConnectSetupApp(t, f.s, "agent")))
	p, err := loadConnectProfileForReport(cmd)
	require.NoError(t, err)
	return p
}

func checksByName(checks []setup.Check) map[string]setup.Check {
	byName := map[string]setup.Check{}
	for _, c := range checks {
		byName[c.Name] = c
	}
	return byName
}

func TestDoctorReportsABrokenConnectJSONAndRunsEveryOtherCheck(t *testing.T) {
	for _, tc := range brokenConnectFiles {
		t.Run(tc.name, func(t *testing.T) {
			f := newOperatorFixture(t)
			watchCompletions(t, f)
			require.NoError(t, f.ledger(t, false).Close())
			breakConnectFile(t, tc.edit)

			p := f.reportProfile(t)
			require.True(t, p.broken())
			checks := connectDoctorChecks(context.Background(), p)
			byName := checksByName(checks)

			file := byName["connect.json"]
			assert.Equal(t, setup.StatusFail, file.Status)
			for _, want := range tc.says {
				assert.Contains(t, file.Message, want)
			}
			assert.Equal(t, connectResetHint("agent"), file.Hint)
			assert.Contains(t, file.Hint, "basecamp connect setup -P agent --reset --serve <project-id>", "a reset serves nothing unless told to")

			// Everything that does not need the file still ran, and passed.
			assert.Equal(t, setup.StatusPass, byName["Token"].Status, "%+v", checks)
			assert.Equal(t, setup.StatusWarn, byName["Identity"].Status)
			assert.Contains(t, byName["Identity"].Message, fmt.Sprintf("Person %d", f.file.Agent.PersonID))
			assert.Equal(t, setup.StatusPass, byName["Stream ticket"].Status, "%+v", checks)
			assert.Contains(t, byName, "Account feed", "the feed is polled (the fake Basecamp has none to serve)")
			assert.Equal(t, setup.StatusPass, byName["Ledger"].Status, "the ledger the connector left is read: %+v", checks)
			assert.Contains(t, byName["Ledger"].Message, "Schema")
			require.Contains(t, byName, "Pings")
			assert.Equal(t, setup.StatusPass, byName["Pings"].Status, "%+v", checks)

			// The command fails as not ready, and leads with the recovery.
			_, err := f.run(t, output.FormatJSON, "doctor")
			require.Error(t, err)
			e := usageError(t, err)
			assert.Equal(t, codeNotReady, e.Code)
			assert.Contains(t, e.Message, "connect.json: It can't be used")
			assert.Contains(t, e.Hint, "--reset")
		})
	}
}

// With neither a state directory nor a credential, nothing says whose
// connector this was: those checks are skipped, by name, rather than run
// against a guess.
func TestDoctorOnABrokenFileWithNoStateAndNoCredentialSkipsWhatItCannotTell(t *testing.T) {
	f := newOperatorFixture(t)
	breakConnectFile(t, brokenConnectFiles[1].edit)
	p := f.reportProfile(t)
	assert.False(t, p.knowsState(), "no connector has run, so there is no state directory")

	p.app = newConnectSetupApp(t, f.s, "nobody-here")
	checks := checksByName(connectDoctorChecks(context.Background(), p))
	assert.Equal(t, setup.StatusFail, checks["connect.json"].Status)
	assert.Equal(t, setup.StatusFail, checks["Token and identity"].Status)
	assert.Equal(t, setup.StatusSkip, checks["Ledger"].Status)
	assert.Equal(t, setup.StatusSkip, checks["Pings"].Status)
}

func TestStatusShowsTheLedgerWhenConnectJSONIsBroken(t *testing.T) {
	for _, tc := range brokenConnectFiles {
		t.Run(tc.name, func(t *testing.T) {
			f := newOperatorFixture(t)
			watchCompletions(t, f)
			l := f.ledger(t, false)
			ctx := context.Background()
			_, err := l.RecordSeen(ctx, eventfeed.Event{ID: 3, Kind: "comment_created", EventType: "comment.created", Action: "created",
				CreatedAt: time.Now(), BucketID: setupProject, CreatorID: setupOperatorPerson, RecordingID: 77}, connector.LanePoll)
			require.NoError(t, err)
			_, err = l.Admission().Commit(ctx, admission.Verdict{
				EventID: 3, EventType: "comment.created", BucketID: setupProject, RecordingID: 77, RequesterID: setupOperatorPerson,
				State: admission.StateBlocked, Reason: admission.ReasonConfigUnreadable,
			})
			require.NoError(t, err)
			require.NoError(t, l.Close())
			breakConnectFile(t, tc.edit)

			out, err := f.run(t, output.FormatJSON, "status")
			require.NoError(t, err, out)
			var envelope struct {
				Data    connectStatusReport `json:"data"`
				Summary string              `json:"summary"`
			}
			require.NoError(t, json.Unmarshal([]byte(out), &envelope), out)
			require.NotNil(t, envelope.Data.Config, out)
			for _, want := range tc.says {
				assert.Contains(t, envelope.Data.Config.Error, want)
			}
			assert.Contains(t, envelope.Data.Config.Hint, "basecamp connect setup -P agent --reset")
			assert.Equal(t, 1, envelope.Data.Config.HeldUnreadable)
			assert.Equal(t, 1, envelope.Data.Status.Blocked[string(admission.ReasonConfigUnreadable)], "the ledger itself is shown")
			assert.Contains(t, envelope.Summary, "connect.json can't be used (1 held until it can)")

			styled, err := f.run(t, output.FormatStyled, "status")
			require.NoError(t, err)
			lines := strings.Split(styled, "\n")
			require.Greater(t, len(lines), 4)
			assert.True(t, strings.HasPrefix(strings.TrimSpace(lines[2]), "connect.json can't be used:"), "it leads with the problem:\n%s", styled)
			assert.Contains(t, styled, "1 request(s) are held because it couldn't be read")
			assert.Contains(t, styled, "Queues")
		})
	}
}

func TestStatusOnABrokenFileWithNoLedgerToShowSaysHowToRecover(t *testing.T) {
	f := newOperatorFixture(t)
	breakConnectFile(t, brokenConnectFiles[1].edit)
	_, err := f.run(t, output.FormatJSON, "status")
	require.Error(t, err)
	e := usageError(t, err)
	assert.Contains(t, e.Message, "connect.json cannot be used")
	assert.Contains(t, e.Hint, "--reset")
}

func TestSetupResetRecoversABrokenFileWithoutEditingIt(t *testing.T) {
	for _, tc := range brokenConnectFiles {
		t.Run(tc.name, func(t *testing.T) {
			f := newOperatorFixture(t)
			watchCompletions(t, f)
			broken := breakConnectFile(t, tc.edit)
			path := connectSetupPath(t, "agent")

			// Without --reset, setup refuses, changes nothing, and names it.
			out, err := runConnectSetupCmd(t, newConnectSetupApp(t, f.s, "agent"), serveArg())
			require.Error(t, err, out)
			e := usageError(t, err)
			assert.Contains(t, e.Hint, "basecamp connect setup -P agent --reset")
			now, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			assert.Equal(t, broken, now, "a refused setup leaves the file as it was")

			out, err = runConnectSetupCmd(t, newConnectSetupApp(t, f.s, "agent"),
				"--operator", fmt.Sprint(setupOperatorPerson), serveArg(), "--reset")
			require.NoError(t, err, out)
			fresh, err := setup.Load(path)
			require.NoError(t, err, "setup wrote a file that loads")
			assert.Contains(t, fresh.Projects, setupProject)
			assert.False(t, fresh.Projects[setupProject].WatchCompletions, "a reset starts from this run's flags alone")

			asides, err := filepath.Glob(path + ".broken-*")
			require.NoError(t, err)
			require.Len(t, asides, 1)
			kept, err := os.ReadFile(asides[0])
			require.NoError(t, err)
			assert.Equal(t, broken, kept, "the old file is kept, byte for byte")
			assert.Contains(t, out, asides[0], "setup says where the old file went")

			// Doctor and status work again.
			p := f.reportProfile(t)
			assert.False(t, p.broken())
		})
	}
}

// A reset whose checks fail writes nothing and moves nothing: the old file
// is set aside only by the write that replaces it.
func TestSetupResetThatFailsLeavesTheOldFile(t *testing.T) {
	f := newOperatorFixture(t)
	broken := breakConnectFile(t, brokenConnectFiles[1].edit)
	path := connectSetupPath(t, "agent")

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, f.s, "agent"),
		"--operator", fmt.Sprint(setupOperatorPerson), "--serve=4040404", "--reset")
	require.Error(t, err, out)
	now, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, broken, now)
	asides, _ := filepath.Glob(path + ".broken-*")
	assert.Empty(t, asides)
}

func TestConnectStateIdentityNeedsExactlyOneDirectory(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	root := filepath.Join(state, "basecamp", "connect")

	_, _, ok := connectStateIdentity("agent", false)
	assert.False(t, ok, "no state at all")

	require.NoError(t, os.MkdirAll(filepath.Join(root, "999-42-agent"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "999-43-my-agent"), 0o700), "another profile whose name ends the same")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "999-44"), 0o700), "a legacy directory names no profile")
	account, person, ok := connectStateIdentity("agent", false)
	require.True(t, ok)
	assert.Equal(t, "999", account)
	assert.Equal(t, int64(42), person)

	require.NoError(t, os.MkdirAll(filepath.Join(root, "888-7-agent"), 0o700))
	_, _, ok = connectStateIdentity("agent", false)
	assert.False(t, ok, "two candidates say nothing")
}

func TestBasecampDoctorHasALinePerConnectorProfile(t *testing.T) {
	f := newOperatorFixture(t)
	setUpSecondProfile(t, f.s, "laptop")

	// The real doctor, so the line is known to be wired in: one per profile.
	// Its own test app has a configuration directory of its own, so the two
	// profiles' connect.json files are copied into it.
	connectDir := filepath.Dir(filepath.Dir(connectSetupPath(t, "agent")))
	app, _ := setupDoctorTestApp(t, "999")
	for _, profile := range []string{"agent", "laptop"} {
		data, err := os.ReadFile(filepath.Join(connectDir, profile, setup.FileName))
		require.NoError(t, err)
		dst := connectSetupPath(t, profile)
		require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o700))
		require.NoError(t, os.WriteFile(dst, data, 0o600))
	}
	var lines []Check
	for _, c := range runDoctorChecks(context.Background(), app, false) {
		if strings.HasPrefix(c.Name, "Connector ") {
			lines = append(lines, c)
		}
	}
	require.Len(t, lines, 2, "%+v", lines)
	assert.Equal(t, `Connector "agent"`, lines[0].Name)
	assert.Equal(t, `Connector "laptop"`, lines[1].Name)

	require.NoError(t, os.RemoveAll(filepath.Dir(connectSetupPath(t, "laptop"))))
	checks := checkConnectorProfiles()
	require.Len(t, checks, 1)
	assert.Equal(t, Check{Name: `Connector "agent"`, Status: "pass", Message: "Set up; its connector is not running now",
		Hint: "Start it with basecamp connect -P agent, or check it with basecamp connect doctor -P agent"}, checks[0])

	breakConnectFile(t, brokenConnectFiles[1].edit)
	checks = checkConnectorProfiles()
	require.Len(t, checks, 1)
	assert.Equal(t, "fail", checks[0].Status)
	assert.Contains(t, checks[0].Message, "connect.json can't be used: parse connect.json: line 3, column 22")
	assert.True(t, strings.HasSuffix(checks[0].Message, "; whether its connector is running can't be told"), checks[0].Message)
	assert.Equal(t, connectResetHint("agent"), checks[0].Hint)
}

// A connector started before its file broke is still running, and the line
// for its profile says so.
func TestBasecampDoctorSaysABrokenProfilesConnectorIsRunning(t *testing.T) {
	f := newOperatorFixture(t)
	c := startProfileConnector(t, f.s, "agent")
	c.awaitRunning(t)
	breakConnectFile(t, brokenConnectFiles[1].edit)

	checks := checkConnectorProfiles()
	require.Len(t, checks, 1)
	assert.Equal(t, "fail", checks[0].Status)
	assert.True(t, strings.HasSuffix(checks[0].Message, fmt.Sprintf("; its connector (agent person %d in account %s) is running",
		f.file.Agent.PersonID, f.file.AccountID)), checks[0].Message)
}

// The file a reset moved aside goes back when the write that should replace
// it fails: a failed reset leaves what was there.
func TestSetupResetPutsTheOldFileBackWhenTheWriteFails(t *testing.T) {
	f := newOperatorFixture(t)
	broken := breakConnectFile(t, brokenConnectFiles[1].edit)
	path := connectSetupPath(t, "agent")
	saved := connectSaveFile
	t.Cleanup(func() { connectSaveFile = saved })
	moved := false
	connectSaveFile = func(held auth.HeldCredential, p string, file setup.File) error {
		asides, _ := filepath.Glob(path + ".broken-*")
		_, statErr := os.Lstat(path)
		moved = len(asides) == 1 && os.IsNotExist(statErr)
		return errors.New("disk full")
	}

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, f.s, "agent"),
		"--operator", fmt.Sprint(setupOperatorPerson), serveArg(), "--reset")
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "disk full")
	require.True(t, moved, "the write ran after the old file was moved aside, so this row reaches the restore")
	now, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, broken, now, "the old file is back")
	asides, _ := filepath.Glob(path + ".broken-*")
	assert.Empty(t, asides)
}

// A connector state directory without a ledger is not a reason to lead with
// "run the connector", which the broken file would refuse.
func TestStatusOnABrokenFileWhoseStateHasNoLedgerLeadsWithTheFile(t *testing.T) {
	f := newOperatorFixture(t)
	dir, err := connectStateDir("agent", f.file, false)
	require.NoError(t, err)
	require.DirExists(t, dir)
	breakConnectFile(t, brokenConnectFiles[1].edit)
	require.True(t, f.reportProfile(t).knowsState(), "the state directory names the agent")

	_, err = f.run(t, output.FormatJSON, "status")
	require.Error(t, err)
	e := usageError(t, err)
	assert.Contains(t, e.Message, "connect.json cannot be used")
	assert.Contains(t, e.Message, "no ledger to show")
	assert.Contains(t, e.Hint, "--reset")
}

func TestShadowStatusFindsAShadowOnlyLedgerWhenTheFileIsBroken(t *testing.T) {
	f := newOperatorFixture(t)
	require.NoError(t, f.ledger(t, true).Close())
	breakConnectFile(t, brokenConnectFiles[1].edit)

	out, err := f.run(t, output.FormatJSON, "status", "--shadow")
	require.NoError(t, err, out)
	var envelope struct {
		Data connectStatusReport `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &envelope), out)
	require.NotNil(t, envelope.Data.Config)
	assert.True(t, envelope.Data.Shadow)
	assert.Equal(t, 1, envelope.Data.Status.Blocked[string(admission.ReasonUnroutable)])
}

// When the state on disk and the credential name different agents, doctor
// reads the state that is there and says they disagree.
func TestDoctorOnABrokenFileSaysWhenTheStateIsAnotherAgents(t *testing.T) {
	f := newOperatorFixture(t)
	other := f.file
	other.Agent.PersonID = f.file.Agent.PersonID + 1
	dir, err := connectStateDir("agent", other, false)
	require.NoError(t, err)
	l, err := connector.OpenLedger(filepath.Join(dir, connector.LedgerFile))
	require.NoError(t, err)
	require.NoError(t, l.Close())
	breakConnectFile(t, brokenConnectFiles[1].edit)

	checks := checksByName(connectDoctorChecks(context.Background(), f.reportProfile(t)))
	assert.Equal(t, setup.StatusWarn, checks["Identity"].Status)
	assert.Contains(t, checks["Identity"].Message, fmt.Sprintf("connector state is for person %d", other.Agent.PersonID))
	assert.Equal(t, setup.StatusPass, checks["Ledger"].Status, "the ledger that is there is read: %+v", checks)
}

// A ledger that opens and then can't be read is no reason to lose the
// diagnosis either.
func TestStatusOnABrokenFileWhoseLedgerCantBeReadLeadsWithTheFile(t *testing.T) {
	f := newOperatorFixture(t)
	require.NoError(t, f.ledger(t, false).Close())
	dir, err := connectStateDir("agent", f.file, false)
	require.NoError(t, err)
	db, err := sql.Open("sqlite", filepath.Join(dir, connector.LedgerFile))
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `INSERT INTO connection (id, state, pid, changed_at, detail) VALUES (1, 'connected', 1, 'not a time', '')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	breakConnectFile(t, brokenConnectFiles[1].edit)

	_, err = f.run(t, output.FormatJSON, "status")
	require.Error(t, err)
	e := usageError(t, err)
	assert.Contains(t, e.Message, "connect.json cannot be used")
	assert.Contains(t, e.Message, "its ledger can't be read either")
	assert.Contains(t, e.Hint, "--reset")
}

// --reset is a policy flag: alone at a terminal it runs the scripted setup,
// not the guided one, which would never reset.
func TestResetAloneIsNotGuidedSetup(t *testing.T) {
	prev := connectSetupInteractive
	t.Cleanup(func() { connectSetupInteractive = prev })
	connectSetupInteractive = func(*appctx.App) bool { return true }

	bare := newConnectSetupCmd()
	require.NoError(t, bare.ParseFlags(nil))
	assert.True(t, connectSetupGuided(bare, &appctx.App{}), "the control: no flags at a terminal is guided")

	reset := newConnectSetupCmd()
	require.NoError(t, reset.ParseFlags([]string{"--reset"}))
	assert.False(t, connectSetupGuided(reset, &appctx.App{}))
}

// A reset with no project is refused, as any first setup is, and the hint
// that fixes it is this run again with a project: following it works.
func TestSetupResetWithNoProjectSaysToResetWithOne(t *testing.T) {
	f := newOperatorFixture(t)
	broken := breakConnectFile(t, brokenConnectFiles[1].edit)
	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, f.s, "agent"), "--operator", fmt.Sprint(setupOperatorPerson), "--reset")
	require.Error(t, err, out)
	e := usageError(t, err)
	assert.Equal(t, codeNotReady, e.Code)
	assert.Equal(t, fmt.Sprintf("Serve one: basecamp connect setup -P agent --operator %d --reset --serve <project-id>", setupOperatorPerson), e.Hint)
	now, _ := os.ReadFile(connectSetupPath(t, "agent"))
	assert.Equal(t, broken, now)

	// Follow it.
	command, ok := strings.CutPrefix(e.Hint, "Serve one: basecamp connect setup -P agent ")
	require.True(t, ok)
	args := strings.Fields(strings.ReplaceAll(command, "<project-id>", fmt.Sprint(setupProject)))
	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, f.s, "agent"), args...)
	require.NoError(t, err, out)
	_, err = setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
}

func TestConnectSetupRerunRepeatsEveryFlagAsGiven(t *testing.T) {
	cmd := newConnectSetupCmd()
	require.NoError(t, cmd.ParseFlags([]string{"--reset", "--allow", "1", "--allow", "2", "--ack-boost", "On it", "--allow-assignments-from-authorized=false", "--trust", "allowlist"}))
	assert.Equal(t, "basecamp connect setup -P agent --ack-boost 'On it' --allow 1 --allow 2 --allow-assignments-from-authorized=false --reset --trust allowlist",
		connectSetupRerun(cmd, "agent"))
}

// Run under the root, so inherited flags reach setup as they do for real: the
// hint repeats setup's own flags alone, quoted so a shell gives each back as
// it was.
func TestConnectSetupRerunLeavesInheritedFlagsOutAndQuotesForAShell(t *testing.T) {
	root := &cobra.Command{Use: "basecamp"}
	root.PersistentFlags().CountP("verbose", "v", "")
	root.PersistentFlags().Bool("json", false, "")
	connect := &cobra.Command{Use: "connect"}
	root.AddCommand(connect)
	cmd := newConnectSetupCmd()
	var rerun string
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		rerun = connectSetupRerun(cmd, "agent")
		return nil
	}
	connect.AddCommand(cmd)
	root.SetArgs([]string{"connect", "setup", "-v", "--json", "--reset", "--ack-boost", "it's on", "--allow", "1"})
	require.NoError(t, root.Execute())

	assert.NotContains(t, rerun, "--verbose")
	assert.NotContains(t, rerun, "--json")
	// The shell parses the hint's own words.
	words, err := exec.CommandContext(context.Background(), "/bin/sh", "-c", "set -- "+strings.TrimPrefix(rerun, "basecamp ")+`; for a in "$@"; do printf '%s\n' "$a"; done`).Output()
	require.NoError(t, err)
	assert.Equal(t, []string{"connect", "setup", "-P", "agent", "--ack-boost", "it's on", "--allow", "1", "--reset"},
		strings.Split(strings.TrimSuffix(string(words), "\n"), "\n"))
}

// A valid file copied from another profile is not this profile's: doctor
// says so as it does a broken one, as show and setup refuse it.
func TestAFileCopiedFromAnotherProfileIsNotSetUp(t *testing.T) {
	f := newOperatorFixture(t)
	breakConnectFile(t, func(s string) string {
		return strings.Replace(s, `"profile": "agent"`, `"profile": "laptop"`, 1)
	})
	checks := checkConnectorProfiles()
	require.Len(t, checks, 1)
	assert.Equal(t, "fail", checks[0].Status)
	assert.Contains(t, checks[0].Message, `connect.json names profile "laptop", and it is profile "agent"'s`)

	p := f.reportProfile(t)
	require.True(t, p.broken())
	assert.Equal(t, f.file.Agent.PersonID, p.file.Agent.PersonID, "the file's agent still says whose state to read")
	file := checksByName(connectDoctorChecks(context.Background(), p))["connect.json"]
	assert.Equal(t, setup.StatusFail, file.Status)
	assert.Equal(t, connectResetHint("agent"), file.Hint)

	// The commands that act on a connector still take the file, as the
	// connector does, so one already running can still be stopped.
	cmd := &cobra.Command{}
	cmd.SetContext(appctx.WithApp(context.Background(), newConnectSetupApp(t, f.s, "agent")))
	_, err := loadConnectProfile(cmd)
	require.NoError(t, err)
}
