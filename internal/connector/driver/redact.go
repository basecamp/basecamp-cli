package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// # Redaction: what leaves a worker, and what is taken out of it first
//
// Everything that crosses out of a worker toward a person or a file — an
// error a driver returns, a log line, a dispatch status line, a tool name in
// an update, the tail of the adapter's stderr — passes through one function,
// Redactor.Sanitize, before it is written anywhere. Err, Stderr and Handler
// are Sanitize applied to an error, to stderr and to a logger; nothing else
// in the connector redacts on its own.
//
// Sanitize removes, in this order:
//
//  1. Every value in Redaction.Secrets, wherever it appears: the task token
//     and the agent's credentials, named by whoever holds them.
//  2. Every value of the worker's environment and of its MCP servers'
//     environments (Redaction.Env) that BaseEnv does not name. BaseEnv is
//     the operator's home, path, locale and terminal, chosen because none of
//     it authenticates anyone; everything a driver or the dispatcher adds by
//     name (an API key, a config directory) is a value the agent was given,
//     and is taken out. Values shorter than minEnvValue are left, since a
//     one-character value would take out every letter it matches.
//  3. Every path under Redaction.Dirs — the connector's state directory,
//     which holds the ledger, and its runtime directory, which holds session
//     files and token sockets — to the end of the path, whether it is written
//     as given or with its symlinks resolved.
//  4. Email addresses: agents volunteer the signed-in account's address
//     unprompted.
//  5. Credential-shaped runs: a bearer header's value, and any unbroken run
//     of 40 or more token characters.
//
// Stderr is further never passed on verbatim: only its last line is kept,
// sanitized, stripped of control characters and cut to maxStderr bytes.
//
// A nil *Redactor still applies rules 4 and 5, so no caller is ever without
// the pattern rules.
//
// Where this can still be broken: a secret the Redactor was not told about
// and that has no credential shape (a short password, say) passes; a secret
// the agent transforms before it writes it (base64, reversed, split across
// lines) passes; and a path outside the named directories is shown as it is.
// The rule removes what the connector knows is secret; it cannot recognize a
// secret it was never shown.

// Redaction names what a Redactor takes out.
type Redaction struct {
	// Secrets are values removed wherever they appear: a task token, an
	// agent credential.
	Secrets []string
	// Env is an environment, as KEY=VALUE, whose values are removed unless
	// BaseEnv names them.
	Env []string
	// Dirs are directories any path under which is removed: the state and
	// runtime directories.
	Dirs []string
}

// With is r with more added.
func (r Redaction) With(more Redaction) Redaction {
	return Redaction{
		Secrets: append(slices.Clone(r.Secrets), more.Secrets...),
		Env:     append(slices.Clone(r.Env), more.Env...),
		Dirs:    append(slices.Clone(r.Dirs), more.Dirs...),
	}
}

// EnvOf is an MCP server's environment map as KEY=VALUE, for Redaction.Env.
func EnvOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

const (
	// minEnvValue is the shortest environment value removed by value.
	minEnvValue = 6
	// maxStderr is the most of a worker's stderr ever passed on.
	maxStderr = 300
)

const (
	redactedSecret = "[redacted]"
	redactedPath   = "[connector path]"
	redactedEmail  = "[email redacted]"
	redactedCred   = "[credential redacted]" //nolint:gosec // G101: the placeholder that replaces a credential, not one
)

