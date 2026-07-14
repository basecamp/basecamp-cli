package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/itchyny/gojq"

	clioutput "github.com/basecamp/cli/output"

	"github.com/basecamp/basecamp-cli/internal/observability"
	"github.com/basecamp/basecamp-cli/internal/presenter"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// NormalizeData converts json.RawMessage and other types to standard Go types.
func NormalizeData(data any) any { return clioutput.NormalizeData(data) }

// TruncationNotice returns a notice string if results may be truncated.
func TruncationNotice(count, defaultLimit int, all bool, explicitLimit int) string {
	return clioutput.TruncationNotice(count, defaultLimit, all, explicitLimit)
}

// TruncationNoticeWithTotal returns a truncation notice using totalCount from the API.
func TruncationNoticeWithTotal(count, totalCount int) string {
	return clioutput.TruncationNoticeWithTotal(count, totalCount)
}

// Response is the success envelope for JSON output.
type Response struct {
	OK               bool                      `json:"ok"`
	Data             any                       `json:"data,omitempty"`
	Summary          string                    `json:"summary,omitempty"`
	Notice           string                    `json:"notice,omitempty"` // Informational message (e.g., truncation warning)
	Breadcrumbs      []Breadcrumb              `json:"breadcrumbs,omitempty"`
	Context          map[string]any            `json:"context,omitempty"`
	Meta             map[string]any            `json:"meta,omitempty"`
	Entity           string                    `json:"-"` // Schema hint for presenter (not serialized)
	DisplayData      any                       `json:"-"` // Alternate data for styled/markdown rendering (not serialized)
	presenterOpts    []presenter.PresentOption // Display options for presenter (not serialized)
	noticeDiagnostic bool                      // when true, emit Notice to stderr in quiet mode
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
	Format    Format
	Writer    io.Writer
	ErrWriter io.Writer // Diagnostic output (notices in quiet mode); defaults to os.Stderr.
	Verbose   bool
	JQFilter  string // jq expression to apply to JSON output (built-in via gojq)
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
	jq   *gojq.Code // compiled jq filter, nil when JQFilter is empty
}

