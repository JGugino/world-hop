//go:build webview

package window

import (
	"sync/atomic"

	webview "github.com/webview/webview_go"
)

var open atomic.Bool

// Open shows the UI in a native webview window. The webview must run on its own
// OS thread; if it can't be created we fall back to the default browser.
// onClose runs after the window closes. Only one window is shown at a time.
func Open(url string, onClose func()) error {
	if !open.CompareAndSwap(false, true) {
		return nil
	}
	go func() {
		defer open.Store(false)
		if onClose != nil {
			defer onClose()
		}
		w := webview.New(false)
		if w == nil {
			_ = openBrowser(url)
			return
		}
		defer w.Destroy()
		w.SetTitle("WorldHop")
		w.SetSize(900, 650, webview.HintNone)
		w.Navigate(url)
		w.Run()
	}()
	return nil
}
