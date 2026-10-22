package mcp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/query/querytest"
)

func TestServerImplementationDeclaresIcon(t *testing.T) {
	checks := assert.New(t)
	must := require.New(t)

	implementation := serverImplementation()

	checks.Equal("msgvault", implementation.Name)
	checks.Equal("https://msgvault.unstaticlabs.com", implementation.WebsiteURL)
	must.Len(implementation.Icons, 1)
	icon := implementation.Icons[0]
	checks.Equal("https://msgvault-mcp.unstaticlabs.com/icon.png", icon.Source)
	checks.Equal("image/png", icon.MIMEType)
	checks.Equal([]string{"128x128"}, icon.Sizes)
	// A data URI would ride along on every initialize response.
	checks.NotContains(icon.Source, "data:")
}

func TestIconRoutesServeTheDeclaredAssets(t *testing.T) {
	// The listener requires a bearer credential for /mcp; the icon a client
	// renders beside the sign-in prompt must be reachable without one.
	handler := newMCPHTTPServer(ServeOptions{Engine: &querytest.MockEngine{}}, HTTPOptions{
		APIKey: "test-key",
	}).Handler

	tests := []struct {
		name        string
		path        string
		contentType string
		want        []byte
	}{
		{
			name:        "declared icon",
			path:        serverIconPath,
			contentType: "image/png",
			want:        serverIconPNG,
		},
		{
			name:        "browser favicon",
			path:        serverFaviconPath,
			contentType: "image/vnd.microsoft.icon",
			want:        serverFaviconICO,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checks := assert.New(t)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.path, nil))

			require.Equal(t, http.StatusOK, recorder.Code)
			checks.Equal(tc.contentType, recorder.Header().Get("Content-Type"))
			checks.Equal("nosniff", recorder.Header().Get("X-Content-Type-Options"))
			checks.Equal(iconCacheControl, recorder.Header().Get("Cache-Control"))
			checks.NotEmpty(tc.want)
			checks.Equal(tc.want, recorder.Body.Bytes())
		})
	}
}

func TestIconRouteRefusesWrites(t *testing.T) {
	checks := assert.New(t)
	handler := newMCPHTTPServer(ServeOptions{Engine: &querytest.MockEngine{}}, HTTPOptions{}).Handler

	// The declaration and the route must not drift apart: a client resolves
	// the absolute source, whose path is the one this listener serves.
	checks.Equal(serverIconURL, "https://msgvault-mcp.unstaticlabs.com"+serverIconPath)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, serverIconPath, nil))
	checks.Equal(http.StatusMethodNotAllowed, recorder.Code)
}

func TestInitializeCarriesTheIconDeclaration(t *testing.T) {
	checks := assert.New(t)
	must := require.New(t)
	handler := newMCPHTTPServer(ServeOptions{Engine: &querytest.MockEngine{}}, HTTPOptions{}).Handler

	recorder, initialized := task5LegacyHTTPPost(t, handler, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"icon-test","version":"icon-test-version"}}}`)
	task3RequireSuccess(t, recorder, initialized)

	info, ok := initialized.Result["serverInfo"].(map[string]any)
	must.True(ok, "result: %#v", initialized.Result)
	checks.Equal("https://msgvault.unstaticlabs.com", info["websiteUrl"])
	icons, ok := info["icons"].([]any)
	must.True(ok, "serverInfo: %#v", info)
	must.Len(icons, 1)
	checks.Equal(map[string]any{
		"src":      "https://msgvault-mcp.unstaticlabs.com/icon.png",
		"mimeType": "image/png",
		"sizes":    []any{"128x128"},
	}, icons[0])
}
