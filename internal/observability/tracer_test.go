package observability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTracer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "trace.log")

	tr, err := NewTracer(TraceHTTP, path)
	require.NoError(t, err)

	assert.Equal(t, path, tr.Path())
	assert.True(t, tr.Enabled(TraceHTTP))
	assert.False(t, tr.Enabled(TraceTUI))

	// Write an event, close to flush, then verify file content
	tr.Log(TraceHTTP, "test.event", "key", "value")
	require.NoError(t, tr.Close())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "test.event")
	assert.Contains(t, string(data), `"key":"value"`)
}

func TestTracer_Categories(t *testing.T) {
	dir := t.TempDir()

	tr, err := NewTracer(TraceAll, filepath.Join(dir, "trace.log"))
	require.NoError(t, err)
	defer tr.Close()

	assert.True(t, tr.Enabled(TraceHTTP))
	assert.True(t, tr.Enabled(TraceTUI))
	assert.True(t, tr.Enabled(TraceAll))
}

func TestTracer_HTTPOnly_SkipsTUI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trace.log")

	tr, err := NewTracer(TraceHTTP, path)
	require.NoError(t, err)

	tr.Log(TraceTUI, "tui.event", "key", "should-not-appear")
	tr.Log(TraceHTTP, "http.event", "key", "should-appear")
	require.NoError(t, tr.Close())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "tui.event")
	assert.Contains(t, string(data), "http.event")
}

func TestTracer_EnableCategories(t *testing.T) {
	dir := t.TempDir()

	tr, err := NewTracer(TraceHTTP, filepath.Join(dir, "trace.log"))
	require.NoError(t, err)
	defer tr.Close()

	assert.True(t, tr.Enabled(TraceHTTP))
	assert.False(t, tr.Enabled(TraceTUI))

	tr.EnableCategories(TraceTUI)
	assert.True(t, tr.Enabled(TraceHTTP))
	assert.True(t, tr.Enabled(TraceTUI))
}

func TestTracer_NilSafe(t *testing.T) {
	var tr *Tracer

	// All methods should be no-ops on nil
	assert.False(t, tr.Enabled(TraceHTTP))
	assert.Equal(t, "", tr.Path())
	assert.Nil(t, tr.Logger())
	assert.NoError(t, tr.Close())

	// Log and EnableCategories should not panic
	tr.Log(TraceHTTP, "test", "key", "value")
	tr.EnableCategories(TraceAll)
}

func TestTracePath(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		path := TracePath("")
		assert.Contains(t, path, "basecamp")
		assert.Contains(t, path, "trace-")
		assert.True(t, strings.HasSuffix(path, ".log"))
	})

	t.Run("custom cache dir", func(t *testing.T) {
		dir := t.TempDir()
		path := TracePath(dir)
		assert.True(t, strings.HasPrefix(path, dir))
		assert.Contains(t, path, "trace-")
	})
}

func TestParseTraceEnv_Empty(t *testing.T) {
	t.Setenv("BASECAMP_TRACE", "")
	t.Setenv("BASECAMP_DEBUG", "")

	tr := ParseTraceEnv()
	assert.Nil(t, tr)
}

func TestParseTraceEnv_HTTP(t *testing.T) {
	t.Setenv("BASECAMP_TRACE", "http")
	t.Setenv("BASECAMP_DEBUG", "")

	tr := ParseTraceEnvWithCacheDir(t.TempDir())
	require.NotNil(t, tr)
	defer tr.Close()

	assert.True(t, tr.Enabled(TraceHTTP))
	assert.False(t, tr.Enabled(TraceTUI))
}

func TestParseTraceEnv_TUI(t *testing.T) {
	t.Setenv("BASECAMP_TRACE", "tui")
	t.Setenv("BASECAMP_DEBUG", "")

	tr := ParseTraceEnvWithCacheDir(t.TempDir())
	require.NotNil(t, tr)
	defer tr.Close()

	assert.False(t, tr.Enabled(TraceHTTP))
	assert.True(t, tr.Enabled(TraceTUI))
}

func TestParseTraceEnv_All(t *testing.T) {
	for _, val := range []string{"all", "1", "true"} {
		t.Run(val, func(t *testing.T) {
			t.Setenv("BASECAMP_TRACE", val)
			t.Setenv("BASECAMP_DEBUG", "")

			tr := ParseTraceEnvWithCacheDir(t.TempDir())
			require.NotNil(t, tr)
			defer tr.Close()

			assert.True(t, tr.Enabled(TraceHTTP))
			assert.True(t, tr.Enabled(TraceTUI))
		})
	}
}

func TestParseTraceEnv_CustomPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.log")

	t.Setenv("BASECAMP_TRACE", path)
	t.Setenv("BASECAMP_DEBUG", "")

	tr := ParseTraceEnv()
	require.NotNil(t, tr)
	defer tr.Close()

	assert.Equal(t, path, tr.Path())
	assert.True(t, tr.Enabled(TraceAll))
}

func TestParseTraceEnv_DebugBackcompat(t *testing.T) {
	for _, val := range []string{"1", "2", "true"} {
		t.Run(val, func(t *testing.T) {
			t.Setenv("BASECAMP_TRACE", "")
			t.Setenv("BASECAMP_DEBUG", val)

			tr := ParseTraceEnvWithCacheDir(t.TempDir())
			require.NotNil(t, tr)
			defer tr.Close()

			assert.True(t, tr.Enabled(TraceHTTP))
			assert.False(t, tr.Enabled(TraceTUI))
		})
	}
}

func TestParseTraceEnv_DebugGarbageIgnored(t *testing.T) {
	// Garbage BASECAMP_DEBUG values that the verbosity parser ignores
	// should not create trace files.
	for _, val := range []string{"yes", "on", "verbose", "0"} {
		t.Run(val, func(t *testing.T) {
			t.Setenv("BASECAMP_TRACE", "")
			t.Setenv("BASECAMP_DEBUG", val)

			tr := ParseTraceEnv()
			assert.Nil(t, tr)
		})
	}
}

func TestParseTraceEnv_UnknownValue(t *testing.T) {
	t.Setenv("BASECAMP_TRACE", "bogus")
	t.Setenv("BASECAMP_DEBUG", "")

	tr := ParseTraceEnv()
	assert.Nil(t, tr)
}

func TestParseTraceEnvWithCacheDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BASECAMP_TRACE", "http")
	t.Setenv("BASECAMP_DEBUG", "")

	tr := ParseTraceEnvWithCacheDir(dir)
	require.NotNil(t, tr)
	defer tr.Close()

	assert.True(t, strings.HasPrefix(tr.Path(), dir))
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(home, "trace.log"), expandHome("~/trace.log"))
	assert.Equal(t, "/absolute/path", expandHome("/absolute/path"))
	assert.Equal(t, "./relative/path", expandHome("./relative/path"))
}

func TestIsPositiveInt(t *testing.T) {
	assert.True(t, isPositiveInt("1"))
	assert.True(t, isPositiveInt("2"))
	assert.True(t, isPositiveInt("42"))
	assert.False(t, isPositiveInt(""))
	assert.False(t, isPositiveInt("0"))
	assert.False(t, isPositiveInt("true"))
	assert.False(t, isPositiveInt("-1"))
	assert.False(t, isPositiveInt("abc"))
}
