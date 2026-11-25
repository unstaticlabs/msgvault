package imap

import (
	"errors"
	"fmt"
	"html"
	"io"
	"sort"
	"strings"

	"github.com/aymanbagabas/go-udiff"
	msgmime "go.kenn.io/msgvault/internal/mime"
	xhtml "golang.org/x/net/html"
)

type draftTextSpan struct {
	start, end         int // byte offsets in the plain-text alternative
	htmlStart, htmlEnd int // byte offsets in the original HTML
	value              string
	block              string
	layout             bool
	nonBreakingSpaces  []int
	edits              []udiff.Edit
}

// paragraphBreak locates the exact layout markup represented by a plain-text
// newline. Adjacent matching paragraphs can be joined without moving inline
// markup or changing the style of the following text.
func paragraphBreak(gap, leftBlock, rightBlock string) (start, end int, ok bool) {
	tokens := xhtml.NewTokenizer(strings.NewReader(gap))
	at := 0
	closeStart, closeEnd := -1, -1
	closeTag := ""
	foundStart, foundEnd := -1, -1
	for {
		kind := tokens.Next()
		if kind == xhtml.ErrorToken {
			break
		}
		begin := at
		at += len(tokens.Raw())
		if kind != xhtml.StartTagToken && kind != xhtml.SelfClosingTagToken && kind != xhtml.EndTagToken {
			continue
		}
		name, _ := tokens.TagName()
		tag := string(name)
		first, last := -1, -1
		switch {
		case tag == "br" && kind != xhtml.EndTagToken:
			first, last = begin, at
		case (tag == "p" || tag == "div") && kind == xhtml.EndTagToken:
			closeStart, closeEnd, closeTag = begin, at, tag
		case closeStart >= 0 && tag == closeTag && leftBlock == rightBlock && strings.TrimSpace(gap[closeEnd:begin]) == "":
			first, last = closeStart, at
			closeStart = -1
		}
		if first < 0 {
			continue
		}
		if foundStart < 0 {
			foundStart = first
		} else if strings.TrimSpace(gap[foundEnd:first]) != "" {
			return 0, 0, false
		}
		foundEnd = last
	}
	return foundStart, foundEnd, foundStart >= 0
}

