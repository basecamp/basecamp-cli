package feedback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeGetter struct {
	err   error
	calls int
}

func (f *fakeGetter) Get(context.Context, string) (*basecamp.Response, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &basecamp.Response{StatusCode: 200, Data: []byte(`{"categories":[]}`)}, nil
}

func newTestStore(t *testing.T, now *time.Time) *Store {
	t.Helper()
	t.Setenv(EnvOverride, "")
	s := NewStore(t.TempDir())
	s.now = func() time.Time { return *now }
	return s
}

func TestOverride(t *testing.T) {
	cases := map[string][2]bool{
		"":      {false, false},
		"1":     {true, true},
		"true":  {true, true},
		"ON":    {true, true},
		"0":     {false, true},
		"false": {false, true},
		"off":   {false, true},
		"maybe": {false, false},
	}
	for value, want := range cases {
		t.Run(value, func(t *testing.T) {
			t.Setenv(EnvOverride, value)
			on, set := Override()
			assert.Equal(t, want, [2]bool{on, set})
		})
	}
}

func TestNothingAdvertisedUntilDetected(t *testing.T) {
	now := time.Now()
	s := newTestStore(t, &now)
	key := Key("https://3.basecampapi.com", "1")

	assert.False(t, s.Visible())
	assert.False(t, s.Enabled(key))
	assert.True(t, s.NeedsRefresh(key))
}

func TestDetectRecordsAdvertised(t *testing.T) {
	now := time.Now()
	s := newTestStore(t, &now)
	key := Key("https://3.basecampapi.com/", "1")

	advertised, err := s.Detect(context.Background(), &fakeGetter{}, key)
	require.NoError(t, err)
	assert.True(t, advertised)
	assert.True(t, s.Enabled(key))
	assert.True(t, s.Visible())
	assert.False(t, s.NeedsRefresh(key), "a fresh answer is not due")

	// Another account on the machine is not enabled by this one's answer,
	// though help (which cannot know the account) shows the command.
	assert.False(t, s.Enabled(Key("https://3.basecampapi.com", "2")))
}

func TestDetectRecordsNotFoundAsOff(t *testing.T) {
	now := time.Now()
	s := newTestStore(t, &now)
	key := Key("", "1")
	s.Record(key, true)

	advertised, err := s.Detect(context.Background(), &fakeGetter{err: basecamp.ErrNotFound("Resource", "x")}, key)
	require.NoError(t, err)
	assert.False(t, advertised)
	assert.False(t, s.Enabled(key))
	assert.False(t, s.Visible())
}

func TestCachedAnswerExpiresAfterTTL(t *testing.T) {
	now := time.Now()
	s := newTestStore(t, &now)
	key := Key("", "1")
	s.Record(key, true)

	now = now.Add(TTL - time.Minute)
	assert.False(t, s.NeedsRefresh(key))
	now = now.Add(2 * time.Minute)
	assert.True(t, s.NeedsRefresh(key))
	assert.True(t, s.Enabled(key), "a stale answer stands until a refresh replaces it")
}

func TestFailedDetectionKeepsAnswerAndBacksOff(t *testing.T) {
	now := time.Now()
	s := newTestStore(t, &now)
	key := Key("", "1")
	s.Record(key, true)
	now = now.Add(TTL + time.Minute)

	_, err := s.Detect(context.Background(), &fakeGetter{err: basecamp.ErrAPI(500, "boom")}, key)
	require.Error(t, err)
	assert.True(t, s.Enabled(key), "a failure keeps the previous answer")
	assert.False(t, s.NeedsRefresh(key), "no retry right after a failure")

	now = now.Add(retryAfterFailure)
	assert.True(t, s.NeedsRefresh(key))
}

func TestOverrideWinsOverCache(t *testing.T) {
	now := time.Now()
	s := newTestStore(t, &now)
	key := Key("", "1")
	s.Record(key, true)

	t.Setenv(EnvOverride, "0")
	assert.False(t, s.Enabled(key))
	assert.False(t, s.Visible())

	s.Record(key, false)
	t.Setenv(EnvOverride, "1")
	assert.True(t, s.Enabled(key))
	assert.True(t, s.Visible())
}

func TestCorruptStateReadsAsNothingAdvertised(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvOverride, "")
	require.NoError(t, os.WriteFile(filepath.Join(dir, stateFile), []byte("{not json"), 0o600))
	s := NewStore(dir)
	assert.False(t, s.Visible())

	s.Record(Key("", "1"), true)
	assert.True(t, s.Visible(), "a corrupt file is replaced by the next answer")
}

func TestDefaultStoreHonorsCacheDirEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BASECAMP_CACHE_DIR", dir)
	t.Setenv(EnvOverride, "")
	DefaultStore().Record(Key("", "1"), true)
	_, err := os.Stat(filepath.Join(dir, stateFile))
	require.NoError(t, err)
}

func TestWantsHint(t *testing.T) {
	assert.True(t, WantsHint(basecamp.ErrAPI(500, "boom")))
	assert.True(t, WantsHint(&basecamp.Error{Code: basecamp.CodeValidation, HTTPStatus: 422}))
	assert.True(t, WantsHint(basecamp.ErrNotFound("Todo", "1")))
	assert.True(t, WantsHint(errors.Join(errors.New("wrapped"), basecamp.ErrAPI(502, "gateway"))))

	assert.False(t, WantsHint(errors.New("plain")))
	assert.False(t, WantsHint(basecamp.ErrAuth("expired")))
	assert.False(t, WantsHint(basecamp.ErrRateLimit(30)))
	assert.False(t, WantsHint(basecamp.ErrNetwork(errors.New("offline"))))
	assert.False(t, WantsHint(basecamp.ErrForbidden("no")))
}

func TestCategories(t *testing.T) {
	for _, c := range []string{"bug", "missing_capability", "confusing", "praise", "other"} {
		assert.True(t, ValidCategory(c), c)
	}
	for _, c := range []string{"", "Bug", "feature", "bug "} {
		assert.False(t, ValidCategory(c), c)
	}
}
