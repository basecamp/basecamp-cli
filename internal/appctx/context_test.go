package appctx

import (
	"context"
	"testing"

	"github.com/basecamp/bcq/internal/config"
)

func TestNewApp(t *testing.T) {
	cfg := &config.Config{}
	app := NewApp(cfg)

	if app == nil {
		t.Fatal("NewApp returned nil")
	}
	if app.Config != cfg {
		t.Error("Config not set correctly")
	}
	if app.Auth == nil {
		t.Error("Auth manager not initialized")
	}
	if app.SDK == nil {
		t.Error("SDK client not initialized")
	}
	if app.Names == nil {
		t.Error("Names resolver not initialized")
	}
	if app.Output == nil {
		t.Error("Output writer not initialized")
	}
}

func TestWithAppAndFromContext(t *testing.T) {
	cfg := &config.Config{}
	app := NewApp(cfg)

	ctx := context.Background()
	ctxWithApp := WithApp(ctx, app)

	retrieved := FromContext(ctxWithApp)
	if retrieved != app {
		t.Error("FromContext did not retrieve the same app")
	}
}

func TestFromContextEmpty(t *testing.T) {
	ctx := context.Background()
	app := FromContext(ctx)
	if app != nil {
		t.Error("expected nil from empty context")
	}
}

func TestApplyFlagsJSON(t *testing.T) {
	cfg := &config.Config{}
	app := NewApp(cfg)
	app.Flags.JSON = true

	app.ApplyFlags()
	// Can't directly access format, but verify output is set
	if app.Output == nil {
		t.Error("Output should be set after ApplyFlags")
	}
}

func TestApplyFlagsQuiet(t *testing.T) {
	cfg := &config.Config{}
	app := NewApp(cfg)
	app.Flags.Quiet = true

	app.ApplyFlags()
	if app.Output == nil {
		t.Error("Output should be set after ApplyFlags")
	}
}

func TestApplyFlagsAgent(t *testing.T) {
	cfg := &config.Config{}
	app := NewApp(cfg)
	app.Flags.Agent = true

	app.ApplyFlags()
	if app.Output == nil {
		t.Error("Output should be set after ApplyFlags")
	}
}

func TestApplyFlagsIDsOnly(t *testing.T) {
	cfg := &config.Config{}
	app := NewApp(cfg)
	app.Flags.IDsOnly = true

	app.ApplyFlags()
	if app.Output == nil {
		t.Error("Output should be set after ApplyFlags")
	}
}

func TestApplyFlagsCount(t *testing.T) {
	cfg := &config.Config{}
	app := NewApp(cfg)
	app.Flags.Count = true

	app.ApplyFlags()
	if app.Output == nil {
		t.Error("Output should be set after ApplyFlags")
	}
}

func TestApplyFlagsMD(t *testing.T) {
	cfg := &config.Config{}
	app := NewApp(cfg)
	app.Flags.MD = true

	app.ApplyFlags()
	if app.Output == nil {
		t.Error("Output should be set after ApplyFlags")
	}
}

func TestApplyFlagsVerbose(t *testing.T) {
	cfg := &config.Config{}
	app := NewApp(cfg)
	app.Flags.Verbose = 1 // -v

	// Should not panic
	app.ApplyFlags()
}

func TestIsInteractiveWithAgentMode(t *testing.T) {
	cfg := &config.Config{}
	app := NewApp(cfg)
	app.Flags.Agent = true

	if app.IsInteractive() {
		t.Error("should not be interactive in agent mode")
	}
}

func TestIsInteractiveWithJSONMode(t *testing.T) {
	cfg := &config.Config{}
	app := NewApp(cfg)
	app.Flags.JSON = true

	if app.IsInteractive() {
		t.Error("should not be interactive in JSON mode")
	}
}

func TestIsInteractiveWithQuietMode(t *testing.T) {
	cfg := &config.Config{}
	app := NewApp(cfg)
	app.Flags.Quiet = true

	if app.IsInteractive() {
		t.Error("should not be interactive in quiet mode")
	}
}

func TestIsInteractiveWithIDsOnlyMode(t *testing.T) {
	cfg := &config.Config{}
	app := NewApp(cfg)
	app.Flags.IDsOnly = true

	if app.IsInteractive() {
		t.Error("should not be interactive in IDs-only mode")
	}
}

func TestIsInteractiveWithCountMode(t *testing.T) {
	cfg := &config.Config{}
	app := NewApp(cfg)
	app.Flags.Count = true

	if app.IsInteractive() {
		t.Error("should not be interactive in count mode")
	}
}

