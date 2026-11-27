package imap

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"
	"time"
	"unicode/utf8"

	msgmime "go.kenn.io/msgvault/internal/mime"
)

type richDraftBody struct {
	plain, formatted      string
	foundPlain, foundHTML bool
}

func renderRichDraft(envelope plainTextEnvelope, body string, formatted *string, now time.Time, original io.Reader, mediaType string, params map[string]string, parsed *msgmime.Message) (ReplyDraft, error) {
	if parsed.BodyHTML == "" || (mediaType != "text/html" && params["boundary"] == "") {
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
		richHTML, err = editDraftHTML(parsed.BodyHTML, parsed.GetBodyText(), body)
		// enmime includes Markdown emphasis in its generated plaintext. A caller
		// editing the visible wording can omit those generated characters.
		if err != nil && parsed.TextFromHTML {
			richHTML, err = editDraftHTML(parsed.BodyHTML, msgmime.StripHTML(parsed.BodyHTML), body)
		}
		if err != nil {
			return ReplyDraft{}, err
		}
	}
	update := &richDraftBody{plain: body, formatted: richHTML}
	var raw bytes.Buffer
	if err := writeDraftEnvelope(&raw, envelope, now); err != nil {
		return ReplyDraft{}, err
	}
	var content bytes.Buffer
	header := textproto.MIMEHeader{}
	if mediaType == "text/html" {
		header.Set("Content-Type", "text/html; charset=utf-8")
		header.Set("Content-Transfer-Encoding", "quoted-printable")
		if err := writeQuotedPrintable(&content, richHTML); err != nil {
			return ReplyDraft{}, err
		}
		update.foundHTML = true
	} else {
		writer := multipart.NewWriter(&content)
		oldBoundary := params["boundary"]
		params["boundary"] = writer.Boundary()
		header.Set("Content-Type", mime.FormatMediaType(mediaType, params))
		if err := update.multipart(writer, original, mediaType, oldBoundary, params["start"]); err != nil {
			return ReplyDraft{}, err
		}
	}
	if !update.foundHTML {
		return ReplyDraft{}, errors.New("draft has no editable HTML alternative")
	}
	if update.foundPlain {
		fmt.Fprintf(&raw, "Content-Type: %s\r\n\r\n", header.Get("Content-Type"))
		raw.Write(content.Bytes())
	} else {
		if mediaType == "multipart/mixed" {
			return ReplyDraft{}, errors.New("draft has no editable plain-text alternative alongside its attachments")
		}
		// Rich-only drafts gain the matching plain alternative. A related
		// group stays together so inline images keep their content IDs.
		writer := multipart.NewWriter(&raw)
		fmt.Fprintf(&raw, "Content-Type: %s\r\n\r\n", mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": writer.Boundary()}))
		if err := writeTextPart(writer, body); err != nil {
			return ReplyDraft{}, err
		}
		part, err := writer.CreatePart(header)
		if err != nil {
			return ReplyDraft{}, fmt.Errorf("create HTML alternative: %w", err)
		}
		if _, err = content.WriteTo(part); err != nil {
			return ReplyDraft{}, fmt.Errorf("write HTML alternative: %w", err)
		}
		if err = writer.Close(); err != nil {
			return ReplyDraft{}, fmt.Errorf("close rich draft alternatives: %w", err)
		}
	}
	return parseRenderedDraft(&raw)
}

// Only the primary body of mixed/related messages is editable. File and inline
// resource parts keep their original transfer-encoded bytes and MIME headers.
func (u *richDraftBody) multipart(w *multipart.Writer, src io.Reader, mediaType, boundary, rootID string) error {
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
		primary := index == 0
		if mediaType == "multipart/related" && rootID != "" {
			primary = part.Header.Get("Content-ID") == rootID
		}
		body := disp != "attachment" && part.FileName() == "" && (mediaType == "multipart/alternative" || primary)
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
			if err := u.multipart(inner, part, typ, oldBoundary, params["start"]); err != nil {
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
