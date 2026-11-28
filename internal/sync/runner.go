// runner.go: the Runner. Where the Engine syncs one world, the Runner runs a whole pass
// over every world, one pass at a time, and keeps the status the tray tooltip and the
// web view's status panel show. The per-world actions the web view offers (resolve,
// restore, download...) are in runner_worlds.go.

// runner.go: the Runner. Where the Engine syncs one world, the Runner runs a whole pass
// over every world, one pass at a time, and keeps the status the tray tooltip and the
// web view's status panel show. The per-world actions the web view offers (resolve,
// restore, download...) are in runner_worlds.go.

package sync

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	stdsync "sync"
	"time"

	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/worlds"
)

// RunnerStatus is the status shape served to the UI (/api/status).
type RunnerStatus struct {
	State      string     `json:"state"` // "idle" | "syncing"
	LastSync   *time.Time `json:"lastSync"`
	LastResult string     `json:"lastResult"`
	// Outcome summarises the last pass: "" (none yet), "ok", "waiting" (only
	// waiting for Minecraft to close), "attention" (needs a choice) or "error".
	Outcome string `json:"outcome"`
	// Result counts what the last pass did.
	Result PassResult `json:"result"`
	// Progress says what is happening right now; set only while syncing.
	Progress *Progress `json:"progress"`
	// Problems explains, in plain words, what kept the last pass from
	// finishing, grouped by kind, each with the worlds it affects.
	Problems []Problem `json:"problems"`
	// Warnings lists worlds that need attention, e.g. a newer version exists
	// but the world is open in Minecraft.
	Warnings []string `json:"warnings"`
	Version  string   `json:"version"`
}

// PassResult counts what a sync pass did.
type PassResult struct {
	Total      int `json:"total"` // worlds looked at
	Uploaded   int `json:"uploaded"`
	Downloaded int `json:"downloaded"` // updated from the cloud, plus new worlds fetched
	UpToDate   int `json:"upToDate"`
}

// Progress is the step a running pass is on.
type Progress struct {
	World string `json:"world"`
	// Action is "checking", "uploading" or "downloading".
	Action string `json:"action"`
	Done   int    `json:"done"`  // worlds finished before this one
	Total  int    `json:"total"` // worlds in this pass
}

// Runner drives the Engine for the tray and local API: it tracks sync state
// for the UI and runs one sync pass at a time over the discovered worlds.
type Runner struct {
	engine *Engine
	list   func() []worlds.World

	// Limited, if set, reports whether the license keeps a world from syncing
	// (without claiming the free slot). It only affects what the UI shows;
	// the Engine's Authorize hook is what actually enforces the limit.
	Limited func(worldID string) bool
	// SavesFolders returns the configured saves-folder sources: the only
	// places a cloud world may be downloaded into. Optional.
	SavesFolders func() []string
	// AutoDownload makes a full sync pass fetch cloud-only worlds this
	// computer has never had, when there is exactly one saves folder to put
	// them in. It only ever creates new folders.
	AutoDownload bool

	plainChecked time.Time // when maintain last looked for a plaintext copy (guarded by pass)

	pass   stdsync.Mutex // one sync pass at a time
	mu     stdsync.Mutex
	status RunnerStatus
}

// NewRunner returns a Runner. engine may be nil (no store configured yet), in
// which case a sync only reports that nothing is configured. list returns the
// worlds to sync and may be nil.
func NewRunner(version string, engine *Engine, list func() []worlds.World) *Runner {
	msg := "No sync has run yet."
	if engine == nil {
		msg = "No sync store configured yet."
	}
	r := &Runner{
		engine: engine,
		list:   list,
		status: RunnerStatus{State: "idle", LastResult: msg, Version: version},
	}
	if engine != nil {
		engine.OnPhase = r.setPhase
	}
	return r
}

// setPhase is the Engine's OnPhase hook: it moves the running pass from
// "checking" to "uploading" or "downloading" once the Engine knows which.
func (r *Runner) setPhase(_, phase string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status.Progress != nil {
		r.status.Progress.Action = phase
	}
}

// setProgress records which world the running pass is on (nil when idle).
func (r *Runner) setProgress(p *Progress) {
	r.mu.Lock()
	r.status.Progress = p
	r.mu.Unlock()
}

// Status returns a snapshot of the current state.
func (r *Runner) Status() RunnerStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// ForceSync runs a sync pass over every world now. If one is already running
// it returns the current status without starting another.
func (r *Runner) ForceSync(ctx context.Context) RunnerStatus {
	if !r.pass.TryLock() {
		return r.Status()
	}
	defer r.pass.Unlock()
	return r.runPass(ctx, r.listWorlds(), true)
}

