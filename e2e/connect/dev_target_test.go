//go:build linux || darwin

package connect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/fakebasecamp"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
)

// The environment devTarget reads. `make test-connect-dev` sets envDev and
// requires the rest; CONTRIBUTING.md says how to get each one.
const (
	// envDev, set to 1, runs the scenarios against the local Basecamp
	// instead of the fake, and skips those only the fake can run.
	envDev = "BASECAMP_CONNECT_DEV"
	// envBaseURL is the CLI's own: the local Basecamp every command talks to.
	envBaseURL = "BASECAMP_BASE_URL"
	// envAgentProfile is the profile of an agent already connected and set
	// up with `basecamp connect setup`, in operator trust mode, serving
	// envProjectID.
	envAgentProfile = "BASECAMP_CONNECT_DEV_AGENT_PROFILE"
	// envOperatorProfile is a profile logged in as the agent's operator:
	// the person connect.json trusts.
	envOperatorProfile = "BASECAMP_CONNECT_DEV_OPERATOR_PROFILE"
	// envUntrustedProfile is a profile logged in as someone else on the
	// project, whom the agent takes no instructions from.
	envUntrustedProfile = "BASECAMP_CONNECT_DEV_UNTRUSTED_PROFILE"
	// envProjectID is the served project the scenarios post in.
	envProjectID = "BASECAMP_CONNECT_DEV_PROJECT_ID"
)

// contributing is where the dev target's prerequisites are written down.
const contributing = `CONTRIBUTING.md, "Running basecamp connect against a local Basecamp"`

// devRequested says `make test-connect-dev` asked for the local Basecamp.
func devRequested() bool { return os.Getenv(envDev) == "1" }

// devSerial runs the scenarios against the local Basecamp one at a time.
var devSerial sync.Mutex

// fakeOnly marks a scenario that runs only against the fake, and says why:
// it needs a fault or a world only the fake can produce. Against the local
// Basecamp it is skipped, so the run lists what it did not cover.
func fakeOnly(t *testing.T, why string) {
	t.Helper()
	if devRequested() {
		t.Skip("fake only: " + why)
	}
}

// devConfig is the dev target's environment, read and checked.
type devConfig struct {
	BaseURL          string
	AgentProfile     string
	OperatorProfile  string
	UntrustedProfile string
	ProjectID        int64
}

// devConfigFrom reads the dev target's environment through getenv. It
// names every variable that is missing at once, and refuses a Basecamp
// that is not local: the scenarios post and trash messages as the
// developer's profiles.
func devConfigFrom(getenv func(string) string) (devConfig, error) {
	var missing []string
	get := func(key string) string {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}
	c := devConfig{
		BaseURL:          get(envBaseURL),
		AgentProfile:     get(envAgentProfile),
		OperatorProfile:  get(envOperatorProfile),
		UntrustedProfile: get(envUntrustedProfile),
	}
	project := get(envProjectID)
	if len(missing) > 0 {
		return devConfig{}, fmt.Errorf("the local Basecamp target needs %s set; see %s", strings.Join(missing, ", "), contributing)
	}

	id, err := strconv.ParseInt(project, 10, 64)
	if err != nil || id <= 0 {
		return devConfig{}, fmt.Errorf("%s=%q is not a project id", envProjectID, project)
	}
	c.ProjectID = id

	u, err := url.Parse(c.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return devConfig{}, fmt.Errorf("%s=%q is not an http(s) URL", envBaseURL, c.BaseURL)
	}
	if !hostutil.IsLocalhost(u.Host) {
		return devConfig{}, fmt.Errorf("%s=%q is not a local Basecamp: the scenarios post and trash messages as your profiles, so they run only against localhost or a *.localhost host", envBaseURL, c.BaseURL)
	}

	if c.AgentProfile == c.OperatorProfile || c.AgentProfile == c.UntrustedProfile || c.OperatorProfile == c.UntrustedProfile {
		return devConfig{}, fmt.Errorf("%s, %s and %s must name three different profiles", envAgentProfile, envOperatorProfile, envUntrustedProfile)
	}
	return c, nil
}

