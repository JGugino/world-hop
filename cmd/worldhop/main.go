// main.go: the WorldHop program. It parses flags, builds every piece (store,
// encryption, sync engine, scheduler, web server, tray) and connects them. See the
// comment on main for the order. docs/technical.md section 2 describes the
// architecture.

package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/JGugino/world-hopper/internal/api"
	"github.com/JGugino/world-hopper/internal/config"
	"github.com/JGugino/world-hopper/internal/crypt"
	"github.com/JGugino/world-hopper/internal/detect"
	"github.com/JGugino/world-hopper/internal/license"
	"github.com/JGugino/world-hopper/internal/link"
	"github.com/JGugino/world-hopper/internal/oauth"
	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/localdir"
	"github.com/JGugino/world-hopper/internal/sync"
	"github.com/JGugino/world-hopper/internal/tray"
	"github.com/JGugino/world-hopper/internal/update"
	"github.com/JGugino/world-hopper/internal/window"
	"github.com/JGugino/world-hopper/internal/worlds"
	"github.com/JGugino/world-hopper/ui"
)

// version is overridden at release time with -ldflags "-X main.version=...".
var version = "dev"

// listFlag collects a repeatable flag.
type listFlag []string

// String returns the collected values joined by commas (flag.Value).
func (l *listFlag) String() string { return strings.Join(*l, ",") }

// Set adds one more value each time the flag is given (flag.Value).
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// idleTimeout is how long the web server lingers with no requests. The UI polls
// every few seconds while open, so only a closed/abandoned view lets it lapse.
const idleTimeout = 3 * time.Minute

