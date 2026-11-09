package commands

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// The timestamp belongs to the record the caller used to prepare the replacement,
// not a fresh read performed merely to make an old body pass.
func addBodyGuardFlags(cmd *cobra.Command) {
	cmd.Flags().String("if-unmodified-since", "", "Require the record's last-seen updated_at (RFC3339); no baseline means no conflict protection")
	cmd.Flags().Bool("force", false, "Override a version conflict with --if-unmodified-since (not invalid or missing timestamps)")
	cmd.Long += "\n\nOptional conflict guard: pass the updated_at from the record used to prepare your edit\nwith --if-unmodified-since. This checks the whole record, including non-body edits\nthrough this command. A different live timestamp refuses before uploads or writes;\n--force explicitly overrides a conflict. Invalid or missing timestamps still refuse.\nWithout a baseline, including first use, writes remain unprotected. The live GET is\nserver-revalidated, but GET then PUT is not atomic: edits in between can still be lost."
	run := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("if-unmodified-since") {
			value, _ := cmd.Flags().GetString("if-unmodified-since")
			baseline, err := time.Parse(time.RFC3339Nano, value)
			if err != nil || baseline.IsZero() {
				return output.ErrUsage("--if-unmodified-since requires a nonzero RFC3339 updated_at timestamp")
			}
		} else if cmd.Flags().Changed("force") {
			return output.ErrUsage("--force requires --if-unmodified-since; without a baseline updates are already unprotected")
		}
		return run(cmd, args)
	}
}

func checkBodyUpdate(cmd *cobra.Command, fetch func() (time.Time, error)) error {
	if !cmd.Flags().Changed("if-unmodified-since") {
		return nil
	}
	value, _ := cmd.Flags().GetString("if-unmodified-since")
	baseline, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || baseline.IsZero() {
		return output.ErrUsage("--if-unmodified-since requires a nonzero RFC3339 updated_at timestamp")
	}
	live, err := fetch()
	if err != nil {
		return convertSDKError(err)
	}
	if live.IsZero() {
		return fmt.Errorf("cannot verify updated_at: live timestamp is missing; refusing update")
	}
	force, _ := cmd.Flags().GetBool("force")
	if !live.Equal(baseline) && !force {
		return fmt.Errorf("update conflict: live updated_at %s differs from your last read %s; re-fetch and reconcile, or pass --force to overwrite (GET then PUT is not atomic)", live.Format(time.RFC3339Nano), baseline.Format(time.RFC3339Nano))
	}
	return nil
}