// devEnv is the environment the CLI gets against the local Basecamp: the
// developer's own, so their config and credentials (keyring included) are
// used as they are, less what would change who a command acts as or where.
// A BASECAMP_TOKEN would stand in for every profile's credential (and the
// connector refuses to run under one); a default profile, account, project
// or to-do list would steer a command the scenarios name exactly.
//
// stateHome, when set, is the connector's state for the test alone: its
// ledger and its instance lock, which live under XDG_STATE_HOME. A
// connector the developer runs for the same agent keeps its own, so a test
// can neither read its ledger nor hold its lock. Empty leaves the
// developer's.
func devEnv(environ []string, stateHome string) []string {
	drop := map[string]bool{
		"BASECAMP_TOKEN":           true,
		"BASECAMP_PROFILE":         true,
		"BASECAMP_ACCOUNT_ID":      true,
		"BASECAMP_PROJECT_ID":      true,
		"BASECAMP_TODOLIST_ID":     true,
		"BASECAMP_NO_UPDATE_CHECK": true,
	}
	if stateHome != "" {
		drop["XDG_STATE_HOME"] = true
	}
	env := make([]string, 0, len(environ)+2)
	for _, kv := range environ {
		key, _, _ := strings.Cut(kv, "=")
		if !drop[key] {
			env = append(env, kv)
		}
	}
	env = append(env, "BASECAMP_NO_UPDATE_CHECK=1")
	if stateHome != "" {
		env = append(env, "XDG_STATE_HOME="+stateHome)
	}
	return env
}

// devTarget is a local Basecamp as a Target, driven with the CLI under
// test and the developer's own profiles.
//
// It assumes the agent is connected and set up already (`basecamp auth
// agent connect` and `basecamp connect setup`), and checks that setup with
// `basecamp connect show` rather than running it: setup rewrites
// connect.json, which is the developer's. Each scenario gets a message of
// its own in the served project, posted by the operator with nobody else
// subscribed, and trashed, comments and all, when the scenario ends. Its
// connector runs under a state directory of its own (see devEnv), and it
// refuses to start while a connector for the same agent is running from the
// developer's state: that connector would be handed the scenario's
// mentions too.
type devTarget struct {
	h     *Harness
	cfg   devConfig
	agent ServingAgent
	// operatorID is the person connect.json trusts.
	operatorID int64
	// message is the scenario's message, which every comment is on.
	message int64
}

func newDevTarget(t *testing.T) *devTarget {
	t.Helper()
	cfg, err := devConfigFrom(os.Getenv)
	require.NoError(t, err)
	dir := t.TempDir()
	// The working directory is a fresh one, so no repository's
	// .basecamp/config.json is read.
	d := &devTarget{
		h:   &Harness{t: t, Dir: dir, env: devEnv(os.Environ(), filepath.Join(dir, "state"))},
		cfg: cfg,
	}

	setup := d.connectShow(t)
	d.agent = ServingAgent{Profile: cfg.AgentProfile, PersonID: setup.Agent.PersonID, AccountID: setup.AccountID}
	d.operatorID = setup.Trust.OperatorID
	d.requireNoRunningConnector(t)

	var msg struct {
		ID      int64     `json:"id"`
		Creator devPerson `json:"creator"`
	}
	d.as(t, cfg.OperatorProfile, &msg, "messages", "create",
		"--project", strconv.FormatInt(cfg.ProjectID, 10), "--no-subscribe",
		"connect e2e "+t.Name()+" "+time.Now().UTC().Format(time.RFC3339),
		"A message the connector's end-to-end scenarios comment on. It is trashed when the scenario ends.")
	require.NotZero(t, msg.ID, "the message has no id")
	d.message = msg.ID
	t.Cleanup(func() { d.trash(t) })
	require.Equal(t, d.operatorID, msg.Creator.ID,
		"%s is person %d, not the operator connect.json trusts (%d)", envOperatorProfile, msg.Creator.ID, d.operatorID)
	return d
}

func (d *devTarget) CLI() *Harness { return d.h }

func (d *devTarget) Agent() ServingAgent { return d.agent }

