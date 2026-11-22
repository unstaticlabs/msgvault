package imap

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDraftHTMLRetainsStyledSpans(t *testing.T) {
	for _, tc := range []struct {
		name, html, plain, edited string
		keep                      []string
	}{
		{"across styles", "<b>Blue sky</b><i> ahead</i>", "Blue sky ahead", "Blue lake ahead", []string{"<b>Blue lake</b>", "<i> ahead</i>"}},
		{"replace across styles", "<b>A11</b><i>22Z</i>", "A1122Z", "AxxZ", []string{"<b>Axx</b>", "<i>Z</i>"}},
		{"quote and link", `<div>Héllo &amp; salut</div><blockquote cite="q"><a href="https://example.test">Source</a></blockquote>`, "Héllo & salut\n> Source", "Héllo <team> & salut\n> Source", []string{"Héllo &lt;team&gt; &amp; salut", `<blockquote cite="q"><a href="https://example.test">Source</a></blockquote>`}},
		{"nonbreaking space", "<b>Hello&nbsp;team</b>", "Hello team", "Bonjour team", []string{"Bonjour\u00a0team"}},
		{"new line", "<b>Hello</b>", "Hello", "Hello\nagain", []string{"<b>Hello<br>again</b>"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := editDraftHTML(tc.html, tc.plain, tc.edited)
			require.NoError(t, err)
			for _, fragment := range tc.keep {
				assert.Contains(t, result, fragment)
			}
		})
	}
}

func TestRichDraftPreservesInlineAndAttachedParts(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	raw := strings.ReplaceAll(`From: owner@example.test
To: to@example.test
Subject: Synthetic fixture
Content-Type: multipart/mixed; boundary=m

--m
Content-Type: multipart/alternative; boundary=a

--a
Content-Type: text/plain; charset=utf-8

old
--a
Content-Type: multipart/related; boundary=r

--r
Content-Type: text/html

<div><b>old</b><img src="cid:sample"></div>
--r
Content-Type: image/png
Content-ID: <sample>
Content-Transfer-Encoding: base64

YWJj
--r--
--a--
--m
Content-Type: application/octet-stream; name="example.bin"
Content-Disposition: attachment; filename="example.bin"
Content-Transfer-Encoding: base64

MTIzNA==
--m--
`, "\n", "\r\n")
	for _, build := range []func([]byte, string, time.Time, string) (ReplyDraft, error){BuildDraftReplacement, BuildIMAPDraftReplacement} {
		got, err := build([]byte(raw), "new", time.Now(), "new@example.test")
		require.NoError(err)
		assert.Equal("new", strings.TrimSpace(got.Parsed.BodyText))
		assert.Contains(got.Parsed.BodyHTML, `<b>new</b><img src="cid:sample">`)
		assert.Contains(string(got.Raw), "\r\nYWJj\r\n")
		assert.Contains(string(got.Raw), "\r\nMTIzNA==\r\n")
		assert.Contains(string(got.Raw), "Content-Id: <sample>")
	}
}

func TestRichDraftExplicitHTMLForDifferentAlternatives(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nContent-Type: multipart/alternative; boundary=x\r\n\r\n--x\r\nContent-Type: text/plain\r\n\r\nFirst\r\n--x\r\nContent-Type: text/html\r\n\r\n<b>Premier</b>\r\n--x--\r\n")
	_, err := BuildDraftReplacement(raw, "Second", time.Now(), "")
	require.ErrorContains(err, "body_html")
	formatted := "<b>Deuxième</b>"
	got, err := BuildRichDraftReplacement(raw, "Second", &formatted, time.Now(), "")
	require.NoError(err)
	assert.Equal("Second", strings.TrimSpace(got.Parsed.BodyText))
	assert.Equal(formatted, strings.TrimSpace(got.Parsed.BodyHTML))
}
