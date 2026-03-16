package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/names"
	"github.com/basecamp/basecamp-cli/internal/output"
)

func TestAtomicWriteFile_OverwriteExisting(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.json")

	// Create initial file
	require.NoError(t, atomicWriteFile(path, []byte(`{"v":1}`)))

	// Overwrite (exercises the Windows pre-remove path)
	require.NoError(t, atomicWriteFile(path, []byte(`{"v":2}`)),
		"overwrite of existing file must succeed")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, `{"v":2}`, string(data))
}

func TestAtomicWriteFile_Permissions(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "secret.json")

	require.NoError(t, atomicWriteFile(path, []byte(`{}`)))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm(),
		"file should have restricted permissions")
}

func TestAtomicWriteFile_NoStaleTempFiles(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.json")

	require.NoError(t, atomicWriteFile(path, []byte(`{}`)))

	entries, err := os.ReadDir(tmpDir)
	require.NoError(t, err)
	for _, e := range entries {
		if e.Name() != "config.json" {
			t.Errorf("stale temp file left behind: %s", e.Name())
		}
	}
}

// --- Trust command helpers ---

// envelope wraps the JSON output from app.OK.
type envelope struct {
	OK   bool            `json:"ok"`
	Data json.RawMessage `json:"data"`
}

func setupConfigTestApp(t *testing.T) (*appctx.App, *bytes.Buffer) {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := &config.Config{
		BaseURL:  "https://3.basecampapi.com",
		CacheDir: t.TempDir(),
		Sources:  make(map[string]string),
	}

	buf := &bytes.Buffer{}
	app := &appctx.App{
		Config: cfg,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
		Flags: appctx.GlobalFlags{JSON: true},
	}
	return app, buf
}

func executeConfigCommand(app *appctx.App, args ...string) error {
	cmd := NewConfigCmd()
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd.Execute()
}

// parseEnvelopeData extracts the data field from the JSON envelope.
func parseEnvelopeData(t *testing.T, buf *bytes.Buffer, dest any) {
	t.Helper()
	var env envelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env), "failed to parse envelope: %s", buf.String())
	assert.True(t, env.OK)
	require.NoError(t, json.Unmarshal(env.Data, dest), "failed to parse data: %s", string(env.Data))
}

// --- Trust subcommand structure tests ---

func TestConfigTrustSubcommands(t *testing.T) {
	cmd := NewConfigCmd()

	for _, name := range []string{"trust", "untrust"} {
		sub, _, err := cmd.Find([]string{name})
		assert.NoError(t, err, "expected subcommand %q to exist", name)
		assert.NotNil(t, sub, "expected subcommand %q to exist", name)
		assert.NotEmpty(t, sub.Short, "expected non-empty Short for %q", name)
	}
}

// --- Trust command tests ---

func TestConfigTrust_ExplicitPath(t *testing.T) {
	app, buf := setupConfigTestApp(t)

	// Create a config file to trust
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".basecamp", "config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0755))
	require.NoError(t, os.WriteFile(configPath, []byte(`{}`), 0644))

	err := executeConfigCommand(app, "trust", configPath)
	require.NoError(t, err)

	var result map[string]any
	parseEnvelopeData(t, buf, &result)
	assert.Equal(t, "trusted", result["status"])

	// Verify trust store was written
	ts := config.NewTrustStore(config.GlobalConfigDir())
	assert.True(t, ts.IsTrusted(configPath))
}

func TestConfigTrust_NonexistentPath(t *testing.T) {
	app, _ := setupConfigTestApp(t)

	err := executeConfigCommand(app, "trust", "/nonexistent/path/config.json")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "config file not found")
}

func TestConfigTrust_ListEmpty(t *testing.T) {
	app, buf := setupConfigTestApp(t)

	err := executeConfigCommand(app, "trust", "--list")
	require.NoError(t, err)

	var result []any
	parseEnvelopeData(t, buf, &result)
	assert.Empty(t, result)
}

func TestConfigTrust_ListWithEntries(t *testing.T) {
	app, buf := setupConfigTestApp(t)

	// Trust a file first
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{}`), 0644))
	require.NoError(t, executeConfigCommand(app, "trust", configPath))

	buf.Reset()
	err := executeConfigCommand(app, "trust", "--list")
	require.NoError(t, err)

	var result []map[string]string
	parseEnvelopeData(t, buf, &result)
	assert.Len(t, result, 1)
	assert.NotEmpty(t, result[0]["path"])
	assert.NotEmpty(t, result[0]["trusted_at"])
}

func TestConfigTrust_ListRejectsArgs(t *testing.T) {
	app, _ := setupConfigTestApp(t)

	err := executeConfigCommand(app, "trust", "--list", "/some/path")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "--list does not accept a path argument")
}

// --- Untrust command tests ---

func TestConfigUntrust_ExplicitPath(t *testing.T) {
	app, buf := setupConfigTestApp(t)

	// Trust, then untrust
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{}`), 0644))
	require.NoError(t, executeConfigCommand(app, "trust", configPath))

	buf.Reset()
	err := executeConfigCommand(app, "untrust", configPath)
	require.NoError(t, err)

	var result map[string]any
	parseEnvelopeData(t, buf, &result)
	assert.Equal(t, "untrusted", result["status"])

	// Verify removed
	ts := config.NewTrustStore(config.GlobalConfigDir())
	assert.False(t, ts.IsTrusted(configPath))
}

