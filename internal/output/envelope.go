package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/basecamp/basecamp-cli/internal/observability"
	"github.com/basecamp/basecamp-cli/internal/presenter"
)

// Response is the success envelope for JSON output.
type Response struct {
	OK          bool           `json:"ok"`
	Data        any            `json:"data,omitempty"`
	Summary     string         `json:"summary,omitempty"`
	Notice      string         `json:"notice,omitempty"` // Informational message (e.g., truncation warning)
	Breadcrumbs []Breadcrumb   `json:"breadcrumbs,omitempty"`
	Context     map[string]any `json:"context,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
	Entity      string         `json:"-"` // Schema hint for presenter (not serialized)
}

// Breadcrumb is a suggested follow-up action.
type Breadcrumb struct {
	Action      string `json:"action"`
	Cmd         string `json:"cmd"`
	Description string `json:"description"`
}

// ErrorResponse is the error envelope for JSON output.
type ErrorResponse struct {
	OK    bool           `json:"ok"`
	Error string         `json:"error"`
	Code  string         `json:"code"`
	Hint  string         `json:"hint,omitempty"`
	Meta  map[string]any `json:"meta,omitempty"`
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

// EffectiveFormat resolves FormatAuto based on TTY detection.
func (w *Writer) EffectiveFormat() Format {
	format := w.opts.Format
	if format == FormatAuto {
		if isTTY(w.opts.Writer) {
			return FormatStyled
		}
		return FormatJSON
	}
	return format
}

// OK outputs a success response.
func (w *Writer) OK(data any, opts ...ResponseOption) error {
	resp := &Response{OK: true, Data: data}
	for _, opt := range opts {
		opt(resp)
	}
	if resp.Entity != "" {
		if err := checkZeroData(resp.Data); err != nil {
			return err
		}
	}
	return w.write(resp)
}

// Err outputs an error response.
func (w *Writer) Err(err error, opts ...ErrorResponseOption) error {
	e := AsError(err)
	resp := &ErrorResponse{
		OK:    false,
		Error: e.Message,
		Code:  e.Code,
		Hint:  e.Hint,
	}
	for _, opt := range opts {
		opt(resp)
	}
	return w.write(resp)
}

// ErrorResponseOption modifies an ErrorResponse.
type ErrorResponseOption func(*ErrorResponse)

// WithErrorStats adds session metrics to the error response metadata.
func WithErrorStats(metrics *observability.SessionMetrics) ErrorResponseOption {
	return func(r *ErrorResponse) {
		if metrics == nil {
			return
		}
		if r.Meta == nil {
			r.Meta = make(map[string]any)
		}
		r.Meta["stats"] = map[string]any{
			"requests":    metrics.TotalRequests,
			"cache_hits":  metrics.CacheHits,
			"cache_rate":  cacheRate(metrics),
			"operations":  metrics.TotalOperations,
			"failed":      metrics.FailedOps,
			"retries":     metrics.TotalRetries,
			"latency_ms":  metrics.TotalLatency.Milliseconds(),
			"duration_ms": metrics.EndTime.Sub(metrics.StartTime).Milliseconds(),
		}
	}
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
			return w.writeQuiet(resp.Data)
		}
		return w.writeQuiet(v)
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
	toEncode := v
	if resp, ok := v.(*Response); ok {
		// Avoid mutating the original Response; encode a shallow copy with normalized data.
		respCopy := *resp
		respCopy.Data = NormalizeData(resp.Data)
		toEncode = &respCopy
	}
	enc := json.NewEncoder(w.opts.Writer)
	enc.SetIndent("", "  ")
	return enc.Encode(toEncode)
}

// writeQuiet outputs data for quiet mode as JSON without the envelope.
// This preserves the JSON contract for --agent and --quiet modes.
func (w *Writer) writeQuiet(v any) error {
	return w.writeJSON(NormalizeData(v))
}

func (w *Writer) writeIDs(v any) error {
	resp, ok := v.(*Response)
	if !ok {
		return w.writeJSON(v)
	}

	// Normalize data to []map[string]any or map[string]any
	data := NormalizeData(resp.Data)

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
	data := NormalizeData(resp.Data)

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

// NormalizeData converts json.RawMessage and other types to standard Go types.
func NormalizeData(data any) any {
	// Handle json.RawMessage by unmarshaling it
	if raw, ok := data.(json.RawMessage); ok {
		var unmarshaled any
		if err := unmarshalPreservingNumbers(raw, &unmarshaled); err == nil {
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
		if err := unmarshalPreservingNumbers(b, &unmarshaled); err != nil {
			return data
		}
		return normalizeUnmarshaled(unmarshaled)
	}
}

// unmarshalPreservingNumbers decodes JSON using UseNumber so numeric values
// remain as json.Number instead of being converted to float64. This preserves
// precision for large integer IDs that exceed 53-bit float64 range.
func unmarshalPreservingNumbers(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(v)
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
	// Schema-aware presenter is opt-in: only activates when a command
	// explicitly sets WithEntity. This preserves the generic renderer as
	// default and avoids surprising users when new schemas are added.
	if resp, ok := v.(*Response); ok && resp.Entity != "" {
		if w.presentStyledEntity(resp) {
			return nil
		}
	}

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
	// Schema-aware presenter is opt-in (see writeStyled comment).
	if resp, ok := v.(*Response); ok && resp.Entity != "" {
		if w.presentMarkdownEntity(resp) {
			return nil
		}
	}

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

// WithNotice adds an informational notice to the response.
// Use this for non-error messages like truncation warnings.
func WithNotice(s string) ResponseOption {
	return func(r *Response) { r.Notice = s }
}

// TruncationNotice returns a notice string if results may be truncated.
// Returns empty string if no truncation warning is needed.
// Parameters:
//   - count: number of results returned
//   - defaultLimit: the default limit for this resource type (e.g., 100)
//   - all: whether --all flag was used
//   - explicitLimit: limit set via --limit flag (0 if not set)
func TruncationNotice(count, defaultLimit int, all bool, explicitLimit int) string {
	// No notice if --all was used (user explicitly requested everything)
	if all {
		return ""
	}

	// Determine the effective limit
	limit := defaultLimit
	if explicitLimit > 0 {
		limit = explicitLimit
	}

	// No notice if no limit was applied (defaultLimit=0 and no explicit limit)
	if limit == 0 {
		return ""
	}

	// If count equals the limit, results are likely truncated
	if count > 0 && count >= limit {
		return fmt.Sprintf("Showing %d results (use --all for complete list)", count)
	}

	return ""
}

// TruncationNoticeWithTotal returns a truncation notice when results are truncated.
// Uses totalCount from API's X-Total-Count header to show accurate counts.
// Returns empty string if no truncation or totalCount is 0 (unavailable).
func TruncationNoticeWithTotal(count, totalCount int) string {
	// No notice if total count unavailable or all results returned
	if totalCount == 0 || count >= totalCount {
		return ""
	}

	return fmt.Sprintf("Showing %d of %d results (use --all for complete list)", count, totalCount)
}

// WithBreadcrumbs adds breadcrumbs to the response.
func WithBreadcrumbs(b ...Breadcrumb) ResponseOption {
	return func(r *Response) { r.Breadcrumbs = append(r.Breadcrumbs, b...) }
}

// WithoutBreadcrumbs removes all breadcrumbs from the response.
func WithoutBreadcrumbs() ResponseOption {
	return func(r *Response) { r.Breadcrumbs = nil }
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

// WithStats adds session metrics to the response metadata.
func WithStats(metrics *observability.SessionMetrics) ResponseOption {
	return func(r *Response) {
		if metrics == nil {
			return
		}
		if r.Meta == nil {
			r.Meta = make(map[string]any)
		}
		r.Meta["stats"] = map[string]any{
			"requests":    metrics.TotalRequests,
			"cache_hits":  metrics.CacheHits,
			"cache_rate":  cacheRate(metrics),
			"operations":  metrics.TotalOperations,
			"failed":      metrics.FailedOps,
			"retries":     metrics.TotalRetries,
			"latency_ms":  metrics.TotalLatency.Milliseconds(),
			"duration_ms": metrics.EndTime.Sub(metrics.StartTime).Milliseconds(),
		}
	}
}

// WithEntity hints which schema to use for entity-aware presentation.
func WithEntity(name string) ResponseOption {
	return func(r *Response) { r.Entity = name }
}

// presentStyledEntity attempts schema-aware rendering for styled output.
// Returns true if the presenter handled it, false to fall back to generic.
func (w *Writer) presentStyledEntity(resp *Response) bool {
	data := NormalizeData(resp.Data)
	var buf strings.Builder

	if !presenter.Present(&buf, data, resp.Entity, presenter.ModeStyled) {
		return false
	}

	var out strings.Builder
	r := NewRenderer(w.opts.Writer, true)

	if resp.Summary != "" {
		out.WriteString(r.Summary.Render(resp.Summary))
		out.WriteString("\n")
	}

	if resp.Notice != "" {
		out.WriteString(r.Hint.Render(resp.Notice))
		out.WriteString("\n")
	}

	if resp.Summary != "" || resp.Notice != "" {
		out.WriteString("\n")
	}

	out.WriteString(buf.String())

	if len(resp.Breadcrumbs) > 0 {
		out.WriteString("\n")
		r.renderBreadcrumbs(&out, resp.Breadcrumbs)
	}

	if stats := extractStats(resp.Meta); stats != nil {
		out.WriteString("\n")
		r.renderStats(&out, stats)
	}

	_, _ = io.WriteString(w.opts.Writer, out.String())
	return true
}

// presentMarkdownEntity attempts schema-aware rendering for Markdown output.
// Returns true if the presenter handled it, false to fall back to generic.
func (w *Writer) presentMarkdownEntity(resp *Response) bool {
	data := NormalizeData(resp.Data)
	var buf strings.Builder

	if !presenter.Present(&buf, data, resp.Entity, presenter.ModeMarkdown) {
		return false
	}

	var out strings.Builder
	mr := NewMarkdownRenderer(w.opts.Writer)

	if resp.Summary != "" {
		out.WriteString("## " + resp.Summary + "\n")
	}

	if resp.Notice != "" {
		out.WriteString("*" + resp.Notice + "*\n")
	}

	if resp.Summary != "" || resp.Notice != "" {
		out.WriteString("\n")
	}

	out.WriteString(buf.String())

	if len(resp.Breadcrumbs) > 0 {
		out.WriteString("\n### Hints\n\n")
		for _, bc := range resp.Breadcrumbs {
			line := "- `" + bc.Cmd + "`"
			if bc.Description != "" {
				line += " — " + bc.Description
			}
			out.WriteString(line + "\n")
		}
	}

	if stats := extractStats(resp.Meta); stats != nil {
		out.WriteString("\n")
		mr.renderStats(&out, stats)
	}

	_, _ = io.WriteString(w.opts.Writer, out.String())
	return true
}

// checkZeroData returns an error when entity-tagged data is a map with every
// value at its zero value (empty string, 0, false, nil). This catches silent
// deserialization failures where the SDK returns a struct with no fields set.
func checkZeroData(data any) error {
	m, ok := toMap(data)
	if !ok {
		return nil // not a map — nothing to check
	}
	if len(m) == 0 {
		return &Error{
			Code:    "empty_response",
			Message: "API returned empty data",
			Hint:    "The response contained no fields. This may indicate a deserialization issue.",
		}
	}
	for _, v := range m {
		if !isZeroValue(v) {
			return nil // at least one non-zero field
		}
	}
	return &Error{
		Code:    "empty_response",
		Message: "API returned empty data",
		Hint:    "All fields in the response are empty. This may indicate a deserialization issue.",
	}
}

// toMap converts data to map[string]any via JSON round-trip if needed.
func toMap(data any) (map[string]any, bool) {
	if m, ok := data.(map[string]any); ok {
		return m, true
	}
	normalized := NormalizeData(data)
	m, ok := normalized.(map[string]any)
	return m, ok
}

// isZeroValue returns true for zero-value primitives: "", 0, false, nil,
// and the Go zero-time JSON sentinel "0001-01-01T00:00:00Z".
func isZeroValue(v any) bool {
	switch val := v.(type) {
	case nil:
		return true
	case string:
		return val == "" || val == "0001-01-01T00:00:00Z"
	case float64:
		return val == 0
	case json.Number:
		return val.String() == "0"
	case bool:
		return !val
	default:
		return false
	}
}

// cacheRate calculates the cache hit rate as a percentage.
func cacheRate(m *observability.SessionMetrics) float64 {
	if m.TotalRequests == 0 {
		return 0
	}
	return float64(m.CacheHits) / float64(m.TotalRequests) * 100
}