// New creates a new output writer.
// If JQFilter is set, the jq expression is parsed and compiled eagerly so
// errors surface immediately rather than on the first write.
func New(opts Options) *Writer {
	if opts.Writer == nil {
		opts.Writer = os.Stdout
	}
	if opts.ErrWriter == nil {
		opts.ErrWriter = os.Stderr
	}
	w := &Writer{opts: opts}
	if opts.JQFilter != "" {
		q, err := gojq.Parse(opts.JQFilter)
		if err == nil {
			code, err := gojq.Compile(q, gojq.WithEnvironLoader(os.Environ))
			if err == nil {
				w.jq = code
			}
		}
		// Best-effort: invalid expressions are caught earlier in PersistentPreRunE;
		// this avoids re-parsing on every write. If compilation fails here (e.g.
		// fallback writer built without early validation), writeJQ re-parses and
		// returns the error on first use.
	}
	return w
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
	if requestID := RequestID(err); requestID != "" {
		if resp.Meta == nil {
			resp.Meta = make(map[string]any)
		}
		resp.Meta["request_id"] = requestID
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
	// In quiet mode (--agent/--quiet), surface diagnostic notices on stderr so
	// automation consumers can detect degraded operations (e.g. unresolved
	// mentions). Only notices marked as diagnostic emit here — informational
	// notices like truncation warnings stay silent. This runs before the --jq
	// early-return so that --agent --jq still emits the diagnostic.
	if w.opts.Format == FormatQuiet {
		if resp, ok := v.(*Response); ok && resp.noticeDiagnostic && resp.Notice != "" {
			// The notice may interpolate API-controlled strings and stderr is
			// a terminal sink; sanitize and keep the diagnostic to one line.
			// Sanitize first, then gate: an all-escape notice collapses to ""
			// and must not emit a blank "notice: " line.
			if notice := sanitizeText(resp.Notice, true, false); notice != "" {
				fmt.Fprintf(w.opts.ErrWriter, "notice: %s\n", notice)
			}
		}
	}

	// --jq flag: serialize to JSON, apply the jq filter, print results
	if w.opts.JQFilter != "" {
		return w.writeJQ(v)
	}

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

// writeJQ serializes to JSON, applies a jq filter via gojq, and writes results.
// When the output format is FormatQuiet (--agent/--quiet), the filter runs on
// the data-only payload; otherwise it runs on the full JSON envelope.
func (w *Writer) writeJQ(v any) error {
	code := w.jq
	if code == nil {
		// Fallback: parse+compile on demand (covers edge case where New failed silently)
		q, err := gojq.Parse(w.opts.JQFilter)
		if err != nil {
			return ErrJQValidation(err)
		}
		code, err = gojq.Compile(q, gojq.WithEnvironLoader(os.Environ))
		if err != nil {
			return ErrJQValidation(err)
		}
	}

	// Determine what to feed the jq filter based on output format.
	// Quiet/agent modes strip the envelope; the jq input should match.
	target := v
	if resp, ok := v.(*Response); ok {
		respCopy := *resp
		respCopy.Data = NormalizeData(resp.Data)

		if w.opts.Format == FormatQuiet {
			target = respCopy.Data
		} else {
			target = &respCopy
		}
	}

	// Serialize to JSON then back to interface{} so gojq gets plain types
	raw, err := json.Marshal(target)
	if err != nil {
		return ErrJQRuntime(fmt.Errorf("marshal: %w", err))
	}
	var input any
	if err := json.Unmarshal(raw, &input); err != nil {
		return ErrJQRuntime(fmt.Errorf("unmarshal: %w", err))
	}

	// Sanitization is TTY-gated: --jq to a terminal strips escape sequences
	// and control characters to prevent terminal injection, while piped or
	// redirected output is the machine-consumption contract (matching
	// writeJSON) and passes bytes through verbatim.
	tty := isTTY(w.opts.Writer)

	iter := code.Run(input)
	for {
		result, ok := iter.Next()
		if !ok {
			break
		}
		if err, ok := result.(error); ok {
			return ErrJQRuntime(err)
		}
		// Print strings without JSON encoding for cleaner output.
		// On a TTY, SanitizeTerminal guards against terminal injection: --jq
		// can select API-controlled strings, and unlike JSON marshaling (which
		// escapes control bytes) this path would otherwise emit raw OSC/CSI
		// sequences.
		if s, ok := result.(string); ok {
			if tty {
				s = richtext.SanitizeTerminal(s)
			}
			fmt.Fprintln(w.opts.Writer, s)
		} else {
			// Compound results (arrays/objects) are marshaled, but Go's JSON
			// encoder only escapes C0 controls — UTF-8-encoded C1 controls
			// (U+0080–U+009F) pass through raw and execute on C1-honoring
			// terminals. On a TTY, sanitize every string leaf (and key) first.
			if tty {
				result = sanitizeJSONValue(result)
			}
			raw, err := json.Marshal(result)
			if err != nil {
				return ErrJQRuntime(fmt.Errorf("result not serializable: %w", err))
			}
			fmt.Fprintln(w.opts.Writer, string(raw))
		}
	}
	return nil
}

// sanitizeJSONValue recursively strips terminal escape sequences and control
// characters from every string in a jq result — map keys, map values, and
// slice elements — before it is marshaled for a TTY-bound --jq sink.
// Non-string leaves (numbers, bools, nil, and gojq's numeric types) pass
// through unchanged.
func sanitizeJSONValue(v any) any {
	switch val := v.(type) {
	case string:
		return richtext.SanitizeTerminal(val)
	case []any:
		for i, elem := range val {
			val[i] = sanitizeJSONValue(elem)
		}
		return val
	case map[string]any:
		return sanitizeJSONMap(val)
	default:
		return v
	}
}

// sanitizeJSONMap rebuilds a map with terminal-safe keys without ever
// dropping an entry. Stripping keys would let a hostile "\x1b[31mtitle"
// collapse onto a legitimate "title" and silently replace or hide that
// field. Instead, keys already free of escapes and controls keep their name
// — a hostile key can never displace one — and keys changed by sanitization
// are visibly escaped (strconv.Quote), re-quoting with delimiters until the
// name is unique so even a literal key that mimics escape notation cannot
// be overwritten. Escaped keys are processed in sorted order so the
// resulting names are deterministic.
func sanitizeJSONMap(val map[string]any) map[string]any {
	out := make(map[string]any, len(val))
	var escaped []string
	for k, elem := range val {
		if richtext.SanitizeTerminal(k) == k {
			out[k] = sanitizeJSONValue(elem)
		} else {
			escaped = append(escaped, k)
		}
	}
	sort.Strings(escaped)
	for _, k := range escaped {
		quoted := strconv.Quote(k)
		name := quoted[1 : len(quoted)-1]
		for {
			if _, taken := out[name]; !taken {
				break
			}
			name = strconv.Quote(name)
		}
		out[name] = sanitizeJSONValue(val[k])
	}
	return out
}

// isTTY checks if the writer is a terminal. It is a package variable so tests
// can simulate a TTY for in-memory writers.
var isTTY = func(w io.Writer) bool {
	if f, ok := w.(*os.File); ok {
		fi, err := f.Stat()
		if err != nil {
			return false
		}
		return (fi.Mode() & os.ModeCharDevice) != 0
	}
	return false
}

// writeJSON emits the full envelope as JSON. Sanitization is TTY-gated,
// matching writeJQ: when the target is a pipe or file, JSON is the
// machine-consumption contract and every byte passes through verbatim so
// data fidelity is preserved for programmatic consumers (FormatAuto only
// selects JSON when stdout is not a TTY, so this is the common case). When
// the target is a terminal — forced --json/--agent on an interactive TTY —
// string leaves are C1/escape-sanitized (via sanitizeJSONValue) so raw C1
// controls cannot execute on a C1-honoring terminal. Other terminal-facing
// sinks (styled, markdown, --ids, and --jq on a TTY) sanitize the same way.
func (w *Writer) writeJSON(v any) error {
	toEncode := v
	if resp, ok := v.(*Response); ok {
		// Avoid mutating the original Response; encode a shallow copy with normalized data.
		respCopy := *resp
		respCopy.Data = NormalizeData(resp.Data)
		toEncode = &respCopy
	}
	// On a TTY, sanitize string leaves to strip C1/escape controls: Go's JSON
	// encoder escapes C0 controls but passes UTF-8-encoded C1 controls
	// (U+0080–U+009F) through raw. Piped/redirected output stays verbatim.
	if isTTY(w.opts.Writer) {
		raw, err := json.Marshal(toEncode)
		if err != nil {
			return err
		}
		// Decode with UseNumber so JSON numbers stay json.Number (a named
		// string type) instead of float64. Large Basecamp IDs (>2^53) would
		// otherwise lose precision or render in exponent form. json.Number
		// passes through sanitizeJSONValue untouched (its `case string:` does
		// not match a named type), so the exact numeric text is preserved.
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var decoded any
		if err := dec.Decode(&decoded); err != nil {
			return err
		}
		enc := json.NewEncoder(w.opts.Writer)
		enc.SetIndent("", "  ")
		return enc.Encode(sanitizeJSONValue(decoded))
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
				fmt.Fprintln(w.opts.Writer, stripIfString(id))
			}
		}
	case map[string]any:
		if id, ok := d["id"]; ok {
			fmt.Fprintln(w.opts.Writer, stripIfString(id))
		}
	}
	return nil
}

