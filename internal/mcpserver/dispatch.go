package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/mcp/catalog"
	"github.com/basecamp/mcp/gateway"
)

// API is the slice of the basecamp-sdk client the dispatcher drives. The
// CLI's *basecamp.AccountClient satisfies it; the client carries auth, token
// refresh, retry, account scoping, and base URL resolution, so the
// dispatcher only assembles paths and bodies.
type API interface {
	Get(ctx context.Context, path string) (*basecamp.Response, error)
	Post(ctx context.Context, path string, body any) (*basecamp.Response, error)
	Put(ctx context.Context, path string, body any) (*basecamp.Response, error)
	Delete(ctx context.Context, path string) (*basecamp.Response, error)
}

// The CLI hands its account-scoped client straight to New.
var _ API = (*basecamp.AccountClient)(nil)

// dispatcher turns catalog operations into basecamp-sdk requests.
//
// Calling convention: the tool call's params object carries the operation's
// path and query parameters by name, and every remaining entry becomes a
// request body property. The describe action serves the schema for all
// three. Failures are in-band isError results per MCP convention.
type dispatcher struct {
	api API
}

func (d dispatcher) handle(ctx context.Context, dom gateway.Domain, op gateway.Operation, params map[string]any) (*mcp.CallToolResult, error) {
	domain, ok := dom.(*catalog.Domain)
	if !ok {
		return gateway.ErrorResult("internal error: domain %q is not a catalog domain", dom.Name()), nil
	}
	full, ok := domain.Operation(op.Action)
	if !ok {
		return gateway.ErrorResult("internal error: action %q not in domain %q", op.Action, dom.Name()), nil
	}

	path, body, err := buildRequest(full, params)
	if err != nil {
		return gateway.ErrorResult("%v", err), nil
	}

	resp, err := d.call(ctx, full.Method, path, body)
	if err != nil {
		return gateway.ErrorResult("%v", err), nil
	}
	if data := bytes.TrimSpace(resp.Data); len(data) == 0 || bytes.Equal(data, []byte("null")) {
		// Completions, repositions, and deletes answer 204 with no body
		// (the SDK normalizes those to JSON null); surface the status so
		// the caller knows the write landed.
		result := map[string]any{"status": resp.StatusCode}
		if location := resp.Headers.Get("Location"); location != "" {
			result["location"] = location
		}
		return gateway.JSONResult(result)
	}
	if next := nextPage(resp.Headers); next > 0 {
		// Paginated listings surface the Link rel="next" page to pass back
		// as the action's page parameter.
		wrapped, err := json.Marshal(map[string]any{"next_page": next, "results": resp.Data})
		if err == nil {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(wrapped)}}}, nil
		}
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(resp.Data)}}}, nil
}

// nextPage extracts the page number from a geared_pagination Link
// rel="next" header, 0 when there is none. Basecamp pages by number: when a
// listing has more, the result is wrapped as {"next_page": N, "results": ...}
// and the caller passes N back as the action's page parameter — a number, to
// match the page parameter's integer schema.
func nextPage(headers http.Header) int {
	for _, link := range headers.Values("Link") {
		for part := range strings.SplitSeq(link, ",") {
			if !strings.Contains(part, `rel="next"`) {
				continue
			}
			start := strings.Index(part, "<")
			end := strings.Index(part, ">")
			if start < 0 || end <= start+1 {
				continue
			}
			u, err := url.Parse(part[start+1 : end])
			if err != nil {
				continue
			}
			if page, err := strconv.Atoi(u.Query().Get("page")); err == nil && page > 0 {
				return page
			}
		}
	}
	return 0
}

func (d dispatcher) call(ctx context.Context, method, path string, body any) (*basecamp.Response, error) {
	switch method {
	case "GET":
		return d.api.Get(ctx, path)
	case "POST":
		return d.api.Post(ctx, path, body)
	case "PUT":
		return d.api.Put(ctx, path, body)
	case "DELETE":
		// No catalog DELETE takes a body; buildRequest already rejected
		// stray params for body-less operations.
		return d.api.Delete(ctx, path)
	default:
		return nil, fmt.Errorf("internal error: unsupported method %s", method)
	}
}

// buildRequest resolves the operation's path template and query string from
// params and gathers the remaining entries into the request body. Missing
// path parameters, stray parameters, and non-scalar path or query values are
// errors pointing at the describe action.
func buildRequest(op *catalog.Operation, params map[string]any) (string, any, error) {
	consumed := map[string]bool{}

	path := op.Path
	for _, p := range op.Params {
		if p.In != "path" {
			continue
		}
		raw, ok := params[p.Name]
		if !ok {
			return "", nil, fmt.Errorf("missing required path parameter %q for action %q (describe the action for its schema)", p.Name, op.Action)
		}
		value, err := scalarString(raw)
		if err != nil {
			return "", nil, fmt.Errorf("path parameter %q: %w", p.Name, err)
		}
		path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(value))
		consumed[p.Name] = true
	}

	query := url.Values{}
	for _, p := range op.Params {
		if p.In != "query" {
			continue
		}
		raw, ok := params[p.Name]
		if !ok {
			if p.Required {
				return "", nil, fmt.Errorf("missing required query parameter %q for action %q (describe the action for its schema)", p.Name, op.Action)
			}
			continue
		}
		// Rails-style array parameters (assignee_ids[], type_names[], ...)
		// repeat the bracketed name once per element.
		if items, isArray := raw.([]any); isArray {
			if !strings.HasSuffix(p.Name, "[]") {
				return "", nil, fmt.Errorf("query parameter %q: must be a string, number, or boolean, got %T", p.Name, raw)
			}
			for _, item := range items {
				value, err := scalarString(item)
				if err != nil {
					return "", nil, fmt.Errorf("query parameter %q: %w", p.Name, err)
				}
				query.Add(p.Name, value)
			}
		} else {
			value, err := scalarString(raw)
			if err != nil {
				return "", nil, fmt.Errorf("query parameter %q: %w", p.Name, err)
			}
			query.Set(p.Name, value)
		}
		consumed[p.Name] = true
	}

	body := map[string]any{}
	for name, value := range params {
		if consumed[name] {
			continue
		}
		if op.Body == nil {
			return "", nil, fmt.Errorf("unknown parameter %q for action %q (describe the action for its schema)", name, op.Action)
		}
		if !bodyAllows(op, name) {
			return "", nil, fmt.Errorf("unknown parameter %q for action %q (describe the action for its body schema)", name, op.Action)
		}
		body[name] = value
	}

	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	if len(body) == 0 && (op.Body == nil || !op.BodyRequired) {
		return path, nil, nil
	}
	return path, body, nil
}

// bodyAllows reports whether the operation's body schema accepts a property
// named name. A schema without declared properties passes everything
// through; otherwise unknown names are rejected unless additionalProperties
// allows them. This is a typo guard, not schema validation: types, required
// properties, and nested constraints are the API's to enforce, and its
// errors come back in-band.
func bodyAllows(op *catalog.Operation, name string) bool {
	properties, ok := op.Body["properties"].(map[string]any)
	if !ok {
		return true
	}
	if _, ok := properties[name]; ok {
		return true
	}
	if extra, present := op.Body["additionalProperties"]; present {
		allowed, isBool := extra.(bool)
		return !isBool || allowed
	}
	return false
}

// scalarString renders a JSON-decoded path or query value for the wire.
func scalarString(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case bool:
		return strconv.FormatBool(v), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("must be a string, number, or boolean, got %T", value)
	}
}
