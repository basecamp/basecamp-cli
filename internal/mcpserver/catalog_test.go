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
		"basecamp_clientside", "basecamp_forwards", "basecamp_account",
	}, tools)
}

// TestCatalogClaimsEveryTag fails when the SDK grows a tag nobody has
// decided about: mapping it in DomainSpecs is the deliberate act.
func TestCatalogClaimsEveryTag(t *testing.T) {
	cat := loadForTest(t)
	assert.Empty(t, cat.Unmapped, "every SDK tag must be claimed by a DomainSpec")
}

// TestCatalogExcludesBinaryUploads pins the operations the sync script drops
// from the vendored model: raw-binary uploads can't ride the JSON tool-call
// convention. Uploads stay a CLI affair (basecamp attach / upload).
func TestCatalogExcludesBinaryUploads(t *testing.T) {
	cat := loadForTest(t)
	excluded := map[string]bool{
		"CreateAttachment":     true,
		"CreateCampfireUpload": true,
		"UpdateAccountLogo":    true,
	}
	total := 0
	for _, d := range cat.Domains {
		for _, op := range d.Operations {
			total++
			assert.False(t, excluded[op.ID], "operation %q should be excluded from the vendored model", op.ID)
		}
	}
	assert.Equal(t, 247, total, "served operation count")
}

// TestCatalogIsAccountScoped pins the rescope: the CLI's account-scoped SDK
// client supplies the account, so no served operation asks the caller for an
// accountId or carries one in its path template.
func TestCatalogIsAccountScoped(t *testing.T) {
	cat := loadForTest(t)
	for _, d := range cat.Domains {
		for _, op := range d.Operations {
			assert.NotContains(t, op.Path, "{accountId}", "operation %q path", op.ID)
			require.True(t, strings.HasPrefix(op.Path, "/"), "operation %q path %q", op.ID, op.Path)
			for _, p := range op.Params {
				assert.NotEqual(t, "accountId", p.Name, "operation %q still declares accountId", op.ID)
			}
		}
	}
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
	assert.Equal(t, "go/"+string(match[1]), provenance.Ref,
		"vendored model must match the basecamp-sdk version go.mod pins — run scripts/sync-mcp-model.sh against that checkout")
}
