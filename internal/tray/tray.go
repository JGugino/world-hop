// Package tray provides the system tray icon and menu. This is where WorldHop
// lives: there is no window, only the tray icon with Open Web View, Force Sync,
// Exit and (when a newer release exists) Update available.
package tray

import (
	"sync"

	"fyne.io/systray"
)

// Handlers are what the menu items do. OpenView and Exit run on the menu's own
// goroutine; ForceSync and OpenUpdate are started in goroutines of their own, so a
// slow sync or a browser launch never freezes the menu.
type Handlers struct {
	OpenView  func()
	ForceSync func()
	// OpenUpdate opens the release page passed to SetUpdate.
	OpenUpdate func(url string)
	Exit       func()
}

var (
	updMu    sync.Mutex
	updItem  *systray.MenuItem
	updLabel string
	updURL   string
)

// SetUpdate shows an "Update available" menu item for version (or hides it when
// version is empty). Safe to call before the tray is up.
func SetUpdate(version, url string) {
	updMu.Lock()
	defer updMu.Unlock()
	updLabel, updURL = version, url
	applyUpdate()
}

// applyUpdate shows or hides the "Update available" item to match the current
// version label. Callers hold updMu.
func applyUpdate() {
	if updItem == nil {
		return
	}
	if updLabel == "" {
		updItem.Hide()
		return
	}
	updItem.SetTitle("Update available: " + updLabel)
	updItem.Show()
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
		upd := systray.AddMenuItem("Update available", "Open the download page")
		updMu.Lock()
		updItem = upd
		applyUpdate()
		updMu.Unlock()
		systray.AddSeparator()
		exit := systray.AddMenuItem("Exit", "Quit WorldHop")
		go func() {
			for {
				select {
				case <-open.ClickedCh:
					h.OpenView()
				case <-sync.ClickedCh:
					go h.ForceSync()
				case <-upd.ClickedCh:
					updMu.Lock()
					u := updURL
					updMu.Unlock()
					if u != "" && h.OpenUpdate != nil {
						go h.OpenUpdate(u)
					}
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

// SetStatus updates the tray icon's hover text. Call it after the tray is up.
func SetStatus(text string) { systray.SetTooltip(text) }

// Quit stops the tray loop.
func Quit() { systray.Quit() }
