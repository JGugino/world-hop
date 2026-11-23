// Package assets embeds the app icon. Source and generator live in assets/icon.
package assets

import _ "embed"

// TrayPNG is the tray icon for Linux and macOS (StatusNotifier and NSStatusItem
// take PNG data).
//
//go:embed icon/png/icon-64.png
var TrayPNG []byte

// TrayICO is the multi-size icon for the Windows tray; fyne.io/systray
// requires ICO bytes there.
//
//go:embed icon/worldhop.ico
var TrayICO []byte
