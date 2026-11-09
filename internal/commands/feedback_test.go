package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/feedback"
	"github.com/basecamp/basecamp-cli/internal/observability"
	"github.com/basecamp/basecamp-cli/internal/output"
)

const feedbackPath = "/99999/feedback.json"

func discoveryRoute(status int) stubRoute {
	body := `{"surfaces":["cli"],"categories":["bug","missing_capability","confusing","praise","other"],"max_details_length":2000}`
	if status != http.StatusOK {
		body = `{"error":"Not found"}`
	}
	return stubRoute{method: http.MethodGet, path: feedbackPath, status: status, body: body}
}

func submitRoute(status int, body string) stubRoute {
	return stubRoute{method: http.MethodPost, path: feedbackPath, status: status, body: body}
}

// useFeedbackStore points detection state at a temp dir and clears any
// override from the environment running the tests.
func useFeedbackStore(t *testing.T) *feedback.Store {
	t.Helper()
	t.Setenv(feedback.EnvOverride, "")
	store := feedback.NewStore(t.TempDir())
	saved := feedbackStore
	feedbackStore = func() *feedback.Store { return store }
	t.Cleanup(func() { feedbackStore = saved })
	return store
}

var feedbackKey = feedback.Key("", "99999")

func requests(transport *recordingTransport, method string) []recordedCall {
	var out []recordedCall
	for _, call := range transport.recorded() {
		if call.Method == method {
			out = append(out, call)
		}
	}
	return out
}

func assertNotAvailable(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	e := output.AsError(err)
	assert.Equal(t, feedback.NotAvailable, e.Message)
	assert.Empty(t, e.Hint, "not available says nothing more")
	assert.NotEqual(t, 0, output.ExitCodeFor(e.Code), "exits non-zero")
}

func TestFeedbackCommandStartsHidden(t *testing.T) {
	cmd := NewFeedbackCmd()
	assert.True(t, cmd.Hidden)

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.HelpFunc()(cmd, nil)
	assert.Equal(t, feedback.NotAvailable+"\n", out.String(), "help of a hidden command describes nothing")
}

func TestFeedbackNotAdvertisedSaysNotAvailable(t *testing.T) {
	store := useFeedbackStore(t)
	app, transport := setupRecordingTestApp(t, discoveryRoute(http.StatusNotFound))

	err := executeRecordingCommand(NewFeedbackCmd(), app, "--category", "bug", "it broke")
	assertNotAvailable(t, err)
	assert.Empty(t, requests(transport, http.MethodPost), "nothing is submitted")
	assert.False(t, store.Enabled(feedbackKey))
	assert.False(t, store.NeedsRefresh(feedbackKey), "the 404 is cached")
}

// While the feature is off, argument mistakes don't reveal the command.
func TestFeedbackNotAdvertisedBeforeArgumentChecks(t *testing.T) {
	useFeedbackStore(t)
	app, _ := setupRecordingTestApp(t, discoveryRoute(http.StatusNotFound))

	assertNotAvailable(t, executeRecordingCommand(NewFeedbackCmd(), app))
	assertNotAvailable(t, executeRecordingCommand(NewFeedbackCmd(), app, "--category", "nonsense", "x"))
}

func TestFeedbackForcedOffMakesNoRequest(t *testing.T) {
	store := useFeedbackStore(t)
	store.Record(feedbackKey, true)
	t.Setenv(feedback.EnvOverride, "0")
	app, transport := setupRecordingTestApp(t, discoveryRoute(http.StatusOK), submitRoute(http.StatusCreated, `{"id":1,"status":"received"}`))

	assertNotAvailable(t, executeRecordingCommand(NewFeedbackCmd(), app, "--category", "bug", "it broke"))
	assert.Empty(t, transport.recorded())
}

