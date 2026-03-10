package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		use      string
		runnable bool
		want     []ArgInfo
	}{
		{"list", true, nil},
		{"todo <content>", true, []ArgInfo{
			{Name: "content", Required: true, Description: "Content", Kind: "text"},
		}},
		{"complete <id|url>...", true, []ArgInfo{
			{Name: "id|url", Required: true, Variadic: true, Description: "ID or URL", Kind: "identifier"},
		}},
		{"show [type] <id|url>", true, []ArgInfo{
			{Name: "type", Required: false, Description: "Type", Kind: "text"},
			{Name: "id|url", Required: true, Description: "ID or URL", Kind: "identifier"},
		}},
		{"card <title> [body]", true, []ArgInfo{
			{Name: "title", Required: true, Description: "Title", Kind: "text"},
			{Name: "body", Required: false, Description: "Body", Kind: "text"},
		}},
		{"add <person-id>...", true, []ArgInfo{
			{Name: "person-id", Required: true, Variadic: true, Description: "Person ID", Kind: "identifier"},
		}},
		{"create <url> [flags]", true, []ArgInfo{
			{Name: "url", Required: true, Description: "URL", Kind: "identifier"},
		}},
		{"people [action]", true, nil},
		{"schedule [action]", true, nil},
		{"completion [shell]", true, []ArgInfo{
			{Name: "shell", Required: false, Description: "Shell", Kind: "text"},
		}},
		{"timeline [me]", true, []ArgInfo{
			{Name: "me", Required: false, Description: "Me", Kind: "text"},
		}},
		{"group [action]", false, nil},
	}

	for _, tt := range tests {
		t.Run(tt.use, func(t *testing.T) {
			cmd := &cobra.Command{Use: tt.use}
			if tt.runnable {
				cmd.RunE = func(*cobra.Command, []string) error { return nil }
			}
			got := ParseArgs(cmd)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseArgsAnnotationOverride(t *testing.T) {
	cmd := &cobra.Command{
		Use: "dispatch [action]",
		Annotations: map[string]string{
			"arg_schema": `[{"name":"id","required":true,"description":"Record ID","kind":"identifier"}]`,
		},
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	got := ParseArgs(cmd)
	require.Len(t, got, 1)
	assert.Equal(t, "id", got[0].Name)
	assert.True(t, got[0].Required)
	assert.Equal(t, "Record ID", got[0].Description)
	assert.Equal(t, "identifier", got[0].Kind)
}

func TestHumanizeArgName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"content", "Content"},
		{"id|url", "ID or URL"},
		{"person-id", "Person ID"},
		{"boost-id|url", "Boost ID or URL"},
		{"todo_id", "Todo ID"},
		{"start_date", "Start Date"},
		{"query", "Query"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, humanizeArgName(tt.input))
		})
	}
}

func TestArgKind(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"id|url", "identifier"},
		{"person-id", "identifier"},
		{"url", "identifier"},
		{"content", "text"},
		{"title", "text"},
		{"date", "date"},
		{"start_date", "date"},
		{"shell", "text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, argKind(tt.name))
		})
	}
}

func TestArgDisplay(t *testing.T) {
	tests := []struct {
		arg  ArgInfo
		want string
	}{
		{ArgInfo{Name: "content", Required: true}, "<content>"},
		{ArgInfo{Name: "body", Required: false}, "[body]"},
		{ArgInfo{Name: "id|url", Required: true, Variadic: true}, "<id|url>..."},
		{ArgInfo{Name: "shell", Required: false}, "[shell]"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, ArgDisplay(tt.arg))
		})
	}
}
