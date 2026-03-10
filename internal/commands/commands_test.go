package commands_test

import (
	"sort"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"

	"github.com/basecamp/basecamp-cli/internal/cli"
	"github.com/basecamp/basecamp-cli/internal/commands"
)

func TestCatalogMatchesRegisteredCommands(t *testing.T) {
	root := buildRootWithAllCommands()

	// Get registered command names
	registered := make(map[string]bool)
	for _, cmd := range root.Commands() {
		registered[cmd.Name()] = true
	}
	// Get catalog command names
	catalog := make(map[string]bool)
	for _, name := range commands.CatalogCommandNames() {
		catalog[name] = true
	}

	// Find commands in catalog but not registered
	var missingFromRegistered []string
	for name := range catalog {
		if !registered[name] {
			missingFromRegistered = append(missingFromRegistered, name)
		}
	}

	// Find commands registered but not in catalog
	var missingFromCatalog []string
	for name := range registered {
		if !catalog[name] {
			missingFromCatalog = append(missingFromCatalog, name)
		}
	}

	// Sort for deterministic output
	sort.Strings(missingFromRegistered)
	sort.Strings(missingFromCatalog)

	// Report failures
	assert.Empty(t, missingFromRegistered, "Commands in catalog but not registered: %v", missingFromRegistered)
	assert.Empty(t, missingFromCatalog, "Commands registered but not in catalog: %v", missingFromCatalog)
}

// buildRootWithAllCommands creates a root command with all subcommands registered,
// mirroring cli.Execute. Shared by TestCatalog and TestSurfaceSnapshot.
func buildRootWithAllCommands() *cobra.Command {
	root := cli.NewRootCmd()
	root.AddCommand(commands.NewAccountsCmd())
	root.AddCommand(commands.NewAuthCmd())
	root.AddCommand(commands.NewProjectsCmd())
	root.AddCommand(commands.NewTodosCmd())
	root.AddCommand(commands.NewTodoCmd())
	root.AddCommand(commands.NewDoneCmd())
	root.AddCommand(commands.NewReopenCmd())
	root.AddCommand(commands.NewMeCmd())
	root.AddCommand(commands.NewPeopleCmd())
	root.AddCommand(commands.NewQuickStartCmd())
	root.AddCommand(commands.NewAPICmd())
	root.AddCommand(commands.NewShowCmd())
	root.AddCommand(commands.NewTodolistsCmd())
	root.AddCommand(commands.NewCommentsCmd())
	root.AddCommand(commands.NewCommentCmd())
	root.AddCommand(commands.NewAssignCmd())
	root.AddCommand(commands.NewUnassignCmd())
	root.AddCommand(commands.NewMessagesCmd())
	root.AddCommand(commands.NewMessageCmd())
	root.AddCommand(commands.NewCardsCmd())
	root.AddCommand(commands.NewCardCmd())
	root.AddCommand(commands.NewURLCmd())
	root.AddCommand(commands.NewSearchCmd())
	root.AddCommand(commands.NewRecordingsCmd())
	root.AddCommand(commands.NewCampfireCmd())
	root.AddCommand(commands.NewScheduleCmd())
	root.AddCommand(commands.NewFilesCmd())
	root.AddCommand(commands.NewVaultsCmd())
	root.AddCommand(commands.NewDocsCmd())
	root.AddCommand(commands.NewUploadsCmd())
	root.AddCommand(commands.NewCheckinsCmd())
	root.AddCommand(commands.NewWebhooksCmd())
	root.AddCommand(commands.NewEventsCmd())
	root.AddCommand(commands.NewSubscriptionsCmd())
	root.AddCommand(commands.NewForwardsCmd())
	root.AddCommand(commands.NewMessageboardsCmd())
	root.AddCommand(commands.NewMessagetypesCmd())
	root.AddCommand(commands.NewTemplatesCmd())
	root.AddCommand(commands.NewLineupCmd())
	root.AddCommand(commands.NewTimesheetCmd())
	root.AddCommand(commands.NewBoostsCmd())
	root.AddCommand(commands.NewBoostShortcutCmd())
	root.AddCommand(commands.NewTodosetsCmd())
	root.AddCommand(commands.NewToolsCmd())
	root.AddCommand(commands.NewConfigCmd())
	root.AddCommand(commands.NewTodolistgroupsCmd())
	root.AddCommand(commands.NewMCPCmd())
	root.AddCommand(commands.NewCommandsCmd())
	root.AddCommand(commands.NewVersionCmd())
	root.AddCommand(commands.NewTimelineCmd())
	root.AddCommand(commands.NewReportsCmd())
	root.AddCommand(commands.NewCompletionCmd())
	root.AddCommand(commands.NewSetupCmd())
	root.AddCommand(commands.NewLoginCmd())
	root.AddCommand(commands.NewLogoutCmd())
	root.AddCommand(commands.NewDoctorCmd())
	root.AddCommand(commands.NewUpgradeCmd())
	root.AddCommand(commands.NewMigrateCmd())
	root.AddCommand(commands.NewAttachCmd())
	root.AddCommand(commands.NewUploadCmd())
	root.AddCommand(commands.NewSkillCmd())
	root.AddCommand(commands.NewTUICmd())
	root.AddCommand(commands.NewProfileCmd())
	root.AddCommand(commands.NewBonfireCmd())
	root.InitDefaultHelpCmd()
	return root
}
