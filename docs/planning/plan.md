# Plan: "WorldHop" (working name) – safe Minecraft world sync between your computers

## Where things are stored
- This plan file: `/root/.claude/plans/i-m-looking-to-create-typed-ullman.md` (outside the project, in the Claude session's own folder).
- Copy inside the project: `/home/user/worldhop/PLAN.md` (not committed; no commits exist yet).
- Code so far: `/home/user/worldhop` (core sync engine, tests passing).
- Status: build order steps 1-2 done; resume at step 3 (scheduler), then step 4 (Drive/Dropbox).

## Context
You want side income (decent, not a living) from a desktop app built around a problem you have: you play a Minecraft world on your desktop, then want it on your laptop, and today you copy it via Google Drive by hand. You want: pick which worlds to sync in a local web view; closing the view leaves only a tray icon; right-click for Force sync / Open / Exit. Sync must work even if the other computer is off when you close the game. **Latest decision: the user links their own Google Drive or Dropbox account inside the app (no "pick a synced folder").** Name is a placeholder, unchecked for trademarks or domains, and should not contain "Minecraft".

## How asynchronous sync works without you running a backend
The desktop pushes the world to the buyer's **own linked cloud account**; the laptop pulls it whenever it next starts, even days later. Data flows directly between the buyer's computers and their Drive/Dropbox; you never host or see worlds or tokens. Storage cost is the buyer's own quota.

### Linking an account (OAuth)
- Click "Connect Google Drive" or "Connect Dropbox" in the web UI. The system browser opens the provider's consent page; the provider redirects to `http://127.0.0.1:<port>` (loopback) and the app completes the code exchange with **PKCE**. Tokens are stored in the OS keychain (`zalando/go-keyring`), never in plain files.
- **Google Drive:** request only `drive.appdata` (a hidden per-app folder) or `drive.file`. Google's docs call both [non-sensitive](https://developers.google.com/workspace/drive/api/guides/api-specific-auth), so no restricted-scope security audit is needed. Recommend `drive.appdata`: users can't accidentally delete or move the files, and the app shows usage and offers "delete cloud copy". The OAuth client ID ships inside the app (not a secret for installed apps).
- **Dropbox:** create the app with **App Folder** access, so it can only touch `Apps/<name>`. Use PKCE with `token_access_type=offline`; the stored [refresh token does not expire and needs no client secret](https://www.dropboxforum.com/discussions/101000042/oauth2-w-refresh-tokens-for-hybrid-apps/512806/replies/512888). Store it once; handle revocation by asking the user to reconnect.
- **One-time setup you do (not code):** a Google Cloud project with the OAuth consent screen published to production, a Dropbox developer app, a small website with a privacy policy and terms (both providers expect one; Google's brand verification needs it). Exactly what review each provider requires for an unverified or small app, and Dropbox's production-approval rules, were **not confirmed** by my search; check each provider's current docs before launch.
- **Quota/limits to design around:** Dropbox free accounts are small (about 2 GB) and Google's free tier is shared with Gmail/Photos (about 15 GB), so show space used and warn early. Dropbox doesn't publish rate limits and Google API quotas apply per project (shared by all your buyers), so **pack many small chunks into larger pack files** (e.g. 8-32 MB) to keep request counts low, and retry with backoff.

### Store layout (why it is safe, on any provider)
- `packs/<hash>`: content-addressed, write-once blobs holding chunks of world files (never modified, deduplicated, so only changed data uploads).
- `worlds/<worldId>/manifest-<generation>-<deviceId>.json`: small list of files and chunk locations. A push uploads packs first, **then** writes a new uniquely named manifest. A half-finished push is invisible because no manifest points to it. Unique names mean no overwrites, so no provider-specific locking is needed.
- A pull lists manifests, picks the newest, downloads missing packs, verifies hashes, builds the world in a temp folder, and atomically swaps it in (after a local snapshot).
- **Conflicts:** each manifest records its parent generation. If both devices changed from the same parent, the app offers keep newest / keep both (copy) / cancel. Nothing is silently overwritten.
- **Cleanup:** keep the last N manifests per world; delete unreferenced packs after a grace period.
- Optional client-side encryption of packs (Pro) so even the cloud provider cannot read worlds.

## Why a purpose-built app can beat current options (search findings; forum and vendor sources)
- Java Edition has [no official cloud saves; players request them](https://feedback.minecraft.net/hc/en-us/community/posts/47007767479309-Minecraft-Cloud-Sync-System-for-Java-Edition).
- Existing solutions are free Fabric-only mods ([World Syncer](https://modrinth.com/mod/world-syncer), [Mat's Cloud Saves](https://www.curseforge.com/minecraft/mc-mods/mats-cloud-saves), [MineGit](https://modrinth.com/mod/minegit), Google Drive Resync), a terminal client, or DIY Syncthing/Dropbox. I found **no paid Java sync app**.
- DIY sync risks: a [Syncthing thread](https://forum.syncthing.net/t/risk-of-minecraft-world-corruption-when-syncing-between-two-devices/26185) discusses world corruption, another reports items missing after a "100% complete" sync, and a stale `session.lock` can travel to the other machine.
- Mods tie you to one loader and version. A standalone app works with vanilla and any launcher.
- **Risks:** price-sensitive audience, free mods exist, no sales data. Mojang's [EULA](https://www.minecraft.net/en-us/eula) allows tools only if they don't look official (no logos, include a "not affiliated with Mojang" notice), and guidelines can change. Realistic outcome: modest sales, driven by reach.

## Safety rules
- Only sync a world that is closed on this machine (check `session.lock` freshness and the game process). Never upload `session.lock`.
- Push when the world closes; pull at app start, on a timer, and on Force sync. If a world is open locally and a newer version exists, warn instead of pulling.
- Local snapshots (keep last N) before any overwrite, with one-click restore.
- Hash verification on every download; atomic swap into place.

## How it works (plain language)
A small program lives in the system tray on each computer. You connect your Google Drive or Dropbox once in a small window. When you quit Minecraft on the desktop, it uploads the changes; later, when the laptop is on, it downloads them. The same window lets you tick which worlds to sync.

## Stack
- **Go** single binary per OS, local HTTP API on 127.0.0.1 with an embedded **SvelteKit (static, TypeScript)** UI.
- **Tray:** `fyne.io/systray` (or `energye/systray`). **Window:** `webview/webview_go` (or Wails v3 if stable at build time; check first); fallback opens the UI in the default browser. Linux needs an appindicator library.
- **Store interface** in `internal/store`: `gdrive` (official `google.golang.org/api/drive/v3` or plain REST), `dropbox` (REST via `net/http`), plus `localdir` (used by tests and as an advanced option for Syncthing users) and `s3` (Pro, `minio-go`) later.
- **OAuth:** `golang.org/x/oauth2` with PKCE and a loopback listener.
- **Chunking:** content-defined chunking (FastCDC or 1-4 MB blocks), SHA-256 or BLAKE3, `klauspost/compress`, optional `XChaCha20-Poly1305` encryption.
- **World discovery:** vanilla `.minecraft/saves`, Prism, MultiMC, CurseForge, Modrinth App, plus custom paths.
- **Licensing:** Gumroad license-key verify with offline grace. Settings in JSON.

## Pricing and free/paid split
- **$12 one-time** (launch $9). No running cost for you because buyers use their own storage.
- **Free:** sync 1 world through one linked account.
- **Pro:** unlimited worlds, both providers at once, S3/R2/B2, encryption, snapshot history, conflict tools, multiple launcher instances, 3+ devices.

## MVP build order
1. World discovery plus chunking, manifest and hashing engine, tested on fixture worlds.
2. `localdir` store: push, pull, atomic swap, snapshots, conflict detection and resolution (all testable offline).
3. Game-running and lock-file detection; push on world close; pull on start and on a timer.
4. OAuth and store for **Google Drive** (`drive.appdata`), then **Dropbox** (App Folder), behind the same interface.
5. Tray icon plus web UI (connect account, world picker, storage usage, status, history, conflict dialog, force sync).
6. Licensing, free-tier limit, installers (zip/MSI, dmg, AppImage/deb) via GitHub Actions.
7. Privacy policy and terms pages; provider app review submissions (start early, since approval can take time).
8. Later: S3 stores, encryption, Bedrock worlds, LAN fast path.

## Out of scope for v1
Bedrock Edition, servers/Realms, mod/config sync, mobile, a hosted "WorldHop Cloud", LAN transfer.

## Files to create (`/home/user/worldhop`)
`cmd/worldhop/main.go`; `internal/{worlds,chunker,manifest,store/{localdir,gdrive,dropbox},oauth,sync,tray,license,api}/`; `ui/` (SvelteKit static, TS); `testdata/` (small fixture worlds including a stale `session.lock`); `.github/workflows/build.yml`; `README.md` with a not-affiliated notice; `site/` (privacy policy and terms).

## Selling it
Gumroad listing with a 30-second GIF: "quit on the desktop, open on the laptop later". Free tier as lead magnet. Post to r/Minecraft, r/admincraft, r/feedthebeast and launcher communities (follow their rules); short YouTube demos. Clear "not an official Minecraft product" line, no Mojang logos. Unsigned apps trigger OS warnings; write install notes.

## Verification
- `go test ./...` with `localdir` and an in-memory fake store: chunk/manifest round-trip; atomic swap (kill mid-pull, world stays intact); push interrupted before manifest write leaves the store consistent; conflict matrix (A only, B only, both changed); lock file never uploaded; hash mismatch rejected.
- Provider adapters tested against recorded HTTP fixtures plus a contract test suite run against both fake and real stores.
- Simulate two devices in one test with two temp world dirs and one shared store; run them sequentially to model "laptop was off".
- Fuzz the manifest parser.
- Playwright (Chromium preinstalled) against the UI for connect-account screen (mocked), world picker, conflict dialog, force sync.
- Manual (needs your accounts and hardware): connect a real Google account and a real Dropbox account on two computers; play, quit, power off the first machine, open on the second; revoke access from the provider's settings and confirm the app asks to reconnect; interrupted sync; deliberate conflict.
