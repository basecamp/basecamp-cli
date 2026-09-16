package mcpserver

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/mcp/catalog"
	"github.com/basecamp/mcp/mcptest"
)

func loadForTest(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := loadCatalog()
	require.NoError(t, err, "catalog must derive cleanly from the vendored model")
	return cat
}

func TestCatalogServesCuratedDomains(t *testing.T) {
	cat := loadForTest(t)

	tools := make([]string, 0, len(cat.Domains))
	for _, d := range cat.Domains {
		tools = append(tools, d.Tool)
		assert.NotEmpty(t, d.Operations, "domain %q has no operations", d.Key)
	}
	assert.Equal(t, []string{
		"basecamp_projects", "basecamp_todos", "basecamp_cards", "basecamp_messages",
		"basecamp_campfires", "basecamp_boosts", "basecamp_schedules", "basecamp_files",
		"basecamp_people", "basecamp_automation", "basecamp_reports", "basecamp_everything",
		"basecamp_eventfeed", "basecamp_clientside", "basecamp_forwards", "basecamp_account",
		"basecamp_recordings",
	}, tools)
}

// TestCatalogClaimsEveryTag fails when the SDK grows a tag nobody has
// decided about: mapping it in DomainSpecs is the deliberate act.
func TestCatalogClaimsEveryTag(t *testing.T) {
	cat := loadForTest(t)
	assert.Empty(t, cat.Unmapped, "every SDK tag must be claimed by a DomainSpec")
}

// TestCatalogExcludesUnservedOperations pins the operations the sync script
// drops from the vendored model. Raw-binary uploads can't ride the JSON
// tool-call convention, so uploads stay a CLI affair (basecamp attach /
// upload). The stream-ticket mint could ride it and is dropped anyway: its
// result is a replayable bearer and a URL embedding it, the dispatcher
// returns a result verbatim into a model transcript with no reveal opt-in,
// and nothing on this surface could open the WebSocket it is for.
func TestCatalogExcludesUnservedOperations(t *testing.T) {
	cat := loadForTest(t)
	excluded := map[string]bool{
		"CreateAttachment":     true,
		"CreateCampfireUpload": true,
		"UpdateAccountLogo":    true,
		"CreateStreamTicket":   true,
	}
	model, composite := 0, 0
	for _, d := range cat.Domains {
		for _, op := range d.Operations {
			if isComposite(op) {
				composite++
				continue
			}
			model++
			assert.False(t, excluded[op.ID], "operation %q should be excluded from the vendored model", op.ID)
		}
	}
	assert.Equal(t, 261, model, "served model operation count")
	assert.Equal(t, 1, composite, "served composite operation count")
}

// isComposite reports the SDK compositions this package serves itself: no
// single Basecamp endpoint backs them, so they carry no method and no path.
// That absence is the catalog's synthetic marker, and the tests below read
// it the way a client would.
func isComposite(op *catalog.Operation) bool {
	return op.Method == "" && op.Path == ""
}

// TestCatalogMarksCompositesSynthetic pins how a composite action announces
// itself: no method, no path, and a summary that says so. A model operation
// always carries both, so the two can never be confused.
func TestCatalogMarksCompositesSynthetic(t *testing.T) {
	cat := loadForTest(t)

	seen := map[string]bool{}
	for _, d := range cat.Domains {
		for _, op := range d.Operations {
			if !isComposite(op) {
				assert.NotEmpty(t, op.Method, "model operation %q must carry a method", op.ID)
				assert.NotEmpty(t, op.Path, "model operation %q must carry a path", op.ID)
				continue
			}
			seen[d.Key+"."+op.Action] = true
			assert.True(t, strings.HasPrefix(op.Summary, "Synthetic: "),
				"composite %q summary must say it is synthetic, got %q", op.ID, op.Summary)
			assert.NotEmpty(t, op.Doc, "composite %q must document what it composes", op.ID)
		}
	}
	assert.Equal(t, map[string]bool{"recordings.summarize": true}, seen)
}

// TestCatalogDeclaresMentionsOnCreateComment pins the other half of the
// composite surface: the mentions parameter is advertised on the model
// operation that posts a comment, so describe shows it and the dispatcher
// accepts it.
func TestCatalogDeclaresMentionsOnCreateComment(t *testing.T) {
	cat := loadForTest(t)

	var op *catalog.Operation
	for _, d := range cat.Domains {
		if d.Key != "messages" {
			continue
		}
		for _, candidate := range d.Operations {
			if candidate.Action == "create_comment" {
				op = candidate
			}
		}
	}
	require.NotNil(t, op, "messages must serve create_comment")

	properties, ok := op.Body["properties"].(map[string]any)
	require.True(t, ok, "create_comment must declare body properties")
	mentions, ok := properties["mentions"].(map[string]any)
	require.True(t, ok, "create_comment must declare a mentions property")
	assert.Equal(t, "array", mentions["type"])
	items, ok := mentions["items"].(map[string]any)
	require.True(t, ok, "mentions must declare its item schema")
	assert.NotEmpty(t, items["anyOf"], "a person id is a number or a quoted number, and the schema must say so")
}

