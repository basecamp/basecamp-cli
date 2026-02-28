package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/skills"
)

// NewSkillCmd creates the skill command.
func NewSkillCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "skill",
		Short: "Print the embedded agent skill file",
		Long:  "Print the SKILL.md embedded in this binary. Any agent can bootstrap from this output.",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := skills.FS.ReadFile("basecamp/SKILL.md")
			if err != nil {
				return fmt.Errorf("reading embedded skill: %w", err)
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), string(data))
			return err
		},
	}
}
