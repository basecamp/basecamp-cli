package richtext

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestShellQuote(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"inert":          {"agent", "agent"},
		"inert symbols":  {"a-b_c.d/e:f@g%h+i=j", "a-b_c.d/e:f@g%h+i=j"},
		"empty":          {"", "''"},
		"space":          {"two words", "'two words'"},
		"semicolon":      {"a;rm -rf /", "'a;rm -rf /'"},
		"substitution":   {"$(id)", "'$(id)'"},
		"backtick":       {"`id`", "'`id`'"},
		"single quote":   {"it's", `'it'\''s'`},
		"only a quote":   {"'", `''\'''`},
		"quote and semi": {"'; id; '", `''\''; id; '\'''`},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, ShellQuote(tc.in))
		})
	}
}
