package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/JGugino/world-hopper/internal/api"
	"github.com/JGugino/world-hopper/internal/detect"
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

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address (must be loopback)")
	headless := flag.Bool("no-tray", false, "serve the API/UI only, without a tray icon")
	storeDir := flag.String("store", "", "sync to this shared folder (local-directory store)")
	stateDir := flag.String("state", "", "per-device state directory (default: user config dir)")
	var savesDirs, worldDirs listFlag
	flag.Var(&savesDirs, "saves", "a saves folder to keep synced (repeatable)")
	flag.Var(&worldDirs, "world", "a single world folder to keep synced (repeatable)")
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
	if engine != nil {
		engine.IsOpen = det.WorldOpen
	}
	svc := sync.NewRunner(version, engine, listWorlds)
	schedCtx, stopSched := context.WithCancel(context.Background())
	defer stopSched()
	if engine != nil {
		go sync.NewScheduler(svc, listWorlds, det.WorldOpen, sync.SchedulerConfig{}).Run(schedCtx)
	}
	srv, err := api.New(*addr, svc, ui.Assets())
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		if err := srv.Serve(); err != nil {
			log.Printf("server stopped: %v", err)
		}
	}()
	log.Printf("WorldHop UI at %s", srv.URL())

	openView := func() {
		if err := window.Open(srv.URL()); err != nil {
			log.Printf("open view: %v", err)
		}
	}

	if *headless {
		select {}
	}

	tray.Run(tray.Handlers{
		OpenView:  openView,
		ForceSync: func() { svc.ForceSync(context.Background()) },
		Exit:      func() { stopSched(); srv.Close() },
	}, nil)
}
