package driver

import (
	"regexp"
	"slices"
	"strings"
)

// BaseEnv is the environment every worker process may get from the
// connector's own: what a program needs to find its home, its tools, its
// locale and its terminal, and nothing that authenticates anyone. A driver
// adds the few variables its agent needs by name; nothing is passed by
// pattern.
var BaseEnv = []string{
	"HOME", "PATH", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "LC_CTYPE",
	"TERM", "TMPDIR", "TZ",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR",
}

// BuildEnv is the environment made of the allowlisted names that lookup has,
// plus extra, which wins over a looked-up value of the same name. Its output
// is sorted, so the same inputs make the same environment.
//
// lookup is os.LookupEnv in production. A name is taken only as given: no
// prefix, no pattern, so a new variable of the host's never reaches a worker
// by resembling an allowed one.
func BuildEnv(allow []string, lookup func(string) (string, bool), extra map[string]string) []string {
	values := map[string]string{}
	for _, name := range allow {
		if name == "" || strings.ContainsAny(name, "=\x00") {
			continue
		}
		if v, ok := lookup(name); ok {
			values[name] = v
		}
	}
	for k, v := range extra {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			continue
		}
		values[k] = v
	}
	out := make([]string, 0, len(values))
	for k, v := range values {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

// EnvMap is BuildEnv's result as a map, for an MCPServer's Env.
func EnvMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}

var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	// bearerPattern is a credential-shaped run: a bearer header value or a
	// long unbroken token.
	bearerPattern = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/\-]+=*|\b[A-Za-z0-9_\-]{40,}\b`)
)

// Redact is the sink's filter for anything taken from an agent stream that is
// logged or stored: agents volunteer the logged-in account's email unprompted,
// and a tool result can carry a token. It is a backstop, not a license: the
// connector logs kinds and ids, not stream text.
func Redact(s string) string {
	s = emailPattern.ReplaceAllString(s, "[email redacted]")
	return bearerPattern.ReplaceAllString(s, "[credential redacted]")
}