func TestConfigUntrust_NotTrusted(t *testing.T) {
	app, buf := setupConfigTestApp(t)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{}`), 0644))

	err := executeConfigCommand(app, "untrust", configPath)
	require.NoError(t, err)

	var result map[string]any
	parseEnvelopeData(t, buf, &result)
	assert.Equal(t, "not_trusted", result["status"])
}

func TestConfigUntrust_DeletedFile(t *testing.T) {
	app, buf := setupConfigTestApp(t)

	// Trust a file, then delete it, then untrust
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{}`), 0644))
	require.NoError(t, executeConfigCommand(app, "trust", configPath))

	// Delete the file
	require.NoError(t, os.Remove(configPath))

	// Untrust with explicit path should still work
	buf.Reset()
	err := executeConfigCommand(app, "untrust", configPath)
	require.NoError(t, err)

	var result map[string]any
	parseEnvelopeData(t, buf, &result)
	assert.Equal(t, "untrusted", result["status"], "must be able to untrust deleted files")
}

// --- Path resolution tests ---

func TestResolveConfigTrustPath_ExplicitMustExist(t *testing.T) {
	_, err := resolveConfigTrustPath([]string{"/nonexistent/config.json"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "config file not found")
}

func TestResolveUntrustPath_ExplicitAllowsMissing(t *testing.T) {
	path, err := resolveUntrustPath([]string{"/nonexistent/config.json"})
	assert.NoError(t, err, "untrust should accept nonexistent explicit paths")
	assert.Contains(t, path, "nonexistent")
}

func TestResolveConfigTrustPath_CWDDiscovery(t *testing.T) {
	tmpDir, _ := filepath.EvalSymlinks(t.TempDir())
	configPath := filepath.Join(tmpDir, ".basecamp", "config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0755))
	require.NoError(t, os.WriteFile(configPath, []byte(`{}`), 0644))

	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(tmpDir))
	defer os.Chdir(origDir)

	path, err := resolveConfigTrustPath(nil)
	require.NoError(t, err)
	assert.Equal(t, configPath, path)
}

func TestResolveConfigTrustPath_NoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(tmpDir))
	defer os.Chdir(origDir)

	_, err := resolveConfigTrustPath(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no .basecamp/config.json found")
}

// --- isAuthorityKey tests ---

func TestIsAuthorityKey(t *testing.T) {
	assert.True(t, isAuthorityKey("base_url"))
	assert.True(t, isAuthorityKey("default_profile"))
	assert.True(t, isAuthorityKey("profiles"))
	assert.False(t, isAuthorityKey("account_id"))
	assert.False(t, isAuthorityKey("project_id"))
	assert.False(t, isAuthorityKey("format"))
}

// --- config set authority-key warning test ---

func TestConfigSet_AuthorityKeyWarnsWithPath(t *testing.T) {
	app, _ := setupConfigTestApp(t)

	// Work in a temp dir so config set writes .basecamp/config.json there
	tmpDir, _ := filepath.EvalSymlinks(t.TempDir())
	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(tmpDir))
	defer os.Chdir(origDir)

	require.NoError(t, os.MkdirAll(".basecamp", 0755))

	// Capture stderr for the warning
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	err := executeConfigCommand(app, "set", "base_url", "https://custom.example.com")
	require.NoError(t, err)

	w.Close()
	var buf [4096]byte
	n, _ := r.Read(buf[:])
	os.Stderr = origStderr

	stderr := string(buf[:n])
	absPath := filepath.Join(tmpDir, ".basecamp", "config.json")

	assert.Contains(t, stderr, "authority key")
	assert.Contains(t, stderr, "requires trust")
	assert.Contains(t, stderr, absPath, "warning must include the exact config path")
	assert.Contains(t, stderr, "'"+absPath+"'", "path must be single-quoted for shell safety")
}

// --- Config project tests ---