func TestFeedbackSubmitsRequestShape(t *testing.T) {
	store := useFeedbackStore(t)
	app, transport := setupRecordingTestApp(t,
		discoveryRoute(http.StatusOK),
		submitRoute(http.StatusCreated, `{"id":10491086999,"status":"received"}`))
	var out bytes.Buffer
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: &out})

	err := executeRecordingCommand(NewFeedbackCmd(), app,
		"--category", "missing_capability", "--subject", "cards list", "--error-code", "api_error",
		"No way to list on-hold cards")
	require.NoError(t, err)

	posts := requests(transport, http.MethodPost)
	require.Len(t, posts, 1)
	assert.Equal(t, feedbackPath, posts[0].Path)
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(posts[0].Body), &body))
	assert.Equal(t, map[string]any{
		"surface":    "cli",
		"category":   "missing_capability",
		"subject":    "cards list",
		"error_code": "api_error",
		"details":    "No way to list on-hold cards",
	}, body)

	var envelope struct {
		OK      bool            `json:"ok"`
		Data    FeedbackReceipt `json:"data"`
		Summary string          `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope), out.String())
	assert.True(t, envelope.OK)
	assert.Equal(t, int64(10491086999), envelope.Data.ID)
	assert.Equal(t, "received", envelope.Data.Status)
	assert.Contains(t, envelope.Summary, "10491086999")
	assert.True(t, store.Enabled(feedbackKey))
}

func TestFeedbackOmitsOptionalFields(t *testing.T) {
	useFeedbackStore(t)
	app, transport := setupRecordingTestApp(t,
		discoveryRoute(http.StatusOK),
		submitRoute(http.StatusCreated, `{"id":1,"status":"received"}`))

	require.NoError(t, executeRecordingCommand(NewFeedbackCmd(), app, "--category", "praise", "Search", "is", "fast"))

	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(transport.last(t).Body), &body))
	assert.Equal(t, map[string]any{"surface": "cli", "category": "praise", "details": "Search is fast"}, body)
}

func TestFeedbackForcedOnSkipsDetection(t *testing.T) {
	useFeedbackStore(t)
	t.Setenv(feedback.EnvOverride, "1")
	app, transport := setupRecordingTestApp(t, submitRoute(http.StatusCreated, `{"id":7,"status":"received"}`))

	require.NoError(t, executeRecordingCommand(NewFeedbackCmd(), app, "--category", "bug", "x"))
	assert.Empty(t, requests(transport, http.MethodGet))
	assert.Len(t, requests(transport, http.MethodPost), 1)
}

func TestFeedbackValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing category", []string{"x"}, "--category is required"},
		{"unknown category", []string{"--category", "feature", "x"}, `unknown category "feature"`},
		{"no details", []string{"--category", "bug"}, "<details> required"},
		{"blank details", []string{"--category", "bug", "   "}, "<details> is empty"},
		{"too long", []string{"--category", "bug", strings.Repeat("é", feedback.MaxDetails+1)}, "the limit is 2000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useFeedbackStore(t)
			app, transport := setupRecordingTestApp(t, discoveryRoute(http.StatusOK))

			err := executeRecordingCommand(NewFeedbackCmd(), app, tc.args...)
			require.Error(t, err)
			e := output.AsError(err)
			assert.Equal(t, output.CodeUsage, e.Code)
			assert.Contains(t, e.Message, tc.want)
			assert.Empty(t, requests(transport, http.MethodPost))
		})
	}
}

func TestFeedbackAcceptsExactlyTheCap(t *testing.T) {
	useFeedbackStore(t)
	app, transport := setupRecordingTestApp(t,
		discoveryRoute(http.StatusOK),
		submitRoute(http.StatusCreated, `{"id":1,"status":"received"}`))

	require.NoError(t, executeRecordingCommand(NewFeedbackCmd(), app, "--category", "bug", strings.Repeat("é", feedback.MaxDetails)))
	assert.Len(t, requests(transport, http.MethodPost), 1)
}

// A 404 on submit means the feature went off since detection.
func TestFeedbackSubmitNotFound(t *testing.T) {
	store := useFeedbackStore(t)
	app, _ := setupRecordingTestApp(t, discoveryRoute(http.StatusOK), submitRoute(http.StatusNotFound, `{"error":"Not found"}`))

	assertNotAvailable(t, executeRecordingCommand(NewFeedbackCmd(), app, "--category", "bug", "x"))
	assert.False(t, store.Enabled(feedbackKey))
}

func TestFeedbackSubmitValidationError(t *testing.T) {
	useFeedbackStore(t)
	app, _ := setupRecordingTestApp(t, discoveryRoute(http.StatusOK),
		submitRoute(http.StatusUnprocessableEntity, `{"error":"Details are too long","field":"details"}`))

	err := executeRecordingCommand(NewFeedbackCmd(), app, "--category", "bug", "x")
	require.Error(t, err)
	e := output.AsError(err)
	assert.Equal(t, output.CodeValidation, e.Code)
	assert.Contains(t, e.Message, "Details are too long")
}

func TestFeedbackSubmitRateLimited(t *testing.T) {
	useFeedbackStore(t)
	limited := submitRoute(http.StatusTooManyRequests, `{"error":"Rate limited"}`)
	limited.header = http.Header{"Retry-After": []string{"3600"}}
	app, transport := setupRecordingTestApp(t, discoveryRoute(http.StatusOK), limited)

	err := executeRecordingCommand(NewFeedbackCmd(), app, "--category", "bug", "x")
	require.Error(t, err)
	e := output.AsError(err)
	assert.Equal(t, output.CodeRateLimit, e.Code)
	assert.Equal(t, "Feedback limit reached", e.Message)
	assert.Contains(t, e.Hint, "Try again in 3600 seconds")
	assert.True(t, e.Retryable)
	assert.Len(t, requests(transport, http.MethodPost), 1, "a submission is never retried")
}

func TestFeedbackDetectionErrorIsReported(t *testing.T) {
	useFeedbackStore(t)
	app, _ := setupRecordingTestApp(t, stubRoute{method: http.MethodGet, path: feedbackPath, status: http.StatusUnauthorized, body: `{}`})

	err := executeRecordingCommand(NewFeedbackCmd(), app, "--category", "bug", "x")
	require.Error(t, err)
	assert.Equal(t, output.CodeAuth, output.AsError(err).Code)
}

func usedApp(t *testing.T, routes ...stubRoute) (*recordingTransport, func(*cobra.Command)) {
	t.Helper()
	app, transport := setupRecordingTestApp(t, routes...)
	app.Collector = observability.NewSessionCollector()
	app.Collector.RecordRequest(observability.RequestMetrics{})
	return transport, func(cmd *cobra.Command) { RefreshFeedbackDetection(cmd, app) }
}

func TestRefreshDetectionAfterAPICommand(t *testing.T) {
	store := useFeedbackStore(t)
	transport, refresh := usedApp(t, discoveryRoute(http.StatusOK))

	refresh(&cobra.Command{Use: "projects"})
	assert.Len(t, requests(transport, http.MethodGet), 1)
	assert.True(t, store.Enabled(feedbackKey))

	refresh(&cobra.Command{Use: "projects"})
	assert.Len(t, requests(transport, http.MethodGet), 1, "a fresh answer is not asked again")
}

func TestRefreshDetectionSkipped(t *testing.T) {
	t.Run("no request made", func(t *testing.T) {
		useFeedbackStore(t)
		app, transport := setupRecordingTestApp(t, discoveryRoute(http.StatusOK))
		app.Collector = observability.NewSessionCollector()
		RefreshFeedbackDetection(&cobra.Command{Use: "version"}, app)
		assert.Empty(t, transport.recorded())
	})
	t.Run("override set", func(t *testing.T) {
		useFeedbackStore(t)
		t.Setenv(feedback.EnvOverride, "1")
		transport, refresh := usedApp(t, discoveryRoute(http.StatusOK))
		refresh(&cobra.Command{Use: "projects"})
		assert.Empty(t, transport.recorded())
	})
	t.Run("wire command", func(t *testing.T) {
		useFeedbackStore(t)
		transport, refresh := usedApp(t, discoveryRoute(http.StatusOK))
		refresh(&cobra.Command{Use: "mcp", Annotations: map[string]string{"stdout_wire": "mcp"}})
		assert.Empty(t, transport.recorded())
	})
	t.Run("auth command", func(t *testing.T) {
		useFeedbackStore(t)
		transport, refresh := usedApp(t, discoveryRoute(http.StatusOK))
		auth := &cobra.Command{Use: "auth"}
		logout := &cobra.Command{Use: "logout"}
		auth.AddCommand(logout)
		refresh(logout)
		assert.Empty(t, transport.recorded())
	})
}

func TestFeedbackHint(t *testing.T) {
	serverErr := &output.Error{Code: output.CodeAPI, Message: "Server error (500)", Cause: basecamp.ErrAPI(500, "Server error (500)")}
	cmd := &cobra.Command{Use: "todos"}

	hint := func(t *testing.T, c *cobra.Command, err error) string {
		t.Helper()
		app, _ := setupRecordingTestApp(t)
		var w bytes.Buffer
		FeedbackHint(&w, c, app, err)
		return w.String()
	}

	t.Run("absent while not advertised", func(t *testing.T) {
		useFeedbackStore(t)
		assert.Empty(t, hint(t, cmd, serverErr))
	})
	t.Run("present once advertised", func(t *testing.T) {
		useFeedbackStore(t).Record(feedbackKey, true)
		assert.Equal(t, feedback.Hint+"\n", hint(t, cmd, serverErr))
	})
	t.Run("present when forced on", func(t *testing.T) {
		useFeedbackStore(t)
		t.Setenv(feedback.EnvOverride, "1")
		assert.Equal(t, feedback.Hint+"\n", hint(t, cmd, serverErr))
	})
	t.Run("absent for errors that are not Basecamp's", func(t *testing.T) {
		useFeedbackStore(t).Record(feedbackKey, true)
		assert.Empty(t, hint(t, cmd, output.ErrUsage("bad flag")))
		assert.Empty(t, hint(t, cmd, convertSDKError(basecamp.ErrAuth("expired"))))
		assert.Empty(t, hint(t, cmd, errors.New("plain")))
	})
	t.Run("absent on feedback itself", func(t *testing.T) {
		useFeedbackStore(t).Record(feedbackKey, true)
		assert.Empty(t, hint(t, NewFeedbackCmd(), serverErr))
	})
}

// The hint is descriptive, never an instruction, and names a command that
// exists.
func TestFeedbackHintWording(t *testing.T) {
	assert.Equal(t, "If this looks like a problem with Basecamp, `basecamp feedback` reports it to the Basecamp team.", feedback.Hint)
}
