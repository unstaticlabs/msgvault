package mcp

import (
	"bytes"
	_ "embed"
	"net/http"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverIconPNG is the icon declared in serverInfo. It is a 128x128
// palette-quantised PNG (a few kilobytes) rather than the full-size brand
// asset: an icon a client cannot fetch quickly is worse than no icon.
//
//go:embed assets/icon.png
var serverIconPNG []byte

// serverFaviconICO is the same mark for browsers, which request /favicon.ico
// on their own when a person opens the listener's hostname.
//
//go:embed assets/favicon.ico
var serverFaviconICO []byte

const (
	serverIconPath    = "/icon.png"
	serverFaviconPath = "/favicon.ico"
	// serverIconURL is absolute because a client fetches the icon out of band,
	// with nothing to resolve a relative source against. Naming this
	// deployment's hostname is what makes the declaration resolvable, and is
	// also what keeps this commit downstream-only.
	serverIconURL = "https://msgvault-mcp.unstaticlabs.com" + serverIconPath
	// serverWebsiteURL is the human-facing side of the same deployment.
	serverWebsiteURL = "https://msgvault.unstaticlabs.com"
	// iconCacheControl lets clients hold the icon for a day. The bytes only
	// change with a new image, and the listener is otherwise no-store.
	iconCacheControl = "public, max-age=86400"
)

// serverImplementation identifies this server to clients. The icons field is
// SEP-973 (protocol version 2025-11-25); no client we use renders it yet, so
// declaring it changes nothing visible today and starts working the day one
// does.
func serverImplementation() *sdkmcp.Implementation {
	return &sdkmcp.Implementation{
		Name:       "msgvault",
		Version:    "1.0.0",
		WebsiteURL: serverWebsiteURL,
		Icons: []sdkmcp.Icon{{
			Source:   serverIconURL,
			MIMEType: "image/png",
			Sizes:    []string{"128x128"},
		}},
	}
}

// registerIconRoutes serves the declared icon from the listener that declares
// it. Both routes are unauthenticated: a client renders the icon beside the
// sign-in prompt, before it holds any credential, and the bytes are a public
// brand asset.
func registerIconRoutes(mux *http.ServeMux) {
	mux.Handle(serverIconPath, iconHandler("icon.png", "image/png", serverIconPNG))
	mux.Handle(serverFaviconPath, iconHandler("favicon.ico", "image/vnd.microsoft.icon", serverFaviconICO))
}

func iconHandler(name, contentType string, content []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", iconCacheControl)
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(content))
	})
}