// setupConfigProjectTestApp creates a test app wired to an httptest server
// that serves project data for the names resolver.
func setupConfigProjectTestApp(t *testing.T) (*appctx.App, *bytes.Buffer, *httptest.Server) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": 12345, "name": "Project Alpha"},
			{"id": 67890, "name": "Project Beta"},
		})
	}))
	t.Cleanup(server.Close)

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := &config.Config{
		BaseURL:   server.URL,
		AccountID: "99999",
		CacheDir:  t.TempDir(),
		Sources:   make(map[string]string),
	}

	sdkClient := basecamp.NewClient(
		&basecamp.Config{BaseURL: server.URL},
		&testTokenProvider{},
	)

	buf := &bytes.Buffer{}
	app := &appctx.App{
		Config: cfg,
		SDK:    sdkClient,
		Names:  names.NewResolver(sdkClient, nil, "99999"),
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
		Flags: appctx.GlobalFlags{JSON: true},
	}
	return app, buf, server
}

// executeConfigProjectCmd runs `config project` with optional extra args,
// registering the persistent --project flag that normally lives on the root command.
func executeConfigProjectCmd(app *appctx.App, extraArgs ...string) error {
	cmd := NewConfigCmd()
	cmd.PersistentFlags().StringVarP(&app.Flags.Project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&app.Flags.Project, "in", "", "Project ID or name (alias for --project)")
	args := append([]string{"project"}, extraArgs...)
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd.Execute()
}

func TestConfigSet_ProjectAlias(t *testing.T) {
	app, _ := setupConfigTestApp(t)

	tmpDir, _ := filepath.EvalSymlinks(t.TempDir())
	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(tmpDir))
	defer os.Chdir(origDir)

	require.NoError(t, os.MkdirAll(".basecamp", 0755))

	err := executeConfigCommand(app, "set", "project", "12345")
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(tmpDir, ".basecamp", "config.json"))
	require.NoError(t, err)
	var saved map[string]any
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, "12345", saved["project_id"])
}

func TestConfigUnset_ProjectAlias(t *testing.T) {
	app, _ := setupConfigTestApp(t)

	tmpDir, _ := filepath.EvalSymlinks(t.TempDir())
	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(tmpDir))
	defer os.Chdir(origDir)

	// Seed a config with project_id
	require.NoError(t, os.MkdirAll(".basecamp", 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".basecamp", "config.json"),
		[]byte(`{"project_id":"12345"}`), 0644))

	err := executeConfigCommand(app, "unset", "project")
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(tmpDir, ".basecamp", "config.json"))
	require.NoError(t, err)
	var saved map[string]any
	require.NoError(t, json.Unmarshal(data, &saved))
	_, exists := saved["project_id"]
	assert.False(t, exists)
}

func TestConfigProject_ExplicitFlag(t *testing.T) {
	app, buf, _ := setupConfigProjectTestApp(t)

	// Work in a temp dir so PersistProjectID writes .basecamp/config.json there
	tmpDir, _ := filepath.EvalSymlinks(t.TempDir())
	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(tmpDir))
	defer os.Chdir(origDir)

	err := executeConfigProjectCmd(app, "--project", "12345")
	require.NoError(t, err)

	var result map[string]any
	parseEnvelopeData(t, buf, &result)
	assert.Equal(t, "12345", result["project_id"])
	assert.Equal(t, "Project Alpha", result["project_name"])
	assert.Equal(t, "set", result["status"])

	// Verify config file was written
	data, err := os.ReadFile(filepath.Join(tmpDir, ".basecamp", "config.json"))
	require.NoError(t, err)
	var saved map[string]any
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, "12345", saved["project_id"])
}

func TestConfigProject_InAlias(t *testing.T) {
	app, buf, _ := setupConfigProjectTestApp(t)

	tmpDir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmpDir))
	defer os.Chdir(origDir)

	err = executeConfigProjectCmd(app, "--in", "12345")
	require.NoError(t, err)

	var result map[string]any
	parseEnvelopeData(t, buf, &result)
	assert.Equal(t, "12345", result["project_id"])
	assert.Equal(t, "Project Alpha", result["project_name"])
	assert.Equal(t, "set", result["status"])

	// Verify config file was written
	data, err := os.ReadFile(filepath.Join(tmpDir, ".basecamp", "config.json"))
	require.NoError(t, err)
	var saved map[string]any
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, "12345", saved["project_id"])
}

func TestConfigProject_ExplicitFlag_InvalidID(t *testing.T) {
	app, _, _ := setupConfigProjectTestApp(t)

	err := executeConfigProjectCmd(app, "--project", "99999")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestConfigProject_NoFlag_NonInteractive(t *testing.T) {
	app, _, _ := setupConfigProjectTestApp(t)

	// No --project flag, JSON mode (non-interactive) — resolver returns usage error
	err := executeConfigProjectCmd(app)
	assert.Error(t, err)
}
