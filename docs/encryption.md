# Client-side encryption (Pro)

Encryption happens on your computer before anything is uploaded, so the cloud
provider only stores ciphertext. It lives in `internal/crypt` as a wrapper around
`store.Store`, so it works the same on Google Drive, a local folder, or any
store added later (for example S3).

## What is protected

| Hidden from the provider | Still visible |
| --- | --- |
| World file contents, file paths, sizes, chunk layout | Roughly how much data there is and when it changed |
| World names (folder names) | Manifest file names, which hold a generation number and a random device id |
| Pack names (HMAC of the plaintext hash, so identical data still dedups without revealing its hash) | Number of worlds, packs and manifests |

## How it works

- Passphrase -> Argon2id (64 MiB, 3 passes) with a random salt -> master key -> HKDF into
  a data key, a naming key and a key-check key.
- Packs, manifests and the world index are sealed with XChaCha20-Poly1305. Each blob is
  bound to its own name (pack name, or world id plus manifest name), so a provider cannot
  swap blobs around or move a manifest to another world without detection.
- A small **key header** (salt, KDF cost, key check) is stored next to the data in a reserved
  `_worldhop-key` area. It holds no secret. The key check means a wrong passphrase is
  reported immediately ("wrong passphrase") instead of failing mid-sync.
- Encrypted worlds live under hashed ids (`_worldhop-w-...`), and an encrypted index lets
  another computer list them.

## Second computer

Turn encryption on in the Encryption tab and enter the **same passphrase**. WorldHop finds
the key header in the cloud account, verifies the passphrase, and the worlds appear. The
first computer sets the passphrase; the form asks for it twice and for an explicit
acknowledgement that a lost passphrase cannot be recovered. Nobody, including the
maintainer and the cloud provider, can reset it.

## Existing unencrypted data

The store interface is write-once and has no delete, so WorldHop cannot remove the old
copy. Turning encryption on starts a separate encrypted layout beside it, and each world
is uploaded again encrypted on its next sync. The old plaintext copy stays until the user
deletes it in the provider's own settings (Google Drive: Settings -> Manage apps ->
WorldHop -> Delete hidden app data). The "last synced" records are kept per layout
(`Engine.StateScope`), so turning encryption on or off never causes a phantom conflict or
pulls older data over newer. Every computer that syncs a world must turn encryption on; a
computer that has not yet will keep using the old layout.

## Remembering the passphrase

Off by default. If the user opts in, the derived key (not the passphrase) is put in the OS
keychain (Keychain, Credential Manager, Secret Service). `settings` (`encryption.json` in
the state dir) only records `enabled` and `remember`. While locked, syncing stops with
"encryption is on but locked"; it never falls back to uploading unencrypted.

## Pro gate

Turning encryption on needs the Pro license (`license.Manager.Pro`). If Pro lapses later,
encrypted worlds keep syncing, so data is never stranded behind a license check.

## Not covered

- Changing the passphrase (re-encrypting everything) is not implemented.
- A provider can still delete data or roll a world back to an older manifest; encryption
  protects privacy and detects modification, not availability.
- Metadata listed under "Still visible" above.
