package mime

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"testing/quick"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmailLinkageExplicitHeaders(t *testing.T) {
	assertions := assert.New(t)
	raw := []byte("Message-ID: <Child@example.test>\r\nIn-Reply-To: (old <false@example.test>) <parent@example.test>\r\nReferences: <root@example.test>\r\n <parent@example.test>\r\nSubject: Fwd: unrelated\r\n\r\nMessage-ID: <body@example.test>\r\n")
	got := ExtractEmailLinkage(raw)
	assertions.Equal("Child@example.test", got.MessageID)
	assertions.Equal([]string{"parent@example.test"}, got.Replies)
	assertions.Equal([]string{"root@example.test", "parent@example.test"}, got.References)
	assertions.Empty(got.AttachedOriginals)
	assertions.Equal("complete", got.Outcome)
}

func TestEmailLinkageAttachedOriginal(t *testing.T) {
	original := "Message-ID: <received@example.test>\r\nFrom: sender@example.test\r\nTo: owner@example.test\r\nSubject: Original\r\n\r\nOriginal body.\r\n"
	for _, encoding := range []string{"7bit", "base64"} {
		t.Run(encoding, func(t *testing.T) {
			assertions := assert.New(t)
			requirements := require.New(t)
			payload := original
			if encoding == "base64" {
				payload = base64.StdEncoding.EncodeToString([]byte(original))
			}
			raw := []byte(fmt.Sprintf("Message-ID: <forward@example.test>\r\nFrom: second-owner@example.test\r\nTo: destination@example.test\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=qa\r\n\r\n--qa\r\nContent-Type: text/plain\r\n\r\nPlease handle this.\r\n--qa\r\nContent-Type: message/rfc822\r\nContent-Disposition: attachment; filename=original.eml\r\nContent-Transfer-Encoding: %s\r\n\r\n%s\r\n--qa--\r\n", encoding, payload))
			parsed, err := Parse(raw)
			requirements.NoError(err)
			assertions.Equal("<forward@example.test>", parsed.MessageID)
			got := ExtractEmailLinkage(raw)
			assertions.Equal([]string{"received@example.test"}, got.AttachedOriginals)
			assertions.Equal("complete", got.Outcome)
		})
	}
}

func TestEmailLinkageDoesNotInferFromInlineForwardOrReport(t *testing.T) {
	for _, body := range []string{
		"Subject: Fwd: Original\r\nX-Msgvault-Forward: 1\r\n\r\n---------- Forwarded message ----------\r\nMessage-ID: <received@example.test>\r\n",
		"MIME-Version: 1.0\r\nContent-Type: multipart/report; report-type=delivery-status; boundary=report\r\n\r\n--report\r\nContent-Type: text/plain\r\n\r\nDelivery failed.\r\n--report\r\nContent-Type: message/rfc822\r\n\r\nMessage-ID: <received@example.test>\r\n\r\nOriginal.\r\n--report--\r\n",
	} {
		got := ExtractEmailLinkage([]byte("Message-ID: <notice@example.test>\r\n" + body))
		assert.Equal(t, "notice@example.test", got.MessageID)
		assert.Empty(t, got.AttachedOriginals)
		assert.Empty(t, got.Replies)
	}
}

func TestEmailLinkageMalformedAndRepeatedIdentity(t *testing.T) {
	for _, headers := range []string{
		"Message-ID: <nested<bad@example.test>>\r\n",
		"Message-ID: <first@example.test>\r\nMessage-ID: <second@example.test>\r\n",
		"Message-ID: <spaces inside@example.test>\r\n",
		"Message-ID: <one@example.test> <two@example.test>\r\n",
	} {
		got := ExtractEmailLinkage([]byte(headers + "\r\nBody"))
		assert.Empty(t, got.MessageID)
		assert.Equal(t, "invalid_headers", got.Outcome)
	}
	got := ExtractEmailLinkage([]byte("Message-ID: <valid@example.test>\r\nIn-Reply-To: <same@example.test> <same@example.test>\r\n\r\nBody"))
	assert.Equal(t, []string{"same@example.test"}, got.Replies)
}

func TestEmailLinkageBoundedHeaders(t *testing.T) {
	got := ExtractEmailLinkage([]byte("X-Long: " + strings.Repeat("x", 256*1024) + "\r\nMessage-ID: <too-late@example.test>\r\n\r\nBody"))
	assert.Empty(t, got.MessageID)
	assert.Equal(t, "header_limit", got.Outcome)
}

func TestEmailLinkageMIMELimitsAndFailure(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{name: "missing boundary", raw: "Content-Type: multipart/mixed\r\n\r\nBody", want: "invalid_mime"},
		{name: "broken attached headers", raw: "Content-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: message/rfc822\r\n\r\nnot a header\r\n\r\nBody\r\n--x--\r\n", want: "invalid_headers"},
		{name: "invalid reference", raw: "References: <nested<bad@example.test>>\r\n\r\nBody", want: "invalid_headers"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractEmailLinkage([]byte(tc.raw))
			assert.Equal(t, tc.want, got.Outcome)
			assert.Empty(t, got.AttachedOriginals)
		})
	}
	body := "Content-Type: text/plain\r\n\r\nBody"
	for i := range 9 {
		boundary := fmt.Sprintf("depth%d", i)
		body = "Content-Type: multipart/mixed; boundary=" + boundary + "\r\n\r\n--" + boundary + "\r\n" + body + "\r\n--" + boundary + "--\r\n"
	}
	assert.Equal(t, "depth_limit", ExtractEmailLinkage([]byte(body)).Outcome)
	body = "Content-Type: multipart/mixed; boundary=x\r\n\r\n" + strings.Repeat("--x\r\nContent-Type: text/plain\r\n\r\nBody\r\n", 257) + "--x--\r\n"
	assert.Equal(t, "part_limit", ExtractEmailLinkage([]byte(body)).Outcome)
	assert.Equal(t, "size_limit", ExtractEmailLinkage(make([]byte, EmailLinkageMaxBytes+1)).Outcome)
}

func TestEmailLinkageStrictIdentityGrammar(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  []string
		valid bool
	}{
		{value: "(comment <fake@example.test>) <Actual@example.test>", want: []string{"Actual@example.test"}, valid: true},
		{value: "(nested (escaped \\))) <id@example.test>", want: []string{"id@example.test"}, valid: true},
		{value: "<id@[127.0.0.1]>", want: []string{"id@[127.0.0.1]"}, valid: true},
		{value: "<Case@example.test> <case@example.test>", want: []string{"Case@example.test", "case@example.test"}, valid: true},
		{value: "<id@example.test> <id@example.test>", want: []string{"id@example.test"}, valid: true},
		{value: "<a..b@example.test>"}, {value: "<id@>"}, {value: "<id@example.test>garbage"},
		{value: "(unfinished <id@example.test>"}, {value: "bare@example.test"},
	} {
		got, valid := emailLinkageIDs(tc.value)
		assert.Equal(t, tc.want, got)
		assert.Equal(t, tc.valid, valid)
	}
}

// Generated valid IDs verify exact, case-preserving identity conservation under
// repeated IDs and nested comments, independently of the production matcher.
func TestEmailLinkageIdentityConservation(t *testing.T) {
	require.NoError(t, quick.Check(func(seed []byte) bool {
		id := "Case-" + hex.EncodeToString(seed) + "@example.test"
		input := "(outer (fake <ignored@example.test>)) <" + id + "> <" + id + ">"
		got, valid := emailLinkageIDs(input)
		return valid && len(got) == 1 && got[0] == id
	}, &quick.Config{MaxCount: 500}))
}
