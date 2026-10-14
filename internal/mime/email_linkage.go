package mime

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"io"
	stdmime "mime"
	"mime/multipart"
	"net/mail"
	"regexp"
	"slices"
	"strings"
)

const (
	EmailLinkageVersion  = 1
	EmailLinkageMaxBytes = 64 << 20
	emailLinkageMaxDepth = 8
	emailLinkageMaxParts = 256
	emailLinkageMaxIDs   = 128
)

// EmailLinkage contains only explicit MIME identity evidence. It never derives
// identity from a subject, display name, forwarded-text marker or quoted body.
// Non-complete outcomes preserve the readable message while explaining why
// automatic linkage or handling cannot be treated as complete.
type EmailLinkage struct {
	Version           int      `json:"version"`
	MessageID         string   `json:"message_id,omitempty"`
	Replies           []string `json:"replies,omitempty"`
	References        []string `json:"references,omitempty"`
	AttachedOriginals []string `json:"attached_originals,omitempty"`
	Outcome           string   `json:"outcome"`
}

// The automatic-link grammar is intentionally separate from the historical
// NormalizeMessageID comparison contract. Unsupported obsolete forms remain
// visible with an incomplete outcome rather than acquiring a guessed identity.
var emailLinkageID = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+(?:\\.[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+)*@(?:[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+(?:\\.[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+)*|\\[[^\\[\\]\\\\\\s]+\\])$")

// ExtractEmailLinkage reads archived headers and explicit message/rfc822 parts.
// It does not recursively read attached originals' bodies or delivery reports.
func ExtractEmailLinkage(raw []byte) EmailLinkage {
	result := EmailLinkage{Version: EmailLinkageVersion, Outcome: "complete"}
	if len(raw) > EmailLinkageMaxBytes {
		result.Outcome = "size_limit"
		return result
	}
	header, err := emailLinkageHeader(bytes.NewReader(raw))
	if err != nil {
		result.Outcome = err.Error()
		return result
	}
	result.MessageID = result.singleID(header)
	result.Replies = result.ids(header["In-Reply-To"])
	result.References = result.ids(header["References"])
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		result.incomplete("invalid_headers")
		return result
	}
	parts := 0
	result.walkParts(message.Body, header.Get("Content-Type"), 0, &parts)
	return result
}

func (e *EmailLinkage) incomplete(reason string) {
	if e.Outcome == "complete" {
		e.Outcome = reason
	}
}

func (e *EmailLinkage) singleID(header mail.Header) string {
	values := header["Message-Id"]
	if len(values) == 0 {
		return ""
	}
	if len(values) != 1 {
		e.incomplete("invalid_headers")
		return ""
	}
	ids, valid := emailLinkageIDs(values[0])
	if !valid || len(ids) != 1 {
		e.incomplete("invalid_headers")
		return ""
	}
	return ids[0]
}

func (e *EmailLinkage) ids(values []string) []string {
	var result []string
	for _, value := range values {
		ids, valid := emailLinkageIDs(value)
		if !valid {
			e.incomplete("invalid_headers")
		}
		for _, id := range ids {
			if !slices.Contains(result, id) {
				if len(result) == emailLinkageMaxIDs {
					e.incomplete("identity_limit")
					return result
				}
				result = append(result, id)
			}
		}
	}
	return result
}

// Ignore CFWS, including nested comments, outside actual bracketed IDs. A
// malformed token cannot create a valid ID by trimming arbitrary brackets.
func emailLinkageIDs(value string) ([]string, bool) {
	var ids []string
	for i := 0; i < len(value); {
		switch value[i] {
		case ' ', '\t', '\r', '\n':
			i++
		case '(':
			depth := 1
			i++
			for i < len(value) && depth > 0 {
				if value[i] == '\\' {
					i += 2
					continue
				}
				if value[i] == '(' {
					depth++
				}
				if value[i] == ')' {
					depth--
				}
				i++
			}
			if depth != 0 || i > len(value) {
				return nil, false
			}
		case '<':
			end := strings.IndexByte(value[i+1:], '>')
			if end < 0 {
				return nil, false
			}
			id := value[i+1 : i+1+end]
			if !emailLinkageID.MatchString(id) {
				return nil, false
			}
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
			if len(ids) > emailLinkageMaxIDs {
				return nil, false
			}
			i += end + 2
		default:
			return nil, false
		}
	}
	return ids, true
}

type emailLinkageError string

func (e emailLinkageError) Error() string { return string(e) }

func emailLinkageHeader(reader io.Reader) (mail.Header, error) {
	buffer := bufio.NewReader(io.LimitReader(reader, maxRecoveryHeaderBytes+1))
	var header bytes.Buffer
	for {
		line, err := buffer.ReadString('\n')
		if header.Len()+len(line) > maxRecoveryHeaderBytes {
			return nil, emailLinkageError("header_limit")
		}
		header.WriteString(line)
		if line == "\r\n" || line == "\n" {
			break
		}
		if err != nil {
			return nil, emailLinkageError("invalid_headers")
		}
	}
	message, err := mail.ReadMessage(bytes.NewReader(header.Bytes()))
	if err != nil {
		return nil, emailLinkageError("invalid_headers")
	}
	return message.Header, nil
}

func (e *EmailLinkage) walkParts(reader io.Reader, contentType string, depth int, count *int) {
	if contentType == "" {
		return
	}
	kind, params, err := stdmime.ParseMediaType(contentType)
	if err != nil {
		e.incomplete("invalid_mime")
		return
	}
	if kind == "multipart/report" || !strings.HasPrefix(kind, "multipart/") {
		return
	}
	if depth >= emailLinkageMaxDepth {
		e.incomplete("depth_limit")
		return
	}
	if params["boundary"] == "" {
		e.incomplete("invalid_mime")
		return
	}
	parts := multipart.NewReader(reader, params["boundary"])
	for {
		part, err := parts.NextPart()
		if err == io.EOF {
			return
		}
		if err != nil {
			e.incomplete("invalid_mime")
			return
		}
		*count++
		if *count > emailLinkageMaxParts {
			_ = part.Close()
			e.incomplete("part_limit")
			return
		}
		partType, _, typeErr := stdmime.ParseMediaType(part.Header.Get("Content-Type"))
		if typeErr != nil && part.Header.Get("Content-Type") != "" {
			e.incomplete("invalid_mime")
		}
		if partType == "message/rfc822" {
			var content io.Reader = part
			if strings.EqualFold(part.Header.Get("Content-Transfer-Encoding"), "base64") {
				content = base64.NewDecoder(base64.StdEncoding, part)
			}
			header, headerErr := emailLinkageHeader(content)
			if headerErr != nil {
				e.incomplete(headerErr.Error())
			} else if id := e.singleID(header); id != "" && !slices.Contains(e.AttachedOriginals, id) {
				if len(e.AttachedOriginals) >= emailLinkageMaxIDs {
					e.incomplete("identity_limit")
				} else {
					e.AttachedOriginals = append(e.AttachedOriginals, id)
				}
			}
		} else {
			e.walkParts(part, part.Header.Get("Content-Type"), depth+1, count)
		}
		_ = part.Close()
	}
}