// main wires the program together, in this order:
//
//  1. Read flags and pick the per-device state directory.
//  2. Build the store stack: the local folder (-store) or the cloud account manager,
//     wrapped in the encryption manager.
//  3. Create the sync engine on top and attach the hooks (license gate, "is the world
//     open", encryption scope).
//  4. Load the saved sync sources, seeding them on first run.
//  5. Create the Runner and Scheduler (automatic syncing), the license re-check and
//     the daily update check.
//  6. Prepare the on-demand web server (Lazy), then run the tray. The web server does
//     not exist yet: Open Web View starts it.
//
// With -no-tray it serves the web view immediately and never starts the tray. Each
// piece is documented in its own package; this function only connects them.
func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address (must be loopback)")
	headless := flag.Bool("no-tray", false, "serve the API/UI only, without a tray icon")
	storeDir := flag.String("store", "", "sync to this shared folder (local-directory store)")
	stateDir := flag.String("state", "", "per-device state directory (default: user config dir)")
	var savesDirs, worldDirs listFlag
	flag.Var(&savesDirs, "saves", "a saves folder to keep synced (repeatable)")
	flag.Var(&worldDirs, "world", "a single world folder to keep synced (repeatable)")
	flag.Parse()

	sd := *stateDir
	if sd == "" {
		cfgDir, err := os.UserConfigDir()
		if err != nil {
			log.Fatal(err)
		}
		sd = filepath.Join(cfgDir, "worldhop")
	}
	// -store picks a plain shared folder; otherwise syncing goes through the
	// user's linked Google Drive account.
	var cloud *link.Manager
	var st store.Store
	if *storeDir != "" {
		st = localdir.New(*storeDir)
	} else {
		ccfg, err := link.LoadConfig(sd)
		if err != nil {
			log.Fatal(err)
		}
		cloud = link.New(ccfg, oauth.KeyringStore{Service: "worldhop"}, sd)
		cloud.OpenURL = window.OpenBrowser
		st = cloud
	}
	lic, err := license.New(sd)
	if err != nil {
		log.Fatal(err)
	}
	// Client-side encryption wraps whichever store is in use. Off by default;
	// turning it on is a Pro feature (see Encryption in the web view).
	encStore, err := crypt.NewManager(st, sd, crypt.KeyringKeys{Service: "worldhop"}, lic.Pro)
	if err != nil {
		log.Fatal(err)
	}
	engine, err := sync.New(encStore, sd)
	if err != nil {
		log.Fatal(err)
	}
	engine.StateScope = encStore.Scope
	engine.Authorize = lic.Authorize
	if cloud != nil {
		// S3-compatible storage is a Pro feature; its secret key lives in the keychain.
		cloud.EnableS3(link.KeyringSecrets{Service: "worldhop"}, lic)
	}

	// Sources live in a config file the UI edits. On first run, seed it with the
	// launcher folders that exist on this computer; -saves / -world add more.
	cfg, fresh, err := config.Load(filepath.Join(sd, "config.json"))
	if err != nil {
		log.Fatal(err)
	}
	if fresh {
		home, _ := os.UserHomeDir()
		var found []worlds.Source
		for _, s := range worlds.DefaultSources(runtime.GOOS, home, os.Getenv("APPDATA")) {
			if st, err := os.Stat(s.Path); err == nil && st.IsDir() {
				found = append(found, s)
			}
		}
		if err := cfg.Seed(found); err != nil {
			log.Printf("save config: %v", err)
		}
	}
	var flagged []worlds.Source
	for _, p := range savesDirs {
		flagged = append(flagged, worlds.Source{Kind: worlds.KindSaves, Path: p})
	}
	for _, p := range worldDirs {
		flagged = append(flagged, worlds.Source{Kind: worlds.KindWorld, Path: p})
	}
	if err := cfg.Seed(flagged); err != nil {
		log.Printf("save config: %v", err)
	}
	// Re-resolved on every sync so new worlds and edits to the list are picked up.
	listWorlds := func() []worlds.World {
		ws, problems := worlds.Resolve(cfg.Sources())
		for _, p := range problems {
			log.Printf("source: %v", p)
		}
		return ws
	}
	det := detect.New()
	engine.IsOpen = det.WorldOpen
	engine.CloudKeep = cfg.CloudSnapshots
	svc := sync.NewRunner(version, engine, listWorlds)
	svc.Limited = lic.Limited
	// Re-check a stored Pro key now and then (refunds, offline grace).
	go func() {
		for {
			lic.Refresh(context.Background())
			time.Sleep(time.Hour)
		}
	}()
	svc.AutoDownload = true
	if cloud != nil {
		// Connecting or disconnecting an account changes what a sync can do, so
		// run a pass now: it clears (or raises) the "no cloud account" message
		// instead of leaving the last pass's result on screen.
		cloud.OnChange = func() { svc.SyncAll(context.Background()) }
	}
	svc.SavesFolders = func() []string {
		var out []string
		for _, s := range cfg.Sources() {
			if s.Kind == worlds.KindSaves {
				out = append(out, s.Path)
			}
		}
		return out
	}
	// Automatic syncing: a pass at startup, one when a world closes, and one
	// every ten minutes. Without this only Force Sync ever ran.
	schedCtx, stopSched := context.WithCancel(context.Background())
	defer stopSched()
	go sync.NewScheduler(svc, listWorlds, det.WorldOpen, sync.SchedulerConfig{}).Run(schedCtx)

	// The HTTP server is started on demand (Open Web View) and stopped again after
	// idleTimeout without requests, so nothing listens while the UI isn't in use.
	// Once a day, look for a newer release (never blocks startup, silent when
	// offline). It only announces; the user chooses to open the download page.
	upd := update.New(version, sd)
	upd.OnChange = func(i update.Info) { tray.SetUpdate(i.Latest, i.URL) }
	go upd.Run(context.Background())
	deps := api.Deps{Sources: cfg, Settings: cfg, Providers: api.StubProviders{}, License: lic, Encryption: encStore, Updates: upd} // -store mode has no cloud accounts
	if cloud != nil {
		deps.Providers, deps.ObjectStorage = link.API{M: cloud}, link.API{M: cloud}
	}
	lazy := api.NewLazy(*addr, svc, ui.Assets(), deps, idleTimeout)

	openView := func() {
		url, err := lazy.Ensure()
		if err != nil {
			log.Printf("start web server: %v", err)
			return
		}
		if err := window.Open(url, lazy.CloseNow); err != nil {
			log.Printf("open view: %v", err)
		}
	}

	if *headless {
		// No tray to trigger it, so serve right away and keep running.
		url, err := lazy.Ensure()
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("WorldHop UI at %s", url)
		select {}
	}

	tray.Run(tray.Handlers{
		OpenView:  openView,
		ForceSync: func() { svc.ForceSync(context.Background()) },
		OpenUpdate: func(url string) {
			if err := window.OpenBrowser(url); err != nil {
				log.Printf("open update page: %v", err)
			}
		},
		Exit: func() { stopSched(); lazy.Close() },
	}, func() {
		// Keep the tray's hover text in step with the sync status.
		go func() {
			var last string
			for {
				if h := svc.Status().Headline(time.Now()); h != last {
					last = h
					tray.SetStatus(h)
				}
				time.Sleep(2 * time.Second)
			}
		}()
	})
}
