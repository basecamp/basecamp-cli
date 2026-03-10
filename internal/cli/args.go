package cli

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// ArgInfo describes a declared positional argument parsed from a command's Use: string.
type ArgInfo struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Variadic    bool   `json:"variadic,omitempty"`
	Description string `json:"description"`
	Kind        string `json:"kind"`
}

// argPattern matches bracket-delimited tokens: <required>, [optional], with optional trailing ...
var argPattern = regexp.MustCompile(`[<\[]([^>\]]+)[>\]](\.\.\.)?`)

// ParseArgs extracts structured arg metadata from a cobra command.
// Returns the annotation override if arg_schema is set.
// Returns nil if the command is not runnable (pure group command).
// Otherwise parses the Use: string after stripping [flags] and [action].
func ParseArgs(cmd *cobra.Command) []ArgInfo {
	if !cmd.Runnable() {
		return nil
	}

	// Annotation override: return parsed JSON if present.
	if schema, ok := cmd.Annotations["arg_schema"]; ok && schema != "" {
		var args []ArgInfo
		if err := json.Unmarshal([]byte(schema), &args); err == nil {
			return args
		}
	}

	use := cmd.Use

	// Strip [flags] — cobra convention, never a real arg.
	use = strings.ReplaceAll(use, " [flags]", "")

	// Strip [action] — subcommand placeholder convention.
	use = strings.ReplaceAll(use, " [action]", "")

	// Drop the command name (first token) to isolate the arg tokens.
	if idx := strings.IndexByte(use, ' '); idx >= 0 {
		use = use[idx+1:]
	} else {
		// No args after command name.
		return nil
	}

	matches := argPattern.FindAllStringSubmatch(use, -1)
	if len(matches) == 0 {
		return nil
	}

	args := make([]ArgInfo, 0, len(matches))
	for _, m := range matches {
		name := m[1]
		// Determine required vs optional from the original bracket type.
		// Required args use <>, optional use [].
		full := m[0]
		required := full[0] == '<'
		variadic := m[2] == "..."

		args = append(args, ArgInfo{
			Name:        name,
			Required:    required,
			Variadic:    variadic,
			Description: humanizeArgName(name),
			Kind:        argKind(name),
		})
	}
	return args
}

// humanizeArgName converts a Use: token name into a human-readable description.
// Rules: | → " or ", -/_ → space, title case with smart casing for id→ID, url→URL.
func humanizeArgName(name string) string {
	// Split on | first
	parts := strings.Split(name, "|")
	for i, p := range parts {
		parts[i] = humanizeToken(p)
	}
	return strings.Join(parts, " or ")
}

// humanizeToken converts a single dash/underscore-separated token to title case.
func humanizeToken(s string) string {
	// Replace - and _ with space
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.ReplaceAll(s, "_", " ")

	words := strings.Fields(s)
	for i, w := range words {
		lower := strings.ToLower(w)
		switch lower {
		case "id":
			words[i] = "ID"
		case "url":
			words[i] = "URL"
		default:
			// Title case: capitalize first letter
			if len(w) > 0 {
				words[i] = strings.ToUpper(w[:1]) + w[1:]
			}
		}
	}
	return strings.Join(words, " ")
}

// argKind derives a kind string from the arg name by matching whole tokens
// after splitting on |, -, and _. This avoids false positives like "video"
// matching "id".
func argKind(name string) string {
	// Split into tokens on delimiters
	tokens := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r == '|' || r == '-' || r == '_'
	})
	for _, tok := range tokens {
		if tok == "id" || tok == "url" {
			return "identifier"
		}
	}
	for _, tok := range tokens {
		if tok == "date" {
			return "date"
		}
	}
	return "text"
}

// ArgDisplay reconstructs bracket notation for display: <content>, [body], <id|url>...
func ArgDisplay(a ArgInfo) string {
	var b strings.Builder
	if a.Required {
		b.WriteByte('<')
	} else {
		b.WriteByte('[')
	}
	b.WriteString(a.Name)
	if a.Required {
		b.WriteByte('>')
	} else {
		b.WriteByte(']')
	}
	if a.Variadic {
		b.WriteString("...")
	}
	return b.String()
}
