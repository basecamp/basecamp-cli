package markdown

import (
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"

	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// glamour reads a Markdown document's text straight out of the source and runs
// most of it through html.UnescapeString before styling it — prose, code spans,
// raw HTML — so "&#27;[31m" in the source reaches the terminal as a live escape
// byte however the Markdown spelled it. Sanitizing the source cannot catch that:
// what it sees is the five harmless characters "&#27;".
//
// The source is therefore rewritten before glamour sees it so that its extra
// decode is the identity: every `&` in the text it decodes becomes `&amp;`, which
// decodes back to `&`. A backslash-escaped `\&` becomes `&amp;` too, since
// CommonMark reads it as a literal ampersand where glamour would show the
// backslash. What glamour reads verbatim is left verbatim — an image's alt text,
// link destinations and autolinks — so a `%26` where a query string had `&` never
// becomes a different URL.
//
// Every `&` is encoded, prose included, because the Markdown that reaches here
// carries no escaping convention of its own: richtext.HTMLToMarkdown decodes
// entities as it converts, so an ampersand in it stands for an ampersand. This is
// where this port differs from hey-cli's, whose converter escapes on the way out.
//
// The rewrite is parsed with the same goldmark configuration glamour uses, so the
// segments it finds are the segments glamour will read.
var sourceParser = goldmark.New(goldmark.WithExtensions(extension.GFM, extension.DefinitionList))

// prepareSource makes md safe to show and safe to hand to glamour. The first
// result is the source with its control characters stripped, which is what a
// fallback shows when glamour cannot render; the second is that source with
// entity decoding neutralized, which only glamour should see — its rewrite is
// glamour's quirk, and shown directly it reads "&amp;" where the source read "&".
//
// deep reports a document nested past maxNestingDepth — quotes in quotes, lists
// in lists, in any spelling — which glamour must not be given at all: its cost
// doubles every few levels of quote nesting, and a line of a hundred ">" is a
// hang. In a TUI that redraws on every keystroke, that is the whole app. Such a
// document is shown as its source instead. The depth is measured on goldmark's own
// tree, so a marker written with a tab, an indent or a list inside a quote counts
// the way glamour would count it.
func prepareSource(md string) (safe, forGlamour string, deep bool) {
	safe = stripControls(md)
	forGlamour, depth := neutralizeEntities(safe)
	return safe, forGlamour, depth > maxNestingDepth
}

// maxNestingDepth is how deep quotes and lists may nest before the document is
// shown unrendered. Rich text from Basecamp never nests past sixteen; at twenty
// glamour still renders in milliseconds.
const maxNestingDepth = 20

// stripControls removes escape sequences, the C0, C1 and DEL controls, the
// bidirectional controls and the invisible format characters, keeping newlines and
// tabs. A body is prose, and Markdown is made of newlines.
//
// It runs over the whole source, link destinations included, on purpose: what a
// link shows and where it goes are then the same text, and a destination that
// differs from the one shown only by a zero-width character is exactly the
// confusable this removes. The Trojan-source class — a right-to-left override that
// shows one thing and means another — is no more welcome in a message than in a
// file name.
//
// hey-cli's sanitizer goes one step further and caps a stack of combining marks,
// which this does not: a line wearing fifty accents renders as sent. That is ugly
// rather than deceptive, and capping it means a state machine that has to tell a
// family emoji's joiners from an attack's.
func stripControls(s string) string {
	return strings.Map(func(r rune) rune {
		if isInvisible(r) {
			return -1
		}
		return r
	}, richtext.SanitizeTerminal(s))
}

// isInvisible reports the bidirectional controls and the format characters that
// take no space: what they do to a line cannot be seen, only read wrong.
//
// Written as code points rather than quoted characters, because a quoted one here
// would be as invisible in this file as it is in a message.
//
// The two joiners are deliberately not among them: U+200D holds a family emoji
// together, and U+200C is a letter's business in several scripts.
func isInvisible(r rune) bool {
	switch r {
	case 0x00ad, // soft hyphen
		0x061c, // arabic letter mark
		0x180e, // mongolian vowel separator
		0x200b, // zero width space
		0x200e, // left-to-right mark
		0x200f, // right-to-left mark
		0x2060, // word joiner
		0xfeff: // zero width no-break space
		return true
	}
	// The embeddings, overrides and isolates.
	return (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

func isControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// sourceSpan is a byte range of the source that glamour will read as text, and
// how it reads it: prose and code are decoded, and an image's alt text is not.
type sourceSpan struct {
	start, stop int
	kind        spanKind
}

type spanKind int

const (
	proseSpan spanKind = iota
	codeSpan
	altSpan
)

// neutralizeEntities rewrites the source for glamour and reports how deeply the
// document nests, both from one parse.
func neutralizeEntities(md string) (string, int) {
	source := []byte(md)
	spans, depth := textSpans(sourceParser.Parser().Parse(text.NewReader(source)))
	if !strings.Contains(md, "&") {
		return md, depth
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })

	var b strings.Builder
	b.Grow(len(md) + 8)
	last := 0
	for _, span := range spans {
		if span.start < last || span.stop > len(source) {
			continue
		}
		b.Write(source[last:span.start])
		b.WriteString(neutralized(string(source[span.start:span.stop]), span.kind))
		last = span.stop
	}
	b.Write(source[last:])
	return b.String(), depth
}

// textSpans finds the spans glamour will decode, and measures how deep quotes and
// lists nest on the way.
func textSpans(doc ast.Node) (spans []sourceSpan, maxDepth int) {
	depth := 0
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		switch n.(type) {
		case *ast.Blockquote, *ast.List:
			if entering {
				depth++
				maxDepth = max(maxDepth, depth)
			} else {
				depth--
			}
		}
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Text:
			kind := proseSpan
			if _, code := n.Parent().(*ast.CodeSpan); code {
				kind = codeSpan
			}
			if underImage(n) {
				kind = altSpan
			}
			spans = append(spans, sourceSpan{n.Segment.Start, n.Segment.Stop, kind})
		case *ast.HTMLBlock:
			lines := n.Lines()
			for i := range lines.Len() {
				line := lines.At(i)
				spans = append(spans, sourceSpan{line.Start, line.Stop, codeSpan})
			}
		case *ast.RawHTML:
			for i := range n.Segments.Len() {
				segment := n.Segments.At(i)
				spans = append(spans, sourceSpan{segment.Start, segment.Stop, codeSpan})
			}
		}
		return ast.WalkContinue, nil
	})
	return spans, maxDepth
}

func underImage(n ast.Node) bool {
	for parent := n.Parent(); parent != nil; parent = parent.Parent() {
		if _, image := parent.(*ast.Image); image {
			return true
		}
	}
	return false
}

// Prose and code both have every `&` encoded, so glamour's one decode shows the
// character the source meant. In prose a `\&` is encoded as well: CommonMark
// reads it as a literal ampersand, and glamour would otherwise show the
// backslash. Alt text glamour shows as written, so it is left alone.
var (
	codeAmpersands = strings.NewReplacer("&", "&amp;")
	textAmpersands = strings.NewReplacer(`\&`, "&amp;", "&", "&amp;")
)

func neutralized(s string, kind spanKind) string {
	switch kind {
	case codeSpan:
		return codeAmpersands.Replace(s)
	case altSpan:
		return s
	default:
		return textAmpersands.Replace(s)
	}
}
