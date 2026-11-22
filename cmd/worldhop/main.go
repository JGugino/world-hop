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
	"github.com/JGugino/world-hopper/internal/detect"
	"github.com/JGugino/world-hopper/internal/link"
	"github.com/JGugino/world-hopper/internal/oauth"
	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/localdir"
	"github.com/JGugino/world-hopper/internal/sync"
	"github.com/JGugino/world-hopper/internal/tray"
	"github.com/JGugino/world-hopper/internal/window"
	"github.com/JGugino/world-hopper/internal/worlds"
	"github.com/JGugino/world-hopper/ui"
)

const version = "0.0.1"

// listFlag collects a repeatable flag.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// idleTimeout is how long the web server lingers with no requests. The UI polls
// every few seconds while open, so only a closed/abandoned view lets it lapse.
const idleTimeout = 3 * time.Minute

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
		cfg, err := os.UserConfigDir()
		if err != nil {
			log.Fatal(err)
		}
		sd = filepath.Join(cfg, "worldhop")
	}
	// -store picks a plain shared folder; otherwise syncing goes through the
	// user's linked Google Drive or Dropbox account.
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
	engine, err := sync.New(st, sd)
	if err != nil {
		log.Fatal(err)
	}
	// Sources are re-resolved on every sync so new worlds are picked up.
	// With none given, fall back to the known launcher locations.
	var sources []worlds.Source
	for _, p := range savesDirs {
		sources = append(sources, worlds.Source{Kind: worlds.KindSaves, Path: p})
	}
	for _, p := range worldDirs {
		sources = append(sources, worlds.Source{Kind: worlds.KindWorld, Path: p})
	}
	listWorlds := func() []worlds.World {
		src := sources
		if len(src) == 0 {
			home, _ := os.UserHomeDir()
			src = worlds.DefaultSources(runtime.GOOS, home, os.Getenv("APPDATA"))
		}
		ws, problems := worlds.Resolve(src)
		for _, p := range problems {
			log.Printf("source: %v", p)
		}
		return ws
	}
	det := detect.New()
	engine.IsOpen = det.WorldOpen
	svc := sync.NewRunner(version, engine, listWorlds)
	// The HTTP server is started on demand (Open Web View) and stopped again after
	// idleTimeout without requests, so nothing listens while the UI isn't in use.
	lazy := api.NewLazy(*addr, svc, ui.Assets(), idleTimeout)
	if cloud != nil {
		lazy.Cloud = cloud
	}

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
		Exit:      func() { lazy.Close() },
	}, nil)
}
