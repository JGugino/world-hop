# Cloud account setup (Google Drive)

## For end users

Nothing to set up. Click **Connect** on the Google Drive card, sign in and approve access
in the browser that opens, and you are done. There is no `.env` or `cloud.json` to create.

## For the maintainer (once)

Release binaries carry the app's own OAuth client IDs, compiled in at build time. Register them
once, then build releases with them.

1. **Google**: in Google Cloud Console enable the Drive API, configure the consent screen, and
   create an OAuth client of type **Desktop app**. The app requests only `drive.appdata`.
   Loopback redirects (`http://127.0.0.1:<random port>/callback`) are allowed for desktop clients.
   Publish the consent screen (move out of "Testing") so any user can sign in.
2. **Build** with the IDs supplied from your environment or CI secrets:

   ```sh
   GOOGLE_CLIENT_ID=... make release
   # optional: GOOGLE_CLIENT_SECRET=...
   ```

   This runs `go build -ldflags "-X github.com/JGugino/world-hopper/internal/link.DefaultGoogleClientID=... "`
   (see the `release` target in the makefile). Do not commit real IDs.

   Put the values in a gitignored `.env` next to the makefile and every release target picks
   them up. `make release-windows` cross-compiles `worldhop.exe` (no console window, default
   browser instead of the native webview) and `make release-all` builds both. For the native
   webview on Windows, build on a Windows machine with a C compiler (e.g. MinGW-w64) and run
   the same `go build` as the `release-windows` target with `CGO_ENABLED=1` and `-tags webview`.

These values are public client identifiers under PKCE, not confidential, and are visible in the
binary. Google's desktop-app client secret is also not truly secret (Google documents this); it is
only included because Google's token endpoint expects it.

If a binary is built without IDs, clicking Connect shows a developer-facing message
("sign-in is not set up in this build") instead of opening the browser.

## Developer override (optional)

For local development you can override the compiled-in values. Environment variables win over
`cloud.json` in the state directory (`<user config dir>/worldhop/` by default, or `-state`),
which wins over the compiled-in defaults.

| Env var | cloud.json key | Notes |
|---|---|---|
| `WORLDHOP_GOOGLE_CLIENT_ID` | `googleClientId` | OAuth client of type "Desktop app" |
| `WORLDHOP_GOOGLE_CLIENT_SECRET` | `googleClientSecret` | optional; not confidential for desktop clients |

## Details

- Google: enable the Drive API; the app requests only `drive.appdata`.
  Redirect URI is `http://127.0.0.1:<random port>/callback` (loopback is allowed for desktop clients).
- Tokens are kept in the OS keychain (service `worldhop`, one entry per provider). If the
  keychain is unavailable linking fails with an error rather than falling back to a file.
- `-store <dir>` still selects the local-directory store and bypasses cloud accounts.

## Store layout

Drive (appDataFolder, flat names plus `appProperties`) holds
`packs/<sha256>` and `worlds/<id>/manifest-<gen>-<device>.json`. Packs upload first, the manifest
last. Manifests are write-once: Drive checks for the name first.

Dropbox support was removed to keep a single provider to support. The `store.Store` interface and the
`link` provider table are still generic, so another provider can be added later.

## S3-compatible storage (Pro)

Pro users can store worlds in their own bucket instead of Google Drive: AWS S3, Cloudflare R2,
Backblaze B2, Wasabi, MinIO or anything else that speaks the S3 API. Nothing needs registering
with a provider; the user enters their own details on the **Cloud account** page:

- **Endpoint**: empty for AWS S3, otherwise the service URL (for R2,
  `https://<account>.r2.cloudflarestorage.com`; for MinIO, e.g. `http://localhost:9000`).
- **Bucket**, optional **folder** (key prefix) and **region** (use `auto` for R2).
- **Path-style addresses**: tick for MinIO and most self-hosted servers.
- **Access key** and **secret key**. The secret (and access key) are stored in the system keychain;
  only the non-secret settings are written to `s3.json` in the state directory.

**Test connection** lists the bucket with the entered details without saving anything. The key needs
list, read and write permission on the bucket. Objects use the same layout as the Google Drive
store (`packs/<sha256>`, `worlds/<id>/manifest-….json`) under the optional folder.

Pro gating uses the license check in `internal/license` (see docs/licensing.md): saving, testing, choosing
S3, and every store call require Pro, so a lapsed license stops S3 syncing.
