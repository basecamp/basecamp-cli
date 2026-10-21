package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// cardTableTransport serves a card table with a three-card Triage column and a
// Done column of 140 cards, paginated 50 to a page with Link and X-Total-Count
// headers the way the API does, and records which Done pages were requested.
type cardTableTransport struct {
	mu        sync.Mutex
	donePages []int
}

const doneColumnCards = 140

func (c *cardTableTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := http.Header{"Content-Type": []string{"application/json"}}
	var body string

	switch req.URL.Path {
	case "/99999/projects/123.json":
		body = `{"id":123,"name":"Test Project","dock":[{"id":777,"name":"kanban_board","title":"Board","enabled":true}]}`
	case "/99999/card_tables/777":
		body = `{"id":777,"title":"Board","lists":[{"id":1,"title":"Triage"},{"id":2,"title":"Done"}]}`
	case "/99999/card_tables/lists/1/cards.json":
		body = cardsJSON(1, 3)
	case "/99999/card_tables/lists/2/cards.json":
		page := 1
		if p := req.URL.Query().Get("page"); p != "" {
			page, _ = strconv.Atoi(p)
		}
		c.mu.Lock()
		c.donePages = append(c.donePages, page)
		c.mu.Unlock()

		first := (page-1)*50 + 1
		count := min(50, doneColumnCards-first+1)
		body = cardsJSON(1000+first, count)
		header.Set("X-Total-Count", strconv.Itoa(doneColumnCards))
		if first+count <= doneColumnCards {
			header.Set("Link", fmt.Sprintf(`<https://3.basecampapi.com/99999/card_tables/lists/2/cards.json?page=%d>; rel="next"`, page+1))
		}
	default:
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"error":"not found"}`)),
			Request:    req,
		}, nil
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func (c *cardTableTransport) pages() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.donePages...)
}

func cardsJSON(firstID, count int) string {
	cards := make([]map[string]any, 0, count)
	for i := range count {
		cards = append(cards, map[string]any{"id": firstID + i, "title": fmt.Sprintf("Card %d", firstID+i)})
	}
	b, _ := json.Marshal(cards)
	return string(b)
}

func runCardTableList(t *testing.T, format output.Format, args ...string) (*cardTableTransport, *bytes.Buffer, error) {
	t.Helper()
	transport, buf, _, err := runCardTableListStderr(t, format, args...)
	return transport, buf, err
}

func runCardTableListStderr(t *testing.T, format output.Format, args ...string) (*cardTableTransport, *bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()
	transport := &cardTableTransport{}
	app, _ := newTestAppWithTransport(t, transport)
	buf, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: format, Writer: buf, ErrWriter: errBuf})
	if format == output.FormatCount {
		app.Flags.Count = true
	}
	err := executeCommand(NewCardsCmd(), app, append([]string{"list", "--in", "123", "--card-table", "777"}, args...)...)
	return transport, buf, errBuf, err
}

// TestCardsListAcrossColumnsCapsEachColumn pins that listing a whole card table
// reads at most cardsPerColumnLimit cards from each column, says which columns
// it cut short, and leaves short columns whole.
func TestCardsListAcrossColumnsCapsEachColumn(t *testing.T) {
	transport, buf, err := runCardTableList(t, output.FormatJSON)
	require.NoError(t, err)

	assert.Equal(t, []int{1, 2}, transport.pages(), "Done should stop after the pages that fill the cap")

	var resp struct {
		Data   []map[string]any `json:"data"`
		Notice string           `json:"notice"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &resp))
	assert.Len(t, resp.Data, 3+cardsPerColumnLimit)
	assert.Contains(t, resp.Notice, "Done (140 total)")
	assert.NotContains(t, resp.Notice, "Triage")
	assert.Contains(t, resp.Notice, "--all")
}

// TestCardsListAcrossColumnsCapIsMachineReadable pins that the JSON envelope
// names every column the cap cut short, with what it showed and the total, so
// a script can tell a capped listing from a complete one without parsing prose.
func TestCardsListAcrossColumnsCapIsMachineReadable(t *testing.T) {
	_, buf, err := runCardTableList(t, output.FormatJSON)
	require.NoError(t, err)

	var resp struct {
		Meta struct {
			TruncatedColumns []map[string]any `json:"truncated_columns"`
		} `json:"meta"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &resp))
	require.Len(t, resp.Meta.TruncatedColumns, 1, "only Done is cut short")
	done := resp.Meta.TruncatedColumns[0]
	assert.EqualValues(t, 2, done["id"])
	assert.Equal(t, "Done", done["title"])
	assert.EqualValues(t, cardsPerColumnLimit, done["shown"])
	assert.EqualValues(t, doneColumnCards, done["total"])
}

// TestCardsListAcrossColumnsCapIsLoudInQuietModes pins that --agent and --quiet,
// which print the data alone, still say on stderr that the list was cut short,
// while stdout stays the bare card array.
func TestCardsListAcrossColumnsCapIsLoudInQuietModes(t *testing.T) {
	_, buf, errBuf, err := runCardTableListStderr(t, output.FormatQuiet)
	require.NoError(t, err)

	assert.Contains(t, errBuf.String(), "notice: Showing the first 100 cards of Done (140 total)")

	var data []map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &data), "stdout must stay data-only")
	assert.Len(t, data, 3+cardsPerColumnLimit)
}

// TestCardsListAcrossColumnsUncappedIsQuiet pins that a listing nothing cut
// short carries no notice and no truncated_columns, in either channel.
func TestCardsListAcrossColumnsUncappedIsQuiet(t *testing.T) {
	_, _, errBuf, err := runCardTableListStderr(t, output.FormatQuiet, "--all")
	require.NoError(t, err)
	assert.Empty(t, errBuf.String())

	_, buf, err := runCardTableList(t, output.FormatJSON, "--all")
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "truncated_columns")
}

// TestCardsListAcrossColumnsAllWalksEveryPage pins that --all, now accepted
// without --column, restores the full walk of every column.
func TestCardsListAcrossColumnsAllWalksEveryPage(t *testing.T) {
	transport, buf, err := runCardTableList(t, output.FormatJSON, "--all")
	require.NoError(t, err)

	assert.Equal(t, []int{1, 2, 3}, transport.pages())

	var resp struct {
		Data   []map[string]any `json:"data"`
		Notice string           `json:"notice"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &resp))
	assert.Len(t, resp.Data, 3+doneColumnCards)
	assert.Empty(t, resp.Notice)
}

// TestCardsListAcrossColumnsCountIsWhole pins that --count still counts every
// card: a count has nowhere to say it was capped.
func TestCardsListAcrossColumnsCountIsWhole(t *testing.T) {
	transport, buf, err := runCardTableList(t, output.FormatCount)
	require.NoError(t, err)

	assert.Equal(t, []int{1, 2, 3}, transport.pages())
	assert.Equal(t, strconv.Itoa(3+doneColumnCards), strings.TrimSpace(buf.String()))
}

// TestCardsListAcrossColumnsRejectsLimitAndPage pins that per-column paging
// flags still require --column.
func TestCardsListAcrossColumnsRejectsLimitAndPage(t *testing.T) {
	for _, args := range [][]string{{"--limit", "5"}, {"--page", "1"}} {
		_, _, err := runCardTableList(t, output.FormatJSON, args...)
		e := cardsUsageError(t, err)
		assert.Equal(t, "Pagination flags require --column", e.Message, "args %v", args)
	}
}
