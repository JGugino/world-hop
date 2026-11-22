//go:build !webview

package window

// Open shows the UI at url using the default browser. onClose is unused here: a
// browser tab can't be observed, so the page reports closing via POST /api/bye.
func Open(url string, onClose func()) error { return openBrowser(url) }
