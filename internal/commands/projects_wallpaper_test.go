package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

const wallpaperURL = "https://3.basecampapi.com/99999/projects/123/wallpaper?v=1"

// wallpaperServer plays bc3's side of the wallpaper endpoints for project 123:
// it records each request and keeps the wallpaper the requests leave behind,
// so a re-read of the project shows what the change did.
type wallpaperServer struct {
	t        *testing.T
	mu       sync.Mutex
	requests []wallpaperRequest
	attached bool
	mode     string
	refuse   string
}

type wallpaperRequest struct {
	method, path, mode string
	upload             *wallpaperUpload
	update             map[string]any
}

type wallpaperUpload struct {
	field, filename, contentType string
	data                         []byte
}

func newWallpaperServer(t *testing.T) (*wallpaperServer, *appctx.App, *bytes.Buffer) {
	t.Helper()
	s := &wallpaperServer{t: t, mode: "fill"}
	server := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(server.Close)
	app, buf := newRequestLevelApp(t, server.URL)
	return s, app, buf
}

func (s *wallpaperServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	req := wallpaperRequest{method: r.Method, path: r.URL.Path, mode: r.URL.Query().Get("mode")}
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodPut && r.URL.Path == "/99999/projects/123/wallpaper.json":
		req.upload = s.readUpload(r)
		s.requests = append(s.requests, req)
		if s.refuse != "" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprintf(w, `{"error":%q}`, s.refuse)
			return
		}
		s.attached = true
		if req.mode != "" {
			s.mode = req.mode
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && r.URL.Path == "/99999/projects/123/wallpaper.json":
		s.requests = append(s.requests, req)
		s.attached = false
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut && r.URL.Path == "/99999/projects/123":
		require.NoError(s.t, json.NewDecoder(r.Body).Decode(&req.update))
		s.requests = append(s.requests, req)
		if mode, ok := req.update["wallpaper_mode"].(string); ok {
			s.mode = mode
		}
		fmt.Fprint(w, s.projectJSON())
	case r.Method == http.MethodGet && r.URL.Path == "/99999/projects/123":
		s.requests = append(s.requests, req)
		fmt.Fprint(w, s.projectJSON())
	default:
		s.requests = append(s.requests, req)
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"not found"}`)
	}
}

func (s *wallpaperServer) readUpload(r *http.Request) *wallpaperUpload {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	require.NoError(s.t, err)
	part, err := multipart.NewReader(r.Body, params["boundary"]).NextPart()
	require.NoError(s.t, err)
	data, err := io.ReadAll(part)
	require.NoError(s.t, err)
	return &wallpaperUpload{
		field:       part.FormName(),
		filename:    part.FileName(),
		contentType: part.Header.Get("Content-Type"),
		data:        data,
	}
}

func (s *wallpaperServer) projectJSON() string {
	if !s.attached {
		return `{"id":123,"name":"Launch","description":"Keep me"}`
	}
	return fmt.Sprintf(`{"id":123,"name":"Launch","description":"Keep me","wallpaper":{"url":%q,"mode":%q}}`, wallpaperURL, s.mode)
}

func (s *wallpaperServer) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.requests))
	for i, r := range s.requests {
		out[i] = r.method + " " + r.path
	}
	return out
}

func (s *wallpaperServer) request(method, path string) wallpaperRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.requests {
		if r.method == method && r.path == path {
			return r
		}
	}
	s.t.Fatalf("no %s %s among %v", method, path, s.requests)
	return wallpaperRequest{}
}

type wallpaperEnvelope struct {
	OK          bool                `json:"ok"`
	Summary     string              `json:"summary"`
	Notice      string              `json:"notice"`
	Breadcrumbs []output.Breadcrumb `json:"breadcrumbs"`
	Data        map[string]any      `json:"data"`
}

func decodeWallpaperEnvelope(t *testing.T, buf *bytes.Buffer) wallpaperEnvelope {
	t.Helper()
	var env wallpaperEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env), "output: %s", buf.String())
	return env
}

func writeWallpaperFile(t *testing.T, name string, size int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte("\x89PNG\r\n\x1a\n"), 0o600))
	if size > 0 {
		require.NoError(t, os.Truncate(path, size))
	}
	return path
}

func TestProjectsWallpaperSetUploadsTheFileAndReturnsTheProject(t *testing.T) {
	s, app, buf := newWallpaperServer(t)
	app.Flags.Project = "123"
	path := writeWallpaperFile(t, "skyline.png", 0)

	require.NoError(t, executeCommand(NewProjectsCmd(), app, "wallpaper", "set", path, "--mode", "tile"))

	assert.Equal(t, []string{
		"PUT /99999/projects/123/wallpaper.json",
		"GET /99999/projects/123",
	}, s.calls())
	put := s.request(http.MethodPut, "/99999/projects/123/wallpaper.json")
	assert.Equal(t, "tile", put.mode)
	require.NotNil(t, put.upload)
	assert.Equal(t, "wallpaper", put.upload.field)
	assert.Equal(t, "skyline.png", put.upload.filename)
	assert.Equal(t, "image/png", put.upload.contentType)
	assert.Equal(t, []byte("\x89PNG\r\n\x1a\n"), put.upload.data)

	env := decodeWallpaperEnvelope(t, buf)
	assert.True(t, env.OK)
	assert.Equal(t, "Wallpaper set: skyline.png (tile)", env.Summary)
	assert.Equal(t, map[string]any{"url": wallpaperURL, "mode": "tile"}, env.Data["wallpaper"])
	assert.Equal(t, "basecamp projects show 123", env.Breadcrumbs[0].Cmd)
}

func TestProjectsWallpaperSetWithoutModeKeepsTheCurrentMode(t *testing.T) {
	s, app, buf := newWallpaperServer(t)
	app.Config.ProjectID = "123"
	path := writeWallpaperFile(t, "skyline.png", 0)

	require.NoError(t, executeCommand(NewProjectsCmd(), app, "wallpaper", "set", path))

	put := s.request(http.MethodPut, "/99999/projects/123/wallpaper.json")
	assert.Empty(t, put.mode, "no mode is sent, so the server keeps its own")
	env := decodeWallpaperEnvelope(t, buf)
	assert.Equal(t, "Wallpaper set: skyline.png", env.Summary)
	assert.Equal(t, map[string]any{"url": wallpaperURL, "mode": "fill"}, env.Data["wallpaper"])
}

func TestProjectsWallpaperSetAcceptsExactlyTheSizeCap(t *testing.T) {
	s, app, _ := newWallpaperServer(t)
	app.Flags.Project = "123"
	path := writeWallpaperFile(t, "big.jpg", maxWallpaperMB<<20)

	require.NoError(t, executeCommand(NewProjectsCmd(), app, "wallpaper", "set", path))

	assert.Equal(t, "image/jpeg", s.request(http.MethodPut, "/99999/projects/123/wallpaper.json").upload.contentType)
}

func TestProjectsWallpaperSetAcceptsServerImageFormats(t *testing.T) {
	for _, tc := range []struct{ filename, contentType string }{
		{"image.png", "image/png"},
		{"image.jpg", "image/jpeg"},
		{"image.gif", "image/gif"},
		{"image.webp", "image/webp"},
		{"image.avif", "image/avif"},
		{"image.heif", "image/heif"},
		{"image.HEIC", "image/heic"},
	} {
		t.Run(tc.filename, func(t *testing.T) {
			s, app, _ := newWallpaperServer(t)
			app.Flags.Project = "123"
			path := writeWallpaperFile(t, tc.filename, 0)

			require.NoError(t, executeCommand(NewProjectsCmd(), app, "wallpaper", "set", path))
			assert.Equal(t, tc.contentType, s.request(http.MethodPut, "/99999/projects/123/wallpaper.json").upload.contentType)
		})
	}
}

func TestProjectsWallpaperSetRefusesBeforeAnyRequest(t *testing.T) {
	cases := []struct {
		name, file string
		size       int64
		args       []string
		message    string
	}{
		{"over the size cap", "big.png", maxWallpaperMB<<20 + 1, nil, "big.png exceeds maximum wallpaper size of 10 MB"},
		{"an SVG", "image.svg", 0, nil, "image.svg: unsupported image type image/svg+xml (use PNG, JPEG, GIF, WebP, AVIF, HEIF, or HEIC)"},
		{"plain text", "notes.txt", 0, nil, "notes.txt: unsupported image type text/plain (use PNG, JPEG, GIF, WebP, AVIF, HEIF, or HEIC)"},
		{"an unknown mode", "skyline.png", 0, []string{"--mode", "stretch"}, `unknown wallpaper mode "stretch"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, app, _ := newWallpaperServer(t)
			app.Flags.Project = "123"
			path := writeWallpaperFile(t, tc.file, tc.size)

			err := executeCommand(NewProjectsCmd(), app, append([]string{"wallpaper", "set", path}, tc.args...)...)

			var outErr *output.Error
			require.ErrorAs(t, err, &outErr)
			assert.Equal(t, output.CodeUsage, outErr.Code)
			assert.Equal(t, tc.message, outErr.Message)
			assert.Empty(t, s.calls())
		})
	}
}

