package markdown

import "github.com/charmbracelet/glamour/ansi"

// terminalStyle styles rendered Markdown with ANSI colors rather than the fixed
// 256-color palettes glamour ships, so a body picks up the reader's terminal
// theme the same way internal/tui does — and follows an Omarchy retint with it.
//
// The numbers are ANSI slots, not colors: 12 is bright blue (emphasis), 14 is
// bright cyan (links), 11 is bright yellow (code). Secondary text is Faint rather
// than bright black, for the reason tui.Styles gives: plenty of themes render
// bright black almost invisibly, while a dimmed foreground stays legible
// everywhere.
//
// The document margin is zero on purpose. A body is drawn into a column that has
// already been indented — a chat line sits right of its clock — and glamour's own
// margin would indent it twice and push the wrap past the edge.
var terminalStyle = ansi.StyleConfig{
	Document: ansi.StyleBlock{
		Margin: uintPointer(0),
	},
	// Quotes carry no color of their own: glamour styles the padding it adds out
	// to the wrap width, and a colored run of spaces cannot be trimmed away
	// afterwards.
	BlockQuote: ansi.StyleBlock{
		Indent:      uintPointer(1),
		IndentToken: stringPointer("│ "),
	},
	List: ansi.StyleList{
		LevelIndent: 2,
	},
	Heading: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			BlockSuffix: "\n",
			Color:       stringPointer("12"),
			Bold:        boolPointer(true),
		},
	},
	// No "## " prefixes: a heading in a message is prose, not a document outline,
	// and leftover hash marks read as markup that failed to render.
	H1: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{Underline: boolPointer(true)},
	},
	Strikethrough: ansi.StylePrimitive{
		CrossedOut: boolPointer(true),
	},
	Emph: ansi.StylePrimitive{
		Italic: boolPointer(true),
	},
	Strong: ansi.StylePrimitive{
		Bold: boolPointer(true),
	},
	HorizontalRule: ansi.StylePrimitive{
		Faint:  boolPointer(true),
		Format: "\n────────\n",
	},
	Item:        ansi.StylePrimitive{BlockPrefix: "• "},
	Enumeration: ansi.StylePrimitive{BlockPrefix: ". "},
	Task: ansi.StyleTask{
		Ticked:   "[✓] ",
		Unticked: "[ ] ",
	},
	Link: ansi.StylePrimitive{
		Color:     stringPointer("14"),
		Underline: boolPointer(true),
	},
	LinkText: ansi.StylePrimitive{
		Color: stringPointer("12"),
	},
	Image: ansi.StylePrimitive{
		Color:     stringPointer("14"),
		Underline: boolPointer(true),
	},
	ImageText: ansi.StylePrimitive{
		Faint:  boolPointer(true),
		Format: "Image: {{.text}} →",
	},
	Code: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Prefix: " ",
			Suffix: " ",
			Color:  stringPointer("11"),
		},
	},
	CodeBlock: ansi.StyleCodeBlock{
		StyleBlock: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Faint: boolPointer(true)},
			Margin:         uintPointer(2),
		},
	},
	Table: ansi.StyleTable{
		CenterSeparator: stringPointer("┼"),
		ColumnSeparator: stringPointer("│"),
		RowSeparator:    stringPointer("─"),
	},
	DefinitionDescription: ansi.StylePrimitive{BlockPrefix: "\n  "},
}

func stringPointer(value string) *string { return &value }

func boolPointer(value bool) *bool { return &value }

func uintPointer(value uint) *uint { return &value }
