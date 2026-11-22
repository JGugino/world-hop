# Cloud account setup (Google Drive and Dropbox)

WorldHop never ships client IDs. Register your own and pass them in at runtime.
Environment variables win over `cloud.json` in the state directory
(`<user config dir>/worldhop/` by default, or `-state`).

| Env var | cloud.json key | Notes |
|---|---|---|
| `WORLDHOP_GOOGLE_CLIENT_ID` | `googleClientId` | OAuth client of type "Desktop app" |
| `WORLDHOP_GOOGLE_CLIENT_SECRET` | `googleClientSecret` | optional; not confidential for desktop clients |
| `WORLDHOP_DROPBOX_APP_KEY` | `dropboxAppKey` | Dropbox app with **App Folder** access |
| `WORLDHOP_DROPBOX_REDIRECT_PORT` | `dropboxRedirectPort` | optional fixed loopback port if the app needs an exact redirect URI |

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
