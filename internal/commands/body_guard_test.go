package commands

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

type bodyGuardTransport struct {
	live         string
	writes       int
	gets         int
	getStatus    int
	detectedType string
	emptyRecord  bool
	paths        []string
}

func (tr *bodyGuardTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet {
		tr.writes++
	} else {
		tr.gets++
	}
	status := http.StatusOK
	if r.Method == http.MethodGet {
		tr.paths = append(tr.paths, r.URL.Path)
		if tr.getStatus != 0 {
			status = tr.getStatus
		} else if tr.detectedType != "" {
			if strings.Contains(r.URL.Path, "/documents/") || (tr.detectedType == "upload" && strings.Contains(r.URL.Path, "/vaults/")) {
				status = http.StatusNotFound
			}
		}
	}
	body := fmt.Sprintf(`{"id":789,"name":"Synthetic","title":"Synthetic","subject":"Synthetic","content":"preserved human body","description":"preserved human body","updated_at":%s,"completion_subscribers":[],"assignees":[]}`, tr.live)
	if r.Method == http.MethodGet && tr.emptyRecord {
		body = "null"
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func bodyGuardCommands() []struct {
	name   string
	newCmd func() *cobra.Command
	args   []string
} {
	project, room, contentType := "123", "456", "text/plain"
	return []struct {
		name   string
		newCmd func() *cobra.Command
		args   []string
	}{
		{"comments", newCommentsUpdateCmd, []string{"789", "replacement ![image](./does-not-exist.png)"}},
		{"messages", newMessagesUpdateCmd, []string{"789", "--body", "replacement ![image](./does-not-exist.png)"}},
		{"todos", newTodosUpdateCmd, []string{"789", "--description", "replacement ![image](./does-not-exist.png)"}},
		{"cards", newCardsUpdateCmd, []string{"789", "--body", "replacement ![image](./does-not-exist.png)"}},
		{"todolists", func() *cobra.Command { return newTodolistsUpdateCmd(&project) }, []string{"789", "--description", "replacement"}},
		{"schedule", func() *cobra.Command { return newScheduleUpdateCmd(&project) }, []string{"789", "--description", "replacement ![image](./does-not-exist.png)"}},
		{"answers", func() *cobra.Command { return newCheckinsAnswerUpdateCmd(&project) }, []string{"789", "replacement ![image](./does-not-exist.png)"}},
		{"chat", func() *cobra.Command { return newChatLineUpdateCmd(&project, &room, &contentType) }, []string{"789", "replacement"}},
		{"files", func() *cobra.Command { return newFilesUpdateCmd(&project) }, []string{"789", "--type", "document", "--content", "replacement ![image](./does-not-exist.png)"}},
		{"projects", newProjectsUpdateCmd, []string{"789", "--description", "replacement"}},
		{"templates", newTemplatesUpdateCmd, []string{"789", "--description", "replacement"}},
		{"gauges", newGaugesUpdateCmd, []string{"789", "--description", "replacement"}},
		{"timesheet", newTimesheetEntryUpdateCmd, []string{"789", "--description", "replacement"}},
		{"notes", newNotesSetCmd, []string{"replacement"}},
		{"files autodetect", func() *cobra.Command { return newFilesUpdateCmd(&project) }, []string{"789", "--content", "replacement"}},
		{"files upload", func() *cobra.Command { return newFilesUpdateCmd(&project) }, []string{"789", "--type", "upload", "--content", "replacement"}},
		{"files vault nonbody", func() *cobra.Command { return newFilesUpdateCmd(&project) }, []string{"789", "--type", "vault", "--title", "replacement"}},
		{"doors", func() *cobra.Command { return newDoorsUpdateCmd(&project) }, []string{"789", "--description", "replacement"}},
		{"doors clear", func() *cobra.Command { return newDoorsUpdateCmd(&project) }, []string{"789", "--description", ""}},
		{"files replace", func() *cobra.Command { return newFilesReplaceCmd(&project) }, []string{"789", "../../go.mod", "--description", "replacement ![image](./does-not-exist.png)"}},
		{"files replace clear", func() *cobra.Command { return newFilesReplaceCmd(&project) }, []string{"789", "../../go.mod", "--description", ""}},
		{"files replace attachment only", func() *cobra.Command { return newFilesReplaceCmd(&project) }, []string{"789", "../../go.mod"}},
		{"cards attachment only", newCardsUpdateCmd, []string{"789", "--attach", "../../go.mod"}},
		{"schedule attachment only", func() *cobra.Command { return newScheduleUpdateCmd(&project) }, []string{"789", "--attach", "../../go.mod"}},
		{"todos clear", newTodosUpdateCmd, []string{"789", "--description", ""}},
		{"files document clear", func() *cobra.Command { return newFilesUpdateCmd(&project) }, []string{"789", "--type", "document", "--content", ""}},
		{"todo nonbody full record", newTodosUpdateCmd, []string{"789", "--title", "replacement"}},
	}
}

func TestBodyGuardRefusesBeforeUploadsAndMutations(t *testing.T) {
	for _, tc := range bodyGuardCommands() {
		t.Run(tc.name, func(t *testing.T) {
			tr := &bodyGuardTransport{live: `"2026-11-08T18:10:58Z"`}
			app, _ := newTestAppWithTransport(t, tr)
			err := executeCommand(tc.newCmd(), app, append(tc.args, "--if-unmodified-since", "2026-11-08T18:00:00Z")...)
			require.Error(t, err)
			require.Contains(t, err.Error(), "conflict")
			require.Zero(t, tr.writes)
			require.Positive(t, tr.gets)
		})
	}
}

func TestBodyGuardTimestampFailures(t *testing.T) {
	for _, live := range []string{`null`, `"0001-01-01T00:00:00Z"`, `"invalid"`} {
		t.Run(live, func(t *testing.T) {
			tr := &bodyGuardTransport{live: live}
			app, _ := newTestAppWithTransport(t, tr)
			err := executeCommand(newCommentsUpdateCmd(), app, "789", "replacement", "--if-unmodified-since", "2026-11-08T18:00:00Z")
			require.Error(t, err)
			require.Zero(t, tr.writes)
		})
	}
	for _, tc := range []struct {
		name  string
		value string
		flags []string
	}{
		{"missing even with force", `null`, []string{"--force"}},
		{"future is not equality", `"2026-11-08T18:00:00Z"`, nil},
		{"different nanoseconds", `"2026-11-08T18:10:58.000000001Z"`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &bodyGuardTransport{live: tc.value}
			app, _ := newTestAppWithTransport(t, tr)
			args := append([]string{"789", "replacement", "--if-unmodified-since", "2026-11-08T18:10:58Z"}, tc.flags...)
			err := executeCommand(newCommentsUpdateCmd(), app, args...)
			require.Error(t, err)
			require.Zero(t, tr.writes)
		})
	}
	for _, tc := range bodyGuardCommands() {
		t.Run(tc.name, func(t *testing.T) {
			tr := &bodyGuardTransport{live: `"2026-11-08T18:00:00Z"`}
			app, _ := newTestAppWithTransport(t, tr)
			err := executeCommand(tc.newCmd(), app, append(tc.args, "--if-unmodified-since", "invalid", "--force")...)
			require.Error(t, err)
			require.Contains(t, err.Error(), "RFC3339")
			require.Zero(t, tr.writes)
			require.Zero(t, tr.gets)
		})
	}
}

func TestBodyGuardUnavailableLiveState(t *testing.T) {
	for _, tc := range bodyGuardCommands() {
		for _, state := range []struct {
			name, live string
			status     int
		}{
			{"missing", `null`, 0},
			{"zero", `"0001-01-01T00:00:00Z"`, 0},
			{"malformed", `"invalid"`, 0},
			{"failed GET", `"2026-11-08T18:00:00Z"`, http.StatusForbidden},
		} {
			for _, force := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/force=%t", tc.name, state.name, force), func(t *testing.T) {
					tr := &bodyGuardTransport{live: state.live, getStatus: state.status}
					app, _ := newTestAppWithTransport(t, tr)
					args := append(append([]string{}, tc.args...), "--if-unmodified-since", "2026-11-08T18:00:00Z")
					if force {
						args = append(args, "--force")
					}
					err := executeCommand(tc.newCmd(), app, args...)
					require.Error(t, err)
					require.Zero(t, tr.writes)
					require.Positive(t, tr.gets)
				})
			}
		}
	}
}