func TestProjectsWallpaperSetRefusesAMissingFileBeforeAnyRequest(t *testing.T) {
	s, app, _ := newWallpaperServer(t)
	app.Flags.Project = "123"

	err := executeCommand(NewProjectsCmd(), app, "wallpaper", "set", filepath.Join(t.TempDir(), "nope.png"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope.png")
	assert.Empty(t, s.calls())
}

func TestProjectsWallpaperSetCarriesTheServersRefusal(t *testing.T) {
	s, app, _ := newWallpaperServer(t)
	s.refuse = "Wallpaper is not an allowed image type"
	app.Flags.Project = "123"
	path := writeWallpaperFile(t, "disguised.png", 0)

	err := executeCommand(NewProjectsCmd(), app, "wallpaper", "set", path)

	require.Error(t, err)
	assert.Contains(t, err.Error(), s.refuse)
	assert.Equal(t, []string{"PUT /99999/projects/123/wallpaper.json"}, s.calls(), "a refused upload is not followed by a read")
}

func TestProjectsWallpaperModeSendsTheCurrentNameWithTheMode(t *testing.T) {
	s, app, buf := newWallpaperServer(t)
	s.attached = true
	app.Flags.Project = "123"

	require.NoError(t, executeCommand(NewProjectsCmd(), app, "wallpaper", "mode", "tile"))

	assert.Equal(t, []string{
		"GET /99999/projects/123",
		"PUT /99999/projects/123",
		"GET /99999/projects/123",
	}, s.calls())
	assert.Equal(t, map[string]any{"name": "Launch", "wallpaper_mode": "tile"},
		s.request(http.MethodPut, "/99999/projects/123").update,
		"only the name rides along, so the description is left alone")

	env := decodeWallpaperEnvelope(t, buf)
	assert.Equal(t, "Wallpaper mode set to tile", env.Summary)
	assert.Equal(t, map[string]any{"url": wallpaperURL, "mode": "tile"}, env.Data["wallpaper"])
}

func TestProjectsWallpaperModeRefusesAnUnknownModeBeforeAnyRequest(t *testing.T) {
	s, app, _ := newWallpaperServer(t)
	app.Flags.Project = "123"

	err := executeCommand(NewProjectsCmd(), app, "wallpaper", "mode", "Tile")

	var outErr *output.Error
	require.ErrorAs(t, err, &outErr)
	assert.Equal(t, output.CodeUsage, outErr.Code)
	assert.Equal(t, `unknown wallpaper mode "Tile"`, outErr.Message)
	assert.Equal(t, "Use fill or tile", outErr.Hint)
	assert.Empty(t, s.calls())
}

func TestProjectsWallpaperRemoveDeletesAndReturnsTheProjectWithoutWallpaper(t *testing.T) {
	s, app, buf := newWallpaperServer(t)
	s.attached = true
	app.Flags.Project = "123"

	require.NoError(t, executeCommand(NewProjectsCmd(), app, "wallpaper", "remove"))

	assert.Equal(t, []string{
		"DELETE /99999/projects/123/wallpaper.json",
		"GET /99999/projects/123",
	}, s.calls())
	env := decodeWallpaperEnvelope(t, buf)
	assert.Equal(t, "Wallpaper removed", env.Summary)
	assert.Equal(t, float64(123), env.Data["id"])
	assert.NotContains(t, env.Data, "wallpaper")
}

func TestProjectsWallpaperReportsAFailedReadAfterTheChange(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"boom"}`)
	}))
	t.Cleanup(server.Close)
	app, buf := newRequestLevelApp(t, server.URL)
	app.Flags.Project = "123"

	require.NoError(t, executeCommand(NewProjectsCmd(), app, "wallpaper", "remove"))

	assert.Equal(t, []string{"DELETE /99999/projects/123/wallpaper.json", "GET /99999/projects/123"}, calls)
	env := decodeWallpaperEnvelope(t, buf)
	assert.True(t, env.OK)
	assert.Equal(t, float64(123), env.Data["id"])
	assert.Contains(t, env.Notice, "Wallpaper removed, but fetching the latest project state failed")
}