// SyncWorlds syncs just the given worlds, waiting for any pass already in
// progress to finish first (so a world that just closed is never dropped).
func (r *Runner) SyncWorlds(ctx context.Context, ws []worlds.World) RunnerStatus {
	r.pass.Lock()
	defer r.pass.Unlock()
	return r.runPass(ctx, ws, false)
}

// SyncAll is ForceSync but waits for a running pass instead of skipping.
func (r *Runner) SyncAll(ctx context.Context) RunnerStatus {
	r.pass.Lock()
	defer r.pass.Unlock()
	return r.runPass(ctx, r.listWorlds(), true)
}

// listWorlds returns the worlds to sync right now. The list is re-resolved from the
// configured sources on every call, so new worlds and source edits are picked up
// without a restart.
func (r *Runner) listWorlds() []worlds.World {
	if r.list == nil {
		return nil
	}
	return r.list()
}

// runPass runs one pass. full means a pass over everything (not a subset of
// worlds that just closed), which is when cloud-only worlds are fetched.
func (r *Runner) runPass(ctx context.Context, ws []worlds.World, full bool) RunnerStatus {
	r.mu.Lock()
	r.status.State = "syncing"
	r.status.Progress = &Progress{Action: "checking", Total: len(ws)}
	r.mu.Unlock()

	out := r.run(ctx, ws, full)

	r.mu.Lock()
	now := time.Now()
	r.status.State = "idle"
	r.status.Progress = nil
	// "Last sync" means the last time something actually synced, so a pass
	// that only hit errors (offline, signed out) leaves it alone.
	if out.outcome != "error" || out.result.Uploaded+out.result.Downloaded+out.result.UpToDate > 0 {
		r.status.LastSync = &now
	}
	r.status.LastResult = out.message
	r.status.Outcome = out.outcome
	r.status.Result = out.result
	r.status.Problems = out.problems
	r.status.Warnings = out.warnings
	st := r.status
	r.mu.Unlock()

	// Housekeeping runs after the result is shown (the pass lock is still held, so
	// no sync overlaps it) and only when the pass went well: a cloud that is
	// misbehaving is no time to delete from it.
	if full && out.outcome == "ok" && ctx.Err() == nil {
		r.maintain(ctx)
	}
	return st
}

// PlaintextRemover is implemented by the store when encryption can be turned on:
// it removes the unencrypted copy of worlds whose encrypted copy is in place (see
// crypt.Manager.RemovePlaintext).
type PlaintextRemover interface {
	// RemovePlaintext returns how many worlds' plaintext copies it removed.
	RemovePlaintext(ctx context.Context) (removed int, err error)
}

// plaintextEvery limits how often a computer looks for a plaintext copy to remove:
// the answer rarely changes, and looking costs a listing request.
const plaintextEvery = time.Hour

// maintain does the cloud housekeeping that a good pass allows: removing the
// plaintext copy once encryption holds its replacement, and deleting old versions
// of worlds (at most daily, see Engine.Cleanup). Failures are logged and retried
// later; they never turn a good sync into a problem.
func (r *Runner) maintain(ctx context.Context) {
	e := r.engine
	if pr, ok := e.Store.(PlaintextRemover); ok && time.Since(r.plainChecked) >= plaintextEvery {
		r.plainChecked = time.Now()
		if n, err := pr.RemovePlaintext(ctx); err != nil && !errors.Is(err, store.ErrUnsupported) {
			log.Printf("remove unencrypted copy: %v", err)
		} else if n > 0 {
			log.Printf("removed the unencrypted copy of %d world(s) from the cloud", n)
		}
	}
	if !e.CleanupDue() || ctx.Err() != nil {
		return
	}
	res, err := e.Cleanup(ctx)
	switch {
	case errors.Is(err, store.ErrUnsupported):
	case err != nil:
		log.Printf("cloud cleanup: %v", err)
	case res.Manifests > 0:
		log.Printf("cloud cleanup removed %d old version(s) and %d pack(s) of %d world(s)", res.Manifests, res.Packs, res.Worlds)
	}
	if ctx.Err() == nil {
		e.MarkCleaned()
	}
}

// passOutcome is what one pass found.
type passOutcome struct {
	message  string
	outcome  string
	result   PassResult
	problems []Problem
	warnings []string
}

// globalKinds are problems that would hit every world the same way (no
// connection, signed out, storage full...), so the pass stops at the first
// instead of retrying each world for minutes.
var globalKinds = map[string]bool{
	ProblemOffline: true, ProblemAuth: true, ProblemNotLinked: true, ProblemQuota: true,
	ProblemBusy: true, ProblemServer: true, ProblemLocked: true, ProblemKey: true,
	ProblemPro: true, ProblemStorageSetup: true,
}

