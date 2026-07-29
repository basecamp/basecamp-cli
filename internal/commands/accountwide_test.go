package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// The root --todolist is a global, so it reaches every account-wide listing
// whether or not the command has any notion of a todolist. I3 lists it among
// the scope-child flags that must be rejected by name rather than dropped.
func TestAccountWideListingsRejectRootTodolist(t *testing.T) {
	cases := []struct {
		name string
		cmd  func() *cobra.Command
		args []string
	}{
		{"messages", NewMessagesCmd, []string{"list"}},
		{"comments", NewCommentsCmd, []string{"list"}},
		{"boost", NewBoostsCmd, []string{"list"}},
		{"forwards", NewForwardsCmd, []string{"list"}},
		{"checkins", NewCheckinsCmd, []string{"answers"}},
		{"files", NewFilesCmd, []string{"list"}},
		{"cards", NewCardsCmd, []string{"list"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, transport := setupRecordingTestApp(t)
			app.Flags.Todolist = "456"

			err := executeRecordingCommand(tc.cmd(), app, tc.args...)
			require.Error(t, err)

			var e *output.Error
			require.True(t, errors.As(err, &e), "expected *output.Error, got %T", err)
			assert.Contains(t, e.Message, "--todolist")
			assert.Empty(t, transport.recorded(), "must reject before any request")
		})
	}
}

