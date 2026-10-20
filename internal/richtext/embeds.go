package richtext

import (
	"bytes"
	"html"
	"regexp"
	"slices"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

type EmbedRef struct {
	SGID        string
	ContentType string
}

type EmbedFunc func(urls []string) map[string]EmbedRef

type EmbedAttachment struct {
	ast.BaseBlock
	Ref EmbedRef
}

var KindEmbedAttachment = ast.NewNodeKind("EmbedAttachment")

func (n *EmbedAttachment) Kind() ast.NodeKind { return KindEmbedAttachment }
func (n *EmbedAttachment) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

func EmbedToHTML(ref EmbedRef) string {
	tag := `<bc-attachment sgid="` + escapeAttr(ref.SGID) + `"`
	if ref.ContentType != "" {
		tag += ` content-type="` + escapeAttr(ref.ContentType) + `"`
	}
	return tag + `></bc-attachment>`
}

func MarkdownToHTMLWithEmbeds(md string, embed EmbedFunc) string {
	if md == "" {
		return ""
	}

	md = strings.ReplaceAll(md, "\r\n", "\n")
	md = strings.ReplaceAll(md, "\r", "\n")
	source := []byte(md)

	doc := mdConverter.Parser().Parse(text.NewReader(source))
	if embed != nil {
		embedStandaloneURLs(doc, source, embed)
	}

	var buf bytes.Buffer
	if err := mdConverter.Renderer().Render(&buf, source, doc); err != nil {
		return "<p>" + html.EscapeString(md) + "</p>"
	}
	return strings.TrimSpace(buf.String())
}

var (
	reStandaloneURL      = regexp.MustCompile(`^https?://[^\s<>"'\\]+$`)
	reCharacterReference = regexp.MustCompile(`&(#[0-9]+|#[xX][0-9a-fA-F]+|[A-Za-z][A-Za-z0-9]*);`)
)

func embedStandaloneURLs(doc ast.Node, source []byte, embed EmbedFunc) {
	paragraphs := topLevelParagraphs(doc)

	var urls []string
	for _, p := range paragraphs {
		for _, line := range paragraphLines(p) {
			if url := standaloneURL(line, source); url != "" && !slices.Contains(urls, url) {
				urls = append(urls, url)
			}
		}
	}
	if len(urls) == 0 {
		return
	}

	refs := embed(urls)
	if len(refs) == 0 {
		return
	}

	for _, p := range paragraphs {
		splitParagraphAtEmbeds(p, source, refs)
	}
}

func topLevelParagraphs(doc ast.Node) []*ast.Paragraph {
	var paragraphs []*ast.Paragraph
	for child := doc.FirstChild(); child != nil; child = child.NextSibling() {
		if p, ok := child.(*ast.Paragraph); ok && !holdsInlineHTML(p) {
			paragraphs = append(paragraphs, p)
		}
	}
	return paragraphs
}

func holdsInlineHTML(p *ast.Paragraph) bool {
	for child := p.FirstChild(); child != nil; child = child.NextSibling() {
		if _, ok := child.(*ast.RawHTML); ok {
			return true
		}
	}
	return false
}

func paragraphLines(p *ast.Paragraph) [][]ast.Node {
	var lines [][]ast.Node
	var line []ast.Node
	for child := p.FirstChild(); child != nil; child = child.NextSibling() {
		line = append(line, child)
		if t, ok := child.(*ast.Text); ok && (t.SoftLineBreak() || t.HardLineBreak()) {
			lines = append(lines, line)
			line = nil
		}
	}
	if len(line) > 0 {
		lines = append(lines, line)
	}
	return lines
}

func standaloneURL(line []ast.Node, source []byte) string {
	var content []ast.Node
	for _, n := range line {
		if t, ok := n.(*ast.Text); ok && len(bytes.TrimSpace(t.Segment.Value(source))) == 0 {
			continue
		}
		content = append(content, n)
	}

	if len(content) == 1 {
		if link, ok := content[0].(*ast.AutoLink); ok && link.AutoLinkType == ast.AutoLinkURL {
			return embeddableURL(string(link.URL(source)))
		}
	}

	var raw []byte
	for _, n := range content {
		t, ok := n.(*ast.Text)
		if !ok || t.IsRaw() {
			return ""
		}
		raw = append(raw, t.Segment.Value(source)...)
	}
	return embeddableURL(string(bytes.TrimSpace(raw)))
}

func embeddableURL(url string) string {
	if reStandaloneURL.MatchString(url) && !reCharacterReference.MatchString(url) {
		return url
	}
	return ""
}

func splitParagraphAtEmbeds(p *ast.Paragraph, source []byte, refs map[string]EmbedRef) {
	lines := paragraphLines(p)
	embeds := make([]*EmbedRef, len(lines))
	embedded := false
	for i, line := range lines {
		if ref, ok := refs[standaloneURL(line, source)]; ok {
			embeds[i] = &ref
			embedded = true
		}
	}
	if !embedded {
		return
	}

	parent := p.Parent()
	var pending *ast.Paragraph
	flush := func() {
		if pending != nil {
			trimTrailingBreak(pending)
			parent.InsertBefore(parent, p, pending)
			pending = nil
		}
	}
	for i, line := range lines {
		if embeds[i] != nil {
			flush()
			parent.InsertBefore(parent, p, &EmbedAttachment{Ref: *embeds[i]})
			continue
		}
		if pending == nil {
			pending = ast.NewParagraph()
		}
		for _, n := range line {
			pending.AppendChild(pending, n)
		}
	}
	flush()
	parent.RemoveChild(parent, p)
}

func trimTrailingBreak(p *ast.Paragraph) {
	if t, ok := p.LastChild().(*ast.Text); ok {
		t.SetSoftLineBreak(false)
		t.SetHardLineBreak(false)
	}
}

func (r *trixRenderer) renderEmbedAttachment(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	if n, ok := node.(*EmbedAttachment); ok {
		_, _ = w.WriteString(EmbedToHTML(n.Ref) + "\n")
	}
	return ast.WalkContinue, nil
}
