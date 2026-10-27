package commands

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// filesFilingTransport has, in Source (123), Docs & Files 500 with folder 501
// ("Specs"), document 456 at its top and upload 457 and cloud file 458 in
// Specs. Dest (999) has Docs & Files 900 with folder 950 ("Contracts") and
// 960 ("2026") inside that. A filed item <id> reads back as <id>0.
func filesFilingTransport() *familyTransport {
	return &familyTransport{gets: map[string]string{
		"/projects":          filingProjects,
		"/projects/123":      `{"id":123,"name":"Source","dock":[{"name":"vault","id":500,"title":"Docs & Files","enabled":true}]}`,
		"/projects/999":      `{"id":999,"name":"Dest","dock":[{"name":"vault","id":900,"title":"Docs & Files","enabled":true}]}`,
		"/vaults/500":        `{"id":500,"type":"Vault","title":"Docs & Files","bucket":{"id":123,"name":"Source"}}`,
		"/vaults/501":        `{"id":501,"type":"Vault","title":"Specs","bucket":{"id":123,"name":"Source"},"parent":{"id":500,"title":"Docs & Files","type":"Vault"}}`,
		"/vaults/900":        `{"id":900,"type":"Vault","title":"Docs & Files","bucket":{"id":999,"name":"Dest"}}`,
		"/vaults/950":        `{"id":950,"type":"Vault","title":"Contracts","bucket":{"id":999,"name":"Dest"},"parent":{"id":900,"title":"Docs & Files","type":"Vault"}}`,
		"/vaults/960":        `{"id":960,"type":"Vault","title":"2026","bucket":{"id":999,"name":"Dest"},"parent":{"id":950,"title":"Contracts","type":"Vault"}}`,
		"/vaults/900/vaults": `[{"id":950,"type":"Vault","title":"Contracts"}]`,
		"/documents/456":     `{"id":456,"type":"Document","title":"Plan","bucket":{"id":123,"name":"Source"},"parent":{"id":500,"title":"Docs & Files","type":"Vault"}}`,
		"/documents/4560":    `{"id":4560,"type":"Document","title":"Plan","bucket":{"id":999,"name":"Dest"},"parent":{"id":900,"title":"Docs & Files","type":"Vault"}}`,
		"/uploads/457":       `{"id":457,"type":"Upload","filename":"spec.pdf","bucket":{"id":123,"name":"Source"},"parent":{"id":501,"title":"Specs","type":"Vault"}}`,
		"/uploads/4570":      `{"id":4570,"type":"Upload","filename":"spec.pdf","bucket":{"id":999,"name":"Dest"},"parent":{"id":900,"title":"Docs & Files","type":"Vault"}}`,
		"/cloud_files/458":   `{"id":458,"type":"CloudFile","title":"Board deck","bucket":{"id":123,"name":"Source"},"parent":{"id":501,"title":"Specs","type":"Vault"}}`,
		"/cloud_files/4580":  `{"id":4580,"type":"CloudFile","title":"Board deck","bucket":{"id":999,"name":"Dest"},"parent":{"id":950,"title":"Contracts","type":"Vault"}}`,
	}}
}

func TestFilesMoveToProjectLandsAtTheTopOfItsDocsAndFiles(t *testing.T) {
	transport := filesFilingTransport()
	run := runFamilyCommand(t, transport, NewFilesCmd(), "move", "456", "--to-project", "Dest")
	require.NoError(t, run.err)

	assert.Equal(t, []string{"/99999/recordings/456/moves"}, transport.posts)
	assert.Equal(t, float64(900), transport.bodies[0]["filing_destination_id"])
	assert.NotContains(t, transport.bodies[0], "parent_id")
	data := run.data(t)
	assert.Equal(t, float64(4560), data["id"])
	assert.Equal(t, float64(456), data["source_id"])
	assert.Equal(t, "Moved file #456 to Dest › Docs & Files as file #4560", run.envelope.Summary)
}

func TestFilesMoveToATopLevelFolderByTitle(t *testing.T) {
	transport := filesFilingTransport()
	run := runFamilyCommand(t, transport, NewFilesCmd(), "move", "456", "--to-project", "Dest", "--to-folder", "contracts")
	require.NoError(t, run.err)
	assert.Equal(t, float64(900), transport.bodies[0]["filing_destination_id"])
	assert.Equal(t, float64(950), transport.bodies[0]["parent_id"])
}

