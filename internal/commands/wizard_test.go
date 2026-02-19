package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// TestIsFirstRunUnauthenticated verifies isFirstRun returns true for unauthenticated,
// non-TTY apps (isFirstRun also checks IsInteractive, which requires a real TTY).
// Since tests don't run in a TTY, isFirstRun returns false even when unauthenticated.
func TestIsFirstRunUnauthenticated(t *testing.T) {
	app, _ := setupQuickstartTestApp(t, "", "")

	// Not a TTY in test environment, so isFirstRun returns false
	assert.False(t, isFirstRun(app), "isFirstRun should be false in non-TTY test")
}

// TestIsFirstRunWithBasecampToken verifies isFirstRun returns false when BASECAMP_TOKEN is set.
func TestIsFirstRunWithBasecampToken(t *testing.T) {
	app, _ := setupQuickstartTestApp(t, "", "")
	t.Setenv("BASECAMP_TOKEN", "test-token-123")

	assert.False(t, isFirstRun(app), "isFirstRun should be false when BASECAMP_TOKEN is set")
}

// TestIsFirstRunAuthenticated verifies isFirstRun returns false when already authenticated.
func TestIsFirstRunAuthenticated(t *testing.T) {
	// BASECAMP_TOKEN makes IsAuthenticated() return true
	t.Setenv("BASECAMP_TOKEN", "test-token-123")
	app, _ := setupQuickstartTestApp(t, "12345", "")

	assert.False(t, isFirstRun(app), "isFirstRun should be false when authenticated")
}

// TestWizardResultJSON verifies the WizardResult struct serializes correctly.
func TestWizardResultJSON(t *testing.T) {
	app, buf := setupQuickstartTestApp(t, "", "")

	result := WizardResult{
		Version:     "1.0.0",
		Status:      "complete",
		AccountID:   "12345",
		AccountName: "Test Company",
		ProjectID:   "67890",
		ProjectName: "My Project",
		ConfigScope: "global",
	}

	err := app.OK(result, output.WithSummary("Setup complete"))
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"account_id": "12345"`)
	assert.Contains(t, out, `"project_id": "67890"`)
	assert.Contains(t, out, `"config_scope": "global"`)
}

// TestWizardSummaryLine verifies summary generation.
func TestWizardSummaryLine(t *testing.T) {
	tests := []struct {
		name     string
		result   WizardResult
		expected string
	}{
		{
			name:     "with account name",
			result:   WizardResult{AccountName: "Test Co"},
			expected: "Setup complete - Test Co",
		},
		{
			name:     "without account name",
			result:   WizardResult{AccountID: "123"},
			expected: "Setup complete",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, wizardSummaryLine(tt.result))
		})
	}
}

// TestWizardBreadcrumbs verifies breadcrumb generation based on wizard outcome.
func TestWizardBreadcrumbs(t *testing.T) {
	t.Run("with project", func(t *testing.T) {
		result := WizardResult{ProjectID: "123"}
		crumbs := wizardBreadcrumbs(result)

		assert.True(t, len(crumbs) >= 2)
		assert.Equal(t, "list_projects", crumbs[0].Action)

		// Should have todos breadcrumb when project is set
		var hasTodos bool
		for _, c := range crumbs {
			if c.Action == "list_todos" {
				hasTodos = true
			}
		}
		assert.True(t, hasTodos, "expected list_todos breadcrumb when project is set")
	})

	t.Run("without project", func(t *testing.T) {
		result := WizardResult{}
		crumbs := wizardBreadcrumbs(result)

		// Should suggest setting a project
		var hasSetProject bool
		for _, c := range crumbs {
			if c.Action == "set_project" {
				hasSetProject = true
			}
		}
		assert.True(t, hasSetProject, "expected set_project breadcrumb when no project")
	})
}

// TestNewSetupCmd verifies the setup command is created correctly.
func TestNewSetupCmd(t *testing.T) {
	cmd := NewSetupCmd()
	assert.Equal(t, "setup", cmd.Use)
	assert.Contains(t, cmd.Short, "setup")
}