func TestBodyGuardFilesAutodetection(t *testing.T) {
	project := "123"
	for _, kind := range []string{"vault", "upload"} {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/force=%t", kind, force), func(t *testing.T) {
				tr := &bodyGuardTransport{live: `"2026-11-08T18:10:58Z"`, detectedType: kind}
				app, _ := newTestAppWithTransport(t, tr)
				args := []string{"789", "--title", "replacement", "--if-unmodified-since", "2026-11-08T18:00:00Z"}
				if force {
					args = append(args, "--force")
				}
				err := executeCommand(newFilesUpdateCmd(&project), app, args...)
				require.Contains(t, strings.Join(tr.paths, " "), "/"+kind+"s/789")
				if force {
					require.NoError(t, err)
					require.Equal(t, 1, tr.writes)
				} else {
					require.ErrorContains(t, err, "conflict")
					require.Zero(t, tr.writes)
				}
			})
		}
	}
}

func TestBodyGuardEmptyLiveRecordRefuses(t *testing.T) {
	for _, tc := range bodyGuardCommands() {
		for _, status := range []int{http.StatusOK, http.StatusNoContent} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, status), func(t *testing.T) {
				tr := &bodyGuardTransport{live: `null`, emptyRecord: true, getStatus: status}
				app, _ := newTestAppWithTransport(t, tr)
				args := append(append([]string{}, tc.args...), "--if-unmodified-since", "2026-11-08T18:00:00Z", "--force")
				require.NotPanics(t, func() {
					require.Error(t, executeCommand(tc.newCmd(), app, args...))
				})
				require.Zero(t, tr.writes)
			})
		}
	}
}

func TestBodyGuardBoostExplicitlyExcluded(t *testing.T) {
	project := "123"
	cmd := newBoostUpdateCmd(&project)
	require.Nil(t, cmd.Flags().Lookup("if-unmodified-since"))
	require.Nil(t, cmd.Flags().Lookup("force"))
	tr := &bodyGuardTransport{live: `null`}
	app, _ := newTestAppWithTransport(t, tr)
	err := executeCommand(cmd, app, "789", "replacement")
	require.NoError(t, err)
	require.Equal(t, 1, tr.writes)
	require.Zero(t, tr.gets)
}

func TestBodyGuardMatchingOverrideAndUnprotected(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		gets  int
	}{
		{"same instant", []string{"--if-unmodified-since", "2026-11-08T13:10:58-05:00"}, 1},
		{"override", []string{"--if-unmodified-since", "2026-11-08T18:00:00Z", "--force"}, 1},
		{"first use no baseline", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &bodyGuardTransport{live: `"2026-11-08T18:10:58Z"`}
			app, _ := newTestAppWithTransport(t, tr)
			err := executeCommand(newCommentsUpdateCmd(), app, append([]string{"789", "replacement"}, tc.flags...)...)
			require.NoError(t, err)
			require.Equal(t, 1, tr.writes)
			require.Equal(t, tc.gets, tr.gets)
		})
	}
}
