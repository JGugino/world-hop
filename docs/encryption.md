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
  `_worldhop-key` area. A passphrase change adds the next generation; the newest is the key in force. It holds no secret. The key check means a wrong passphrase is
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

## Changing the passphrase

Encryption tab, **Change passphrase**. Enter the current passphrase and the new one twice, and
acknowledge that a lost passphrase cannot be recovered. A new passphrase means a new key, so
WorldHop re-encrypts every world with it in the background and shows progress. Nothing becomes
visible to your other computers until it finishes, so stopping it, a failure or a crash changes
nothing; starting again with the same new passphrase picks up where it left off.

- Syncing is paused on this computer while it runs. Avoid syncing from your other computers at
  the same time: a version pushed after the last catch-up pass stays in the old copy.
- When it finishes, other computers ask for the **new** passphrase (the old one stops working
  for new data) and carry on without conflicts.
- It re-uploads your worlds, so it takes as long and uses as much upload as your worlds are big.
  Only each world's newest versions are carried over.
- The previous encrypted copy stays in your cloud account, readable with the **old** passphrase,
  because WorldHop cannot delete from your storage. If you changed the passphrase because the old
  one was exposed, delete the old copy with your provider.
- It is not a Pro feature and works while locked, so a lapsed license never strands your data.

## If the cloud rolls back or deletes things

WorldHop never repairs by itself; it warns, stops that world (or encryption) and asks.

- **Worlds.** If your cloud no longer holds versions of a world this computer had seen (deleted
  or rolled back), the world shows "Cloud data missing" and is not synced. Choose **Use the cloud
  as it is now** (forget the missing history; any difference from this computer's copy then
  appears as a normal conflict you settle) or **Upload this computer's copy** (rebuild what the
  cloud lost). Data a stored version needs that has gone missing can only be rebuilt from a
  computer that has the world. This also appears if you connected a different cloud account.
- **The key.** If the encryption key in your cloud is older than the one this computer has used,
  or gone, syncing stops. **Put the key back** writes this computer's saved copy back (it holds
  no secret). **Go by what the cloud shows** forgets the newer key here and asks for a
  passphrase again; data written under the newer key stays in the cloud but can't be read here.
- A computer can only notice what it has seen: a brand-new computer detects nothing, and data is
  checked when a world is uploaded or downloaded.

## Remembering the passphrase

Off by default. If the user opts in, the derived key (not the passphrase) is put in the OS
keychain (Keychain, Credential Manager, Secret Service). `settings` (`encryption.json` in
the state dir) only records `enabled` and `remember`. While locked, syncing stops with
"encryption is on but locked"; it never falls back to uploading unencrypted.

## Pro gate

Turning encryption on needs the Pro license (`license.Manager.Pro`). If Pro lapses later,
encrypted worlds keep syncing, so data is never stranded behind a license check.

## Not covered

- Removing the old copy after a passphrase change: the store has no delete, so that is done with
  the provider.
- A provider can still delete data or roll a world back; encryption protects privacy and
  detects modification, not availability. WorldHop notices missing or rolled-back data on any
  computer that had seen it, and stops until you choose (see below), but it cannot prevent or
  undo it.
- Metadata listed under "Still visible" above.
