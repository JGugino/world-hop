// Package window opens the web UI: in a native webview when built with
// `-tags webview`, otherwise in the user's default browser.
package window

import (
	"fmt"
	"os/exec"
	"runtime"
)

// OpenBrowser opens url in the default browser.
func OpenBrowser(url string) error { return openBrowser(url) }

// openBrowser opens url in the default browser.
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	case "linux":
		return exec.Command("xdg-open", url).Start()
	}
	return fmt.Errorf("unsupported platform %s", runtime.GOOS)
}
