package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/JGugino/world-hopper/internal/api"
	"github.com/JGugino/world-hopper/internal/store/localdir"
	"github.com/JGugino/world-hopper/internal/sync"
	"github.com/JGugino/world-hopper/internal/tray"
	"github.com/JGugino/world-hopper/internal/window"
	"github.com/JGugino/world-hopper/internal/worlds"
	"github.com/JGugino/world-hopper/ui"
)

const version = "0.0.1"

// idleTimeout is how long the web server lingers with no requests. The UI polls
// every few seconds while open, so only a closed/abandoned view lets it lapse.
const idleTimeout = 3 * time.Minute

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address (must be loopback)")
	headless := flag.Bool("no-tray", false, "serve the API/UI only, without a tray icon")
	storeDir := flag.String("store", "", "sync to this shared folder (local-directory store)")
	stateDir := flag.String("state", "", "per-device state directory (default: user config dir)")
	flag.Parse()

	var engine *sync.Engine
	if *storeDir != "" {
		sd := *stateDir
		if sd == "" {
			cfg, err := os.UserConfigDir()
			if err != nil {
				log.Fatal(err)
			}
			sd = filepath.Join(cfg, "worldhop")
		}
		var err error
		if engine, err = sync.New(localdir.New(*storeDir), sd); err != nil {
			log.Fatal(err)
		}
	}
	listWorlds := func() []worlds.World {
		home, _ := os.UserHomeDir()
		return worlds.Discover(worlds.DefaultRoots(runtime.GOOS, home, os.Getenv("APPDATA")))
	}
	svc := sync.NewRunner(version, engine, listWorlds)
	// The HTTP server is started on demand (Open Web View) and stopped again after
	// idleTimeout without requests, so nothing listens while the UI isn't in use.
	lazy := api.NewLazy(*addr, svc, ui.Assets(), idleTimeout)

	openView := func() {
		url, err := lazy.Ensure()
		if err != nil {
			log.Printf("start web server: %v", err)
			return
		}
		if err := window.Open(url); err != nil {
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
