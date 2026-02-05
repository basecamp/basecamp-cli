package resolve

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/basecamp/bcq/internal/config"
	"github.com/basecamp/bcq/internal/tui"
)

// PersistOption represents a config persistence option.
type PersistOption struct {
	Key   string // Config key (e.g., "account_id")
	Value string // Value to persist
	Scope string // "local" or "global"
}

// PromptAndPersist asks the user if they want to save a value as default,
// and if so, which scope to save it in. Returns true if the value was saved.
func PromptAndPersist(opt PersistOption) (bool, error) {
	// Ask if user wants to save as default
	shouldSave, err := tui.ConfirmSetDefault(opt.Key)
	if err != nil {
		return false, err
	}
	if !shouldSave {
		return false, nil
	}

	// Ask where to save
	scope := opt.Scope
	if scope == "" {
		scope, err = tui.SelectScope()
		if err != nil {
			return false, err
		}
	}

	// Persist the value
	if err := PersistValue(opt.Key, opt.Value, scope); err != nil {
		return false, err
	}

	return true, nil
}

// PersistValue saves a config value to the specified scope.
func PersistValue(key, value, scope string) error {
	var configPath string

	switch scope {
	case "global":
		configPath = filepath.Join(config.GlobalConfigDir(), "config.json")
	case "local":
		configPath = filepath.Join(".basecamp", "config.json")
	default:
		return fmt.Errorf("invalid scope: %s (must be 'local' or 'global')", scope)
	}

	// Ensure directory exists
	configDir := filepath.Dir(configPath)
	if err := os.MkdirAll(configDir, 0750); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Load existing config or create new
	configData := make(map[string]any)
	if data, err := os.ReadFile(configPath); err == nil { //nolint:gosec // G304: Path is from trusted config location
		_ = json.Unmarshal(data, &configData) // Ignore error - start fresh if invalid
	}

	// Set the value
	configData[key] = value

	// Write back
	data, err := json.MarshalIndent(configData, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, append(data, '\n'), 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// PersistAccountID is a convenience function for persisting an account ID.
func PersistAccountID(accountID, scope string) error {
	return PersistValue("account_id", accountID, scope)
}

// PersistProjectID is a convenience function for persisting a project ID.
func PersistProjectID(projectID, scope string) error {
	return PersistValue("project_id", projectID, scope)
}

// PersistTodolistID is a convenience function for persisting a todolist ID.
func PersistTodolistID(todolistID, scope string) error {
	return PersistValue("todolist_id", todolistID, scope)
}

// PromptAndPersistAccountID prompts the user to save an account ID.
func PromptAndPersistAccountID(accountID string) (bool, error) {
	return PromptAndPersist(PersistOption{
		Key:   "account_id",
		Value: accountID,
	})
}

// PromptAndPersistProjectID prompts the user to save a project ID.
func PromptAndPersistProjectID(projectID string) (bool, error) {
	return PromptAndPersist(PersistOption{
		Key:   "project_id",
		Value: projectID,
	})
}

// PromptAndPersistTodolistID prompts the user to save a todolist ID.
func PromptAndPersistTodolistID(todolistID string) (bool, error) {
	return PromptAndPersist(PersistOption{
		Key:   "todolist_id",
		Value: todolistID,
	})
}

// PersistDefaultProfile is a convenience function for persisting a default profile.
func PersistDefaultProfile(profileName, scope string) error {
	return PersistValue("default_profile", profileName, scope)
}

// PromptAndPersistDefaultProfile prompts the user to save a default profile.
func PromptAndPersistDefaultProfile(profileName string) (bool, error) {
	return PromptAndPersist(PersistOption{
		Key:   "default_profile",
		Value: profileName,
	})
}