// The grouped aggregates nest their items inside project groups. --count and
// --ids read the display rows so they report todos rather than projects, and
// --md renders rows at all instead of a heading with no table.
func TestAccountWideGroupedListingsFeedEveryOutputMode(t *testing.T) {
	body := `[{"bucket":{"id":1,"name":"Alpha"},"todos":[{"id":11,"title":"A1"},{"id":12,"title":"A2"}]},
	          {"bucket":{"id":2,"name":"Beta"},"todos":[{"id":21,"title":"B1"}]}]`

	run := func(t *testing.T, format output.Format) string {
		t.Helper()
		app, _ := setupRecordingTestApp(t, stubRoute{
			method: http.MethodGet, path: "/99999/todos/open.json", status: http.StatusOK, body: body,
		})
		buf := &bytes.Buffer{}
		app.Output = output.New(output.Options{Format: format, Writer: buf})
		require.NoError(t, executeRecordingCommand(newTodosListCmd(), app, "--all-projects", "--page", "1"))
		return buf.String()
	}

	assert.Equal(t, "3\n", run(t, output.FormatCount), "counts todos, not project groups")
	assert.Equal(t, "11\n12\n21\n", run(t, output.FormatIDs))

	md := run(t, output.FormatMarkdown)
	assert.Contains(t, md, "| Alpha |")
	assert.Contains(t, md, "A1")

	// --json keeps the grouping the SDK returned.
	app, _ := setupRecordingTestApp(t, stubRoute{
		method: http.MethodGet, path: "/99999/todos/open.json", status: http.StatusOK, body: body,
	})
	buf := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: buf})
	require.NoError(t, executeRecordingCommand(newTodosListCmd(), app, "--all-projects", "--page", "1"))

	var envelope struct {
		Data []struct {
			Bucket struct {
				Name string `json:"name"`
			} `json:"bucket"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	require.Len(t, envelope.Data, 2)
	assert.Equal(t, "Alpha", envelope.Data[0].Bucket.Name)
}

// An account-wide row the user cannot attribute to a project is not actionable,
// so the recording feeds carry the bucket name into their display rows.
func TestAccountWideRecordingFeedsCarryProject(t *testing.T) {
	body := `[{"id":7,"title":"Message","subject":"Ship it","type":"Message",
	           "bucket":{"id":1,"name":"Alpha"},"created_at":"2026-01-01T00:00:00Z"}]`

	app, _ := setupRecordingTestApp(t, stubRoute{
		method: http.MethodGet, path: "/99999/messages.json", status: http.StatusOK, body: body,
	})
	buf := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatMarkdown, Writer: buf})
	require.NoError(t, executeRecordingCommand(NewMessagesCmd(), app, "list", "--all-projects", "--page", "1"))

	out := buf.String()
	assert.Contains(t, out, "Alpha", "styled/markdown rows must name the project")
	assert.Contains(t, out, "Ship it", "subject wins over the generic recording title")
	assert.True(t, strings.Contains(out, "| Project |") || strings.Contains(out, "Project"),
		"expected a project column, got:\n%s", out)
}

// The flat overdue aggregates return items from every project with the project
// in a nested bucket, which both generic renderers skip by name. Without
// display rows, two otherwise identical overdue todos cannot be told apart.
func TestAccountWideOverdueListingsNameTheirProject(t *testing.T) {
	t.Run("todos", func(t *testing.T) {
		body := `[{"id":11,"title":"Ship it","due_on":"2020-01-01","bucket":{"id":1,"name":"Alpha"}}]`
		app, _ := setupRecordingTestApp(t, stubRoute{
			method: http.MethodGet, path: "/99999/todos/overdue.json", status: http.StatusOK, body: body,
		})
		buf := &bytes.Buffer{}
		app.Output = output.New(output.Options{Format: output.FormatMarkdown, Writer: buf})
		require.NoError(t, executeRecordingCommand(newTodosListCmd(), app, "--all-projects", "--overdue"))
		assert.Contains(t, buf.String(), "Alpha")
		assert.Contains(t, buf.String(), "Ship it")
	})

	t.Run("cards", func(t *testing.T) {
		body := `[{"id":21,"title":"Fix it","due_on":"2020-01-01","bucket":{"id":2,"name":"Beta"}}]`
		app, _ := setupRecordingTestApp(t, stubRoute{
			method: http.MethodGet, path: "/99999/cards/overdue.json", status: http.StatusOK, body: body,
		})
		buf := &bytes.Buffer{}
		app.Output = output.New(output.Options{Format: output.FormatMarkdown, Writer: buf})
		require.NoError(t, executeRecordingCommand(NewCardsCmd(), app, "list", "--all-projects", "--overdue"))
		assert.Contains(t, buf.String(), "Beta")
		assert.Contains(t, buf.String(), "Fix it")
	})
}

// A page beyond int32 used to clamp, serving a different page than the one
// asked for. I3 forbids silently altering a flag as much as dropping it.
func TestAccountWidePageRejectsOutOfRange(t *testing.T) {
	_, err := accountWidePage(math.MaxInt32+1, false)
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "--page is out of range")

	within, err := accountWidePage(math.MaxInt32, false)
	require.NoError(t, err)
	assert.Equal(t, int32(math.MaxInt32), within)

	all, err := accountWidePage(9999999999, true)
	require.NoError(t, err, "--all wins before the range check")
	assert.Equal(t, int32(0), all)
}

// --- accountWideCollect -------------------------------------------------
//
// The bounded walk is the whole reason the account-wide listings are usable at
// account scale, so each guard gets its own test. The request count is asserted
// as often as the payload: "stopped early" and "returned the right items" are
// different claims, and only the first one is what makes these commands fast.

// collectGroup stands in for the project-grouped payloads, where the top-level
// element is a project and the items nest inside it.
type collectGroup struct{ items []int }

func countCollectGroups(groups []collectGroup) int {
	total := 0
	for _, g := range groups {
		total += len(g.items)
	}
	return total
}

// collectPager serves canned pages and records how many were asked for.
type collectPager[T any] struct {
	pages    [][]T
	metas    []basecamp.ListMeta
	err      error
	errOn    int32
	requests int
	asked    []int32
}

func (p *collectPager[T]) fetch(page int32) ([]T, basecamp.ListMeta, error) {
	p.requests++
	p.asked = append(p.asked, page)
	if p.err != nil && page == p.errOn {
		return nil, basecamp.ListMeta{}, p.err
	}
	idx := int(page - 1)
	var meta basecamp.ListMeta
	if idx < len(p.metas) {
		meta = p.metas[idx]
	}
	if idx >= len(p.pages) {
		return nil, meta, nil
	}
	return p.pages[idx], meta, nil
}

func TestAccountWideCollectStopsOnEmptyPage(t *testing.T) {
	pager := &collectPager[int]{pages: [][]int{{1, 2, 3}, {}}}

	items, capped, _, err := accountWideCollect(pager.fetch, accountWideFlatCount[int], 100)

	require.NoError(t, err)
	assert.Equal(t, []int{1, 2, 3}, items)
	assert.False(t, capped, "running out of listing is not hitting the cap")
	assert.Equal(t, 2, pager.requests, "stops at the empty page rather than walking on")
}

// A page can be non-empty at the top level while adding no items, when every
// group on it is empty. Counting groups would call that progress and loop.
func TestAccountWideCollectStopsWhenAPageAddsNoItems(t *testing.T) {
	pager := &collectPager[collectGroup]{pages: [][]collectGroup{
		{{items: []int{1, 2}}},
		{{items: nil}},
		{{items: []int{3}}},
	}}

	items, capped, _, err := accountWideCollect(pager.fetch, countCollectGroups, 100)

	require.NoError(t, err)
	assert.Equal(t, 2, countCollectGroups(items))
	assert.False(t, capped)
	assert.Equal(t, 2, pager.requests, "the no-progress page ends the walk; page 3 is never asked for")
}

// The helper deliberately does not trim: it stops at the first page boundary at
// or past the cap and leaves the exact cut to the caller.
func TestAccountWideCollectOvershootsToThePageBoundary(t *testing.T) {
	pager := &collectPager[int]{pages: [][]int{{1, 2, 3}, {4, 5, 6}, {7, 8, 9}}}

	items, capped, _, err := accountWideCollect(pager.fetch, accountWideFlatCount[int], 4)

	require.NoError(t, err)
	assert.True(t, capped)
	assert.Equal(t, []int{1, 2, 3, 4, 5, 6}, items, "overshoots by up to one page; the caller trims")
	assert.Equal(t, 2, pager.requests, "stops as soon as the cap is met")
}

func TestAccountWideCollectStopsExactlyAtAPageBoundary(t *testing.T) {
	pager := &collectPager[int]{pages: [][]int{{1, 2, 3, 4, 5}, {6, 7, 8, 9, 10}}}

	items, capped, _, err := accountWideCollect(pager.fetch, accountWideFlatCount[int], 5)

	require.NoError(t, err)
	assert.True(t, capped)
	assert.Len(t, items, 5)
	assert.Equal(t, 1, pager.requests, "a cap met by page 1 must not fetch page 2")
}

// TotalCount bounds the walk against len(items) — the top-level elements. On a
// grouped feed those are groups, not the todos or cards inside them. Comparing
// it against the inner counter would end the walk on the first page here.
func TestAccountWideCollectBoundsTotalCountAgainstTopLevelLength(t *testing.T) {
	meta := basecamp.ListMeta{TotalCount: 2}
	pager := &collectPager[collectGroup]{
		pages: [][]collectGroup{
			{{items: []int{1, 2, 3, 4, 5}}},
			{{items: []int{6, 7, 8, 9, 10}}},
			{{items: []int{11}}},
		},
		metas: []basecamp.ListMeta{meta, meta, meta},
	}

	items, capped, _, err := accountWideCollect(pager.fetch, countCollectGroups, 100)

	require.NoError(t, err)
	assert.False(t, capped, "exhausting the listing is not hitting the cap")
	assert.Len(t, items, 2, "two groups is the whole listing TotalCount promised")
	assert.Equal(t, 10, countCollectGroups(items))
	assert.Equal(t, 2, pager.requests,
		"TotalCount counts groups; comparing it against the 5 items on page 1 would stop a page early")
}

// X-Total-Count is the account-wide total on every page, so the first page's
// meta is what the truncation notice should report.
func TestAccountWideCollectReportsFirstPageMeta(t *testing.T) {
	pager := &collectPager[int]{
		pages: [][]int{{1, 2, 3}, {}},
		metas: []basecamp.ListMeta{{TotalCount: 42}, {TotalCount: 7}},
	}

	_, _, meta, err := accountWideCollect(pager.fetch, accountWideFlatCount[int], 100)

	require.NoError(t, err)
	assert.Equal(t, 42, meta.TotalCount, "later pages must not overwrite the account-wide total")
}

func TestAccountWideCollectWalksPositivePagesOnly(t *testing.T) {
	pager := &collectPager[int]{pages: [][]int{{1}, {2}, {3}, {}}}

	_, _, _, err := accountWideCollect(pager.fetch, accountWideFlatCount[int], 100)

	require.NoError(t, err)
	assert.Equal(t, []int32{1, 2, 3, 4}, pager.asked,
		"page 0 is the full-account crawl this helper exists to avoid")
}

func TestAccountWideCollectPropagatesFetchError(t *testing.T) {
	boom := errors.New("boom")
	pager := &collectPager[int]{pages: [][]int{{1, 2, 3}, {4}}, err: boom, errOn: 2}

	items, capped, _, err := accountWideCollect(pager.fetch, accountWideFlatCount[int], 100)

	require.ErrorIs(t, err, boom)
	assert.Nil(t, items)
	assert.False(t, capped)
}

// A listing that ends exactly at the cap is complete, not truncated. Checking
// the cap before exhaustion reported capped=true here, and the caller turned
// that into "more may exist" about a listing with nothing left in it.
func TestAccountWideCollectExactCapIsNotCapped(t *testing.T) {
	pager := &collectPager[int]{
		pages: [][]int{{1, 2, 3, 4, 5}},
		metas: []basecamp.ListMeta{{TotalCount: 5}},
	}

	items, capped, _, err := accountWideCollect(pager.fetch, accountWideFlatCount[int], 5)

	require.NoError(t, err)
	assert.Len(t, items, 5)
	assert.False(t, capped, "TotalCount == limit means the cap and the end of the listing coincide")
	assert.Equal(t, 1, pager.requests)
}

// Exhaustion still has to lose to the trim: holding the whole listing does not
// make a result complete when the caller is about to cut it down to the cap.
func TestAccountWideCollectExhaustedButOvershootingIsCapped(t *testing.T) {
	pager := &collectPager[int]{
		pages: [][]int{{1, 2, 3, 4, 5}},
		metas: []basecamp.ListMeta{{TotalCount: 5}},
	}

	_, capped, _, err := accountWideCollect(pager.fetch, accountWideFlatCount[int], 3)

	require.NoError(t, err)
	assert.True(t, capped, "the caller's trim to 3 drops two items that exist")
}

// X-Total-Count is the account-wide total on every page that carries it, but
// not every page carries it. The bound comes from page 1 so that a later page
// omitting the header cannot switch it off mid-walk.
func TestAccountWideCollectBoundsOnFirstPageTotalOnly(t *testing.T) {
	pager := &collectPager[int]{
		pages: [][]int{{1, 2}, {3, 4}, {5, 6}, {7, 8}},
		metas: []basecamp.ListMeta{{TotalCount: 4}, {}, {}, {}},
	}

	items, capped, meta, err := accountWideCollect(pager.fetch, accountWideFlatCount[int], 100)

	require.NoError(t, err)
	assert.Equal(t, []int{1, 2, 3, 4}, items, "page 1 declared four items; the walk stops there")
	assert.False(t, capped)
	assert.Equal(t, 4, meta.TotalCount)
	assert.Equal(t, 2, pager.requests,
		"pages 3 and 4 omit the header; reading the total from them would walk past the declared end")
}
