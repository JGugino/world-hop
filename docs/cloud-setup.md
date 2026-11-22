# Cloud account setup (Google Drive and Dropbox)

## For end users

Nothing to set up. Click **Connect** next to Google Drive or Dropbox, sign in and approve access
in the browser that opens, and you are done. There is no `.env` or `cloud.json` to create.

## For the maintainer (once)

Release binaries carry the app's own OAuth client IDs, compiled in at build time. Register them
once, then build releases with them.

1. **Google**: in Google Cloud Console enable the Drive API, configure the consent screen, and
   create an OAuth client of type **Desktop app**. The app requests only `drive.appdata`.
   Loopback redirects (`http://127.0.0.1:<random port>/callback`) are allowed for desktop clients.
   Publish the consent screen (move out of "Testing") so any user can sign in.
2. **Dropbox**: create an app with **App Folder** access and register
   `http://127.0.0.1:<port>/callback` as a redirect URI. Pin that port at build time with
   `DROPBOX_REDIRECT_PORT` if Dropbox needs an exact match. PKCE with `token_access_type=offline`
   means the refresh token needs no app secret.
3. **Build** with the IDs supplied from your environment or CI secrets:

   ```sh
   GOOGLE_CLIENT_ID=... DROPBOX_APP_KEY=... make release
   # optional: GOOGLE_CLIENT_SECRET=... DROPBOX_REDIRECT_PORT=...
   ```

   This runs `go build -ldflags "-X github.com/JGugino/world-hopper/internal/link.DefaultGoogleClientID=... "`
   (see the `release` target in the makefile). Do not commit real IDs.

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
| `WORLDHOP_DROPBOX_APP_KEY` | `dropboxAppKey` | Dropbox app with **App Folder** access |
| `WORLDHOP_DROPBOX_REDIRECT_PORT` | `dropboxRedirectPort` | optional fixed loopback port |

## Details

- Google: enable the Drive API; the app requests only `drive.appdata`.
  Redirect URI is `http://127.0.0.1:<random port>/callback` (loopback is allowed for desktop clients).
- Dropbox: register `http://127.0.0.1:<port>/callback` as a redirect URI; PKCE with
  `token_access_type=offline`, so the refresh token needs no app secret.
- Tokens are kept in the OS keychain (service `worldhop`, one entry per provider). If the
  keychain is unavailable linking fails with an error rather than falling back to a file.
- `-store <dir>` still selects the local-directory store and bypasses cloud accounts.

## Store layout

Drive (appDataFolder, flat names plus `appProperties`) and Dropbox (App Folder paths) both hold
`packs/<sha256>` and `worlds/<id>/manifest-<gen>-<device>.json`. Packs upload first, the manifest
last. Manifests are write-once: Dropbox enforces it with `mode: add`; Drive checks for the name first.
