package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Response is the success envelope for JSON output.
type Response struct {
	OK          bool           `json:"ok"`
	Data        any            `json:"data,omitempty"`
	Summary     string         `json:"summary,omitempty"`
	Breadcrumbs []Breadcrumb   `json:"breadcrumbs,omitempty"`
	Context     map[string]any `json:"context,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
}

// Breadcrumb is a suggested follow-up action.
type Breadcrumb struct {
	Action      string `json:"action"`
	Cmd         string `json:"cmd"`
	Description string `json:"description"`
}

// ErrorResponse is the error envelope for JSON output.
type ErrorResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	Code  string `json:"code"`
	Hint  string `json:"hint,omitempty"`
}

// Format specifies the output format.
type Format int

const (
	FormatAuto Format = iota // Auto-detect: TTY → Styled, non-TTY → JSON
	FormatJSON
	FormatMarkdown // Literal Markdown syntax (portable, pipeable)
	FormatStyled   // ANSI styled output (forced, even when piped)
	FormatQuiet
	FormatIDs
	FormatCount
)

// Options controls output behavior.
type Options struct {
	Format  Format
	Writer  io.Writer
	Verbose bool
}

// DefaultOptions returns options for standard output.
func DefaultOptions() Options {
	return Options{
		Format: FormatAuto,
		Writer: os.Stdout,
	}
}

// Writer handles all output formatting.
type Writer struct {
	opts Options
}

// New creates a new output writer.
func New(opts Options) *Writer {
	if opts.Writer == nil {
		opts.Writer = os.Stdout
	}
	return &Writer{opts: opts}
}

// OK outputs a success response.
func (w *Writer) OK(data any, opts ...ResponseOption) error {
	resp := &Response{OK: true, Data: data}
	for _, opt := range opts {
		opt(resp)
	}
	return w.write(resp)
}

// Err outputs an error response.
func (w *Writer) Err(err error) error {
	e := AsError(err)
	resp := &ErrorResponse{
		OK:    false,
		Error: e.Message,
		Code:  e.Code,
		Hint:  e.Hint,
	}
	return w.write(resp)
}

func (w *Writer) write(v any) error {
	format := w.opts.Format

	// Auto-detect format: TTY → Styled, non-TTY → JSON
	if format == FormatAuto {
		if isTTY(w.opts.Writer) {
			format = FormatStyled
		} else {
			format = FormatJSON
		}
	}

	switch format {
	case FormatQuiet:
		// Extract just the data field for quiet mode
		if resp, ok := v.(*Response); ok {
			return w.writeJSON(resp.Data)
		}
		return w.writeJSON(v)
	case FormatIDs:
		return w.writeIDs(v)
	case FormatCount:
		return w.writeCount(v)
	case FormatMarkdown:
		return w.writeLiteralMarkdown(v)
	case FormatStyled:
		return w.writeStyled(v)
	default:
		return w.writeJSON(v)
	}
}

// isTTY checks if the writer is a terminal.
func isTTY(w io.Writer) bool {
	if f, ok := w.(*os.File); ok {
		fi, err := f.Stat()
		if err != nil {
			return false
		}
		return (fi.Mode() & os.ModeCharDevice) != 0
	}
	return false
}

func (w *Writer) writeJSON(v any) error {
	enc := json.NewEncoder(w.opts.Writer)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (w *Writer) writeIDs(v any) error {
	resp, ok := v.(*Response)
	if !ok {
		return w.writeJSON(v)
	}

	// Normalize data to []map[string]any or map[string]any
	data := normalizeData(resp.Data)

	// Handle slice of objects with ID field
	switch d := data.(type) {
	case []map[string]any:
		for _, item := range d {
			if id, ok := item["id"]; ok {
				fmt.Fprintln(w.opts.Writer, id)
			}
		}
	case map[string]any:
		if id, ok := d["id"]; ok {
			fmt.Fprintln(w.opts.Writer, id)
		}
	}
	return nil
}

func (w *Writer) writeCount(v any) error {
	resp, ok := v.(*Response)
	if !ok {
		return w.writeJSON(v)
	}

	// Normalize data to a standard type
	data := normalizeData(resp.Data)

	switch d := data.(type) {
	case []any:
		fmt.Fprintln(w.opts.Writer, len(d))
	case []map[string]any:
		fmt.Fprintln(w.opts.Writer, len(d))
	default:
		fmt.Fprintln(w.opts.Writer, 1)
	}
	return nil
}

// normalizeData converts json.RawMessage and other types to standard Go types.
func normalizeData(data any) any {
	// Handle json.RawMessage by unmarshaling it
	if raw, ok := data.(json.RawMessage); ok {
		var unmarshaled any
		if err := json.Unmarshal(raw, &unmarshaled); err == nil {
			return normalizeUnmarshaled(unmarshaled)
		}
		return data
	}

	// Handle typed structs/slices by marshaling then unmarshaling
	// This converts struct types to map[string]any
	switch data.(type) {
	case []map[string]any, map[string]any, []any:
		return data // Already normalized
	case nil:
		return data
	default:
		// Try to convert via JSON round-trip
		b, err := json.Marshal(data)
		if err != nil {
			return data
		}
		var unmarshaled any
		if err := json.Unmarshal(b, &unmarshaled); err != nil {
			return data
		}
		return normalizeUnmarshaled(unmarshaled)
	}
}

// normalizeUnmarshaled converts []any to []map[string]any if all elements are maps.
func normalizeUnmarshaled(v any) any {
	switch d := v.(type) {
	case []any:
		// Check if all elements are maps, convert to []map[string]any
		if len(d) == 0 {
			return []map[string]any{}
		}
		maps := make([]map[string]any, 0, len(d))
		for _, item := range d {
			if m, ok := item.(map[string]any); ok {
				maps = append(maps, m)
			} else {
				return v // Mixed types, return as-is
			}
		}
		return maps
	default:
		return v
	}
}

// writeStyled outputs ANSI styled terminal output.
func (w *Writer) writeStyled(v any) error {
	r := NewRenderer(w.opts.Writer, true) // Force styled
	switch resp := v.(type) {
	case *Response:
		return r.RenderResponse(w.opts.Writer, resp)
	case *ErrorResponse:
		return r.RenderError(w.opts.Writer, resp)
	default:
		return w.writeJSON(v)
	}
}

// writeLiteralMarkdown outputs literal Markdown syntax (portable, pipeable).
func (w *Writer) writeLiteralMarkdown(v any) error {
	r := NewMarkdownRenderer(w.opts.Writer)
	switch resp := v.(type) {
	case *Response:
		return r.RenderResponse(w.opts.Writer, resp)
	case *ErrorResponse:
		return r.RenderError(w.opts.Writer, resp)
	default:
		return w.writeJSON(v)
	}
}

// ResponseOption modifies a Response.
type ResponseOption func(*Response)

// WithSummary adds a summary to the response.
func WithSummary(s string) ResponseOption {
	return func(r *Response) { r.Summary = s }
}

// WithBreadcrumbs adds breadcrumbs to the response.
func WithBreadcrumbs(b ...Breadcrumb) ResponseOption {
	return func(r *Response) { r.Breadcrumbs = append(r.Breadcrumbs, b...) }
}

// WithContext adds context to the response.
func WithContext(key string, value any) ResponseOption {
	return func(r *Response) {
		if r.Context == nil {
			r.Context = make(map[string]any)
		}
		r.Context[key] = value
	}
}

// WithMeta adds metadata to the response.
func WithMeta(key string, value any) ResponseOption {
	return func(r *Response) {
		if r.Meta == nil {
			r.Meta = make(map[string]any)
		}
		r.Meta[key] = value
	}
}
