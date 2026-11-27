//go:build !windows

// icon_other.go: the tray icon on Linux and macOS, which take PNG data.

package tray

import "github.com/JGugino/world-hopper/assets"

var icon = assets.TrayPNG
