package richtext

import (
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// StyleFinding is one way a rich-text body breaks the writing rules agents
// are given for Basecamp: a raw URL, one long flat block, or several people
// mentioned at the head of the post.
type StyleFinding struct {
	Check   string `json:"check"`
	Message string `json:"message"`
}

// The checks CheckStyle runs, as they appear in StyleFinding.Check.
const (
	StyleBareURL      = "bare_url"
	StyleFlatBody     = "flat_body"
	StyleLeadMentions = "lead_mentions"
)

const (
	flatBodyWordLimit = 120
	bareURLShownRunes = 60
)

var (
	reProseURL      = regexp.MustCompile(`(?i)https?://[^\s\x00-\x1f<>"]+`)
	reWholeBlockURL = regexp.MustCompile(`(?i)^https?://\S+$`)
)

// styleBlockTags are the elements that start a block of their own. A body
// with a second one, or with a <br>, is laid out in more than one block.
var styleBlockTags = map[string]bool{
	"p": true, "div": true, "li": true, "blockquote": true, "pre": true, "tr": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
}

// styleBlock collects one block's text while the tokenizer is inside it.
// blocks lists them in the order they open, which is reading order.
type styleBlock struct {
	tag      string
	text     strings.Builder // the block's own text, outside nested blocks and attachments
	prose    []string        // text outside links, code and attachments
	mentions []string        // mention attachments and mention links, by key
	attached bool            // holds a <bc-attachment>
}

// CheckStyle reads the HTML a rich-text write is about to send and reports
// what breaks three structural writing rules. It is deterministic and reads
// only the markup:
//
//   - a bare URL: an http(s) URL in prose rather than on a link's words. A URL
//     that is a link's href or text, or inside code, is not prose, and a URL
//     that is the whole of its block is how a Basecamp embed is written;
//   - a flat body: more than 120 words in at most one block, with no second
//     paragraph, list item, heading, quote or line break;
//   - lead mentions: more than one person mentioned in the first block that
//     has content, counting mention attachments, mention links and the @Name
//     text the CLI resolves into mentions.
func CheckStyle(content string) []StyleFinding {
	var (
		stack      []*styleBlock
		implicit   *styleBlock
		blocks     []*styleBlock
		structure  int
		words      int
		linkDepth  int
		codeDepth  int
		attachment int
	)
	current := func() *styleBlock {
		if len(stack) > 0 {
			return stack[len(stack)-1]
		}
		if implicit == nil {
			implicit = &styleBlock{}
			blocks = append(blocks, implicit)
		}
		return implicit
	}

	z := html.NewTokenizer(strings.NewReader(content))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break // io.EOF, or markup the tokenizer gives up on: check what was read
		}
		tok := z.Token()
		switch tt {
		case html.TextToken:
			if attachment > 0 {
				continue
			}
			words += len(strings.Fields(tok.Data))
			b := current()
			b.text.WriteString(tok.Data)
			if linkDepth == 0 && codeDepth == 0 {
				b.prose = append(b.prose, tok.Data)
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			selfClosing := tt == html.SelfClosingTagToken
			switch {
			case styleBlockTags[tok.Data]:
				structure++
				if len(stack) == 0 {
					implicit = nil // text after this block starts a new one
				}
				if selfClosing {
					continue
				}
				if tok.Data == "pre" {
					codeDepth++
				}
				b := &styleBlock{tag: tok.Data}
				blocks = append(blocks, b)
				stack = append(stack, b)
			case tok.Data == "br":
				structure++
			case tok.Data == "a":
				href := attr(tok, "href")
				if scheme, value, ok := strings.Cut(href, ":"); ok && (scheme == "mention" || scheme == "person") {
					current().mentions = append(current().mentions, scheme+":"+value)
				}
				if !selfClosing {
					linkDepth++
				}
			case tok.Data == "code":
				if !selfClosing {
					codeDepth++
				}
			case tok.Data == "bc-attachment":
				b := current()
				b.attached = true
				if strings.EqualFold(attr(tok, "content-type"), "application/vnd.basecamp.mention") {
					b.mentions = append(b.mentions, "sgid:"+attr(tok, "sgid"))
				}
				if !selfClosing {
					attachment++
				}
			}
		case html.EndTagToken:
			switch {
			case styleBlockTags[tok.Data]:
				for i := len(stack) - 1; i >= 0; i-- {
					if stack[i].tag == tok.Data {
						stack = stack[:i]
						break
					}
				}
				if tok.Data == "pre" && codeDepth > 0 {
					codeDepth--
				}
			case tok.Data == "a" && linkDepth > 0:
				linkDepth--
			case tok.Data == "code" && codeDepth > 0:
				codeDepth--
			case tok.Data == "bc-attachment" && attachment > 0:
				attachment--
			}
		case html.ErrorToken, html.CommentToken, html.DoctypeToken:
			// Not content: comments and doctypes carry no text a reader sees.
		}
	}

	var findings []StyleFinding
	findings = append(findings, bareURLFindings(blocks)...)
	if words > flatBodyWordLimit && structure <= 1 {
		findings = append(findings, StyleFinding{
			Check: StyleFlatBody,
			Message: fmt.Sprintf("one block of %d words: give each idea its own paragraph or line, "+
				"and put several items in a real list", words),
		})
	}
	if first := firstBlockWithContent(blocks); first != nil {
		if people := blockMentions(first); len(people) > 1 {
			findings = append(findings, StyleFinding{
				Check: StyleLeadMentions,
				Message: fmt.Sprintf("%d people mentioned at the start: ask the one person whose call it is, "+
					"and mention no one who has nothing to do", len(people)),
			})
		}
	}
	return findings
}

