//go:build windows

// icon_windows.go: the tray icon on Windows, which needs ICO data.

package tray

import "github.com/JGugino/world-hopper/assets"

// Windows' tray needs ICO data, so use the multi-size .ico.
var icon = assets.TrayICO