var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	// bearerPattern is a credential-shaped run: a bearer header value or a
	// long unbroken token.
	bearerPattern = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/\-]+=*|\b[A-Za-z0-9_\-]{40,}\b`)
)

// Redactor applies a Redaction. Build one with NewRedactor; it is safe for
// concurrent use.
type Redactor struct {
	values *strings.Replacer
	paths  *regexp.Regexp
}

// NewRedactor compiles r.
func NewRedactor(r Redaction) *Redactor {
	seen := map[string]bool{}
	var values []string
	add := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			values = append(values, v)
		}
	}
	for _, s := range r.Secrets {
		add(s)
	}
	base := map[string]bool{}
	for _, name := range BaseEnv {
		base[name] = true
	}
	for _, kv := range r.Env {
		name, value, ok := strings.Cut(kv, "=")
		if ok && !base[name] && len(value) >= minEnvValue {
			add(value)
		}
	}
	// Longest first, so a value that contains another is removed whole.
	slices.SortFunc(values, func(a, b string) int { return len(b) - len(a) })
	pairs := make([]string, 0, 2*len(values))
	for _, v := range values {
		pairs = append(pairs, v, redactedSecret)
	}

	var dirs []string
	for _, d := range r.Dirs {
		if d == "" {
			continue
		}
		d = filepath.Clean(d)
		dirs = append(dirs, d)
		if resolved, err := filepath.EvalSymlinks(d); err == nil && resolved != d {
			dirs = append(dirs, resolved)
		}
	}
	slices.SortFunc(dirs, func(a, b string) int { return len(b) - len(a) })
	var paths *regexp.Regexp
	if len(dirs) > 0 {
		alternatives := make([]string, len(dirs))
		for i, d := range dirs {
			alternatives[i] = regexp.QuoteMeta(d)
		}
		// The directory, and the rest of the path up to the first character
		// that ends a path in a message: a space, a quote, a bracket, or the
		// punctuation an error puts after a file name.
		paths = regexp.MustCompile(`(?:` + strings.Join(alternatives, "|") + `)(?:/[^\s"'` + "`" + `)\]:;,]*)?`)
	}
	return &Redactor{values: strings.NewReplacer(pairs...), paths: paths}
}

// Sanitize is the one function every text crossing out of a worker passes
// through. See the rule above.
func (r *Redactor) Sanitize(s string) string {
	if r != nil {
		s = r.values.Replace(s)
		if r.paths != nil {
			s = r.paths.ReplaceAllString(s, redactedPath)
		}
	}
	s = emailPattern.ReplaceAllString(s, redactedEmail)
	return bearerPattern.ReplaceAllString(s, redactedCred)
}

// Stderr is what may be passed on of a worker's stderr: its last non-empty
// line, sanitized, on one line, and no longer than maxStderr bytes.
func (r *Redactor) Stderr(text string) string {
	text = strings.TrimRightFunc(text, unicode.IsSpace)
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		text = text[i+1:]
	}
	text = r.Sanitize(text)
	text = strings.Map(func(c rune) rune {
		if unicode.IsControl(c) {
			return ' '
		}
		return c
	}, text)
	if len(text) > maxStderr {
		text = strings.ToValidUTF8(text[len(text)-maxStderr:], "")
	}
	return text
}

// Err is err with its message sanitized. errors.Is still answers for every
// error err wraps, and errors.As for a *StartError, whose own error is
// sanitized in turn; nothing else of the original chain is reachable, so no
// wrapped message can carry a secret past it.
func (r *Redactor) Err(err error) error {
	if err == nil {
		return nil
	}
	var already *redactedError
	if errors.As(err, &already) && already.by == r {
		return err
	}
	return &redactedError{msg: r.Sanitize(err.Error()), orig: err, by: r}
}

type redactedError struct {
	msg  string
	orig error
	by   *Redactor
}

func (e *redactedError) Error() string { return e.msg }

func (e *redactedError) Is(target error) bool { return errors.Is(e.orig, target) }

func (e *redactedError) As(target any) bool {
	switch t := target.(type) {
	case **StartError:
		var started *StartError
		if !errors.As(e.orig, &started) {
			return false
		}
		*t = &StartError{Process: started.Process, Err: e.by.Err(started.Err)}
		return true
	case **redactedError:
		*t = e
		return true
	}
	return false
}

// Format keeps %+v and %#v from reaching the original error.
func (e *redactedError) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(e.msg)) }

// Handler is h with every message and attribute sanitized. A string, an
// error or any value that is not a number, a boolean, a time or a duration
// is written as its sanitized text.
func (r *Redactor) Handler(h slog.Handler) slog.Handler {
	return &redactingHandler{next: h, r: r}
}

type redactingHandler struct {
	next slog.Handler
	r    *Redactor
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, h.r.Sanitize(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = h.attr(a)
	}
	return &redactingHandler{next: h.next.WithAttrs(clean), r: h.r}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{next: h.next.WithGroup(name), r: h.r}
}

func (h *redactingHandler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindInt64, slog.KindUint64, slog.KindFloat64, slog.KindBool, slog.KindTime, slog.KindDuration:
		return slog.Attr{Key: a.Key, Value: v}
	case slog.KindGroup:
		group := v.Group()
		clean := make([]slog.Attr, len(group))
		for i, g := range group {
			clean[i] = h.attr(g)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(clean...)}
	case slog.KindString:
		return slog.String(a.Key, h.r.Sanitize(v.String()))
	default:
		return slog.String(a.Key, h.r.Sanitize(fmt.Sprint(v.Any())))
	}
}
