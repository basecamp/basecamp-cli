//go:build unix

package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

const (
	agentID    int64 = 52007412
	operatorID int64 = 26909558
	projectID  int64 = 48699913
	otherProj  int64 = 555
)

// validFile is a complete connect.json for the agent profile, rooted in dir.
func validFile(t *testing.T) File {
	t.Helper()
	f := New("agent")
	f.AccountID = "2914079"
	f.Agent = Agent{PersonID: agentID, Kind: KindAgent}
	f.Trust.OperatorID = operatorID
	f.Projects[projectID] = admission.Route{Path: t.TempDir(), Class: "internal", WatchCompletions: true}
	f.Projects[otherProj] = admission.Route{Path: t.TempDir()}
	return f
}

func configDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	// Where ownership cannot be read, connect.json must live under the
	// user's profile directory; make the test's root that directory.
	t.Setenv("USERPROFILE", root)
	dir := filepath.Join(root, "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	return dir
}

// TestAdmissionReadsWhatSetupWrites is the seam: the file setup saves is the
// policy admission parses, watch_completions included, with nothing lost on
// the way through either decoder.
func TestAdmissionReadsWhatSetupWrites(t *testing.T) {
	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)
	f := validFile(t)
	require.NoError(t, save(path, f))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	p, err := admission.ParsePolicy(data)
	require.NoError(t, err)
	p.AgentID = agentID
	require.NoError(t, p.Validate())

	assert.Equal(t, admission.TrustOperator, p.Trust.Mode)
	assert.Equal(t, operatorID, p.Trust.OperatorID)
	assert.Equal(t, f.Projects, p.Projects)
	assert.True(t, p.Projects[projectID].WatchCompletions)

	loaded, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, f, loaded)
}

func TestSaveWritesTheSpecDefaults(t *testing.T) {
	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)
	require.NoError(t, save(path, validFile(t)))

	var raw map[string]any
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, "spawn", raw["driver"])
	assert.EqualValues(t, 2, raw["concurrency"])
	assert.Equal(t, "45m0s", raw["deadline"])
	assert.Equal(t, false, raw["worktrees"])
}

func TestLoadReportsAMissingFileAsNotExist(t *testing.T) {
	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)
	_, err = Load(path)
	assert.True(t, errors.Is(err, os.ErrNotExist), "got %v", err)
}

// A misspelled key silently ignored is a setting the operator believes is on
// and is not; for trust and routes that is not a harmless typo.
func TestParseRefusesUnknownKeys(t *testing.T) {
	data, err := json.Marshal(validFile(t))
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	projects := raw["projects"].(map[string]any)
	route := projects["48699913"].(map[string]any)
	delete(route, "watch_completions")
	route["watch_completion"] = true
	data, err = json.Marshal(raw)
	require.NoError(t, err)

	_, err = Parse(data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "watch_completion")
}

func TestValidateFailsClosed(t *testing.T) {
	for name, mutate := range map[string]func(*File){
		"wrong version":             func(f *File) { f.Version = 2 },
		"no profile":                func(f *File) { f.Profile = "" },
		"profile with a slash":      func(f *File) { f.Profile = "../agent" },
		"non-numeric account":       func(f *File) { f.AccountID = "acme" },
		"account not canonical":     func(f *File) { f.AccountID = "02914079" },
		"empty trust mode":          func(f *File) { f.Trust.Mode = "" },
		"no agent":                  func(f *File) { f.Agent.PersonID = 0 },
		"unknown agent kind":        func(f *File) { f.Agent.Kind = "robot" },
		"agent with an identity":    func(f *File) { f.Agent.IdentityID = 7 },
		"bot user without identity": func(f *File) { f.Agent.Kind = KindBotUser },
		"no operator":               func(f *File) { f.Trust.OperatorID = 0 },
		"operator is the agent":     func(f *File) { f.Trust.OperatorID = agentID },
		"unknown trust mode":        func(f *File) { f.Trust.Mode = "domain" },
		"agent in the allowlist": func(f *File) {
			f.Trust = admission.Trust{Mode: admission.TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{agentID}}
		},
		"relative route":       func(f *File) { f.Projects[projectID] = admission.Route{Path: "work/app"} },
		"unclean route":        func(f *File) { f.Projects[projectID] = admission.Route{Path: "/work/../etc"} },
		"route without a path": func(f *File) { f.Projects[projectID] = admission.Route{} },
		"class with spaces":    func(f *File) { f.Projects[projectID] = admission.Route{Path: "/work", Class: "a b"} },
		"unknown driver":       func(f *File) { f.Driver = "fork" },
		"no concurrency":       func(f *File) { f.Concurrency = 0 },
		"too much concurrency": func(f *File) { f.Concurrency = MaxConcurrency + 1 },
		"deadline too short":   func(f *File) { f.Deadline = Duration(time.Second) },
		"deadline too long":    func(f *File) { f.Deadline = Duration(48 * time.Hour) },
		"route for a non-project": func(f *File) {
			f.Projects[-1] = admission.Route{Path: "/work"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := validFile(t)
			mutate(&f)
			assert.Error(t, f.Validate())

			path, err := Path(configDir(t), "agent")
			require.NoError(t, err)
			assert.Error(t, save(path, f), "Save must refuse what Validate refuses")
			_, statErr := os.Stat(path)
			assert.True(t, os.IsNotExist(statErr), "nothing is written")
		})
	}
	require.NoError(t, validFile(t).Validate())
}

