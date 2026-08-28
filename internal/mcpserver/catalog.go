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
