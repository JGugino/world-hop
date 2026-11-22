# world-hopper

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
