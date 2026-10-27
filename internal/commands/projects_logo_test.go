package commands

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

func TestProjectsLogoRemoveReadsProjectWithoutLogo(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete && r.URL.Path == "/99999/projects/123/logo.json" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/99999/projects/123" {
			fmt.Fprint(w, `{"id":123,"name":"Launch","wallpaper":{"mode":"tile","url":"keep"}}`)
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	app, buf := newRequestLevelApp(t, server.URL)
	app.Config.ProjectID = "123"
	require.NoError(t, executeCommand(NewProjectsCmd(), app, "logo", "remove"))
	assert.Equal(t, []string{"DELETE /99999/projects/123/logo.json", "GET /99999/projects/123"}, calls)
	env := decodeWallpaperEnvelope(t, buf)
	assert.Equal(t, "Logo removed", env.Summary)
	assert.NotContains(t, env.Data, "logo")
	assert.Equal(t, map[string]any{"url": "keep", "mode": "tile"}, env.Data["wallpaper"])
}

const projectLogoURL = "https://3.basecampapi.com/99999/projects/123/logo?v=1"

func TestProjectsLogoSetUploadsAndReadsProject(t *testing.T) {
	var calls []string
	var upload *wallpaperUpload
	parser := &wallpaperServer{t: t}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/99999/projects/123/logo.json":
			upload = parser.readUpload(r)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/99999/projects/123":
			fmt.Fprintf(w, `{"id":123,"name":"Launch","description":"Keep me","logo":{"url":%q},"wallpaper":{"url":"wallpaper-url","mode":"tile"}}`, projectLogoURL)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	app, buf := newRequestLevelApp(t, server.URL)
	app.Flags.Project = "123"
	path := writeWallpaperFile(t, "brand.png", 0)
	require.NoError(t, executeCommand(NewProjectsCmd(), app, "logo", "set", path))
	assert.Equal(t, []string{"PUT /99999/projects/123/logo.json", "GET /99999/projects/123"}, calls)
	require.NotNil(t, upload)
	assert.Equal(t, "logo", upload.field)
	assert.Equal(t, "brand.png", upload.filename)
	assert.Equal(t, "image/png", upload.contentType)
	assert.Equal(t, []byte("\x89PNG\r\n\x1a\n"), upload.data)
	env := decodeWallpaperEnvelope(t, buf)
	assert.True(t, env.OK)
	assert.Equal(t, "Logo set: brand.png", env.Summary)
	assert.Equal(t, map[string]any{"url": projectLogoURL}, env.Data["logo"])
	assert.Equal(t, "Keep me", env.Data["description"])
	assert.Equal(t, map[string]any{"url": "wallpaper-url", "mode": "tile"}, env.Data["wallpaper"])
}

func TestProjectsLogoFollowUpsPreserveScope(t *testing.T) {
	for _, scope := range []struct{ name, profile, source, suffix string }{
		{"configured", "", string(config.SourceGlobal), ""},
		{"override", "staging", string(config.SourceFlag), " --profile staging --account 99999"},
	} {
		for _, action := range []string{"set", "remove"} {
			t.Run(scope.name+"/"+action, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet {
						fmt.Fprint(w, `{"id":123,"name":"Launch"}`)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				}))
				defer server.Close()
				app, buf := newRequestLevelApp(t, server.URL)
				app.Flags.Project = "123"
				app.Config.ActiveProfile = scope.profile
				app.Config.Sources = map[string]string{"account_id": scope.source}
				args := []string{"logo", action}
				want := map[string]string{"show": "basecamp projects show 123" + scope.suffix}
				if action == "set" {
					args = append(args, writeWallpaperFile(t, "brand.png", 0))
					want["remove"] = "basecamp projects logo remove --project 123" + scope.suffix
				} else {
					want["set"] = "basecamp projects logo set <file> --project 123" + scope.suffix
				}
				require.NoError(t, executeCommand(NewProjectsCmd(), app, args...))
				env := decodeWallpaperEnvelope(t, buf)
				got := map[string]string{}
				for _, crumb := range env.Breadcrumbs {
					got[crumb.Action] = crumb.Cmd
				}
				assert.Equal(t, want, got)
			})
		}
	}
}

