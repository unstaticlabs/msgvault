package imap

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"

	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aymanbagabas/go-udiff"
	msgmime "go.kenn.io/msgvault/internal/mime"
	xhtml "golang.org/x/net/html"
)

type richDraftBody struct {
	plain, formatted      string
	foundPlain, foundHTML bool
}

func renderRichDraft(envelope plainTextEnvelope, body string, formatted *string, now time.Time, original io.Reader, mediaType string, params map[string]string, parsed *msgmime.Message) (ReplyDraft, error) {
	if parsed.BodyHTML == "" || params["boundary"] == "" {
		return ReplyDraft{}, errors.New("draft replacement does not support this message; use a plain-text message or an editable rich alternative")
	}
	var richHTML string
	if formatted != nil {
		if !utf8.ValidString(*formatted) || strings.ContainsRune(*formatted, '\x00') {
			return ReplyDraft{}, errors.New("invalid formatted draft body")
		}
		richHTML = *formatted
	} else {
		var err error
		richHTML, err = editDraftHTML(parsed.BodyHTML, parsed.BodyText, body)
		if err != nil {
			return ReplyDraft{}, err
		}
	}
	update := &richDraftBody{plain: body, formatted: richHTML}
	var raw bytes.Buffer
	if err := writeDraftEnvelope(&raw, envelope, now); err != nil {
		return ReplyDraft{}, err
	}
	writer := multipart.NewWriter(&raw)
	oldBoundary := params["boundary"]
	params["boundary"] = writer.Boundary()
	fmt.Fprintf(&raw, "Content-Type: %s\r\n\r\n", mime.FormatMediaType(mediaType, params))
	if err := update.multipart(writer, original, mediaType, oldBoundary); err != nil {
		return ReplyDraft{}, err
	}
	if !update.foundPlain || !update.foundHTML {
		return ReplyDraft{}, errors.New("draft replacement requires one editable plain-text and HTML alternative")
	}
	return parseRenderedDraft(&raw)
}

// Only the primary body of mixed/related messages is editable. File and inline
// resource parts keep their original transfer-encoded bytes and MIME headers.
func (u *richDraftBody) multipart(w *multipart.Writer, src io.Reader, mediaType, boundary string) error {
	r := multipart.NewReader(src, boundary)
	for index := 0; ; index++ {
		part, err := r.NextRawPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read rich draft part: %w", err)
		}
		typ, params, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		disp, _, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		body := disp != "attachment" && part.FileName() == "" && (mediaType == "multipart/alternative" || index == 0)
		var replacement *string
		if body && mediaType == "multipart/alternative" && typ == "text/plain" {
			if u.foundPlain {
				return errors.New("multiple plain-text draft alternatives")
			}
			u.foundPlain = true
			replacement = &u.plain
		}
		if body && (mediaType == "multipart/alternative" || mediaType == "multipart/related") && typ == "text/html" {
			if u.foundHTML {
				return errors.New("multiple HTML draft alternatives")
			}
			u.foundHTML = true
			replacement = &u.formatted
		}
		if replacement != nil {
			params["charset"] = "utf-8"
			part.Header.Set("Content-Type", mime.FormatMediaType(typ, params))
			part.Header.Set("Content-Transfer-Encoding", "quoted-printable")
			part.Header.Del("Content-MD5")
			part.Header.Del("Content-Length")
		}
		// A fresh delimiter keeps text that looks like an old MIME boundary editable.
		nested := body && (typ == "multipart/alternative" || typ == "multipart/related") && params["boundary"] != ""
		oldBoundary := params["boundary"]
		var subBody bytes.Buffer
		if nested {
			inner := multipart.NewWriter(&subBody)
			params["boundary"] = inner.Boundary()
			part.Header.Set("Content-Type", mime.FormatMediaType(typ, params))
			if err := u.multipart(inner, part, typ, oldBoundary); err != nil {
				return err
			}
		}
		out, err := w.CreatePart(part.Header)
		if err != nil {
			return fmt.Errorf("create rich draft MIME part: %w", err)
		}
		switch {
		case replacement != nil:
			err = writeQuotedPrintable(out, *replacement)
		case nested:
			_, err = subBody.WriteTo(out)
		default:
			_, err = io.Copy(out, part)
		}
		if err != nil {
			return err
		}
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close rich draft MIME: %w", err)
	}
	return nil
}

type draftTextSpan struct {
	start, end         int // byte offsets in the plain-text alternative
	htmlStart, htmlEnd int // byte offsets in the original HTML
	value              string
	nonBreakingSpaces  []int
	edits              []udiff.Edit
}

// Change only text tokens covered by an edit. Unchanged tags, attributes,
// quotations and entities remain byte-identical in the decoded HTML.
func editDraftHTML(original, plain, edited string) (string, error) {
	plain = strings.ReplaceAll(plain, "\r\n", "\n")
	edited = strings.ReplaceAll(edited, "\r\n", "\n")
	edits := udiff.Strings(plain, edited)
	if len(edits) == 0 {
		return original, nil
	}
	var spans []draftTextSpan
	tok := xhtml.NewTokenizer(strings.NewReader(original))
	plainAt, htmlAt := 0, 0
	hidden := ""
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
		case xhtml.ErrorToken, xhtml.CommentToken, xhtml.DoctypeToken, xhtml.SelfClosingTagToken:
			continue
		case xhtml.StartTagToken:
			name, _ := tok.TagName()
			tag := string(name)
			if hidden == "" && (tag == "head" || tag == "script" || tag == "style") {
				hidden = tag
			}
		case xhtml.EndTagToken:
			name, _ := tok.TagName()
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
			span := draftTextSpan{start: at, end: at + len(value), htmlStart: start, htmlEnd: htmlAt, value: decoded}
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
