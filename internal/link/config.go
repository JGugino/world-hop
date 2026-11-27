// config.go: where the Google OAuth client IDs come from: compiled in at release, with
// optional developer overrides.

package link

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Build-time defaults. A release binary has the app's own registered OAuth
// client IDs compiled in, so end users just click Connect and sign in; no
// .env or cloud.json is needed. Set them with -ldflags, e.g.
//
//	-X github.com/JGugino/world-hopper/internal/link.DefaultGoogleClientID=...
//
// These are public client identifiers under PKCE, not confidential. Google's
// "Desktop app" client secret is likewise not truly secret (Google says so),
// but it is still sent on token requests, so it is compiled in too. They must
// never be committed to the repository.
var (
	DefaultGoogleClientID     string
	DefaultGoogleClientSecret string
)

// Config holds the OAuth client settings. Compiled-in defaults are used unless
// a developer overrides them, which is optional and not for end users:
// a cloud.json in the state directory, then environment variables (env wins).
//
//	WORLDHOP_GOOGLE_CLIENT_ID      Google OAuth client (type "Desktop app")
//	WORLDHOP_GOOGLE_CLIENT_SECRET  optional; Google issues one for desktop clients
//
// cloud.json uses the keys googleClientId and googleClientSecret.
type Config struct {
	GoogleClientID     string `json:"googleClientId"`
	GoogleClientSecret string `json:"googleClientSecret"`
}

// LoadConfig starts from the compiled-in defaults, then applies the optional
// developer override stateDir/cloud.json, then the environment.
func LoadConfig(stateDir string) (Config, error) {
	c := Config{
		GoogleClientID:     DefaultGoogleClientID,
		GoogleClientSecret: DefaultGoogleClientSecret,
	}
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
	return c, nil
}
