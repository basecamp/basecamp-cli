// Package mcpserver assembles the MCP server behind `basecamp mcp`:
// Basecamp's tool catalog derived from basecamp-sdk's model exports,
// dispatched through the CLI's authenticated, account-scoped SDK client.
//
// The generic machinery — joining behavior-model.json with openapi.json,
// rendering domain gateway tools, action dispatch, read-only filtering, the
// in-band describe action — lives in the shared toolkit at
// github.com/basecamp/mcp. This package supplies the product half: the
// curated DomainSpecs mapping basecamp-sdk tags to domains, the vendored
// model snapshot under model/ (synced by scripts/sync-mcp-model.sh,
// provenance recorded), and the dispatcher that turns catalog operations
// into basecamp-sdk requests.
package mcpserver

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"

	"github.com/basecamp/mcp/catalog"
)

//go:embed model/behavior-model.json model/openapi.json
var modelFS embed.FS

// loadCatalog derives Basecamp's catalog from the embedded model snapshot,
// with every operation rescoped to the CLI's configured account.
func loadCatalog() (*catalog.Catalog, error) {
	model, err := fs.Sub(modelFS, "model")
	if err != nil {
		return nil, fmt.Errorf("embedded model: %w", err)
	}
	cat, err := catalog.Load(catalog.Spec{
		ToolPrefix: "basecamp_",
		Domains:    DomainSpecs,
		Model:      model,
	})
	if err != nil {
		return nil, err
	}
	if err := rescopeToAccount(cat); err != nil {
		return nil, err
	}
	styles, err := paginationStyles(model)
	if err != nil {
		return nil, err
	}
	if err := synthesizePageParams(cat, styles); err != nil {
		return nil, err
	}
	if err := installComposites(cat); err != nil {
		return nil, err
	}
	return cat, nil
}

// rescopeToAccount removes the accountId path parameter from every
// operation: the CLI's account-scoped SDK client supplies the account, the
// same way every other basecamp command does, so MCP callers never pass one
// and describe never advertises one. The SDK export declares accountId on
// every operation; an operation without it means the model changed shape
// under us, so fail loudly rather than serve a parameter the dispatcher
// would ignore.
func rescopeToAccount(cat *catalog.Catalog) error {
	const prefix = "/{accountId}"
	for _, d := range cat.Domains {
		for _, op := range d.Operations {
			if !strings.HasPrefix(op.Path, prefix+"/") {
				return fmt.Errorf("operation %q path %q is not account-scoped", op.ID, op.Path)
			}
			params := op.Params[:0]
			found := false
			for _, p := range op.Params {
				if p.In == "path" && p.Name == "accountId" {
					found = true
					continue
				}
				params = append(params, p)
			}
			if !found {
				return fmt.Errorf("operation %q declares no accountId path parameter", op.ID)
			}
			op.Path = strings.TrimPrefix(op.Path, prefix)
			op.Params = params
		}
	}
	return nil
}

// Pagination styles the behavior model declares, as basecamp-sdk spells them.
const (
	// paginationLink pages by number, through a Link rel="next" header the
	// dispatcher reads back into next_page.
	paginationLink = "link"
	// paginationCursor pages by an opaque position the response body
	// carries (basecamp-sdk#914): the event feed's poll lanes.
	paginationCursor = "cursor"
)

// paginationStyles reads each paginated operation's declared style from the
// behavior model. The toolkit's catalog keeps only whether an operation is
// paginated, not how, and the page parameter depends on how.
func paginationStyles(model fs.FS) (map[string]string, error) {
	raw, err := fs.ReadFile(model, "behavior-model.json")
	if err != nil {
		return nil, fmt.Errorf("embedded behavior model: %w", err)
	}
	var doc struct {
		Operations map[string]struct {
			Pagination *struct {
				Style string `json:"style"`
			} `json:"pagination"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("embedded behavior model: %w", err)
	}
	styles := make(map[string]string, len(doc.Operations))
	for id, op := range doc.Operations {
		if op.Pagination != nil {
			styles[id] = op.Pagination.Style
		}
	}
	return styles, nil
}

// synthesizePageParams gives every Link-style paginated operation a page query
// parameter. The SDK export marks a handful of operations paginated without
// declaring one (ListWebhooks, ListChatbots, ...); left alone, that makes
// every page after the first unreachable over MCP — the dispatcher rejects
// parameters an operation does not declare, so the next_page value a listing
// returns could never be passed back. Synthesizing from the paginated trait
// covers whatever the model marks, and no-ops once the export declares the
// parameter itself.
//
// Only Link style, keyed off the style the model declares rather than any
// operation's name. A cursor-style operation pages by a position its own
// response carries, and a page number means nothing to it: BC3 ignores the
// parameter, so a caller passing page=2 would be served the first page again
// with nothing in the answer to say so. Those operations keep the entry-point
// parameters the model gives them (since, position) and nothing else.
//
// A paginated operation with a style this function does not recognize stops
// the load. Defaulting it either way is a silent guess — a page parameter that
// does nothing, or a listing whose later pages cannot be reached — so a new
// style has to be decided about here before the server will start.
func synthesizePageParams(cat *catalog.Catalog, styles map[string]string) error {
	for _, d := range cat.Domains {
		for _, op := range d.Operations {
			if !op.Paginated {
				continue
			}
			switch style := styles[op.ID]; style {
			case paginationCursor:
				continue
			case paginationLink:
			default:
				return fmt.Errorf("operation %q declares pagination style %q, which the page parameter has not been decided for", op.ID, style)
			}
			if declaresPage(op) {
				continue
			}
			op.Params = append(op.Params, catalog.Param{
				Name:        "page",
				In:          "query",
				Description: "Page number for paginating through results. Defaults to 1.",
				Schema:      map[string]any{"type": "integer"},
			})
		}
	}
	return nil
}

func declaresPage(op *catalog.Operation) bool {
	for _, p := range op.Params {
		if p.In == "query" && p.Name == "page" {
			return true
		}
	}
	return false
}
