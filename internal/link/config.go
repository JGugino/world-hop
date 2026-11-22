package link

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
)

// Config holds the OAuth client settings. Nothing is compiled in: client IDs
// come from the environment or an optional cloud.json in the state directory
// (environment wins). Client IDs for installed apps are not secrets, but they
// are per-developer, so they are configured rather than hard-coded.
//
//	WORLDHOP_GOOGLE_CLIENT_ID      Google OAuth client (type "Desktop app")
//	WORLDHOP_GOOGLE_CLIENT_SECRET  optional; Google issues one for desktop clients
//	WORLDHOP_DROPBOX_APP_KEY       Dropbox app key (App Folder access)
//	WORLDHOP_DROPBOX_REDIRECT_PORT optional fixed loopback port, if the Dropbox
//	                               app must register an exact redirect URI
//
// cloud.json uses the keys googleClientId, googleClientSecret, dropboxAppKey
// and dropboxRedirectPort.
type Config struct {
	GoogleClientID      string `json:"googleClientId"`
	GoogleClientSecret  string `json:"googleClientSecret"`
	DropboxAppKey       string `json:"dropboxAppKey"`
	DropboxRedirectPort int    `json:"dropboxRedirectPort"`
}

// LoadConfig reads stateDir/cloud.json (if present) then applies the environment.
func LoadConfig(stateDir string) (Config, error) {
	var c Config
	b, err := os.ReadFile(filepath.Join(stateDir, "cloud.json"))
	if err == nil {
		if err := json.Unmarshal(b, &c); err != nil {
			return c, err
		}
	} else if !os.IsNotExist(err) {
		return c, err
	}
	if v := os.Getenv("WORLDHOP_GOOGLE_CLIENT_ID"); v != "" {
		c.GoogleClientID = v
	}
	if v := os.Getenv("WORLDHOP_GOOGLE_CLIENT_SECRET"); v != "" {
		c.GoogleClientSecret = v
	}
	if v := os.Getenv("WORLDHOP_DROPBOX_APP_KEY"); v != "" {
		c.DropboxAppKey = v
	}
	if v := os.Getenv("WORLDHOP_DROPBOX_REDIRECT_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.DropboxRedirectPort = n
		}
	}
	return c, nil
}