// stripIfString makes a string value safe for the line-oriented IDs sink:
// escape sequences and control characters are stripped, and remaining
// whitespace (including the newlines and tabs SanitizeTerminal preserves)
// collapses to single spaces so one value cannot span or inject extra
// lines. Non-string values (json.Number ids, etc.) pass through unchanged.
func stripIfString(v any) any {
	if s, ok := v.(string); ok {
		return sanitizeText(s, true, false)
	}
	return v
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
// The value is stored verbatim so machine (JSON) output preserves the
// original content; terminal sinks (styled/markdown renderers, quiet-mode
// stderr diagnostics) sanitize at render time.
func WithSummary(s string) ResponseOption {
	return func(r *Response) { r.Summary = s }
}

// WithNotice adds an informational notice to the response.
// Use this for non-error messages like truncation warnings.
// Like WithSummary, the value is stored verbatim; terminal sinks sanitize.
func WithNotice(s string) ResponseOption {
	return func(r *Response) { r.Notice = s; r.noticeDiagnostic = false }
}

// WithDiagnostic sets a notice that is also emitted to stderr in quiet mode.
// Use this for degraded-operation warnings (e.g. unresolved mentions) that
// automation consumers need to detect. Truncation and other informational
// notices should use WithNotice instead.
func WithDiagnostic(s string) ResponseOption {
	return func(r *Response) { r.Notice = s; r.noticeDiagnostic = true }
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

// WithDisplayData provides alternate data for styled/markdown rendering.
// When set, the presenter uses this instead of Data, keeping Data untouched
// for JSON serialization. Use this when the response wrapper struct should be
// preserved for machine consumption but a different shape (e.g. an unwrapped
// slice) is better for human-oriented output.
func WithDisplayData(data any) ResponseOption {
	return func(r *Response) { r.DisplayData = data }
}

// WithGroupBy overrides the schema's default group_by field for task list rendering.
// For example, WithGroupBy("due_on") groups todos by due date instead of project.
func WithGroupBy(field string) ResponseOption {
	return func(r *Response) {
		r.presenterOpts = append(r.presenterOpts, presenter.WithGroupBy(field))
	}
}

// presentStyledEntity attempts schema-aware rendering for styled output.
// Returns true if the presenter handled it, false to fall back to generic.
func (w *Writer) presentStyledEntity(resp *Response) bool {
	src := resp.Data
	if resp.DisplayData != nil {
		src = resp.DisplayData
	}
	data := NormalizeData(src)
	var buf strings.Builder

	if !presenter.Present(&buf, data, resp.Entity, presenter.ModeStyled, resp.presenterOpts...) {
		return false
	}

	var out strings.Builder
	r := NewRenderer(w.opts.Writer, true)

	// sanitizeText (single-line) defends against terminal injection from
	// API-controlled summary/notice content and keeps each value on one line.
	// Sanitization happens only here at the render sink — machine (JSON)
	// output carries these verbatim.
	// Sanitize first, then gate: an all-escape summary/notice collapses to ""
	// and must emit no blank styled line or trailing spacer.
	summary := sanitizeText(resp.Summary, true, false)
	if summary != "" {
		out.WriteString(r.Summary.Render(summary))
		out.WriteString("\n")
	}

	notice := sanitizeText(resp.Notice, true, false)
	if notice != "" {
		out.WriteString(r.Hint.Render(notice))
		out.WriteString("\n")
	}

	if summary != "" || notice != "" {
		out.WriteString("\n")
	}

	out.WriteString(buf.String())

	// Comments live on resp.Data, not on DisplayData (which may be set for
	// chat_line etc.). The presenter only renders fields declared in YAML
	// schemas, so comments must be appended separately.
	if commentData, ok := NormalizeData(resp.Data).(map[string]any); ok {
		if comments := topLevelComments(commentData); len(comments) > 0 {
			out.WriteString("\n")
			out.WriteString(r.Header.Render("Comments:"))
			out.WriteString("\n")
			r.renderCommentsSection(&out, comments)
		}
	}

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
	src := resp.Data
	if resp.DisplayData != nil {
		src = resp.DisplayData
	}
	data := NormalizeData(src)
	var buf strings.Builder

	if !presenter.Present(&buf, data, resp.Entity, presenter.ModeMarkdown, resp.presenterOpts...) {
		return false
	}

	var out strings.Builder
	mr := NewMarkdownRenderer(w.opts.Writer)

	// Sink-level ANSI stripping (see presentStyledEntity). Sanitize first, then
	// gate: an all-escape summary/notice collapses to "" and must not emit an
	// empty "## " heading or "*...*" line.
	summary := sanitizeText(resp.Summary, true, false)
	if summary != "" {
		out.WriteString("## " + summary + "\n")
	}

	notice := sanitizeText(resp.Notice, true, false)
	if notice != "" {
		out.WriteString("*" + notice + "*\n")
	}

	if summary != "" || notice != "" {
		out.WriteString("\n")
	}

	out.WriteString(buf.String())

	// Comments live on resp.Data (see styled presenter comment above).
	if commentData, ok := NormalizeData(resp.Data).(map[string]any); ok {
		if comments := topLevelComments(commentData); len(comments) > 0 {
			out.WriteString("\n## Comments\n\n")
			mr.renderCommentsSection(&out, comments)
		}
	}

	if len(resp.Breadcrumbs) > 0 {
		out.WriteString("\n### Hints\n\n")
		for _, bc := range resp.Breadcrumbs {
			line := "- `" + sanitizeText(bc.Cmd, true, false) + "`"
			if bc.Description != "" {
				line += " — " + sanitizeText(bc.Description, true, false)
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