func TestFilesMoveToANestedFolderByURLFindsItsDocsAndFiles(t *testing.T) {
	transport := filesFilingTransport()
	run := runFamilyCommand(t, transport, NewFilesCmd(), "move", "458", "--to-folder", "https://3.basecamp.com/99999/buckets/999/vaults/960")
	require.NoError(t, run.err)
	assert.Equal(t, float64(900), transport.bodies[0]["filing_destination_id"])
	assert.Equal(t, float64(960), transport.bodies[0]["parent_id"])
	assert.Equal(t, "CloudFile", run.data(t)["type"], "a cloud file reads back as itself")
}

func TestFilesMoveFolderTitleNotAtTheTopListsWhatIs(t *testing.T) {
	transport := filesFilingTransport()
	run := runFamilyCommand(t, transport, NewFilesCmd(), "move", "456", "--to-project", "Dest", "--to-folder", "2026")
	e := run.failure(t)
	assert.Equal(t, `No folder "2026" at the top of Dest's Docs & Files`, e.Message)
	assert.Contains(t, e.Hint, "950 (Contracts)")
	assert.Contains(t, e.Hint, "nested folder's ID or URL")
	assert.Empty(t, transport.posts)
}

func TestFilesMoveSeveralFilesEachOne(t *testing.T) {
	transport := filesFilingTransport()
	run := runFamilyCommand(t, transport, NewFilesCmd(), "move", "456", "457", "--to-project", "Dest")
	require.NoError(t, run.err)

	assert.Equal(t, []string{"/99999/recordings/456/moves", "/99999/recordings/457/moves"}, transport.posts)
	var data []map[string]any
	require.NoError(t, json.Unmarshal(run.envelope.Data, &data))
	require.Len(t, data, 2)
	assert.Equal(t, "spec.pdf", data[1]["filename"])
	assert.Equal(t, float64(457), data[1]["source_id"])
}

func TestFilesMoveSeveralReportsEachWhenOneFails(t *testing.T) {
	transport := filesFilingTransport()
	transport.refuse = map[int64]int{456: 403}
	run := runFamilyCommand(t, transport, NewFilesCmd(), "move", "456", "457", "--to-project", "Dest")
	e := run.failure(t)
	assert.Equal(t, output.CodeForbidden, e.Code)
	assert.Contains(t, e.Message, "#457 as #4570 in Dest › Docs & Files")
	assert.Contains(t, e.Message, "#456 refused")
	assert.Contains(t, e.Hint, "run the command again for #456 alone")
	assert.NotContains(t, e.Hint, "#457", "a file that moved is never offered for another run")
	assert.Len(t, transport.posts, 2)
}

func TestFilesMoveToAFolderInItsOwnProjectPointsAtTo(t *testing.T) {
	transport := filesFilingTransport()
	run := runFamilyCommand(t, transport, NewFilesCmd(), "move", "456", "--to-folder", "501")
	e := run.failure(t)
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Equal(t, "Move it to that folder and keep its ID with: basecamp files move 456 --to 501", e.Hint)
	assert.Empty(t, transport.posts)
}

func TestFilesMoveRefusesToBesideToProject(t *testing.T) {
	transport := filesFilingTransport()
	run := runFamilyCommand(t, transport, NewFilesCmd(), "move", "456", "--to", "501", "--to-project", "Dest")
	e := run.failure(t)
	assert.Contains(t, e.Message, "cannot be combined with --to-project or --to-folder")
	assert.Empty(t, transport.posts)
}

func TestFilesMoveToAFolderInAnotherProjectPointsAtToFolder(t *testing.T) {
	transport := filesFilingTransport()
	run := runFamilyCommand(t, transport, NewFilesCmd(), "move",
		"https://3.basecamp.com/99999/buckets/123/documents/456", "--to", "https://3.basecamp.com/99999/buckets/999/vaults/950")
	e := run.failure(t)
	assert.Equal(t, "Move it to that project with: basecamp files move 456 --to-folder 950", e.Hint)
	assert.Empty(t, transport.posts)
}

func TestFilesCopyWithoutDestinationGoesIntoItsOwnFolder(t *testing.T) {
	transport := filesFilingTransport()
	run := runFamilyCommand(t, transport, NewFilesCmd(), "copy", "457")
	require.NoError(t, run.err)
	assert.Equal(t, []string{"/99999/recordings/457/copies"}, transport.posts)
	assert.Equal(t, float64(500), transport.bodies[0]["filing_destination_id"])
	assert.Equal(t, float64(501), transport.bodies[0]["parent_id"])
}

func TestFilesCopyRefusesTheListingFolderFlag(t *testing.T) {
	transport := filesFilingTransport()
	run := runFamilyCommand(t, transport, NewFilesCmd(), "copy", "456", "--folder", "950")
	e := run.failure(t)
	assert.Contains(t, e.Hint, "--to-folder")
	assert.Empty(t, transport.posts)
}

