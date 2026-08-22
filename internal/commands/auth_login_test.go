package commands

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
)

// TestAuthLoginDeviceCodeForcesRemoteMode is the regression test for the
// --device-code → Remote flag mapping. Discovery falls back to Launchpad
// (pointed at a 404 test server), where remote mode is observable: it prints
// the paste-callback instructions instead of opening a browser and listening
// on the loopback callback port.
func TestAuthLoginDeviceCodeForcesRemoteMode(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	// Pin SSH auto-detection off so only the flag can select remote mode.
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_CLIENT", "")
	t.Setenv("SSH_TTY", "")

	// No protected-resource metadata (404) → Launchpad fallback, pointed at
	// this server. The token endpoint is never reached.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("BASECAMP_LAUNCHPAD_URL", srv.URL)

	// Remote mode reads the pasted callback URL from os.Stdin. Swap in an
	// immediate EOF so the prompt fails fast instead of blocking.
	devNull, err := os.Open(os.DevNull)
	require.NoError(t, err)
	defer devNull.Close()
	origStdin := os.Stdin
	os.Stdin = devNull
	t.Cleanup(func() { os.Stdin = origStdin })

	cfg := &config.Config{BaseURL: srv.URL}
	authMgr := auth.NewManager(cfg, srv.Client())
	authMgr.SetStore(auth.NewStore(tmpDir))
	app := &appctx.App{Config: cfg, Auth: authMgr}

	// Safety net: if the mapping regresses to local mode, the loopback
	// listener would otherwise wait out its full five-minute timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := NewAuthCmd()
	cmd.SetArgs([]string{"login", "--device-code"})
	cmd.SetContext(appctx.WithApp(ctx, app))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	err = cmd.Execute()
	require.Error(t, err, "EOF on the paste prompt must abort the login")
	assert.Contains(t, err.Error(), "no input received")

	output := out.String()
	assert.Contains(t, output, "Remote Authentication",
		"--device-code must select the remote paste-callback flow")
	assert.Contains(t, output, "Paste the callback URL")
	assert.NotContains(t, output, "Opening browser",
		"remote mode must not attempt a browser launch")
}