// bareURLFindings reports each distinct URL written in a block's prose, unless
// the URL is the block's whole content.
func bareURLFindings(blocks []*styleBlock) []StyleFinding {
	var findings []StyleFinding
	seen := map[string]bool{}
	for _, b := range blocks {
		if reWholeBlockURL.MatchString(strings.TrimSpace(b.text.String())) {
			continue
		}
		for _, run := range b.prose {
			for _, url := range reProseURL.FindAllString(run, -1) {
				url = trimURLMatch(url)
				if seen[url] {
					continue
				}
				seen[url] = true
				findings = append(findings, StyleFinding{
					Check: StyleBareURL,
					Message: fmt.Sprintf("raw URL in the text (%s): link it on the words that say what it is, "+
						"never as a raw URL", shortenURL(url)),
				})
			}
		}
	}
	return findings
}

func shortenURL(url string) string {
	r := []rune(url)
	if len(r) <= bareURLShownRunes {
		return url
	}
	return string(r[:bareURLShownRunes-1]) + "…"
}

// firstBlockWithContent skips the empty paragraphs Markdown conversion puts
// between blocks as spacers.
func firstBlockWithContent(blocks []*styleBlock) *styleBlock {
	for _, b := range blocks {
		if b.attached || len(b.mentions) > 0 || strings.TrimSpace(b.text.String()) != "" {
			return b
		}
	}
	return nil
}

// blockMentions returns the distinct people a block mentions: attachments and
// mention links by their key, and @sgid: and @Name text as the CLI would
// resolve it.
func blockMentions(b *styleBlock) map[string]bool {
	people := map[string]bool{}
	for _, key := range b.mentions {
		people[key] = true
	}
	for _, run := range b.prose {
		for _, m := range reSGIDMention.FindAllStringSubmatch(run, -1) {
			people["sgid:"+m[3]] = true
		}
		run = reSGIDMention.ReplaceAllString(run, "$1")
		for _, m := range reMentionInput.FindAllStringSubmatch(run, -1) {
			people["name:"+strings.ToLower(m[2])] = true
		}
	}
	return people
}

func attr(tok html.Token, name string) string {
	for _, a := range tok.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}
