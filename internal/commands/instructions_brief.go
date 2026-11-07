package commands

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// instructionsBriefMaxRunes caps one brief line. A lead with no sentence end
// would otherwise carry its whole item, and the brief is meant to be read at
// a glance.
const instructionsBriefMaxRunes = 200

var (
	// instructionsListMarker matches a list item's marker at the start of a
	// line stripped of its indentation: a number with . or ), or a bullet,
	// then a space or the end.
	instructionsListMarker = regexp.MustCompile(`^(\d{1,9}[.)]|[-*+])( +|$)`)
	// instructionsThematicBreak matches ---, * * * and the like, which look
	// like a bullet but are rules.
	instructionsThematicBreak = regexp.MustCompile(`^ {0,3}([-*_])( *[-*_]){2,} *$`)
	instructionsSentenceEnd   = regexp.MustCompile(`[.!?]["')\]]*(\s|$)`)
)

// instructionsBrief reduces Markdown instructions to one line per top-level
// list item: the bold span the item leads with, or else its first sentence.
// Headings, prose paragraphs, code blocks and nested items are left out, so
// a style guide written as a list of bold-led rules comes down to the rules'
// names. A document with no list has no brief.
func instructionsBrief(content string) []string {
	var (
		brief   []string
		marker  string
		text    []string
		open    bool // a top-level item is open
		collect bool // still reading its first paragraph
		indent  int  // where the open item's content starts
		fence   string
	)
	flush := func() {
		if open {
			if lead := instructionsLead(strings.Join(text, " ")); lead != "" {
				brief = append(brief, marker+" "+lead)
			}
		}
		open, collect, text = false, false, nil
	}

	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line = strings.ReplaceAll(line, "\t", "    ")
		trimmed := strings.TrimSpace(line)
		lineIndent := len(line) - len(strings.TrimLeft(line, " "))

		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:3]
			if open && lineIndent < indent {
				flush()
			}
			collect = false
			continue
		}
		if trimmed == "" {
			collect = false
			continue
		}
		if instructionsThematicBreak.MatchString(line) {
			flush()
			continue
		}
		if m := instructionsListMarker.FindStringSubmatch(line[lineIndent:]); m != nil {
			if open && lineIndent >= indent {
				collect = false // a nested item: not part of the lead
				continue
			}
			if lineIndent <= 3 {
				flush()
				open, collect = true, true
				marker = m[1]
				if !strings.ContainsAny(marker[len(marker)-1:], ".)") {
					marker = "-"
				}
				indent = lineIndent + len(m[0])
				if m[2] == "" {
					indent = lineIndent + len(m[1]) + 1
				}
				text = []string{strings.TrimSpace(line[lineIndent+len(m[0]):])}
				continue
			}
		}
		if strings.HasPrefix(trimmed, "#") {
			flush()
			continue
		}
		if open && lineIndent >= indent {
			if collect {
				text = append(text, trimmed)
			}
			continue
		}
		if open && collect {
			text = append(text, trimmed) // a lazy continuation line
			continue
		}
		flush() // prose after the list
	}
	flush()
	return brief
}

// instructionsLead is an item's lead: its opening bold span, or else its
// first sentence, on one line.
func instructionsLead(text string) string {
	text = richtext.SanitizeSingleLine(text)
	for _, delim := range []string{"**", "__"} {
		if strings.HasPrefix(text, delim) {
			if end := strings.Index(text[len(delim):], delim); end > 0 {
				return instructionsClip(strings.TrimSpace(text[len(delim) : len(delim)+end]))
			}
		}
	}
	if loc := instructionsSentenceEnd.FindStringIndex(text); loc != nil {
		text = strings.TrimSpace(text[:loc[1]])
	}
	return instructionsClip(text)
}

func instructionsClip(s string) string {
	if utf8.RuneCountInString(s) <= instructionsBriefMaxRunes {
		return s
	}
	runes := []rune(s)
	return strings.TrimSpace(string(runes[:instructionsBriefMaxRunes-1])) + "…"
}
