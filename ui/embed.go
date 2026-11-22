// Package ui embeds the built SvelteKit static site (ui/build).
// Run `npm run build` in ui/ before `go build` to include the real UI.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:build
var embedded embed.FS

// Assets returns the built UI rooted at the site's index.html.
func Assets() fs.FS {
	sub, err := fs.Sub(embedded, "build")
	if err != nil {
		return embedded
	}
	return sub
}