func TestFilesMoveAndCopyAreOnEveryFilesGroup(t *testing.T) {
	for name, group := range map[string]func() *cobra.Command{
		"uploads": NewUploadsCmd,
		"docs":    NewDocsCmd,
		"vaults":  NewVaultsCmd,
	} {
		t.Run(name, func(t *testing.T) {
			transport := filesFilingTransport()
			run := runFamilyCommand(t, transport, group(), "copy", "456", "--to-project", "Dest", "--include-comments")
			require.NoError(t, run.err)
			assert.Equal(t, true, transport.bodies[0]["include_comments"])

			transport = filesFilingTransport()
			run = runFamilyCommand(t, transport, group(), "move", "456", "--to-project", "Dest")
			require.NoError(t, run.err)
			assert.Equal(t, []string{"/99999/recordings/456/moves"}, transport.posts)
		})
	}
}

func TestFilesRefuseAnItemNamedTwiceBeforeFilingAny(t *testing.T) {
	for name, tc := range map[string]struct {
		args    []string
		message string
	}{
		"the same id twice":                  {[]string{"copy", "456", "456", "--to-project", "Dest"}, "456 and 456 are both file #456"},
		"copy by its old id and its new one": {[]string{"copy", "457", "111", "456", "--to-project", "Dest"}, "111 and 456 are both file #456"},
		"move by its old id and its new one": {[]string{"move", "111", "456", "--to-project", "Dest"}, "111 and 456 are both file #456"},
	} {
		t.Run(name, func(t *testing.T) {
			transport := filesFilingTransport()
			transport.gets["/documents/111"] = transport.gets["/documents/456"]
			run := runFamilyCommand(t, transport, NewFilesCmd(), tc.args...)
			e := run.failure(t)

			assert.Equal(t, output.CodeUsage, e.Code)
			assert.Equal(t, tc.message, e.Message)
			assert.Empty(t, transport.posts, "nothing is filed")
		})
	}
}

func TestFilesShowFindsCloudFilesAndGoogleDocuments(t *testing.T) {
	for name, tc := range map[string]struct {
		id, typ string
	}{
		"a cloud file":        {"4580", "CloudFile"},
		"a Google document":   {"4590", "GoogleDocument"},
		"a typed cloud file":  {"4580", "CloudFile"},
		"a typed Google file": {"4590", "GoogleDocument"},
	} {
		t.Run(name, func(t *testing.T) {
			transport := filesFilingTransport()
			transport.gets["/google_documents/4590"] = `{"id":4590,"type":"GoogleDocument","title":"Roadmap","bucket":{"id":999,"name":"Dest"},"parent":{"id":900,"title":"Docs & Files","type":"Vault"}}`
			args := []string{"show", tc.id, "--in", "Dest"}
			if strings.HasPrefix(name, "a typed") {
				args = append(args, "--type", map[string]string{"CloudFile": "cloud_file", "GoogleDocument": "google_document"}[tc.typ])
			}
			run := runFamilyCommand(t, transport, NewFilesCmd(), args...)
			require.NoError(t, run.err)
			assert.Equal(t, tc.typ, run.data(t)["type"])
		})
	}
}

func TestFilesCopyOfACloudFileFollowsUpWithCommandsThatFindIt(t *testing.T) {
	transport := filesFilingTransport()
	run := runFamilyCommand(t, transport, NewFilesCmd(), "copy", "458", "--to-project", "Dest")
	require.NoError(t, run.err)
	require.NotEmpty(t, run.envelope.Breadcrumbs)
	show := strings.Fields(run.envelope.Breadcrumbs[0].Cmd)[1:]
	shown := runFamilyCommand(t, transport, NewFilesCmd(), show[1:]...)
	require.NoError(t, shown.err, "the breadcrumb %v finds the copy", show)
	assert.Equal(t, float64(4580), shown.data(t)["id"])

	transport.stuck = map[int64]bool{458: true}
	e := runFamilyCommand(t, transport, NewFilesCmd(), "copy", "458", "--to-project", "Dest").failure(t)
	assert.Equal(t, output.CodeInProgress, e.Code)
	assert.Contains(t, e.Hint, "basecamp timeline --in 999 --all", "files list does not list cloud files, so the check is the timeline")
	assert.NotContains(t, e.Hint, "files list")
}

