package sync

import (
	"fmt"
	"strings"
	"time"
)

// Ago says how long ago t was, e.g. "just now" or "5 min ago".
func Ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return t.Format("2 Jan")
}

// Headline is the status in one line, for places with no room for more, like
// the tray tooltip (Windows cuts those at 127 characters).
func (s RunnerStatus) Headline(now time.Time) string {
	var line string
	switch {
	case s.State == "syncing":
		line = "Syncing…"
		if p := s.Progress; p != nil && p.World != "" {
			verb := map[string]string{"uploading": "Uploading", "downloading": "Downloading"}[p.Action]
			if verb == "" {
				verb = "Checking"
			}
			line = fmt.Sprintf("%s %s (%d of %d)", verb, p.World, min(p.Done+1, max(p.Total, 1)), max(p.Total, 1))
		}
	case len(s.Problems) > 0:
		p := s.Problems[0]
		line = p.Title
		if p.Kind == ProblemWorldOpen && len(p.Worlds) > 0 {
			line = "Waiting for Minecraft to close: " + strings.Join(p.Worlds, ", ")
		}
	case s.LastSync != nil:
		line = "Up to date · synced " + Ago(*s.LastSync, now)
	default:
		line = "No sync yet"
	}
	r := []rune("WorldHop: " + line)
	if len(r) > 120 {
		r = append(r[:119], '…')
	}
	return string(r)
}
