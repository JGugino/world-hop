# Licensing and releases

## What the free version and Pro do

| | Free | Pro |
|---|---|---|
| Worlds synced (per computer) | 1 | unlimited |
| Cloud storage | the user's own Google Drive | same |

Pro is a one-time purchase on Gumroad ($12; $9 at launch, set on the Gumroad
product, not in code). Gumroad sells to the user, emails them a license key, and
the user pastes it into **License** in the web view. No file editing.

The free world is the first one to sync, or whichever the user picks on the
License page. Nothing else changes: status, snapshots and restore still work
for locked worlds; only push, pull and conflict resolution are refused.

## How it is enforced

`sync.Engine.Authorize` (set to `license.Manager.Authorize` in `main.go`) runs
before every push, pull and conflict resolution, so the scheduler, Force Sync
and the per-world buttons are all covered. The UI only reflects the result.

A key is verified with Gumroad's `licenses/verify` API (refunded, charged-back
or lost-dispute purchases are rejected). It is re-checked every 7 days while
online. If Gumroad cannot be reached, Pro keeps working for 30 days since the
last successful check, then falls back to free until it can check again.

Known limits (by design, to stay simple): the check is local, so a determined
user can patch the binary; a key is not limited to a number of computers.

## One-time maintainer setup

1. Create the Gumroad product with **Generate a unique license key** on. Copy
   its **product ID** (Gumroad calls this `product_id`; it is not the URL slug).
2. Add repository secrets: `GUMROAD_PRODUCT_ID`, optionally `PURCHASE_URL` (the
   product page), plus `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` from
   [cloud-setup.md](cloud-setup.md).
3. Tag a release: `git tag v0.1.0 && git push origin v0.1.0`. The
   `release` workflow runs the checks, builds each OS, and opens a **draft**
   GitHub release with the installers for you to review.

Local release build: `make release GOOGLE_CLIENT_ID=... GUMROAD_PRODUCT_ID=...`
(or put them in `.env`). `make release-windows` builds `worldhop.exe` and `make release-all` both.
A build without a product ID still runs but cannot activate Pro.

## Installers

- Windows: `WorldHop-<version>-windows-amd64.zip` (unzip and run `worldhop.exe`).
- macOS: `WorldHop-<version>.dmg` with an unsigned `WorldHop.app` (no Dock icon;
  lives in the menu bar). First launch: right-click, Open.
- Linux: `worldhop_<version>_amd64.deb` and a `.tar.gz`. The tray needs an
  AppIndicator-capable desktop.

Not done yet: code signing / notarization, an MSI, and an AppImage.