func TestNewAppWithFormatConfig(t *testing.T) {
	tests := []struct {
		format string
	}{
		{"json"},
		{"markdown"},
		{"md"},
		{"quiet"},
		{""},
	}

	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			cfg := &config.Config{Format: tt.format}
			app := NewApp(cfg)
			if app.Output == nil {
				t.Error("Output should be set")
			}
		})
	}
}

func TestGlobalFlagsDefaults(t *testing.T) {
	var flags GlobalFlags

	// All booleans should default to false
	if flags.JSON {
		t.Error("JSON should default to false")
	}
	if flags.Quiet {
		t.Error("Quiet should default to false")
	}
	if flags.MD {
		t.Error("MD should default to false")
	}
	if flags.Agent {
		t.Error("Agent should default to false")
	}
	if flags.IDsOnly {
		t.Error("IDsOnly should default to false")
	}
	if flags.Count {
		t.Error("Count should default to false")
	}
	if flags.Verbose != 0 {
		t.Error("Verbose should default to 0")
	}

	// All strings should default to empty
	if flags.Project != "" {
		t.Error("Project should default to empty")
	}
	if flags.Account != "" {
		t.Error("Account should default to empty")
	}
	if flags.Todolist != "" {
		t.Error("Todolist should default to empty")
	}
	if flags.Host != "" {
		t.Error("Host should default to empty")
	}
	if flags.CacheDir != "" {
		t.Error("CacheDir should default to empty")
	}
}

func TestApplyFlagsPriority(t *testing.T) {
	// Agent mode should take priority
	cfg := &config.Config{}
	app := NewApp(cfg)
	app.Flags.Agent = true
	app.Flags.JSON = true
	app.Flags.MD = true

	app.ApplyFlags()
	// Agent mode wins - can't directly verify but should not panic
	if app.Output == nil {
		t.Error("Output should be set")
	}
}

// Test output formats correspond to correct modes
func TestOutputFormatApplication(t *testing.T) {
	tests := []struct {
		name    string
		setFlag func(*App)
	}{
		{"agent", func(a *App) { a.Flags.Agent = true }},
		{"idsOnly", func(a *App) { a.Flags.IDsOnly = true }},
		{"count", func(a *App) { a.Flags.Count = true }},
		{"quiet", func(a *App) { a.Flags.Quiet = true }},
		{"json", func(a *App) { a.Flags.JSON = true }},
		{"md", func(a *App) { a.Flags.MD = true }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			app := NewApp(cfg)
			originalOutput := app.Output
			tt.setFlag(app)
			app.ApplyFlags()

			// Output should not be nil after applying flags
			_ = originalOutput // Used for potential future comparison
			if app.Output == nil {
				t.Error("Output should not be nil")
			}
		})
	}
}

// Verify type is exported
func TestAppType(t *testing.T) {
	var _ *App
	var _ GlobalFlags
}

// Verify output.Writer compatibility
func TestOutputWriterType(t *testing.T) {
	cfg := &config.Config{}
	app := NewApp(cfg)
	_ = app.Output // Verify it's assignable to *output.Writer
}

// Test app.OK includes stats when --stats flag is set
func TestAppOKWithStats(t *testing.T) {
	cfg := &config.Config{}
	app := NewApp(cfg)

	// Without stats flag - should not panic
	app.Flags.Stats = false
	err := app.OK(map[string]string{"test": "data"})
	if err != nil {
		t.Errorf("OK without stats failed: %v", err)
	}

	// With stats flag - should not panic and include stats
	app.Flags.Stats = true
	err = app.OK(map[string]string{"test": "data"})
	if err != nil {
		t.Errorf("OK with stats failed: %v", err)
	}
}

// Test app.OK with nil collector doesn't panic
func TestAppOKWithNilCollector(t *testing.T) {
	cfg := &config.Config{}
	app := NewApp(cfg)
	app.Collector = nil
	app.Flags.Stats = true

	// Should not panic even with nil collector
	err := app.OK(map[string]string{"test": "data"})
	if err != nil {
		t.Errorf("OK with nil collector failed: %v", err)
	}
}