// Change only text tokens covered by an edit. Unchanged tags, attributes,
// quotations and entities remain byte-identical in the decoded HTML.
func editDraftHTML(original, plain, edited string) (string, error) {
	plain = strings.ReplaceAll(plain, "\r\n", "\n")
	edited = strings.ReplaceAll(edited, "\r\n", "\n")
	// Mail clients commonly serialize an extra terminal CRLF in the plain
	// alternative. It has no corresponding HTML text node.
	if strings.HasSuffix(plain, "\n") {
		plain = strings.TrimRight(plain, "\n")
		edited = strings.TrimRight(edited, "\n")
	}
	edits := udiff.Strings(plain, edited)
	if len(edits) == 0 {
		return original, nil
	}
	var spans []draftTextSpan
	tok := xhtml.NewTokenizer(strings.NewReader(original))
	plainAt, htmlAt := 0, 0
	hidden := ""
	emptyAt := -1
	block := ""
	for {
		kind := tok.Next()
		if kind == xhtml.ErrorToken {
			if !errors.Is(tok.Err(), io.EOF) {
				return "", tok.Err()
			}
			break
		}
		start := htmlAt
		htmlAt += len(tok.Raw())
		switch kind {
		case xhtml.ErrorToken, xhtml.CommentToken, xhtml.DoctypeToken:
			continue
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			name, _ := tok.TagName()
			tag := string(name)
			if hidden == "" && (tag == "p" || tag == "div") {
				block = original[start:htmlAt]
			}
			if hidden == "" && emptyAt < 0 && tag == "br" {
				emptyAt = start
			}
			if hidden == "" && (tag == "head" || tag == "script" || tag == "style") {
				hidden = tag
			}
		case xhtml.EndTagToken:
			name, _ := tok.TagName()
			if hidden == "" && emptyAt < 0 {
				switch string(name) {
				case "span", "b", "strong", "i", "em", "div", "p", "body":
					emptyAt = start
				}
			}
			if string(name) == hidden {
				hidden = ""
			}
		case xhtml.TextToken:
			if hidden != "" {
				continue
			}
			decoded := string(tok.Text())
			value := strings.ReplaceAll(decoded, "\u00a0", " ")
			if value == "" {
				continue
			}
			offset := strings.Index(plain[plainAt:], value)
			if offset < 0 {
				continue
			}
			at := plainAt + offset
			if gap := plain[plainAt:at]; len(spans) > 0 && strings.Contains(gap, "\n") && strings.TrimSpace(gap) == "" {
				prev := spans[len(spans)-1]
				base := prev.htmlEnd
				if a, b, ok := paragraphBreak(original[base:start], prev.block, block); ok {
					spans = append(spans, draftTextSpan{start: plainAt, end: at, htmlStart: base + a, htmlEnd: base + b, value: gap, layout: true})
				}
			}
			span := draftTextSpan{start: at, end: at + len(value), htmlStart: start, htmlEnd: htmlAt, value: decoded, block: block}
			if decoded != value {
				for offset, r := range decoded {
					if r == '\u00a0' {
						span.nonBreakingSpaces = append(span.nonBreakingSpaces, offset-len(span.nonBreakingSpaces))
					}
				}
			}
			spans = append(spans, span)
			plainAt = at + len(value)
		}
	}
	if plain == "" && emptyAt >= 0 && strings.TrimSpace(msgmime.StripHTML(original)) == "" {
		return original[:emptyAt] + strings.ReplaceAll(html.EscapeString(edited), "\n", "<br>") + original[emptyAt:], nil
	}
	for _, edit := range edits {
		i := sort.Search(len(spans), func(i int) bool { return spans[i].end > edit.Start })
		// An insertion at a boundary inherits the preceding text's style.
		if edit.Start == edit.End && i > 0 && spans[i-1].end == edit.Start {
			i--
		}
		pos := edit.Start
		for {
			if i == len(spans) || spans[i].start > pos {
				return "", errors.New("the plain and formatted versions differ at this edit; supply the matching HTML with body_html or --body-html")
			}
			span := &spans[i]
			end := min(edit.End, span.end)
			newText := ""
			if pos == edit.Start {
				newText = edit.New
			}
			span.edits = append(span.edits, udiff.Edit{Start: pos - span.start, End: end - span.start, New: newText})
			if end == edit.End {
				break
			}
			pos = end
			i++
		}
	}
	var result strings.Builder
	last := 0
	for _, span := range spans {
		if len(span.edits) == 0 {
			continue
		}
		result.WriteString(original[last:span.htmlStart])
		if span.layout {
			value, err := udiff.Apply(span.value, span.edits)
			if err != nil {
				return "", fmt.Errorf("edit rich draft paragraph: %w", err)
			}
			result.WriteString(strings.ReplaceAll(html.EscapeString(value), "\n", "<br>"))
			last = span.htmlEnd
			continue
		}
		pos := 0
		for _, e := range span.edits {
			// A nonbreaking space occupies two UTF-8 bytes in the HTML text.
			start := e.Start + sort.SearchInts(span.nonBreakingSpaces, e.Start)
			end := e.End + sort.SearchInts(span.nonBreakingSpaces, e.End)
			result.WriteString(html.EscapeString(span.value[pos:start]))
			result.WriteString(strings.ReplaceAll(html.EscapeString(e.New), "\n", "<br>"))
			pos = end
		}
		result.WriteString(html.EscapeString(span.value[pos:]))
		last = span.htmlEnd
	}
	result.WriteString(original[last:])
	return result.String(), nil
}