// TestCatalogIsAccountScoped pins the rescope: the CLI's account-scoped SDK
// client supplies the account, so no served operation asks the caller for an
// accountId or carries one in its path template.
func TestCatalogIsAccountScoped(t *testing.T) {
	cat := loadForTest(t)
	for _, d := range cat.Domains {
		for _, op := range d.Operations {
			if isComposite(op) {
				continue // no request of its own to scope
			}
			assert.NotContains(t, op.Path, "{accountId}", "operation %q path", op.ID)
			require.True(t, strings.HasPrefix(op.Path, "/"), "operation %q path %q", op.ID, op.Path)
			for _, p := range op.Params {
				assert.NotEqual(t, "accountId", p.Name, "operation %q still declares accountId", op.ID)
			}
		}
	}
}

// TestCatalogPaginatedActionsTakePage pins the synthesized page parameter:
// every operation the behavior model marks paginated must declare a page
// query parameter, whether the OpenAPI export supplies it or loadCatalog
// synthesizes it. Otherwise the next_page value a listing returns could
// never be passed back — the dispatcher rejects undeclared parameters.
func TestCatalogPaginatedActionsTakePage(t *testing.T) {
	cat := loadForTest(t)
	paginated := 0
	for _, d := range cat.Domains {
		for _, op := range d.Operations {
			if !op.Paginated {
				continue
			}
			paginated++
			pages := 0
			for _, p := range op.Params {
				if p.In != "query" || p.Name != "page" {
					continue
				}
				pages++
				assert.Equal(t, "integer", p.Schema["type"], "operation %q page schema", op.ID)
				assert.NotEmpty(t, p.Description, "operation %q page description", op.ID)
			}
			assert.Equal(t, 1, pages, "operation %q must declare exactly one page query parameter", op.ID)
		}
	}
	assert.Equal(t, 61, paginated, "paginated operation count")
}

// TestCatalogSnapshot renders the full served surface — every tool
// description, action, and flag — so a model sync or curation change shows
// its whole effect as a reviewable diff. Regenerate with -update.
func TestCatalogSnapshot(t *testing.T) {
	cat := loadForTest(t)

	var b strings.Builder
	for _, d := range cat.Domains {
		b.WriteString("== " + d.Tool + "\n")
		b.WriteString(d.Description())
		b.WriteString("\n")
	}
	mcptest.Snapshot(t, "testdata/catalog_snapshot.txt", []byte(b.String()))
}

// TestCatalogModelProvenance keeps the vendored snapshot in lockstep with
// the basecamp-sdk release the CLI builds against: a go.mod bump without a
// snapshot refresh (or vice versa) fails here, so MCP never advertises
// routes from a different SDK version than the one linked in.
func TestCatalogModelProvenance(t *testing.T) {
	data, err := os.ReadFile("model/PROVENANCE.json")
	require.NoError(t, err)
	var provenance struct {
		Source string   `json:"source"`
		Commit string   `json:"commit"`
		Ref    string   `json:"ref"`
		Files  []string `json:"files"`
	}
	require.NoError(t, json.Unmarshal(data, &provenance))
	assert.Equal(t, "github.com/basecamp/basecamp-sdk", provenance.Source)
	assert.NotEmpty(t, provenance.Commit)

	gomod, err := os.ReadFile("../../go.mod")
	require.NoError(t, err)
	match := regexp.MustCompile(`github\.com/basecamp/basecamp-sdk/go (v\S+)`).FindSubmatch(gomod)
	require.NotNil(t, match, "basecamp-sdk dependency not found in go.mod")
	version := string(match[1])

	// A tagged release names itself: the snapshot records the tag the
	// export came from. A pseudo-version names a commit instead —
	// vX.Y.Z-0.YYYYMMDDhhmmss-abcdef123456 — and its last segment is the
	// 12-character prefix of that commit, which is the stronger thing to
	// pin: the snapshot must have been taken from the very commit go.mod
	// links in, not merely from a matching version string.
	// Either way the ref must not be a dirty describe: the sync script
	// marks a checkout with uncommitted model edits, and a snapshot taken
	// from one corresponds to no commit at all.
	assert.False(t, strings.HasSuffix(provenance.Ref, "-dirty"),
		"vendored model was synced from a dirty basecamp-sdk checkout (%s) — sync from a clean one", provenance.Ref)

	if commit, ok := pseudoVersionCommit(version); ok {
		assert.True(t, strings.HasPrefix(provenance.Commit, commit),
			"vendored model was synced from commit %s, but go.mod links in %s — run scripts/sync-mcp-model.sh against that checkout",
			provenance.Commit, version)
		return
	}
	assert.Equal(t, version, strings.TrimPrefix(provenance.Ref, "go/"),
		"vendored model must match the basecamp-sdk version go.mod pins — run scripts/sync-mcp-model.sh against that checkout")
}

// pseudoVersionCommit returns the commit prefix a Go pseudo-version carries,
// and false for a plain release version. The timestamp follows a "-" when
// the version has no base prerelease (v0.0.0-20260916083520-5acb2f9aaca9)
// and a "." when it has one (v0.18.1-0.20260916083520-5acb2f9aaca9), so
// both separators are accepted.
func pseudoVersionCommit(version string) (string, bool) {
	match := regexp.MustCompile(`[-.]([0-9]{14})-([0-9a-f]{12})$`).FindStringSubmatch(version)
	if match == nil {
		return "", false
	}
	return match[2], true
}
