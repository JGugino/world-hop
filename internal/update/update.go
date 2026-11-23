// Package update checks GitHub for a newer WorldHop release. It only tells the
// user a newer version exists and where to download it; it never downloads or
// installs anything itself, so there is nothing to verify and nothing that can
// replace the app without the user's say-so. Every failure (offline, private
// repository, rate limit, odd response) is quiet: the check just reports
// nothing and tries again later.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// LatestURL is the GitHub API endpoint for the newest published
	// (non-draft, non-prerelease) release.
	LatestURL = "https://api.github.com/repos/JGugino/world-hopper/releases/latest"
	// pagePrefixURL is the only place a release link may point; anything else in
	// the response is ignored so the app never offers an arbitrary link.
	pagePrefix = "https://github.com/JGugino/world-hopper/"
	// Interval is how often a check is due.
	Interval  = 24 * time.Hour
	stateFile = "update.json"
)

// Info is what the UI and tray show.
type Info struct {
	Available bool   `json:"available"`
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	// URL is the release page to open to download the new version.
	URL string `json:"url,omitempty"`
}

type saved struct {
	CheckedAt time.Time `json:"checkedAt"`
	Latest    string    `json:"latest"`
	URL       string    `json:"url"`
}

// Checker looks for new releases at most once a day.
type Checker struct {
	Current string // this build's version, e.g. "v0.1.0"; "dev" disables checking
	URL     string // defaults to LatestURL
	HTTP    *http.Client
	// OnChange is called (from a background goroutine) when the available
	// update changes, so the tray can follow.
	OnChange func(Info)

	dir string
	now func() time.Time
	mu  sync.Mutex
	st  saved
}

// New returns a checker that keeps its last result in dir so a restart does
// not trigger another request.
func New(current, dir string) *Checker {
	c := &Checker{Current: current, URL: LatestURL, dir: dir, now: time.Now,
		HTTP: &http.Client{Timeout: 15 * time.Second}}
	if b, err := os.ReadFile(filepath.Join(dir, stateFile)); err == nil {
		json.Unmarshal(b, &c.st)
	}
	return c
}

// Info reports the newest known release compared with this build.
func (c *Checker) Info() Info {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.infoLocked()
}

func (c *Checker) infoLocked() Info {
	i := Info{Current: c.Current}
	if cur, ok := parse(c.Current); ok && c.st.Latest != "" && safeURL(c.st.URL) {
		if lat, ok := parse(c.st.Latest); ok && newer(lat, cur) {
			i.Available, i.Latest, i.URL = true, c.st.Latest, c.st.URL
		}
	}
	return i
}

// Run checks now if a check is due, then keeps checking once a day until ctx
// ends. Call it in a goroutine; it never blocks startup.
func (c *Checker) Run(ctx context.Context) {
	if _, ok := parse(c.Current); !ok {
		return // development build
	}
	if i := c.Info(); i.Available && c.OnChange != nil {
		c.OnChange(i)
	}
	for {
		c.mu.Lock()
		wait := Interval - c.now().Sub(c.st.CheckedAt)
		c.mu.Unlock()
		if wait <= 0 {
			c.Check(ctx)
			wait = Interval
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(min(wait, time.Hour)):
		}
	}
}

// Check asks GitHub once. Failures are swallowed (and retried at the next
// interval) apart from being returned for tests.
func (c *Checker) Check(ctx context.Context) error {
	tag, page, err := c.fetch(ctx)
	c.mu.Lock()
	before := c.infoLocked()
	// Record the attempt even on failure so an offline or private-repo
	// install does not retry every hour.
	c.st.CheckedAt = c.now()
	if err == nil {
		c.st.Latest, c.st.URL = tag, page
	}
	after := c.infoLocked()
	b, _ := json.Marshal(c.st)
	c.mu.Unlock()
	os.WriteFile(filepath.Join(c.dir, stateFile), b, 0o600)
	if after != before && c.OnChange != nil {
		c.OnChange(after)
	}
	return err
}

func (c *Checker) fetch(ctx context.Context) (tag, page string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "WorldHop-update-check")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK { // 404: no releases or private repo; 403: rate limited
		return "", "", errors.New("update check: http " + strconv.Itoa(resp.StatusCode))
	}
	var rel struct {
		Tag        string `json:"tag_name"`
		HTMLURL    string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return "", "", err
	}
	if rel.Draft || rel.Prerelease {
		return "", "", errors.New("update check: not a stable release")
	}
	if _, ok := parse(rel.Tag); !ok || !safeURL(rel.HTMLURL) {
		return "", "", errors.New("update check: unexpected response")
	}
	return rel.Tag, rel.HTMLURL, nil
}

func safeURL(u string) bool { return strings.HasPrefix(u, pagePrefix) }

// parse reads "v1.2.3" or "1.2.3" (anything after the patch number, such as
// "-rc1", is ignored). Non-version strings like "dev" return false.
func parse(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, _, _ = strings.Cut(v, "+")
	v, _, _ = strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func newer(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}
