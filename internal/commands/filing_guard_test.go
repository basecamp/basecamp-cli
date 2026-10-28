package commands_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/commands"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/names"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// filingReads is, for every command that moves or copies recordings, the
// container flags it inherits and reads. Each other container flag it
// inherits must be refused before anything is sent.
var filingReads = map[string][]string{
	"basecamp cards column move":       {"card-table"},
	"basecamp cards copy":              {},
	"basecamp cards move":              {"card-table"},
	"basecamp docs move":               {},
	"basecamp files move":              {},
	"basecamp todolistgroups copy":     {},
	"basecamp todolistgroups position": {},
	"basecamp todolists copy":          {},
	"basecamp todolists position":      {},
	"basecamp todos copy":              {},
	"basecamp todos position":          {},
	"basecamp tools reposition":        {},
	"basecamp vaults move":             {},
}

// notFilings are move and copy commands that file nothing into a container.
var notFilings = map[string]string{
	"basecamp cards step move": "reorders a step within its card",
	"basecamp subtasks move":   "reorders a subtask within its parent",
	"basecamp templates copy":  "makes a template from a project",
}

func TestEveryMoveAndCopyCommandDeclaresTheContainerFlagsItReads(t *testing.T) {
	declared := map[string][]string{}
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
		path := cmd.CommandPath()
		reads, guarded := cmd.Annotations[commands.AnnotationFilingReads]
		if guarded {
			declared[path] = strings.Fields(reads)
		}
		names := append([]string{cmd.Name()}, cmd.Aliases...)
		if (slices.Contains(names, "move") || slices.Contains(names, "copy")) && !guarded {
			assert.Contains(t, notFilings, path, "%s moves or copies but does not guard the container flags it ignores", path)
		}
	}
	walk(buildRootWithAllCommands())

	assert.Equal(t, filingReads, declared)
}

func TestFilingCommandsRefuseContainerFlagsTheyDoNotRead(t *testing.T) {
	rows := 0
	for path, reads := range filingReads {
		for _, flag := range inheritedContainerFlags(t, path) {
			args := append(strings.Fields(path)[1:], "1", "--"+flag, "2")
			if slices.Contains(reads, flag) {
				t.Run(path+" reads --"+flag, func(t *testing.T) {
					_, err := runAtRoot(t, args)
					if err != nil {
						assert.NotContains(t, err.Error(), "does not read")
					}
				})
				continue
			}
			rows++
			t.Run(path+" refuses --"+flag, func(t *testing.T) {
				requests, err := runAtRoot(t, args)
				require.Error(t, err)
				e := output.AsError(err)
				assert.Equal(t, output.CodeUsage, e.Code)
				assert.Contains(t, e.Message, "--"+flag)
				assert.Empty(t, requests, "nothing is sent")
			})
		}
	}
	assert.Positive(t, rows)
}

func TestTodosCopyRefusesTheTodolistFlagItWouldIgnore(t *testing.T) {
	for name, args := range map[string][]string{
		"after the command":  {"todos", "copy", "456", "--todolist", "901"},
		"before the command": {"--todolist", "901", "todos", "copy", "456"},
	} {
		t.Run(name, func(t *testing.T) {
			requests, err := runAtRoot(t, args)
			require.Error(t, err)
			e := output.AsError(err)
			assert.Equal(t, output.CodeUsage, e.Code)
			assert.Equal(t, "basecamp todos copy does not read --todolist, so it would ignore it", e.Message)
			assert.Contains(t, e.Hint, "--to-list")
			assert.Empty(t, requests, "nothing is read or copied")
		})
	}
}

// inheritedContainerFlags lists the container flags the command at path
// inherits: those its command groups add, and the root's --todolist. --project
// and --in name the recording's own project, which the commands check.
func inheritedContainerFlags(t *testing.T, path string) []string {
	t.Helper()
	cmd, _, err := buildRootWithAllCommands().Find(strings.Fields(path)[1:])
	require.NoError(t, err)
	flags := []string{"todolist"}
	for group := cmd.Parent(); group.HasParent(); group = group.Parent() {
		group.PersistentFlags().VisitAll(func(f *pflag.Flag) {
			if f.Name != "project" && f.Name != "in" && !slices.Contains(flags, f.Name) {
				flags = append(flags, f.Name)
			}
		})
	}
	sort.Strings(flags)
	return flags
}

// runAtRoot runs args through the whole command tree against a
// transport that records every request and answers each with a 404.
func runAtRoot(t *testing.T, args []string) ([]string, error) {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	transport := &recordingTransport{}
	cfg := &config.Config{AccountID: "99999"}
	sdk := basecamp.NewClient(&basecamp.Config{BaseURL: "https://3.basecampapi.com"}, staticToken{},
		basecamp.WithTransport(transport), basecamp.WithMaxRetries(1))
	authMgr := auth.NewManager(cfg, nil)
	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdk,
		Names:  names.NewResolver(sdk, authMgr, cfg.AccountID),
		Output: output.New(output.Options{Format: output.FormatJSON, Writer: &bytes.Buffer{}}),
	}

	root := buildRootWithAllCommands()
	root.PersistentPreRunE = nil
	root.PersistentPostRunE = nil
	root.SetContext(appctx.WithApp(context.Background(), app))
	root.SetArgs(args)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	err := root.Execute()
	return transport.sent(), err
}

type staticToken struct{}

func (staticToken) AccessToken(context.Context) (string, error) { return "test-token", nil }

type recordingTransport struct {
	mu       sync.Mutex
	requests []string
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req.Method+" "+req.URL.Path)
	header := http.Header{"Content-Type": []string{"application/json"}}
	return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{}`)), Header: header}, nil
}

func (r *recordingTransport) sent() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.requests)
}
