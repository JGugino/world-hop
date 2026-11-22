// Package tray provides the system tray icon and menu.
package tray

import (
	_ "embed"

	"fyne.io/systray"
)

//go:embed icon.png
var icon []byte

type Handlers struct {
	OpenView  func()
	ForceSync func()
	Exit      func()
}

// Run blocks (on the calling goroutine/thread) until the tray exits.
// onReady is called once the tray is up.
func Run(h Handlers, onReady func()) {
	systray.Run(func() {
		systray.SetIcon(icon)
		systray.SetTitle("WorldHop")
		systray.SetTooltip("WorldHop")
		open := systray.AddMenuItem("Open Web View", "Open the WorldHop window")
		sync := systray.AddMenuItem("Force Sync", "Sync worlds now")
		systray.AddSeparator()
		exit := systray.AddMenuItem("Exit", "Quit WorldHop")
		go func() {
			for {
				select {
				case <-open.ClickedCh:
					h.OpenView()
				case <-sync.ClickedCh:
					go h.ForceSync()
				case <-exit.ClickedCh:
					h.Exit()
					systray.Quit()
					return
				}
			}
		}()
		if onReady != nil {
			onReady()
		}
	}, nil)
}

// Quit stops the tray loop.
func Quit() { systray.Quit() }
