package main

import (
	"context"
	"flag"
	"log"

	"github.com/JGugino/world-hopper/internal/api"
	"github.com/JGugino/world-hopper/internal/sync"
	"github.com/JGugino/world-hopper/internal/tray"
	"github.com/JGugino/world-hopper/internal/window"
	"github.com/JGugino/world-hopper/ui"
)

const version = "0.0.1"

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address (must be loopback)")
	headless := flag.Bool("no-tray", false, "serve the API/UI only, without a tray icon")
	flag.Parse()

	svc := sync.New(version)
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
		Exit:      func() { srv.Close() },
	}, nil)
}
