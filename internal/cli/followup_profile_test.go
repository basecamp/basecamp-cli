package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/commands"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// A suggested follow-up is pasted into another shell, which may resolve a
// different profile. However this run chose its profile, the follow-up names it.
func TestFollowUpNamesTheProfileHoweverItWasChosen(t *testing.T) {
	cases := map[string]struct {
		config   func(url string) string
		env      string
		args     []string
		followUp string
	}{
		"no profiles": {
			config: func(url string) string {
				return fmt.Sprintf(`{"base_url": %q, "account_id": "99999"}`, url)
			},
			followUp: "basecamp unspotlight 456",
		},
		"default_profile": {
			config:   profilesConfig("staging"),
			followUp: "basecamp unspotlight 456 --profile staging",
		},
		"BASECAMP_PROFILE": {
			config:   profilesConfig("production"),
			env:      "staging",
			followUp: "basecamp unspotlight 456 --profile staging",
		},
		"--profile": {
			config:   profilesConfig("production"),
			args:     []string{"--profile", "staging"},
			followUp: "basecamp unspotlight 456 --profile staging",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var requested []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requested = append(requested, r.Method+" "+r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":456,"type":"Message","title":"Kickoff"}`))
			}))
			t.Cleanup(srv.Close)

			isolateRootTest(t)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("BASECAMP_TOKEN", "fake")
			t.Setenv("BASECAMP_PROFILE", c.env)
			t.Setenv("BASECAMP_ACCOUNT_ID", "")
			t.Setenv("BASECAMP_BASE_URL", "")
			dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "basecamp")
			require.NoError(t, os.MkdirAll(dir, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"),
				[]byte(c.config(srv.URL)), 0o600))

			root := NewRootCmd()
			root.AddCommand(commands.NewSpotlightCmd())
			root.SetArgs(append(c.args, "spotlight", "456", "--hints", "--json"))
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
			stdout := captureStdout(t, func() { require.NoError(t, root.Execute()) })

			var envelope output.Response
			require.NoError(t, json.Unmarshal(stdout, &envelope), string(stdout))
			assert.Equal(t, []string{"POST /99999/recordings/456/spotlight.json"}, requested)
			assert.Contains(t, envelope.Breadcrumbs, output.Breadcrumb{
				Action: "unspotlight", Cmd: c.followUp, Description: "Remove the spotlight",
			})
		})
	}
}

func profilesConfig(defaultProfile string) func(url string) string {
	return func(url string) string {
		return fmt.Sprintf(`{"default_profile": %q, "profiles": {
			"production": {"base_url": %q, "account_id": "11111"},
			"staging": {"base_url": %q, "account_id": "99999"}}}`, defaultProfile, url, url)
	}
}

func captureStdout(t *testing.T, run func()) []byte {
	t.Helper()
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = orig })
	done := make(chan []byte)
	go func() {
		out, _ := io.ReadAll(reader)
		done <- out
	}()
	run()
	os.Stdout = orig
	writer.Close()
	return <-done
}
