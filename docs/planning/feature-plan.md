# WorldHop feature plan (planning only, no code changed)

Written 2026-11-23 after reading the repo at `main` (commit 0a15844, PR 34). No PRs were opened.

**Revision 2 (same day):** features I did not recommend (mods and configs sync, live "who has it open" presence, automatic world merging, the launcher pre-launch hook, "sync complete" notifications) are removed. Josh's answers are applied: conflict resolution is Free, the world sync id becomes editable in a safe way, device names are anonymous by default and set in a Settings page, and Free cloud history is kept for 14 days.

**How to read the estimates.** They are rough guesses, not measurements. One "thread" means one focused Claude session that produces a tested PR for you to review (roughly a few hours of work, or about 1 to 2 days for a human developer). The real bottleneck is usually not writing code but **testing on real Windows and macOS machines**, which I do not have; I add a note where that matters. Anything marked "needs verifying" is something I inferred from reading the code and did not run.

## 1. Summary table

| # | Feature | Effort (rough) | Worth adding? | Tier |
|---|---|---|---|---|
| 1 | Better snapshot summaries | 1 thread | Yes, do first. It is the foundation for 2, 3, 4 and 7a | Free |
| 2 | Restore / rollback any version | 1 thread (needs #1) | Yes | Local snapshot restore stays free; browsing and restoring any cloud version is Pro |
| 3 | Conflict handling, side by side | 1 thread (needs #1) | Yes | Free (decided) |
| 4 | "Who last saved the world" + out-of-date warning | 1 thread | Yes; the warning can only warn, not block | Free |
| 5 | Tray notifications | 1 thread, plus your testing on Windows | Yes, for problems and new versions | Free |
| 6 | Retention + storage usage | 0.5 thread local; 2 to 3 threads cloud (risky) | Local yes. Cloud cleanup is needed but is the riskiest item here | Free keeps 14 days of cloud history automatically; Pro configures it |
| 7a | Sync history timeline | 1 thread (needs #1, #2) | Yes, good Pro headline | Pro |
| 7b | World list with thumbnails | 0.5 to 1 thread | Yes, cheap polish | Free (I would not paywall it) |
| 8 | Other launchers + editable world id | 1 thread | Mostly already done; small fixes | Free |
| 9 | Styled cloud sign-in return page | 0.5 thread | Yes, cheap polish; mockups approved | Free |

Recommended order is in section 10.

## 2. How the code works today (what the plan is grounded in)

- **Engine** (`internal/sync/engine.go`). A world is a folder containing `level.dat`. Its store id is the folder name made safe (`worlds.IDFromName`), so two devices match worlds by folder name.
- **Versions are manifests.** A push chunks the world (content-defined chunking), uploads new chunks in ~16 MiB pack files, and only then writes a new immutable `manifest-<generation>-<deviceId>.json`. A manifest holds `deviceId` (a random 12-hex string), `generation`, `parents`, `createdAt`, the file list (path, size, mode, mtime, chunk hashes) and chunk locations. Nothing is ever overwritten. Heads are manifests no other manifest lists as a parent. Two heads means a conflict.
- **No metadata about the event.** A manifest does not record the trigger (world closed, timer, force sync, conflict resolution), a human device name, play time or a note. The file list does give file count and total size for free.
- **Local snapshots** (`internal/sync/snapshot.go`). When a pull, conflict resolution or restore replaces a local world, the old folder is kept as `snapshots/<worldId>/<timestamp>-<reason>`. The reason is one of `pull`, `conflict`, `pre-restore`, `recovered`. **This is why the UI shows "pull" for everything**: the snapshot id is just a timestamp plus that reason word, and the UI splits it on the first dash (`snapshotDTO` in `internal/api/routes.go`). Snapshots are full copies of the world (a rename when on the same volume), the newest 5 per world are kept (`SnapshotsKeep`, not user configurable).
- **Restore exists, but only for those local snapshots** (`Engine.Restore`, `POST /api/worlds/{id}/restore`, UI button). It snapshots the current world first (`pre-restore`), then swaps the old one in. The cloud versions have no UI at all.
- **Conflicts** (`Engine.Resolve`). Four strategies already exist: keep local, keep remote, keep newest, keep both. The UI shows buttons for them. There is no view of *what* differs.
- **Status** per world is one of synced, local-ahead, remote-ahead, conflict, unknown. It does not say who or when.
- **Scheduler** (`internal/sync/scheduler.go`). Polls open/closed state every 5 s, pushes a world 3 s after it closes, and runs a full pass every 10 minutes. World open detection combines the OS file lock on `session.lock` with a Java process check (`internal/detect`).
- **Tray** (`internal/tray`). `fyne.io/systray`: Open Web View, Force Sync, an update item, Exit, and a status tooltip. systray has no notification API.
- **Stores.** The `Store` interface is deliberately minimal and write-once: `PutPack/GetPack`, `PutManifest/GetManifest`, `ListManifests`, `ListWorlds`. **There is no delete and no way to list packs.** Google Drive (`drive.appdata`), S3 (Pro) and localdir implement it.
- **Encryption** (`internal/crypt`). A wrapper store. Pack names become HMACs, manifest bodies are encrypted, and each world id is mapped through an HMAC too. Manifest file names still show generation and the random device id. Note a comment in `engine.go`: the plain layout and the encrypted layout of the same cloud account can coexist, and the sync state is scoped separately for each.
- **Known gaps** (from earlier notes): change-passphrase, rollback protection for old manifests, old pack cleanup, first-run flow.
- **Launchers.** `worlds.DefaultRoots` already finds vanilla, Prism, MultiMC, CurseForge and Modrinth App saves folders on Windows, macOS and Linux, and the Saves tab lets the user add any folder.

---

## 3. Feature 1: Better snapshot summaries

**What it involves.**
Two kinds of things are called "snapshot" and both need richer labels:
1. *Cloud versions* (manifests).
2. *Local safety snapshots*.

Plan:
- Add an optional `meta` object to the manifest. This is additive: `manifest.Parse` uses plain `json.Unmarshal` and `Validate` only checks `Version == 1`, so old app versions keep reading new manifests and I would not bump the version. Fields: `deviceName`, `trigger` (`world-closed`, `timer`, `force`, `conflict-keep-local`, `restore`, ...), `appVersion`, `note`, `playSeconds`, `lastPlayed`, `gameVersion`, `levelName`, and `thumbnail` (see 7b).
- File count and total size come straight from the manifest. "Changed since the previous version" comes from diffing the file lists against the parent manifest (also in the store and cached on disk).
- Add a **device name** setting on a new **Settings page** in the UI, stored next to `device-id` in the state dir. **The hostname is never read or stored.** The default is an anonymous label made from the random device id, such as "Computer 7F3A", and the user can rename it ("Desktop", "Laptop"). The manifest carries that name so other computers can show it. A first-run hint on the Settings page asks the user to name each computer.
- Local snapshots: write a small `<id>.meta.json` **next to** the snapshot folder, not inside it (a file inside would be copied into the world on restore). Record reason, which cloud version it replaced, size and the device that made it. `pruneSnapshots` must delete the sidecar too. Reasons get plain-language labels: "Replaced by newer version from Laptop", "Backup before restore", "Kept when resolving a conflict".
- **Play time.** Two sources, best used together: (a) the scheduler already knows when a world goes open to closed, so it can record session length (only accurate while WorldHop runs); (b) the world's `stats/<uuid>.json` holds a play-time counter (the key name differs between game versions: `minecraft:play_time` in newer ones, `play_one_minute` in older), so the delta since the last version works as a fallback. Needs verifying against a few game versions.
- **Reading level.dat** (gzip + NBT) for level name, game version and last-played needs a small NBT reader (~150 lines, no new dependency needed; go.mod is stdlib plus systray, webview, keyring, x/crypto).
- **User note or name.** Cloud manifests are immutable, so you cannot edit a note after the fact. Practical design: a note applies to the *next* push ("Sync now with a note", or a note box on the world card that is consumed by the next version), and local snapshots can be renamed freely because the sidecar is a mutable local file. Post-hoc editing of cloud notes would need a new kind of mutable object in the store; I would not build that.

**Effort (rough):** 1 thread. The NBT reader and play-time logic are the fiddly part (older and newer game versions differ).

**Problems and risks.**
- *Encryption:* the manifest body is encrypted, so device names, notes and play time stay private when encryption is on. When it is off they are plain text in the user's own Drive or S3 bucket. Because the name is chosen by the user and defaults to an anonymous label, nothing personal is stored unless they type it. Mention this in the privacy page.
- *Old devices:* a computer running an older WorldHop shows no summary for versions written by a newer one. Harmless; show "unknown" for missing fields.
- *Accuracy:* play time from sessions is wrong if WorldHop was not running; from stats it is wrong if the player changed accounts. Label it as approximate.
- *Storage:* a thumbnail inside the manifest adds tens of KB to every version. Keep it small, or only embed when it changed.

**Good idea?** Yes, and the most valuable item on the list: it turns a technical list into something a player recognises ("Tuesday 9:14 pm from Laptop, 38 min played, 12 files changed"). Almost everything else here leans on it.

**Tier:** Free. It is the base layer for safety features.

---

## 4. Feature 2: Restore and rollback of any snapshot

**What it involves.**
- *Local snapshots:* already restorable with a safety backup (`pre-restore`). Improvement: better labels from feature 1, and an "Undo last sync" shortcut that restores the newest snapshot.
- *Any cloud version* (new). Because nothing is ever deleted from the store today, every old manifest and its packs still exist. A rollback should be a **forward change**, not rewriting history:
  1. Pick old manifest M. Check every pack it needs is still in the store.
  2. Build M in the staging folder using the existing `materialize` (it reuses local data to avoid downloads).
  3. Swap it in with `swapWithSnapshot(... "pre-restore")`, so the current world is kept as a safety backup first.
  4. Write a **new manifest** that copies M's files and chunks, with generation = newest head + 1 and parents = all current heads. No data is uploaded because the chunks already exist.
  5. Other computers then see it as an ordinary newer version and pull it. If another computer has unsynced changes, it goes through the normal conflict path and nothing is lost.
- API: `GET /api/worlds/{id}/versions` and `POST /api/worlds/{id}/restore-version`.
- Works with the same checks as a pull: refuses if the world is open, requires encryption unlocked.

**Effort (rough):** 1 thread, after feature 1.

**Problems and risks.**
- **Data loss:** the main protection is the pre-restore backup plus the fact that history is never rewritten. Test: restore, then immediately restore the version that was current before (undo of an undo).
- **Disk space:** restore needs room for staging plus the kept old copy (roughly 2 to 3 times the world size on the same volume). Check free space before starting and show a clear message. There is already a `disk-full` problem kind to reuse.
- **Newer game version:** rolling back is fine, but a version saved with a *newer* Minecraft than the one installed here may refuse to open or be downgraded badly. Once feature 1 records `gameVersion`, warn before restoring a version from a different game version.
- **Retention interaction:** restore only works for versions that still exist. Once cloud cleanup (feature 6) is built, the UI must only offer retained versions and "pinned" versions must be exempt from cleanup.
- **Rollback protection (existing gap):** if you later add "a device refuses a head with a lower generation than it last saw", a legitimate rollback must stay compatible. The forward-manifest design above is, because the new manifest has a higher generation.
- **Encryption:** works unchanged, since it only uses the store through the same wrapper.

**Good idea?** Yes. It is the "undo" that makes people trust a sync tool, and it is cheap because the data is already there.

**Tier:** Free: restoring local snapshots and "undo last sync" (as today). Pro: browse and restore any cloud version (that is the "snapshot history" in your plan).

---

## 5. Feature 3: Conflict handling, side by side

**What it involves.**
Today a conflict is detected and the UI offers four buttons without showing what differs, which is a hard decision to make blind. Add a **compare view**:
- For each side (this computer, the other head(s)): device name, time, play time, last played, file count, size, game version (from feature 1).
- A three-way file diff using the last synced manifest (`state.Base`, which is still in the store) as the common ancestor: files changed only here, only there, and on both sides. For Minecraft, almost every active session changes `level.dat`, some region files and `playerdata`, so "both sides changed region r.0.0.mca" is the usual case.
- Plain-language recommendation, not a decision: "The Laptop version is newer and has 38 more minutes of play."
- Keep both exists already. Improve its naming and explain how to open each copy.

**Effort (rough):** 1 thread (diff logic is small; most of the work is UI), after feature 1.

**Problems and risks.**
- **Keep-both side effect (needs verifying):** it creates `<world> (conflict copy ...)` inside the saves folder. The scanner would then discover that as a new world with its own id. For free users that second world hits the one-world limit, and for Pro users it syncs as a second world. Decide whether the copy should be created in a different place, excluded from sync, or deliberately synced. A user-facing note ("this copy is a separate world") is the minimum.
- **Encryption:** unaffected, the diff uses decrypted manifests.
- **Base manifest missing:** after cloud cleanup (feature 6), the common ancestor might be gone. Fall back to a two-way comparison.
- **Prevention is better than cure:** most conflicts happen because a computer played a stale world. See feature 4.

**Good idea?** Yes.

**Tier:** Free (decided: fixing conflicting worlds is not paywalled). Update the pricing and free-vs-Pro wording in the Gumroad listing and docs if they currently list conflict tools as Pro.

---

## 6. Feature 4: "Who last saved the world" and an out-of-date warning

**What it involves.**
- **Status line per world:** "Last saved by Laptop 2 h ago. This computer is up to date / behind / has newer changes." This comes from the head manifest (device name, createdAt) plus the existing status, and needs feature 1's device names.
- **Warning before opening an out-of-date world.** A tray app cannot block Minecraft from launching. What is possible, from most to least useful:
  1. **Make the stale window small.** Full pass every 10 minutes is long. Poll the cloud more often (for example every 2 to 3 minutes) while no game is running. With cached manifests this is one list request per world; check Drive rate limits and cost before choosing.
  2. **Pull the moment it is safe.** When a game process is not running and a world is remote-ahead, pull immediately instead of waiting for the next pass.
  3. **Notify when it is already too late:** when the game opens a world that is behind, send a notification (feature 5): "Newer version of X from Laptop exists. Close the game to let WorldHop update it, or you may get a conflict."
**Effort (rough):** 1 thread for the status line, faster polling and the "already open" warning.

**Problems and risks.**
- Expectation risk: wording must not promise "we stop you". Say "we warn you".
- Scope: "who last saved" only. There is no live "who has it open now" indicator.
- Shorter polling raises API usage per user. Google's quota is shared by all your users (per project), so test the request count and back off when idle. Not measured.
- Device names must be set for this to read well (feature 1).

**Good idea?** Yes for the status line and faster pulls. A tray app cannot block the game from launching, so the warning is honest about only warning.

**Tier:** Free (it prevents conflicts and is part of the core promise).

---

## 7. Feature 5: Tray notifications

**What it involves.**
- A small notification layer in a new `internal/notify` package, with an event source in the Runner (pass finished, problem appeared or cleared, update found, newer version arrived).
- `fyne.io/systray` cannot show notifications, so this needs per-platform code: Windows toast, macOS `osascript`/UserNotifications, Linux D-Bus (godbus is already an indirect dependency). A library such as `beeep` could cover all three but adds a dependency; pick after a quick check of its Windows behaviour.
- **Rules to avoid spam:** notify on *state changes*, not on every pass (the scheduler runs every 10 min, so a persistent error would otherwise fire six times an hour). Dedupe by problem kind and world, rate limit, and clear when resolved.
- Categories and defaults: failures that need you (auth expired, quota, conflict, encryption locked): **on**. A newer version was downloaded: **on**. Update available: **on, once per version**. There is no "sync complete" notification; the tray tooltip already covers that.
- A toggle per category on the Settings page (no config files, per your rule) and a "do not notify" master switch.

**Effort (rough):** 1 thread. The code is small, the platform differences are what take time.

**Problems and risks.**
- **Platform differences:** Windows toasts for an unsigned app without a registered app id may show a generic name or not show reliably; macOS notifications via `osascript` appear as "Script Editor", and there is no Mac build yet; Linux depends on a notification daemon being present (the tray already needs an appindicator). I cannot test any of this here; it needs you on real Windows.
- **Do Not Disturb / full-screen games:** the OS mostly handles this; do not use sounds.
- **Headless mode (`-no-tray`)** should skip notifications.
- Silent failure is the biggest trust risk for a background sync app, so failure notifications have real value even when the rest is cosmetic.

**Good idea?** Yes for problems and new versions.

**Tier:** Free.

---

## 8. Feature 6: Snapshot retention and storage usage

This one has two very different halves.

### 6a. Local snapshots (small)
- Today: keep the newest 5 per world, hard coded, full copies.
- Add: choose "keep last N" or "keep N days" (UI setting), always keep at least 1, show disk usage per world and a "delete old snapshots" button. Pinned/named snapshots (feature 1 notes) are never auto-deleted.
- **Risk:** disk use. A 2 GB world times 5 snapshots is 10 GB under the app's state folder, and this is invisible to most users today. A usage figure is arguably more urgent than the retention setting.
- **Effort (rough):** 0.5 thread.

### 6b. Cloud retention (large, and the riskiest feature in this document)
Today nothing is ever deleted from the cloud: every version and pack stays forever, so usage only grows. The Google Drive free quota is shared with Gmail and Photos, so this will eventually bite users. The plan always said "keep last N manifests, delete unreferenced packs after a grace period", but it is not built.

What it involves:
- Extend the `Store` interface with `DeleteManifest`, `DeletePack`, and a pack listing with size and modified time, then implement for Drive, S3, localdir, the encryption wrapper and the fake stores, with contract tests. `drive.appdata` allows deleting the app's own files, so no new scope is needed (check this when adding).
- **Decided policy (Josh: Free keeps a month at most, a couple of weeks may be better).** My recommendation: **Free keeps 14 days of cloud history**, with a floor: it always keeps the current head(s) and the newest 3 versions of every world even if they are older, so a world nobody played for a month never loses its latest state. Pro makes it configurable (days or count, up to unlimited) and adds pinning. Why 14 and not 30: most "oops" moments are noticed within days, and half the storage of a month matters on a 15 GB Drive that is shared with Gmail and Photos. 30 days would also be defensible; the number is one constant, so it is easy to change later. Pinned versions are never deleted.
- Also keep: the ancestors needed to decide status for devices that have been offline.
- Cleanup runs once a day at most, only while the store is reachable and (if encrypted) unlocked. Show the policy plainly on the Settings page ("Cloud history is kept for 14 days. Upgrade to keep it longer.") so deletion never surprises anyone.
- Free users still get the local safety snapshots as before, so a restore is available even after cloud history ages out.
- **Safer algorithm:** delete only packs that were referenced by manifests being removed and are not referenced by any retained manifest. Do **not** do "delete every pack not referenced". Reason: the plain and encrypted layouts can coexist in one cloud account, and under the plain layout the encrypted layout's HMAC-named packs look unreferenced. A naive mark-and-sweep run in one mode could delete the other mode's data, which is a data-loss bug. The manifest-driven approach never touches names it cannot attribute. Orphans from interrupted pushes are small and can be left alone, or swept later with a grace period.
- **Races:** a push uploads packs before writing its manifest, so another device's in-flight pack looks unreferenced for a while. The manifest-driven approach avoids that too (it only deletes packs from old manifests), but a push that *reuses* an old pack right when cleanup deletes it is still possible. Use a grace period, and have push verify that packs it intends to reuse still exist (re-upload if missing).
- **Offline devices:** a laptop that has been off for months has a base manifest that may have been cleaned. The engine must still classify correctly using the stored tree hash (`state.BaseTree`): unchanged local plus a different head means remote-ahead; changed local means conflict. Needs tests, and cleanup must never remove heads.
- **Poor reclaim:** packs hold ~16 MiB of chunks from many versions, and a pack stays alive if any retained version references even one chunk in it. A world that changes a little on every session may free much less space than expected unless packs are periodically repacked (compaction). I have no measurements; build a benchmark with fixture worlds before promising users anything.
- **Encryption:** cleanup needs the key (to read manifests), so it can only run while unlocked, and never on a locked store.
- **Storage usage view:** per world: number of cloud versions, bytes referenced by retained versions, total store size (from the pack listing), plus local snapshot size. With "Free up space" showing a preview ("will delete 14 versions, about X GB").

**Effort (rough):** store interface + Drive/S3 delete + contract tests: 1 thread. Retention engine with races, offline-device and encryption tests: 1 to 2 threads. Usage view UI: 1 thread (can be folded into the timeline thread). Everything touching deletion should have the most tests of any feature.

**Good idea?** Local retention and usage: yes. Cloud cleanup: necessary eventually, but do it late, after restore and the timeline exist (so you know what users actually keep), and ship it conservative (14 days plus the 3-newest-versions floor, with a dry-run preview first). A bug here deletes users' worlds, so I would put the extra review time here.

**Tier:** Free gets the automatic 14-day policy and the usage view. Pro gets configurable limits, pinning and manual cleanup. The 14-day cleanup applies to everyone by default, otherwise free users fill their Drive.

---

## 9. Feature 7: Pro extras (history timeline and thumbnails)

### 7a. Sync history timeline
A per-world timeline of cloud versions: time, device, trigger, size, changed files, play time, note, thumbnail, with Restore, Pin and Compare buttons. It is the UI for features 1 and 2. Needs `GET /api/worlds/{id}/versions` (the engine already loads all manifests, cached). With encryption on it only works unlocked.
- **Effort (rough):** 1 thread, after 1 and 2.
- **Risk:** long histories (hundreds of versions) need paging; versions written by old app versions lack metadata.
- **Good idea?** Yes. It makes "I can go back in time" visible and is the clearest reason to pay for Pro.
- **Tier:** Pro.

### 7b. World list with thumbnails
Show the world's `icon.png` (Minecraft's 64 by 64 world icon, present in most modern worlds but not guaranteed), the in-game level name from `level.dat` (the folder name is often "New World (3)"), game version and last played.
- Local worlds: serve the file via `GET /api/worlds/{id}/icon`.
- Cloud-only worlds (not yet downloaded): embed a small thumbnail in the manifest meta (only worth it if size stays small), otherwise show a placeholder.
- Shares the NBT reader with feature 1.
- **Effort (rough):** 0.5 to 1 thread.
- **Risk:** purely cosmetic; missing icons need a good placeholder.
- **Good idea?** Yes, cheap, and it makes the app feel finished.
- **Tier:** I would keep it Free. Paywalling a thumbnail feels petty and the free tier is your advertising.

---

## 10. Feature 8: Support for other launchers, plus editable world id

**What exists.** `DefaultRoots` already looks for Vanilla, Prism, MultiMC, CurseForge and Modrinth App saves folders on Windows, macOS and Linux, and the Saves tab can add any folder or single world. Game detection finds any Java process with a Minecraft main class, and the lock check works at the file level, so it is launcher independent. So "support" is largely done; the work left is path coverage and a world-id issue:

- **Gaps in discovery (needs verifying on real installs):** Prism or MultiMC with a custom instance directory or a portable install (MultiMC on Windows is typically unzipped anywhere, so there is no standard path; Prism can be told to use another folder), Prism as a Flatpak (`~/.var/app/org.prismlauncher.PrismLauncher/...`), the macOS CurseForge location (usually under `~/Documents/curseforge/...`), and other launchers (ATLauncher, GDLauncher, Technic and similar). Improvement: read Prism's own config for its instance folder; add the Flatpak path; add a "scan for Minecraft saves" helper that checks common places and offers results.
- **World id collision and the editable id (Josh: fine as long as nothing breaks badly).** World id is the folder name. Several instances each with a world called "New World" collide, and `Resolve` reports "world id already used" and only syncs the first. Plan: keep folder-name ids as the default so existing setups keep working, and add a **Sync name** per world on the Saves/Worlds screen, stored in the app's own settings (a local map of folder to id, no hand-edited file). To keep it safe:
  - Changing the id of a world that already has cloud versions would make the cloud treat it as a brand new world. So the UI never offers a bare "edit id" for those. It offers two explicit actions: **"Link to an existing cloud world"** (pick from the cloud world list; the first sync then goes through the normal pull or conflict path) and **"Sync as a new world"** (re-uploads; cheap because chunks are deduplicated, but it counts against the free one-world limit and leaves the old cloud world in place).
  - Free editing is allowed only for worlds that have never synced.
  - Validate with `manifest.ValidID`, warn on collision before saving, and never delete anything when an id changes.
  - Test: rename, then sync from two computers, and confirm the old id's versions are untouched.
- **Not covered:** Bedrock (out of scope), servers and Realms.

**Effort (rough):** 0.5 thread for path additions and tests with fake directory trees; +0.5 for the editable sync name. Cannot confirm on real launchers from here.

**Good idea?** Yes, but as small fixes, not a big feature. Launchers are how people find this app, so wider discovery helps adoption.

**Tier:** Free.

---

## 10b. Feature 9: Styled cloud sign-in return page

**What exists.** After Google sign-in, the browser lands on the app's local callback (`/callback` in `internal/oauth/oauth.go`, around line 138). It answers with two bare, unstyled lines of HTML: "Connected" or "Could not connect" plus the raw error text.

**Plan (mockups approved by Josh, 2026-11-24).** Replace those lines with one small, self-contained page in the web UI's Minecraft style (dark bevelled panel, pixel heading, grass strip, cube logo). It must be self-contained (inline CSS, embedded font and logo) because the callback runs on its own loopback server, not the UI server. States:
- **Connected:** green check, "Google Drive is now linked to WorldHop", "You can close this tab and return to WorldHop", tip that WorldHop keeps running in the tray.
- **Not connected (access_denied):** friendly message that nothing was linked and no files were touched, and how to try again.
- **Could not connect (other errors):** plain message plus the technical detail in a small box (HTML-escaped).
- Keep the "Not affiliated with Mojang or Microsoft" footer. Provider name comes from the flow (Google Drive only for now; Dropbox returns later).
- Leave the stray "unexpected state" 400 as is.
- Tests: one per state, checking status, content type, escaping and that the result still reaches the flow.

Mockups and HTML sources are kept outside the repo.

**Effort (rough):** 0.5 thread; fits alongside the Settings page work or the first-run flow. **Tier:** Free.

---

## 11. Recommended order and dependencies

```
Phase 1 (foundation, all Free, can run in parallel)
  A. Snapshot metadata + device name + play time (#1)        <- everything below depends on it
  B. World info: icon, level name, version (#7b)             <- shares the NBT reader with A; do together or right after
  C. Tray notifications (#5)                                 <- independent
  D. Launcher path gaps + editable sync name (#8)            <- independent
  D2. Styled cloud sign-in return page (#9)                  <- independent, small
  S. Settings page (device name, notification toggles, sync names, retention info) <- small; do with A

Phase 2 (safety features, need A)
  E. Compare view for conflicts (#3)
  F. Last saved by / faster pulls / "already open" warning (#4)  <- uses C for the warning
  G. Restore any cloud version, undo last sync (#2)

Phase 3 (Pro value and housekeeping, need A and G)
  H. Sync history timeline (#7a)                             <- needs A and G
  I. Local retention + usage (#6a)
  J. Store delete/list-packs + cloud retention + usage view (#6b) <- needs G and H first; most testing

```

Rough total for phases 1 to 3: about 10 to 11 threads, of which the cloud retention step is the biggest and riskiest. Reviewing and testing on real Windows and macOS machines will probably take longer than the code. If you only do part of it, **A, E, F, G and C** give the most safety and trust for the least risk.

Cross-cutting rules I would apply to all of it:
- Manifest changes must stay additive (no version bump) so older installs keep working.
- Never delete or rewrite versions except in the dedicated retention work.
- Every new feature that reads versions needs a "store is encrypted and locked" path and a test.
- Each new screen needs an empty and error state (the existing Problem and status system already has the vocabulary).

## 12. Decisions recorded (Josh, 2026-11-23)

1. Conflict resolution is **Free**. Fixing conflicting worlds is not paywalled.
2. The world sync id becomes editable, **only in the safe form** described in feature 8 (link to an existing cloud world, or sync as a new one; never silently re-key a synced world).
3. **No hostname is stored.** Device names are anonymous by default ("Computer 7F3A") and set by the user on a Settings page.
4. Free cloud history is kept **14 days** (confirmed), with a floor of the head and the 3 newest versions; Pro configures it.
5. The styled cloud sign-in return page (feature 9) is added to the plan; mockups approved 2026-11-24.

## 13. Still open

- Update the Gumroad listing and docs if they list conflict tools as Pro.
