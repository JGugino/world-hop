# WorldHop Technical Documentation

How WorldHop is built, how it syncs, and how to build and release it. Written from the code on `main` (all PRs through #34), not from the plan documents. Where the code and a plan differ, this document describes the code and lists the difference under Known gaps.

## 1. Overview

WorldHop keeps Minecraft Java worlds in sync between computers. It runs as a system tray app. A local web page (the "web view") is the only user interface. Worlds are stored in the user's own cloud account (Google Drive, or any S3-compatible bucket for Pro), never on a WorldHop server.

| Piece | Technology |
|---|---|
| App | Go (module `github.com/JGugino/world-hopper`), single binary |
| Tray | `fyne.io/systray` |
| Web view UI | SvelteKit 2 + Svelte 5, static adapter, embedded in the binary (`ui/embed.go`) |
| Pixel font | Press Start 2P (SIL OFL), bundled in `ui/static/fonts` so the UI works offline |
| Cloud storage | Google Drive (`drive.appdata` scope), S3-compatible (SigV4, no SDK) |
| Encryption | Argon2id, HKDF, XChaCha20-Poly1305 |
| Licensing | Gumroad license keys |
| Secrets | OS keychain via `zalando/go-keyring` |

Tray menu: **Open Web View**, **Force Sync**, **Exit**, plus an **Update available: vX** item that appears only when a newer release exists.

## 2. Architecture

```
tray (cmd/worldhop) ──► Runner ──► Engine ──► crypt.Manager ──► link.Manager ──► gdrive | s3 | (localdir)
        │                 ▲            │
        │            Scheduler     worlds, detect, chunker, manifest
        └──► api.Lazy ──► api.Server (127.0.0.1) ──► embedded SvelteKit UI
```

`cmd/worldhop/main.go` wires everything together in this order:

1. Pick the per-device state directory (`os.UserConfigDir()/worldhop`, or `-state`).
2. Build the store. With `-store <dir>` it is a plain shared folder (`localdir`, cloud accounts off). Otherwise it is `link.Manager`, which delegates to the chosen cloud provider.
3. Wrap that store in `crypt.Manager` (client-side encryption, off by default).
4. Create the sync `Engine` on top, with `Authorize` set to the license check and `IsOpen` set to the detector.
5. Load `config.json`, seeding it on first run with the launcher saves folders that exist on this computer.
6. Start the `Scheduler`, the daily update checker and an hourly license re-check.
7. Run the tray. The web server does not exist yet.

### 2.1 Packages

| Package | Responsibility |
|---|---|
| `internal/worlds` | Finds worlds (a folder with `level.dat`); world id from folder name; default launcher paths (vanilla, Prism, MultiMC, CurseForge, Modrinth) |
| `internal/config` | `config.json`: the list of sources the user added (saves folders and single worlds) |
| `internal/chunker` | Content-defined chunking (gear rolling hash) |
| `internal/manifest` | Manifest format, validation, tree hash |
| `internal/store` | `Store` interface and name validation; `localdir`, `gdrive`, `s3` backends; `storetest` fakes and a contract test suite |
| `internal/sync` | Engine (status, push, pull, resolve, download), snapshots, atomic swap, Runner (UI status), Scheduler |
| `internal/detect` | Is Minecraft running; is this world open |
| `internal/crypt` | Encryption wrapper around any `Store` |
| `internal/link` | Cloud account manager: provider choice, OAuth link flow, S3 settings, `store.Store` that follows the choice |
| `internal/oauth` | Loopback PKCE flow and refreshing token source; tokens in the keychain |
| `internal/httpx` | Shared HTTP client with retry and 401 refresh |
| `internal/license` | Gumroad verification, free tier limit, Pro gate |
| `internal/update` | Notify-only release check |
| `internal/api` | Local HTTP API and static UI server, lazy start/stop |
| `internal/tray`, `internal/window` | Tray icon and menu; opening the UI |
| `ui/` | SvelteKit web view (routes: Worlds `/`, Saves `/sources`, Cloud `/cloud`, Encryption `/encryption`, License `/license`) |

### 2.2 Web server lifecycle

The HTTP server runs only while the web view is in use (`api.Lazy`):

- **Open Web View** calls `Lazy.Ensure()`, which starts a server on a free port of `127.0.0.1` (default address `127.0.0.1:0`) and opens it.
- The page calls `POST /api/bye` when it closes. After a 5 second grace period (so a reload survives) the server stops if nothing else has made a request.
- Idle timeout is 3 minutes with no requests at all. The UI polls `/api/status` every 3 seconds while open, so only an abandoned view lapses.
- With `-no-tray` the server starts immediately and stays up.

Local API hardening (`hostGuard`): requests whose `Host` is not `127.0.0.1` or `localhost` get 403 (DNS rebinding). State-changing requests (anything but GET/HEAD) with an `Origin` header that does not match the host get 403 (cross-site POST).

### 2.3 Opening the UI

By default (`!webview` build tag) the UI opens in the default browser: `rundll32 url.dll,FileProtocolHandler` on Windows, `open` on macOS, `xdg-open` on Linux. A build with `-tags webview` (needs cgo and a platform webview) opens a native 900x650 window instead and falls back to the browser if it cannot be created. The released Windows and Linux binaries use the browser path.

### 2.4 Local API

| Route | Purpose |
|---|---|
| `GET /api/status`, `POST /api/sync` | Runner status; Force Sync |
| `POST /api/bye` | Page is closing |
| `GET /api/update` | Update info |
| `GET/POST /api/sources`, `POST /api/sources/remove` | List, add, remove sources |
| `POST /api/pick-folder` | Native folder dialog |
| `GET /api/worlds`, `POST /api/worlds/{id}/sync`, `/resolve` | World list, per-world sync, conflict resolution |
| `GET /api/worlds/{id}/snapshots`, `POST /api/worlds/{id}/restore` | Snapshots and restore |
| `GET /api/cloud-worlds`, `POST /api/cloud-worlds/{id}/download` | Worlds that exist only in the cloud |
| `GET /api/providers`, `POST /api/providers/{id}/link` and `/unlink` | Cloud accounts |
| `GET/PUT /api/s3`, `POST /api/s3/test` | S3 settings (Pro) |
| `GET /api/encryption`, `POST /api/encryption/enable`, `/unlock`, `/lock`, `/disable` | Encryption |
| `GET /api/license`, `POST /api/license/activate`, `/deactivate`, `/free-world` | Licensing |

## 3. Sync model

### 3.1 Worlds and sources

A **source** is a saves folder (every world in it, including ones created later) or a single world folder. Any number of each. A world is any folder containing `level.dat`. Sources are re-resolved on every pass, so new worlds and list edits are picked up without a restart.

The **world id** is the folder name with every character outside `A-Za-z0-9._-` replaced by `-`, trimmed, at most 128 characters. Computers match worlds by this id, so the folder name must be the same on every computer. If two worlds on one computer produce the same id, the first wins and the other is reported as a problem.

### 3.2 Chunks, packs, manifests

Files are split with a gear rolling hash (FastCDC style): minimum 256 KiB, average about 1 MiB (`AvgBits = 20`), maximum 4 MiB. The gear table is derived from SHA-256 so boundaries are identical on every device. A small edit inside a large region file only changes chunks near the edit.

Chunks are SHA-256 addressed. New chunks are appended into **packs** of about 16 MiB (`packTarget`); a pack is named by the SHA-256 of its bytes and uploaded once (`PutPack` is idempotent). Chunks the store already has are reused.

A **manifest** describes one immutable version of a world: world id, device id, generation, parent manifest names, creation time, the file list (path, size, mode, mtime, chunk hashes) and where each chunk lives (`pack`, `offset`, `length`). Manifests are named `manifest-<12 digit generation>-<device id>.json` and are validated when read (safe relative paths, no duplicates, sizes match chunk lengths). `session.lock` is never synced.

### 3.3 The store contract

`store.Store` needs only write-once blobs and listing. No locking, no overwrite, no delete:

```
PutPack / GetPack            immutable, content-addressed
PutManifest / GetManifest    never overwrites (returns ErrExists)
ListManifests(world)         manifest names for a world
ListWorlds()                 ids of worlds with at least one manifest
```

Google Drive and S3 use the same layout: `packs/<sha256>` and `worlds/<id>/<manifest>`.

### 3.4 Versions, heads and conflicts

Each push writes a new manifest whose `parents` are all the heads it saw. A **head** is a manifest no other manifest names as a parent. One head means a clean history. Several heads means two computers pushed without syncing in between: a conflict.

Each device remembers, per world, the manifest it last synced with and its tree hash (`worlds/<id>.json` in the state dir). Comparing the local tree hash, the remembered base and the heads gives the status:

| Status | Meaning |
|---|---|
| `no-remote` | Nothing stored yet |
| `no-local` | Stored, but no local copy |
| `synced` | Identical |
| `local-ahead` | Local changes to push |
| `remote-ahead` | Newer stored version to pull |
| `conflict` | Local and stored versions diverged, or several heads |

`Sync` looks once and does what the status calls for. Conflicts are reported, never overwritten. `Resolve` offers four strategies: `keep-local` (uploaded as a new version superseding all heads), `keep-remote` (a merge manifest is written when there are several heads, then the stored version is applied), `keep-newest` (whichever was modified last) and `keep-both` (local copied to `<world> (conflict copy <date time>)`, then stored wins).

### 3.5 Push and pull

**Push** scans the world (a stat cache turns "hash everything" into "stat every file" when little changed), reads only files holding chunks the store lacks, uploads packs with up to 6 concurrent requests, and writes the manifest **last**. Until the manifest exists, the push is invisible to other computers. If the game touches a file between scan and read, the manifest describes what was actually uploaded.

**Pull** (`apply`) builds the new version in a staging folder next to the world (`.worldhopper-staging-<name>`), reusing local data and downloading only missing chunks, verifies every hash (`ErrIntegrity` on mismatch), then swaps it in. The replaced world is renamed aside (`.worldhopper-old-<name>`) and filed as a snapshot. At every instant the old or the new world is present; a crash is repaired by `Recover` at the next pass. A world with sync state that is now missing locally is treated as deleted or moved on purpose and is not fetched again unless asked.

**Download** creates a cloud-only world as a new folder in a saves folder. It never touches an existing folder (`ErrDestExists`). On a full pass with `AutoDownload` and exactly one saves folder, cloud-only worlds this computer never had are fetched automatically.

### 3.6 Snapshots and restore

Before any pull, conflict resolution or restore replaces a world, the old version is kept as a local snapshot under `snapshots/<world id>/<UTC timestamp>-<reason>`. On the same volume the old folder is simply renamed (no copy); otherwise it is copied first. The newest 5 are kept (`SnapshotsKeep`). **Restore** swaps a snapshot back in after snapshotting the current world (so a restore can be undone); the next push uploads the restored world as a new version. Snapshots are local only.

### 3.7 When syncing happens

The `Scheduler` polls every 5 seconds and runs:

- a full pass at startup,
- a full pass every 10 minutes,
- a push for a world that went from open to closed and stayed closed for 3 seconds.

Force Sync (tray or UI) runs a pass immediately. The `Runner` runs one pass at a time; Force Sync during a pass returns the current status.

### 3.8 Detecting an open world

`detect.WorldOpen` checks `session.lock`. No lock file means closed. Otherwise it probes the OS lock (`fcntl F_GETLK` on Linux and macOS; a read-lock violation test on Windows). If the lock is not held but the file was written in the last 10 seconds and a Minecraft Java process is running, the world counts as open. If the lock cannot be probed it falls back to a Java process check (`ps`, or PowerShell CIM on Windows). When it cannot tell, it assumes the world is open, because syncing an open world risks corruption. The engine refuses to push, pull or resolve an open world (`ErrWorldOpen`), and a waiting world is reported as "waiting for Minecraft to close".

### 3.9 Status and problems

`GET /api/status` returns state (`idle`/`syncing`), last sync time and result, an outcome (`ok`, `waiting`, `attention`, `error`), counts (total, uploaded, downloaded, up to date), progress (`checking`, `uploading`, `downloading`) and a list of problems. Errors are mapped to plain-language problems of these kinds: world open, conflict, free-world limit, Pro required, no cloud account, authorization lost, encryption locked, key problem, damaged data, stopped, disk full, permission, offline, server, unknown. The tray tooltip follows the headline every 2 seconds.

## 4. Cloud providers

`link.Manager` implements `store.Store` by delegating to the chosen provider, so the engine is built once and follows the user's choice. The choice is saved in `provider.json`. With no provider chosen, store calls return `ErrNotLinked` and the UI says there is no cloud account.

### 4.1 Google Drive

Uses only the `drive.appdata` scope, so WorldHop sees its own hidden app folder and nothing else in the user's Drive. Drive has no real paths, so objects are flat files named `packs/<hash>` and `worlds/<id>/<manifest>`, with world ids and kinds stored in `appProperties` for listing. Uploads over 5 MiB use the resumable protocol.

Sign-in is the installed-app OAuth flow with PKCE on a loopback port: the app opens one browser consent page, receives the redirect on `127.0.0.1`, and exchanges the code. Tokens are stored in the OS keychain (Keychain, Credential Manager, Secret Service), never in plain files. A revoked or expired grant moves the account to `needs-reconnect`.

OAuth client IDs are compiled into release builds (`DefaultGoogleClientID`, `DefaultGoogleClientSecret`, set by `-ldflags`). For development, `cloud.json` in the state dir (keys `googleClientId`, `googleClientSecret`) and the environment variables `WORLDHOP_GOOGLE_CLIENT_ID` and `WORLDHOP_GOOGLE_CLIENT_SECRET` override them, environment last. Google lists desktop client secrets as not truly confidential, but they are still never committed.

### 4.2 S3-compatible storage (Pro)

Any S3-compatible service (AWS S3, Cloudflare R2, Backblaze B2, MinIO, Wasabi) over the S3 REST API with SigV4 signing implemented in-repo, no SDK. Settings: endpoint, region (default `us-east-1`), bucket, optional key prefix, path-style addressing, access key and secret key. Non-secret settings live in `s3.json`; the access key and secret key are stored together in the keychain. The Cloud page can test the connection. The provider is checked against the license on every store call, so a lapsed license stops S3 even if the store is already built.

### 4.3 Local folder store

`-store <dir>` syncs through a shared folder instead of a cloud account. It is a developer and test mode; it turns cloud accounts off.

### 4.4 Dropbox

Not implemented. An earlier build had a Dropbox store; the product decision is Google Drive only for now, and `internal/store` contains only `localdir`, `gdrive` and `s3`.

## 5. Encryption (Pro)

`crypt.Manager` wraps any `Store`, so it behaves the same on every provider. Off by default.

- Passphrase to Argon2id (64 MiB, 3 passes, random salt) to a master key, then HKDF into a data key, a naming key and a key-check key.
- Packs, manifests and the world index are sealed with XChaCha20-Poly1305. Each blob is bound to its own name, so a provider cannot swap blobs or move a manifest to another world without detection (`ErrTampered`).
- Pack names are HMACs of the plaintext hash, so identical data still deduplicates without revealing its hash. World ids are hashed (`_worldhop-w-...`); an encrypted index lets another computer list worlds.
- A **key header** (salt, KDF cost, key check) is stored in the cloud account under reserved `_worldhop-*` ids. It holds no secret. The key check reports a wrong passphrase immediately.
- Still visible to the provider: roughly how much data there is and when it changed, manifest file names (generation and random device id), and the number of worlds, packs and manifests.

**Second computer:** enable encryption and enter the same passphrase. The key header is found in the account, the passphrase is verified, and worlds appear. Creating a new key needs the passphrase twice and an explicit acknowledgement that a lost passphrase cannot be recovered.

**Existing plaintext data:** the store is write-once with no delete, so the app cannot remove the old copy. Enabling encryption starts a separate encrypted layout beside it and each world is re-uploaded encrypted on its next sync. The user must delete the old plaintext copy in the provider's own settings. "Last synced" records, the manifest cache and the stat cache are kept per layout (`Engine.StateScope`, value `enc`) so toggling never causes a false conflict.

**Remembering the passphrase:** off by default. If the user opts in, the derived key (not the passphrase) goes in the OS keychain. `encryption.json` records only `enabled` and `remember`. While locked, syncing stops with "encryption is on but locked"; it never falls back to uploading unencrypted.

**Pro gate:** turning encryption on needs Pro. If Pro later lapses, encrypted worlds keep syncing so data is never stranded.

## 6. Licensing

| | Free | Pro |
|---|---|---|
| Worlds synced per computer | 1 (`FreeWorlds`) | Unlimited |
| Google Drive | Yes | Yes |
| S3-compatible storage | No | Yes |
| Client-side encryption | No | Yes |
| Snapshots, status, restore | Yes (also for locked worlds) | Yes |

Pro is a one-time Gumroad purchase. Gumroad emails a license key; the user pastes it into the License page. The price and offers are set on the Gumroad product, not in code. Store link: store.gugino.org.

**Enforcement** is in the engine, not the UI. `Engine.Authorize` (set to `license.Manager.Authorize`) runs before every push, pull, download and conflict resolution, so the scheduler, Force Sync and the per-world buttons are all covered. Locked worlds can still show status and restore snapshots. The free world is the first world to sync, or the one the user picks on the License page (`/api/license/free-world`).

**Verification:** the key is checked against `https://api.gumroad.com/v2/licenses/verify` with the product id compiled into the build. Refunded, charged-back or disputed purchases are rejected. A stored key is re-checked every 7 days while online (the app wakes hourly and re-checks when due; a refund or dispute drops the key and returns the app to free, while a network failure just keeps the grace period running). If Gumroad cannot be reached, Pro keeps working for 30 days since the last successful check, then falls back to free. State is in `license.json`; a damaged file falls back to free rather than locking the user out. A build with no product id cannot activate keys.

**Known limits, by design:** the check is local so a determined user can patch the binary, and a key is not limited to a number of computers.

## 7. Update check

`internal/update` asks GitHub for the latest published (non-draft, non-prerelease) release of `JGugino/world-hop` at most once per 24 hours and remembers the result in `update.json`. It is **notify-only**: it never downloads or installs anything. A newer version adds the tray item "Update available: vX", and the UI can open the release page. Links are accepted only if they start with `https://github.com/JGugino/world-hop/` (the old `world-hopper` name is also accepted, for links saved by earlier builds). Every failure is silent. A build with version `dev` never checks.

## 8. State and data locations

State directory: `os.UserConfigDir()/worldhop` (override with `-state`). On Windows that is `%APPDATA%\worldhop`, on macOS `~/Library/Application Support/worldhop`, on Linux `~/.config/worldhop`.

| Path | Contents |
|---|---|
| `device-id` | Random 12 hex character id for this computer |
| `config.json` | Sources (saves folders and single worlds) |
| `provider.json` | Chosen cloud provider |
| `s3.json` | S3 settings without the secret key |
| `encryption.json` | `enabled` and `remember` only |
| `license.json` | License key, last verification, free world |
| `update.json` | Last update check |
| `cloud.json` | Optional developer override of OAuth client IDs |
| `worlds/<id>.json` | Last synced manifest and tree hash per world (`worlds/enc/` when encrypted) |
| `manifests/<id>/` | Cached immutable manifests |
| `cache/<id>.json` | Scan stat cache |
| `snapshots/<id>/` | Local snapshots |

In the OS keychain (service `worldhop`): OAuth tokens, the S3 access and secret keys and, if remembered, the derived encryption key. Next to each world, temporary `.worldhopper-staging-*` and `.worldhopper-old-*` folders exist only during a swap.

## 9. Build and release

### 9.1 Build

```
cd ui && pnpm install && pnpm build     # UI first; the binary embeds ui/build
go build ./cmd/worldhop                 # plain build: browser UI, tray
```

CI (`ci.yml`, on pull requests and pushes to `main`) runs `pnpm check`, `pnpm build`, `go build ./...`, `go vet ./...` and `go test -race ./...`.

### 9.2 Makefile targets

| Target | What it does |
|---|---|
| `make dev` | Builds the UI and a binary with the IDs from `.env`, then runs it with cloud sign-in |
| `make ui` | `pnpm build` in `ui/` (shared by every release target) |
| `make release` | Native release build into `dist/worldhop` |
| `make release-linux` | `dist/worldhop-linux-amd64`, cross-compiled with CGO off |
| `make release-windows` | `dist/worldhop-windows-amd64.exe`, CGO off, `-H windowsgui` (no console window), embeds the icon resource |
| `make release-mac` | `dist/worldhop-macos-arm64` and `-amd64`. **Mac only**: the tray needs cgo and Apple's SDK (`MAC_ARCHS=arm64` builds one) |
| `make release-all` | Linux and Windows, plus macOS when run on a Mac (otherwise skipped with a warning) |
| `make package-linux`, `package-macos` | `.deb` and `.tar.gz`; `.dmg` (scripts in `packaging/`) |
| `make syso`, `make icons` | Refresh the Windows icon resource; regenerate icons from `assets/icon/icon.svg` |

Release targets pass `check-release-env`, which fails unless `GOOGLE_CLIENT_ID` is set.

### 9.3 `.env` and compiled-in IDs

The makefile includes a gitignored `.env` and exports it. Values are compiled in with `-ldflags -X`:

| Variable | Compiled into | Required |
|---|---|---|
| `GOOGLE_CLIENT_ID` | `link.DefaultGoogleClientID` | Yes |
| `GOOGLE_CLIENT_SECRET` | `link.DefaultGoogleClientSecret` | No |
| `GUMROAD_PRODUCT_ID` | `license.GumroadProductID` | No, but without it Pro cannot be activated |
| `PURCHASE_URL` | `license.PurchaseURL` | No |
| `VERSION` | `main.version` | No (default `dev`) |

Never commit real IDs. End users need no `.env` or config file.

### 9.4 Release workflow

Pushing a tag `v*` (or manual dispatch) runs `release.yml`: checks (UI build, `go vet`, `go test -race`), then a package job per OS (Linux `.deb` and `.tar.gz`, macOS `.dmg`, Windows `.zip` with README), then, for tags only, a **draft** GitHub release with the installers attached for review. Repository secrets used: `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET`, `GUMROAD_PRODUCT_ID`, `PURCHASE_URL`.

Installers: `WorldHop-<version>-windows-amd64.zip`, `WorldHop-<version>.dmg` (unsigned `WorldHop.app`, no Dock icon), `worldhop_<version>_amd64.deb` and `worldhop-<version>-linux-amd64.tar.gz`. The Linux tray needs an AppIndicator-capable desktop.

## 10. Testing

Unit and integration tests live beside each package. `internal/store/storetest` provides fake Google Drive and S3 servers and a contract suite every backend must pass, plus an end-to-end test. Tests cover the engine (sync, conflicts, scheduler, runner, problems, performance and a benchmark), crypto over both local and fake S3 stores, license checks, the API and the update checker. The real Google, S3 and Gumroad services are not contacted by tests.

## 11. Known gaps

From the code and docs:

- Changing the encryption passphrase (re-encrypting everything) is not implemented.
- A provider can still delete data or roll a world back to an older manifest. Encryption detects modification, not rollback or deletion.
- No cloud cleanup: old manifests and packs are never deleted (the store has no delete), so cloud usage only grows. The app cannot remove a plaintext copy after encryption is enabled.
- Snapshots are local only (newest 5 per world).
- No code signing (Windows SmartScreen warning) and no macOS notarization. No MSI or AppImage.
- No macOS release has been produced from this repository's tooling yet; it must be built on a Mac.
- Unlinking removes the stored tokens locally; the code does not revoke them with the provider.
- Manual testing on real Windows and macOS with an open world, and with real Google and S3 accounts, is still owed.

Further features are planned but not built; they are not tracked in this repo.

## 12. Further reading

Repository docs: `README.md`, `docs/cloud-setup.md`, `docs/encryption.md`, `docs/licensing.md`, `docs/privacy.md`, `docs/terms.md`. Source is under the Functional Source License (FSL-1.1-MIT). Store: store.gugino.org.
