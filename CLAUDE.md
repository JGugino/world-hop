# CLAUDE.md

Orientation for working on WorldHop. Read the docs listed below before changing behavior.

## What WorldHop is

A system tray app that keeps Minecraft Java worlds in sync between a user's computers, through the user's **own** cloud storage (Google Drive, or an S3-compatible bucket with Pro). There is no WorldHop server. The only UI is a locally hosted web page ("web view"). The tray menu is **Open Web View**, **Force Sync**, **Exit**, plus an **Update available** item when a newer release exists.

## Architecture (short)

- Go single binary. Module path is still `github.com/JGugino/world-hopper` (the repo is now `world-hop`).
- Entry point `cmd/worldhop`. Packages under `internal/`: `worlds`, `config`, `chunker`, `manifest`, `store` (`localdir`, `gdrive`, `s3`), `sync` (engine, snapshots, runner, scheduler), `detect`, `crypt`, `link`, `oauth`, `httpx`, `license`, `update`, `api`, `tray`, `window`.
- Web view: SvelteKit (static) in `ui/`, embedded in the binary via `ui/embed.go`. Local web server runs only while the view is open.
- Worlds are chunked, packed and described by manifests in a write-once store. Conflicts are never auto-resolved; the user chooses.
- `docs/technical.md` has the full package map, sync model and API.

## Docs and plans

| File | What it is |
|---|---|
| `docs/technical.md` | Technical documentation, written from the code on `main` (through PR #34). Describes what the code does; known gaps are listed in section 11. |
| `docs/user-guide.md` | End-user guide |
| `docs/cloud-setup.md`, `docs/licensing.md`, `docs/encryption.md` | Setup and feature details |
| `docs/privacy.md`, `docs/terms.md` | Published policies |

Planning documents are not kept in the repo. `docs/technical.md` describes what the code does today.

## Build and test

```
cd ui && pnpm install && pnpm build     # UI first; the binary embeds ui/build
go build ./cmd/worldhop                 # plain build: browser UI, tray
go vet ./... && go test -race ./...
```

CI runs `pnpm check`, `pnpm build`, `go build ./...`, `go vet ./...`, `go test -race ./...`.

Makefile targets: `make dev` (builds UI plus a binary with IDs from `.env`, runs with cloud sign-in), `make release-windows`, `make release-mac` (Mac only: needs cgo and Apple's SDK), `make release-linux`, `make release-all`.

Release builds read a gitignored `.env` and compile IDs in with `-ldflags -X`: `GOOGLE_CLIENT_ID` (required), `GOOGLE_CLIENT_SECRET`, `GUMROAD_PRODUCT_ID`, `PURCHASE_URL`, `VERSION`. Never commit real IDs. End users need no `.env` or config file.

## Key decisions

- **Google Drive only for now.** Dropbox is not implemented and may return later.
- **Pro** (one-time Gumroad purchase) = unlimited worlds, S3-compatible storage, client-side encryption. Free = 1 world on Google Drive, with snapshots and restore.
- **Update check is notify-only.** It never downloads or installs anything.
- **Conflict tools are Free**, not Pro.
- One browser prompt for cloud login; saves can be any number of folders or single worlds.
- Device names stay anonymous (no hostname stored). Dropped ideas: mods sync, live presence, auto-merge, pre-launch hook, "sync complete" notifications.

## Working notes

- Rebase on `main` before finishing a branch.
- Store link for docs and posts: store.gugino.org. Free download: github.com/JGugino/world-hop/releases.