// problemList groups problems by kind, keeping first-seen order.
type problemList struct {
	list []Problem
	idx  map[string]int
}

// add records a problem for a world (or for no world in particular, with an empty
// name). The first problem of a kind is kept; later ones only add their world.
func (l *problemList) add(p Problem, world string) {
	if l.idx == nil {
		l.idx = map[string]int{}
	}
	i, ok := l.idx[p.Kind]
	if !ok {
		i = len(l.list)
		l.idx[p.Kind] = i
		l.list = append(l.list, p)
		l.list[i].Worlds = nil
	}
	if world != "" {
		l.list[i].Worlds = append(l.list[i].Worlds, world)
	}
}

// run does the actual work of a pass: for each world it calls Engine.Sync and
// sorts the result into counts, problems and warnings. A problem that would hit every
// world the same way (see globalKinds) stops the pass immediately. A full pass also
// fetches cloud-only worlds (autoDownload). It never decides to resolve a conflict or
// repair lost data; those become problems for the user.
func (r *Runner) run(ctx context.Context, ws []worlds.World, full bool) passOutcome {
	if r.engine == nil {
		return passOutcome{message: "No sync store configured yet.", outcome: "attention",
			problems: []Problem{Explain(ErrNoStore)}}
	}
	if len(ws) == 0 && !(full && r.canAutoDownload()) {
		return passOutcome{message: "No worlds found.", outcome: "attention", problems: []Problem{{
			Kind: "no-worlds", Level: LevelWarn,
			Title:  "WorldHop didn't find any Minecraft worlds",
			Action: "Add your saves folder on the Sources tab.",
			Fix:    "/sources"}}}
	}
	var res PassResult
	res.Total = len(ws)
	var probs problemList
	var warnings []string
	var stopped bool
	for i, w := range ws {
		if ctx.Err() != nil {
			return passOutcome{message: "Sync cancelled.", outcome: "ok", result: res, problems: probs.list, warnings: warnings}
		}
		r.setProgress(&Progress{World: w.Name, Action: "checking", Done: i, Total: len(ws)})
		sr, err := r.engine.Sync(ctx, w)
		if err == nil || errors.Is(err, ErrWorldOpen) {
			switch {
			case sr.Pushed:
				res.Uploaded++
			case sr.Pulled:
				res.Downloaded++
			case sr.Status == StatusConflict:
				probs.add(Explain(&ConflictError{WorldID: w.ID}), w.Name)
			case sr.Status == StatusRemoteAhead && err != nil:
				warnings = append(warnings, fmt.Sprintf("%s has a newer version in the cloud but is open in Minecraft. Close it to update.", w.Name))
			case err == nil:
				res.UpToDate++ // up to date, or a cloud-only world with no local copy
			}
		}
		if err == nil {
			continue
		}
		p := Explain(err)
		if globalKinds[p.Kind] {
			probs.add(p, "")
			stopped = true
			break
		}
		probs.add(p, w.Name)
	}
	if full && ctx.Err() == nil && !stopped {
		n, err := r.autoDownload(ctx, ws)
		res.Downloaded += n
		if err != nil {
			probs.add(Explain(err), "")
		}
	}
	return passOutcome{
		message:  summarize(res),
		outcome:  outcomeOf(probs.list),
		result:   res,
		problems: probs.list,
		warnings: warnings,
	}
}

// outcomeOf summarises a list of problems as one outcome: "error" if any failed,
// "attention" if any needs a choice, "waiting" if there are only informational
// ones (like a world that is open), otherwise "ok".
func outcomeOf(ps []Problem) string {
	out := "ok"
	for _, p := range ps {
		switch {
		case p.Level == LevelError:
			return "error"
		case p.Level == LevelWarn:
			out = "attention"
		case out == "ok":
			out = "waiting"
		}
	}
	return out
}

// plural formats a count with the right noun ("1 world", "2 worlds").
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// summarize is the one-line result of a pass, e.g. "1 uploaded, 2 up to date."
// Anything that went wrong is in Problems, not here.
func summarize(r PassResult) string {
	var parts []string
	if r.Uploaded > 0 {
		parts = append(parts, fmt.Sprintf("%d uploaded", r.Uploaded))
	}
	if r.Downloaded > 0 {
		parts = append(parts, fmt.Sprintf("%d downloaded", r.Downloaded))
	}
	if r.UpToDate > 0 {
		parts = append(parts, fmt.Sprintf("%d up to date", r.UpToDate))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Checked %s.", plural(r.Total, "world", "worlds"))
	}
	return "Synced: " + strings.Join(parts, ", ") + "."
}