func (d *devTarget) OperatorMentionsAgent(t *testing.T) Posted {
	t.Helper()
	return d.post(t, d.cfg.OperatorProfile)
}

func (d *devTarget) UntrustedMentionsAgent(t *testing.T) Posted {
	t.Helper()
	p := d.post(t, d.cfg.UntrustedProfile)
	require.NotEqual(t, d.operatorID, p.AuthorID, "%s is the operator", envUntrustedProfile)
	return p
}

// post has profile comment on the scenario's message, mentioning the agent
// by its person id as the CLI's Markdown writes a mention, and returns the
// comment as Basecamp answered the create: the same representation the
// connector reads.
func (d *devTarget) post(t *testing.T, profile string) Posted {
	t.Helper()
	var c struct {
		ID      int64  `json:"id"`
		Type    string `json:"type"`
		Title   string `json:"title"`
		AppURL  string `json:"app_url"`
		Content string `json:"content"`
		Parent  struct {
			ID int64 `json:"id"`
		} `json:"parent"`
		Bucket struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"bucket"`
		Creator devPerson `json:"creator"`
	}
	ask := "Could you review the launch plan, [@agent](person:" + strconv.FormatInt(d.agent.PersonID, 10) + ")? Thanks"
	d.as(t, profile, &c, "comments", "create", strconv.FormatInt(d.message, 10), ask)
	instruction, err := withoutMention(c.Content)
	require.NoError(t, err, "the comment as Basecamp holds it: %q", c.Content)
	return Posted{
		RecordingID: c.ID,
		ParentID:    c.Parent.ID,
		BucketID:    c.Bucket.ID,
		ProjectName: c.Bucket.Name,
		Type:        c.Type,
		Title:       c.Title,
		URL:         c.AppURL,
		AuthorID:    c.Creator.ID,
		AuthorName:  c.Creator.Name,
		Instruction: instruction,
	}
}

// mentionElement is one mention attachment, start tag to end tag, as
// Basecamp writes it into rich text.
var mentionElement = regexp.MustCompile(`(?s)<bc-attachment\b[^>]*\bcontent-type="application/vnd\.basecamp\.mention"[^>]*>.*?</bc-attachment>`)

// withoutMention is content with its one mention element replaced by a
// space, which is what the connector hands off of a comment that mentions
// only the agent. It is worked out here from the markup, not by the
// connector's own code, so the two can disagree.
func withoutMention(content string) (string, error) {
	found := mentionElement.FindAllStringIndex(content, -1)
	if len(found) != 1 {
		return "", fmt.Errorf("want one mention in the comment, found %d", len(found))
	}
	return content[:found[0][0]] + " " + content[found[0][1]:], nil
}

// devPerson is a person as a Basecamp record names them.
type devPerson struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// devSetup is the part of connect.json the scenarios depend on.
type devSetup struct {
	AccountID int64 `json:"-"`
	Agent     struct {
		PersonID int64 `json:"person_id"`
	} `json:"agent"`
	Trust struct {
		Mode       string `json:"mode"`
		OperatorID int64  `json:"operator_id"`
	} `json:"trust"`
	Projects map[string]json.RawMessage `json:"projects"`
}

// connectShow reads the agent's setup and requires what the scenarios
// depend on: operator trust, so the untrusted profile is untrusted, and the
// project served.
func (d *devTarget) connectShow(t *testing.T) devSetup {
	t.Helper()
	var file struct {
		devSetup
		AccountID string `json:"account_id"`
	}
	d.data(t, d.h.MustRun(t, "connect", "show", "-P", d.cfg.AgentProfile, "--json"), &file)
	s := file.devSetup
	var err error
	s.AccountID, err = strconv.ParseInt(file.AccountID, 10, 64)
	require.NoError(t, err, "connect.json's account_id %q", file.AccountID)
	require.Equal(t, "operator", s.Trust.Mode,
		"%s is set up in %q trust mode; the scenarios need operator trust, setup's default", envAgentProfile, s.Trust.Mode)
	require.Contains(t, s.Projects, strconv.FormatInt(d.cfg.ProjectID, 10),
		"%s does not serve project %d; serve it with: basecamp connect setup -P %s --serve %d", envAgentProfile, d.cfg.ProjectID, d.cfg.AgentProfile, d.cfg.ProjectID)
	return s
}

// requireNoRunningConnector refuses to go on while a connector for the
// agent runs from the developer's own state, which `connect status` reads
// without taking its lock. A profile whose connector has never run has no
// ledger, and status says so.
func (d *devTarget) requireNoRunningConnector(t *testing.T) {
	t.Helper()
	own := &Harness{t: t, Dir: d.h.Dir, env: devEnv(os.Environ(), "")}
	res := own.Run(t, "connect", "status", "-P", d.cfg.AgentProfile, "--json")
	if res.ExitCode != 0 {
		require.Contains(t, res.Stdout+res.Stderr, "has no ledger yet", "basecamp connect status -P %s\n%s%s", d.cfg.AgentProfile, res.Stdout, res.Stderr)
		return
	}
	var status struct {
		LockHolder *struct {
			PID       int    `json:"pid"`
			PIDStatus string `json:"pid_status"`
		} `json:"lock_holder"`
	}
	d.data(t, res, &status)
	if status.LockHolder != nil && status.LockHolder.PIDStatus == "present" {
		t.Fatalf("a connector for %s may be running (pid %d): stop it first, or it is handed the scenarios' mentions too", d.cfg.AgentProfile, status.LockHolder.PID)
	}
}

// as runs a command as profile in the agent's account, requires that it
// succeeds, and decodes its JSON data into v.
func (d *devTarget) as(t *testing.T, profile string, v any, args ...string) {
	t.Helper()
	args = append(args, "-P", profile, "--account", strconv.FormatInt(d.agent.AccountID, 10), "--json")
	d.data(t, d.h.MustRun(t, args...), v)
}

// trash trashes the scenario's message, and with it every comment on it.
// It runs as a cleanup, after the test's context has ended.
func (d *devTarget) trash(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	res := d.h.run(ctx, t, "messages", "trash", strconv.FormatInt(d.message, 10),
		"-P", d.cfg.OperatorProfile, "--account", strconv.FormatInt(d.agent.AccountID, 10), "--json")
	if res.ExitCode != 0 {
		t.Errorf("trash the scenario's message %d: exit %d\n%s%s", d.message, res.ExitCode, res.Stdout, res.Stderr)
	}
}

// data decodes a command's JSON envelope's data into v.
func (d *devTarget) data(t *testing.T, res Result, v any) {
	t.Helper()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Stdout), &envelope), res.Stdout)
	require.NotEmpty(t, envelope.Data, "no data: %s", res.Stdout)
	require.NoError(t, json.Unmarshal(envelope.Data, v), res.Stdout)
}

// The dev target refuses to start on an environment it cannot run in, and
// says what is wrong with it, all of it at once.
func TestDevConfigNamesEveryMissingVariable(t *testing.T) {
	_, err := devConfigFrom(envOf(map[string]string{envAgentProfile: "agent"}))
	require.Error(t, err)
	for _, key := range []string{envBaseURL, envOperatorProfile, envUntrustedProfile, envProjectID} {
		assert.Contains(t, err.Error(), key)
	}
	assert.NotContains(t, err.Error(), envAgentProfile)
	assert.Contains(t, err.Error(), "CONTRIBUTING.md")

	_, err = devConfigFrom(envOf(nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), envAgentProfile)
}

func TestDevConfigReadsAValidEnvironment(t *testing.T) {
	c, err := devConfigFrom(envOf(validDevEnv()))
	require.NoError(t, err)
	assert.Equal(t, devConfig{
		BaseURL:          "http://3.basecamp.localhost:3001",
		AgentProfile:     "agent",
		OperatorProfile:  "rob",
		UntrustedProfile: "jason",
		ProjectID:        2085958499,
	}, c)
}

func TestDevConfigRefusesWhatIsNotAProjectOrALocalBasecamp(t *testing.T) {
	for _, project := range []string{"Launch", "0", "-4", "12x"} {
		env := validDevEnv()
		env[envProjectID] = project
		_, err := devConfigFrom(envOf(env))
		assert.ErrorContains(t, err, "not a project id", "%q", project)
	}
	for _, base := range []string{"https://3.basecampapi.com", "http://basecamp.example", "3.basecamp.localhost:3001", "ftp://localhost"} {
		env := validDevEnv()
		env[envBaseURL] = base
		_, err := devConfigFrom(envOf(env))
		assert.ErrorContains(t, err, envBaseURL, "%q", base)
	}
	for _, base := range []string{"http://localhost:3001", "http://127.0.0.1:3001", "http://[::1]:3001", "https://3.basecamp.localhost"} {
		env := validDevEnv()
		env[envBaseURL] = base
		_, err := devConfigFrom(envOf(env))
		assert.NoError(t, err, "%q", base)
	}
	env := validDevEnv()
	env[envUntrustedProfile] = env[envOperatorProfile]
	_, err := devConfigFrom(envOf(env))
	assert.ErrorContains(t, err, "three different profiles")
}

// The CLI against a local Basecamp gets the developer's environment, less
// what would change who a command acts as, with the connector's state the
// test's own.
func TestDevEnvKeepsTheDevelopersCredentialsAndIsolatesTheConnectorState(t *testing.T) {
	environ := []string{
		"HOME=/home/dev",
		"XDG_CONFIG_HOME=/home/dev/.config",
		"XDG_STATE_HOME=/home/dev/.local/state",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus",
		"BASECAMP_BASE_URL=http://3.basecamp.localhost:3001",
		"BASECAMP_TOKEN=secret",
		"BASECAMP_PROFILE=rob",
		"BASECAMP_ACCOUNT_ID=1",
		"BASECAMP_PROJECT_ID=2",
		"BASECAMP_TODOLIST_ID=3",
		"BASECAMP_NO_UPDATE_CHECK=0",
	}
	assert.Equal(t, []string{
		"HOME=/home/dev",
		"XDG_CONFIG_HOME=/home/dev/.config",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus",
		"BASECAMP_BASE_URL=http://3.basecamp.localhost:3001",
		"BASECAMP_NO_UPDATE_CHECK=1",
		"XDG_STATE_HOME=/tmp/test/state",
	}, devEnv(environ, "/tmp/test/state"))

	own := devEnv(environ, "")
	assert.Contains(t, own, "XDG_STATE_HOME=/home/dev/.local/state", "the developer's own state, to look for their connector")
	assert.NotContains(t, own, "BASECAMP_TOKEN=secret")
}

// The instruction a request line should carry is worked out from the
// comment's markup: its one mention, start tag to end tag, becomes a space,
// whatever Basecamp renders inside it.
func TestWithoutMentionTakesOutTheOneMention(t *testing.T) {
	got, err := withoutMention(`<p>Could you review the launch plan, ` +
		`<bc-attachment sgid="BAh7CEkiCGdpZAY6BkVU--1b0c" content-type="application/vnd.basecamp.mention">` +
		`<figure><img src="/avatar.png"><figcaption>Chef</figcaption></figure></bc-attachment>? Thanks</p>`)
	require.NoError(t, err)
	assert.Equal(t, `<p>Could you review the launch plan,  ? Thanks</p>`, got)

	got, err = withoutMention("<p>Hi " + fakebasecamp.Mention(fakebasecamp.AgentID) + "</p>")
	require.NoError(t, err)
	assert.Equal(t, "<p>Hi  </p>", got)

	for _, content := range []string{
		`<p>No mention here</p>`,
		`<p>` + fakebasecamp.Mention(1) + fakebasecamp.Mention(2) + `</p>`,
		`<p><bc-attachment sgid="x" content-type="image/png"></bc-attachment></p>`,
	} {
		_, err := withoutMention(content)
		assert.Error(t, err, "%q", content)
	}
}

func validDevEnv() map[string]string {
	return map[string]string{
		envBaseURL:          "http://3.basecamp.localhost:3001",
		envAgentProfile:     "agent",
		envOperatorProfile:  "rob",
		envUntrustedProfile: "jason",
		envProjectID:        "2085958499",
	}
}

func envOf(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}