func TestPathRefusesAProfileThatIsNotAName(t *testing.T) {
	for _, name := range []string{"", "..", "a/b", "a b", `a\b`} {
		_, err := Path("/cfg", name)
		assert.Error(t, err, name)
	}
	path, err := Path("/cfg", "agent_1")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/cfg", "connect", "agent_1", "connect.json"), path)
}

func TestParseRefusesDuplicateKeysAndTrailingData(t *testing.T) {
	data, err := json.Marshal(validFile(t))
	require.NoError(t, err)
	_, err = Parse(data)
	require.NoError(t, err)

	for name, doc := range map[string]string{
		"trailing ]":             string(data) + "]",
		"trailing }":             string(data) + "}",
		"trailing object":        string(data) + "{}",
		"duplicate top-level":    `{"driver":"acp",` + string(data[1:]),
		"duplicate operator_id":  strings.Replace(string(data), `"operator_id":`, `"operator_id":1,"operator_id":`, 1),
		"duplicate nested route": strings.Replace(string(data), `"48699913":{`, `"48699913":{"path":"/elsewhere",`, 1),
		"case variant key":       strings.Replace(string(data), `"trust":`, `"Trust":`, 1),
		"long s variant key":     strings.Replace(string(data), `"worktrees"`, `"worktreeſ"`, 1),
		"padded project id":      strings.Replace(string(data), `"48699913":{`, `"048699913":{`, 1),
		"signed project id":      strings.Replace(string(data), `"48699913":{`, `"+48699913":{`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			require.NotEqual(t, string(data), doc, "the fixture changed the document")
			_, err := Parse([]byte(doc))
			assert.Error(t, err)
		})
	}
}

// VerifyAgent is what makes a credential swapped behind setup's back
// harmless: whoever acts on connect.json proves the file and the credential
// name one agent.
func TestVerifyAgent(t *testing.T) {
	f := validFile(t)
	require.NoError(t, f.VerifyAgent(KindAgent, agentID, 0))

	assert.Error(t, f.VerifyAgent(KindAgent, agentID+1, 0), "another person")
	assert.Error(t, f.VerifyAgent(KindAgent, 0, 0), "no person")
	assert.Error(t, f.VerifyAgent(KindBotUser, agentID, 4242), "another kind")

	bot := validFile(t)
	bot.Agent = Agent{PersonID: agentID, Kind: KindBotUser, IdentityID: 4242}
	require.NoError(t, bot.VerifyAgent(KindBotUser, agentID, 4242))
	assert.Error(t, bot.VerifyAgent(KindBotUser, agentID, 99), "another identity")
	assert.Error(t, bot.VerifyAgent(KindAgent, agentID, 4242), "another kind")
}

// connect.json is written only under the credential's lock. The proof is
// auth's to make: no other package can implement it, and Save refuses
// anything that is not a live one.
func TestSaveRefusesWithoutAHeldCredential(t *testing.T) {
	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)
	require.Error(t, Save(nil, path, validFile(t)))
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "nothing is written")
}

// A proof this package could make for itself is not one auth made: a struct
// that embeds the interface promotes its unexported method and can answer
// anything, and Save refuses it.
type forgedHold struct{ auth.HeldCredential }

// It answers everything a reader might ask, and is still not a proof auth
// made.
func (forgedHold) Valid() bool                    { return true }
func (forgedHold) Key() string                    { return "profile:agent" }
func (forgedHold) Credentials() *auth.Credentials { return &auth.Credentials{AccessToken: "forged"} }

func TestSaveRefusesAForgedHold(t *testing.T) {
	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)
	require.Error(t, Save(forgedHold{}, path, validFile(t)))
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "nothing is written")
}

// A lock on another profile's credential is not a lock on this one.
func TestSaveRefusesAHoldOnAnotherProfile(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	store := auth.NewStore(t.TempDir())
	const otherKey = "profile:other"
	require.NoError(t, store.Save(otherKey, &auth.Credentials{AccessToken: "t", OAuthType: "agent"}))

	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)
	f := validFile(t) // profile "agent"
	err = store.WithCredential(context.Background(), otherKey, func(held auth.HeldCredential) error {
		return Save(held, path, f)
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profile \"agent\"")
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "nothing is written")
}

func TestWorkerIsOneSetupKnowsAndDefaultsToClaude(t *testing.T) {
	f := validFile(t)
	assert.Equal(t, WorkerClaude, f.WorkerName())
	f.Worker = ""
	require.NoError(t, f.Validate(), "a file written before the field existed")
	assert.Equal(t, WorkerClaude, f.WorkerName())
	f.Worker = "gemini"
	assert.Error(t, f.Validate())

	_, err := Apply(validFile(t), Changes{Worker: "gemini"})
	assert.Error(t, err)
	next, err := Apply(validFile(t), Changes{Worker: WorkerClaude})
	require.NoError(t, err)
	assert.Equal(t, WorkerClaude, next.Worker)
}
