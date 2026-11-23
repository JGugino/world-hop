# world-hopper
A minecraft world syncing application


## Development

Requires Go 1.24+ and Node 22+.

```sh
cd ui && npm install && npm run build   # builds the static UI into ui/build (embedded into the binary)
cd .. && go build -o worldhop ./cmd/worldhop
./worldhop            # tray icon + local UI on 127.0.0.1 (random port, logged at start)
./worldhop -no-tray   # API/UI only
```

- Tray menu: Open Web View, Force Sync, Exit.
- By default "Open Web View" opens the default browser. For a native window build with
  `go build -tags webview ./cmd/worldhop` (needs cgo; webkit2gtk on Linux, WebView2 on Windows).
- UI dev: run the app with `-addr 127.0.0.1:47800` and `npm run dev` in `ui/` (proxies `/api`).
- Licensing (free: 1 world, Pro: unlimited) and release packaging: see `docs/licensing.md`.

WorldHop is not an official Minecraft product and is not affiliated with Mojang or Microsoft.

A Minecraft world syncing application. Not affiliated with Mojang or Microsoft.

See the project plan for the design. Status: build steps 1-2 (core engine and
`localdir` store) are implemented; tray, web UI, game detection and cloud
providers are still to come.

## Layout

- `internal/worlds` – find worlds (vanilla, Prism, MultiMC, CurseForge, Modrinth, custom)
- `internal/chunker` – content-defined chunking (gear hash, ~1 MiB average)
- `internal/manifest` – manifest format, validation, tree hash
- `internal/store` – storage interface; `store/localdir` is the folder backend
- `internal/sync` – push, pull, atomic swap, snapshots, conflicts
- `cmd/worldhopper` – development CLI

## Try it

    go build -o worldhopper ./cmd/worldhopper
    ./worldhopper -store /path/to/shared -saves ~/.minecraft/saves discover
    ./worldhopper -store /path/to/shared push "My-World"
    ./worldhopper -store /path/to/shared pull "My-World"

## Test

    go test -race ./...
    go test ./internal/manifest -fuzz FuzzParse -fuzztime 30s