func TestProjectsLogoSetAcceptsServerFormatsAndSizeBoundary(t *testing.T) {
	for _, tc := range []struct {
		file, mime string
		size       int64
	}{
		{"brand.png", "image/png", 5 << 20},
		{"brand.jpg", "image/jpeg", 0},
		{"brand.gif", "image/gif", 0},
		{"brand.webp", "image/webp", 0},
		{"brand.avif", "image/avif", 0},
		{"brand.heif", "image/heif", 0},
		{"brand.HEIC", "image/heic", 0},
	} {
		t.Run(tc.file, func(t *testing.T) {
			var upload *wallpaperUpload
			parser := &wallpaperServer{t: t}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					upload = parser.readUpload(r)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				fmt.Fprint(w, `{"id":123,"name":"Launch"}`)
			}))
			defer server.Close()
			app, _ := newRequestLevelApp(t, server.URL)
			app.Config.ProjectID = "123"
			require.NoError(t, executeCommand(NewProjectsCmd(), app, "logo", "set", writeWallpaperFile(t, tc.file, tc.size)))
			require.NotNil(t, upload)
			assert.Equal(t, tc.mime, upload.contentType)
		})
	}
}

func TestProjectsLogoRejectsInvalidInputBeforeRequests(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	for _, tc := range []struct {
		name    string
		args    []string
		message string
	}{
		{"missing argument", nil, "<file> required"},
		{"too many arguments", []string{"one.png", "two.png"}, "accepts at most 1 arg(s)"},
		{"oversize", []string{"big.png"}, "big.png exceeds maximum logo size of 5 MB"},
		{"svg", []string{"image.svg"}, "unsupported image type image/svg+xml"},
		{"text", []string{"notes.txt"}, "unsupported image type text/plain"},
		{"missing file", []string{"missing.png"}, "missing.png"},
		{"directory", []string{"directory"}, "directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, app, _ := newWallpaperServer(t)
			app.Flags.Project = "123"
			args := append([]string{"logo", "set"}, tc.args...)
			if len(tc.args) == 1 {
				switch tc.name {
				case "missing file":
					args[2] = filepath.Join(t.TempDir(), tc.args[0])
				case "directory":
					args[2] = t.TempDir()
				default:
					size := int64(0)
					if tc.name == "oversize" {
						size = 5<<20 + 1
					}
					args[2] = writeWallpaperFile(t, tc.args[0], size)
				}
			}
			err := executeCommand(NewProjectsCmd(), app, args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.message)
			assert.Empty(t, s.calls())
		})
	}
}

func TestProjectsLogoMutationRefusalDoesNotReadBack(t *testing.T) {
	for _, action := range []string{"set", "remove"} {
		for _, status := range []int{http.StatusForbidden, http.StatusUnprocessableEntity} {
			t.Run(fmt.Sprintf("%s/%d", action, status), func(t *testing.T) {
				var calls []string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls = append(calls, r.Method+" "+r.URL.Path)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					fmt.Fprint(w, `{"error":"Logo refused"}`)
				}))
				defer server.Close()
				app, _ := newRequestLevelApp(t, server.URL)
				app.Flags.Project = "123"
				args := []string{"logo", action}
				method := "DELETE"
				if action == "set" {
					args = append(args, writeWallpaperFile(t, "brand.png", 0))
					method = "PUT"
				}
				err := executeCommand(NewProjectsCmd(), app, args...)
				var outErr *output.Error
				require.ErrorAs(t, err, &outErr)
				assert.Equal(t, status, outErr.HTTPStatus)
				assert.Contains(t, err.Error(), "Logo refused")
				assert.Equal(t, []string{method + " /99999/projects/123/logo.json"}, calls)
			})
		}
	}
}

func TestProjectsLogoReadbackFailureReportsSuccessfulMutation(t *testing.T) {
	for _, action := range []string{"set", "remove"} {
		t.Run(action, func(t *testing.T) {
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				if r.Method != http.MethodGet {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":"cannot read project"}`)
			}))
			defer server.Close()
			app, buf := newRequestLevelApp(t, server.URL)
			app.Flags.Project = "123"
			app.Config.ActiveProfile = "staging"
			app.Config.Sources = map[string]string{"account_id": string(config.SourceFlag)}
			args := []string{"logo", action}
			method := "DELETE"
			if action == "set" {
				args = append(args, writeWallpaperFile(t, "brand.png", 0))
				method = "PUT"
			}
			require.NoError(t, executeCommand(NewProjectsCmd(), app, args...))
			env := decodeWallpaperEnvelope(t, buf)
			assert.True(t, env.OK)
			assert.Equal(t, float64(123), env.Data["id"])
			assert.Contains(t, env.Notice, "but fetching the latest project state failed")
			assert.Equal(t, "basecamp projects show 123 --profile staging --account 99999", env.Breadcrumbs[0].Cmd)
			assert.Equal(t, []string{method + " /99999/projects/123/logo.json", "GET /99999/projects/123"}, calls)
		})
	}
}