func TestFilesCopyRecoveryListsArchivedItemsWhereTheyShow(t *testing.T) {
	transport := filesFilingTransport()
	transport.gets["/documents/456"] = strings.Replace(transport.gets["/documents/456"], `"type":"Document"`, `"type":"Document","status":"archived"`, 1)
	transport.stuck = map[int64]bool{456: true}
	e := runFamilyCommand(t, transport, NewFilesCmd(), "copy", "456", "--to-project", "Dest").failure(t)
	assert.Contains(t, e.Hint, "basecamp recordings list Document --in 999 --status archived --sort created_at --direction desc --all")
}

func TestFilesCopyOfAGoogleDocumentPointsAtItsFolderInBasecamp(t *testing.T) {
	transport := filesFilingTransport()
	transport.gets["/google_documents/459"] = `{"id":459,"type":"GoogleDocument","title":"Roadmap","bucket":{"id":123,"name":"Source"},"parent":{"id":501,"title":"Specs","type":"Vault"}}`
	transport.stuck = map[int64]bool{459: true}
	e := runFamilyCommand(t, transport, NewFilesCmd(), "copy", "459", "--to-project", "Dest", "--to-folder", "Contracts").failure(t)
	assert.Equal(t, output.CodeInProgress, e.Code)
	assert.Contains(t, e.Hint, "basecamp files show 950 --in 999, and open its app_url in Basecamp")
	assert.NotContains(t, e.Hint, "timeline", "the timeline never shows a Google document")
}

func TestFilesFolderTitleSharedWithItsDocsAndFilesIsAmbiguous(t *testing.T) {
	transport := filesFilingTransport()
	transport.gets["/vaults/900/vaults"] = `[{"id":950,"type":"Vault","title":"Contracts"},{"id":951,"type":"Vault","title":"Docs & Files"}]`
	run := runFamilyCommand(t, transport, NewFilesCmd(), "copy", "456", "--to-project", "Dest", "--to-folder", "docs & files")
	e := run.failure(t)

	assert.Equal(t, output.CodeAmbiguous, e.Code)
	assert.Contains(t, e.Hint, "900  Docs & Files (the top of Docs & Files)")
	assert.Contains(t, e.Hint, "951  Docs & Files")
	assert.Empty(t, transport.posts)
}

func TestFilesFolderTitleOfItsDocsAndFilesAloneIsTheTop(t *testing.T) {
	transport := filesFilingTransport()
	run := runFamilyCommand(t, transport, NewFilesCmd(), "copy", "456", "--to-project", "Dest", "--to-folder", "Docs & Files")
	require.NoError(t, run.err)
	assert.Equal(t, float64(900), transport.bodies[0]["filing_destination_id"])
	assert.NotContains(t, transport.bodies[0], "parent_id")
}

func TestFilesCopySeveralNamesWhereEachLanded(t *testing.T) {
	transport := filesFilingTransport()
	transport.gets["/documents/4560"] = `{"id":4560,"type":"Document","title":"Plan","bucket":{"id":123,"name":"Source"},"parent":{"id":500,"title":"Docs & Files","type":"Vault"}}`
	transport.gets["/uploads/4570"] = `{"id":4570,"type":"Upload","filename":"spec.pdf","bucket":{"id":123,"name":"Source"},"parent":{"id":501,"title":"Specs","type":"Vault"}}`
	run := runFamilyCommand(t, transport, NewFilesCmd(), "copy", "456", "457")
	require.NoError(t, run.err)

	assert.Equal(t, float64(500), transport.bodies[0]["filing_destination_id"])
	assert.Equal(t, float64(501), transport.bodies[1]["parent_id"], "each copy goes into its own original's folder")
	assert.Equal(t, "Copied 2 files: #456 as #4560 in Source › Docs & Files, #457 as #4570 in Source › Specs", run.envelope.Summary)
	crumbs := make([]string, 0, len(run.envelope.Breadcrumbs))
	for _, b := range run.envelope.Breadcrumbs {
		crumbs = append(crumbs, b.Cmd)
	}
	assert.Contains(t, crumbs, "basecamp files show 4560 --in 123")
	assert.Contains(t, crumbs, "basecamp files show 4570 --in 123")
}

func TestFilesCopyOfADraftDocumentThatTimesOutIsLookedForAmongDrafts(t *testing.T) {
	transport := filesFilingTransport()
	transport.gets["/documents/456"] = strings.Replace(transport.gets["/documents/456"], `"type":"Document"`, `"type":"Document","status":"drafted"`, 1)
	transport.stuck = map[int64]bool{456: true}
	e := runFamilyCommand(t, transport, NewFilesCmd(), "copy", "456", "--to-project", "Dest").failure(t)
	assert.Equal(t, output.CodeInProgress, e.Code)
	assert.Contains(t, e.Hint, "basecamp drafts list --all")
	assert.NotContains(t, e.Hint, "--status drafted")
}
