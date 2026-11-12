package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

type preferencesCapture struct {
	gets    int
	putSeen bool
	putBody map[string]any
}

// setupPreferencesMockServer serves GET and PUT /my/preferences.json,
// recording the PUT body into capture. A GET answers with the preferences the
// last PUT set, or the defaults before any.
func setupPreferencesMockServer(t *testing.T, accountID string, capture *preferencesCapture) *httptest.Server {
	t.Helper()

	prefs := map[string]any{
		"url":            "https://3.basecampapi.com/" + accountID + "/my/preferences.json",
		"app_url":        "https://3.basecamp.com/" + accountID + "/my/preferences",
		"time_zone_name": "Central Time (US & Canada)",
		"first_week_day": "Sunday",
		"time_format":    "twelve_hour",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := fmt.Sprintf("/%s/my/preferences.json", accountID)

		switch {
		case r.URL.Path == path && r.Method == http.MethodGet:
			capture.gets++
			json.NewEncoder(w).Encode(prefs)
		case r.URL.Path == path && r.Method == http.MethodPut:
			capture.putSeen = true
			body, _ := io.ReadAll(r.Body)
			capture.putBody = map[string]any{}
			if err := json.Unmarshal(body, &capture.putBody); err != nil {
				http.Error(w, fmt.Sprintf("bad body: %v", err), http.StatusBadRequest)
				return
			}
			if person, ok := capture.putBody["person"].(map[string]any); ok {
				for k, v := range person {
					prefs[k] = v
				}
			}
			json.NewEncoder(w).Encode(prefs)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

type preferencesResult struct {
	OK      bool   `json:"ok"`
	Summary string `json:"summary"`
	Notice  string `json:"notice"`
	Data    struct {
		FirstWeekDay string `json:"first_week_day"`
		TimeFormat   string `json:"time_format"`
		TimeZoneName string `json:"time_zone_name"`
		Updated      bool   `json:"updated"`
	} `json:"data"`
}

func TestPeoplePreferencesShow(t *testing.T) {
	capture := &preferencesCapture{}
	server := setupPreferencesMockServer(t, "99999", capture)
	app, buf := setupPeopleMockApp(t, server)

	err := executePeopleCommand(NewPeopleCmd(), app, "preferences", "me")
	require.NoError(t, err)

	assert.False(t, capture.putSeen, "a bare invocation must not write")
	assert.Equal(t, 1, capture.gets)

	var result preferencesResult
	require.NoError(t, json.Unmarshal(buf.Bytes(), &result), "output: %s", buf.String())
	assert.Equal(t, "Sunday", result.Data.FirstWeekDay)
	assert.Equal(t, "twelve_hour", result.Data.TimeFormat)
	assert.Equal(t, "Central Time (US & Canada)", result.Data.TimeZoneName)
	assert.Equal(t, "Your preferences: week starts Sunday, 12-hour time, Central Time (US & Canada)", result.Summary)
}

func TestPeoplePreferencesDefaultsToMe(t *testing.T) {
	capture := &preferencesCapture{}
	server := setupPreferencesMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	err := executePeopleCommand(NewPeopleCmd(), app, "preferences")
	require.NoError(t, err)
	assert.Equal(t, 1, capture.gets)
}

func TestPeoplePreferencesUpdateSendsOnlyGivenFields(t *testing.T) {
	capture := &preferencesCapture{}
	server := setupPreferencesMockServer(t, "99999", capture)
	app, buf := setupPeopleMockApp(t, server)

	err := executePeopleCommand(NewPeopleCmd(), app, "preferences", "me", "--week-start", "monday")
	require.NoError(t, err)

	require.True(t, capture.putSeen)
	person, ok := capture.putBody["person"].(map[string]any)
	require.True(t, ok, "fields go inside person, got: %v", capture.putBody)
	assert.Equal(t, map[string]any{"first_week_day": "Monday"}, person)

	var result preferencesResult
	require.NoError(t, json.Unmarshal(buf.Bytes(), &result), "output: %s", buf.String())
	assert.Equal(t, "Updated your preferences", result.Summary)
	assert.Equal(t, "Monday", result.Data.FirstWeekDay, "the read-back shows the new value")
	assert.Equal(t, "twelve_hour", result.Data.TimeFormat)
}

func TestPeoplePreferencesUpdateAllFields(t *testing.T) {
	capture := &preferencesCapture{}
	server := setupPreferencesMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	err := executePeopleCommand(NewPeopleCmd(), app, "preferences", "me",
		"--week-start", "Saturday", "--time-format", "24h", "--time-zone", "Europe/London")
	require.NoError(t, err)

	require.True(t, capture.putSeen)
	assert.Equal(t, map[string]any{
		"first_week_day": "Saturday",
		"time_format":    "twenty_four_hour",
		"time_zone_name": "Europe/London",
	}, capture.putBody["person"])
}

func TestPeoplePreferencesTimeFormatNames(t *testing.T) {
	for input, want := range map[string]string{
		"12h":              "twelve_hour",
		"24H":              "twenty_four_hour",
		"twelve_hour":      "twelve_hour",
		"twenty_four_hour": "twenty_four_hour",
	} {
		got, err := normalizeTimeFormat(input)
		require.NoError(t, err, input)
		assert.Equal(t, want, got, input)
	}
}

func TestPeoplePreferencesRejectsBadInputBeforeAnyRequest(t *testing.T) {
	for name, args := range map[string][]string{
		"unknown day":         {"--week-start", "Funday"},
		"empty day":           {"--week-start", ""},
		"unknown time format": {"--time-format", "military"},
		"empty time format":   {"--time-format", ""},
		"empty time zone":     {"--time-zone", "  "},
	} {
		t.Run(name, func(t *testing.T) {
			capture := &preferencesCapture{}
			server := setupPreferencesMockServer(t, "99999", capture)
			app, _ := setupPeopleMockApp(t, server)

			err := executePeopleCommand(NewPeopleCmd(), app, append([]string{"preferences", "me"}, args...)...)
			require.Error(t, err)

			var e *output.Error
			require.True(t, errors.As(err, &e))
			assert.Equal(t, output.CodeUsage, e.Code)
			assert.False(t, capture.putSeen)
			assert.Zero(t, capture.gets)
		})
	}
}

func TestPeoplePreferencesRejectsOtherTarget(t *testing.T) {
	capture := &preferencesCapture{}
	server := setupPreferencesMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	err := executePeopleCommand(NewPeopleCmd(), app, "preferences", "1001", "--week-start", "Monday")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Contains(t, e.Message, `Only "me" is a valid target`)
	assert.False(t, capture.putSeen)
	assert.Zero(t, capture.gets)
}

// A failed read-back keeps the completed update reported as a success, with a
// diagnostic, rather than turning it into an error.
func TestPeoplePreferencesReadBackFailureStillSucceeds(t *testing.T) {
	putSeen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/99999/my/preferences.json" && r.Method == http.MethodPut {
			putSeen = true
			w.Write([]byte(`{}`))
			return
		}
		http.Error(w, "boom", http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	app, buf := setupPeopleMockApp(t, server)

	err := executePeopleCommand(NewPeopleCmd(), app, "preferences", "me", "--time-format", "24h")
	require.NoError(t, err, "output: %s", buf.String())
	assert.True(t, putSeen)

	var result preferencesResult
	require.NoError(t, json.Unmarshal(buf.Bytes(), &result), "output: %s", buf.String())
	assert.True(t, result.OK)
	assert.True(t, result.Data.Updated)
	assert.Contains(t, result.Notice, "reading them back failed")
}
