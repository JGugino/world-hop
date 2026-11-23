//go:build windows

package tray

import "github.com/JGugino/world-hopper/assets"

// Windows' tray needs ICO data, so use the multi-size .ico.
var icon = assets.TrayICO
