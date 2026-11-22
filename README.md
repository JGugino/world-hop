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
- Sync calls are stubs for now (`internal/sync`).

WorldHop is not an official Minecraft product and is not affiliated with Mojang or Microsoft.
