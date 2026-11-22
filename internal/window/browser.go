//go:build !webview

package window

// Open shows the UI at url using the default browser.
func Open(url string) error { return openBrowser(url) }