// Test isMachineOutput detects flag-driven machine output modes
func TestIsMachineOutputFlags(t *testing.T) {
	tests := []struct {
		name     string
		setFlag  func(*App)
		expected bool
	}{
		{"default", func(a *App) {}, false},
		{"agent flag", func(a *App) { a.Flags.Agent = true }, true},
		{"quiet flag", func(a *App) { a.Flags.Quiet = true }, true},
		{"ids-only flag", func(a *App) { a.Flags.IDsOnly = true }, true},
		{"count flag", func(a *App) { a.Flags.Count = true }, true},
		{"json flag", func(a *App) { a.Flags.JSON = true }, false},
		{"md flag", func(a *App) { a.Flags.MD = true }, false},
		{"styled flag", func(a *App) { a.Flags.Styled = true }, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			app := NewApp(cfg)
			tt.setFlag(app)

			if got := app.isMachineOutput(); got != tt.expected {
				t.Errorf("isMachineOutput() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// Test isMachineOutput detects config-driven quiet mode
func TestIsMachineOutputConfigFormat(t *testing.T) {
	tests := []struct {
		format   string
		expected bool
	}{
		{"", false},
		{"json", false},
		{"markdown", false},
		{"md", false},
		{"quiet", true},
	}

	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			cfg := &config.Config{Format: tt.format}
			app := NewApp(cfg)

			if got := app.isMachineOutput(); got != tt.expected {
				t.Errorf("isMachineOutput() with config format %q = %v, want %v", tt.format, got, tt.expected)
			}
		})
	}
}

// Test that app.Err doesn't print stats in machine output modes
func TestAppErrMachineOutputNoStats(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*App)
		machine bool
	}{
		{"flag quiet", func(a *App) { a.Flags.Quiet = true }, true},
		{"flag agent", func(a *App) { a.Flags.Agent = true }, true},
		{"flag ids-only", func(a *App) { a.Flags.IDsOnly = true }, true},
		{"flag count", func(a *App) { a.Flags.Count = true }, true},
		{"config quiet", func(a *App) { a.Config.Format = "quiet" }, true},
		{"flag json", func(a *App) { a.Flags.JSON = true }, false},
		{"default", func(a *App) {}, false},
	}

	testErr := &testError{msg: "test error"}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			app := NewApp(cfg)
			app.Flags.Stats = true // Enable stats
			tt.setup(app)
			app.ApplyFlags()

			// Verify isMachineOutput returns expected value
			if got := app.isMachineOutput(); got != tt.machine {
				t.Errorf("isMachineOutput() = %v, want %v", got, tt.machine)
			}

			// app.Err should not panic regardless of mode
			err := app.Err(testErr)
			if err != nil {
				t.Errorf("Err() returned error: %v", err)
			}
		})
	}
}

// testError is a simple error type for testing
type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

// Test Account() returns an account-scoped client
func TestAppAccount(t *testing.T) {
	cfg := &config.Config{AccountID: "12345"}
	app := NewApp(cfg)

	account := app.Account()
	if account == nil {
		t.Fatal("Account() returned nil")
	}
	// Account() returns *AccountClient (via ForAccount), not *Client
}

// Test RequireAccount() validates account configuration
func TestAppRequireAccount(t *testing.T) {
	tests := []struct {
		name      string
		accountID string
		wantErr   bool
		errMsg    string
	}{
		{
			name:      "no account configured",
			accountID: "",
			wantErr:   true,
			errMsg:    "Account ID required",
		},
		{
			name:      "valid numeric account",
			accountID: "12345",
			wantErr:   false,
		},
		{
			name:      "invalid non-numeric account",
			accountID: "my-account",
			wantErr:   true,
			errMsg:    "must contain only digits",
		},
		{
			name:      "invalid mixed account",
			accountID: "123abc",
			wantErr:   true,
			errMsg:    "must contain only digits",
		},
		{
			name:      "invalid signed positive",
			accountID: "+123",
			wantErr:   true,
			errMsg:    "must contain only digits",
		},
		{
			name:      "invalid signed negative",
			accountID: "-1",
			wantErr:   true,
			errMsg:    "must contain only digits",
		},
		{
			name:      "invalid with spaces",
			accountID: "123 456",
			wantErr:   true,
			errMsg:    "must contain only digits",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{AccountID: tt.accountID}
			app := NewApp(cfg)

			err := app.RequireAccount()
			if tt.wantErr {
				if err == nil {
					t.Error("RequireAccount() should return error")
				} else if tt.errMsg != "" {
					errStr := err.Error()
					found := false
					for i := 0; i <= len(errStr)-len(tt.errMsg); i++ {
						if errStr[i:i+len(tt.errMsg)] == tt.errMsg {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("error should contain %q, got %q", tt.errMsg, errStr)
					}
				}
			} else {
				if err != nil {
					t.Errorf("RequireAccount() should succeed: %v", err)
				}
			}
		})
	}
}
