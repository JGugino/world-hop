---
title: WorldHop Privacy Policy
---

# WorldHop Privacy Policy

**Effective date:** November 26, 2026
**Operator:** Gugino Software Solutions ("we", "us")
**Contact:** gugino@gugino.org

WorldHop is a desktop app that syncs your Minecraft worlds between your own computers using
your own cloud storage. This policy explains what the app does with data. The short version:
**we run no servers that receive your data, and we do not collect, see, sell or share your
worlds, files, account details or usage.**

## 1. What WorldHop does with your data

WorldHop runs entirely on your computer. It has no WorldHop account, no WorldHop server and no
analytics or telemetry. We never receive your data. Everything below happens between your computer
and the services you choose.

| Data | Where it goes | Why |
|---|---|---|
| Minecraft world files you choose to sync | Your computer, and **your own** storage: a hidden app-data folder in your Google Drive, or (Pro) your own Amazon S3 or S3-compatible bucket | To sync your worlds between your computers |
| Settings (which folders to sync, sync state, snapshots) | A `worldhop` folder in your operating system's user config directory, on your computer | To remember your setup and let you restore |
| Google sign-in tokens (access and refresh tokens) | Your operating system's keychain (Windows Credential Manager, macOS Keychain, or the Linux secret service) on your computer | To stay connected to your Drive without asking you to sign in each time |
| S3 access keys (Pro, if you use S3) | Your operating system's keychain | To reach your bucket |
| Encryption passphrase (Pro, optional) | Never stored. If you tick "remember", only a derived key is kept in your keychain | To encrypt before upload |
| Pro license key | On your computer, and sent to Gumroad to verify it (see section 3) | To unlock Pro |

## 2. Google user data

If you connect Google Drive, WorldHop asks for one permission only:

`https://www.googleapis.com/auth/drive.appdata`

This lets WorldHop read and write files in a **hidden application-data folder** that Google
creates for the app in your Drive. WorldHop **cannot see, list, read or change any of your other
Drive files.** WorldHop does not request your name, email address, contacts, calendar or any other
Google data.

- **What we access:** only the files WorldHop itself stored in that hidden folder (packed world
  data and small index files).
- **How it is used:** only to upload, download, list and delete those files so your worlds sync
  between your computers. It is not used for advertising, profiling or any other purpose.
- **Where it goes:** the data moves directly between your computer and Google. It does not pass
  through, and is not stored on, any server operated by us.
- **Retention:** the data stays in your Drive until you delete it (section 5).

### Google API Services User Data Policy

WorldHop's use and transfer to any other app of information received from Google APIs will adhere
to the [Google API Services User Data Policy](https://developers.google.com/terms/api-services-user-data-policy),
including the Limited Use requirements. In particular:

- We use Google user data only to provide and improve the user-facing sync feature you asked for.
- We do not transfer Google user data to anyone, except as needed to provide that feature, to
  comply with law, or as part of a merger or sale with notice to you. In practice we receive none,
  since it never leaves your computer and Google.
- We do not use Google user data for serving advertisements.
- We do not allow humans to read Google user data. We have no access to it.

## 3. Connections the app makes

WorldHop contacts only these services, and only for the purposes shown:

- **Google** (`accounts.google.com`, `oauth2.googleapis.com`, `www.googleapis.com`): sign-in,
  token refresh, and the Drive app-data folder. Used only if you choose Google Drive.
- **Your S3 provider** (the endpoint you enter): to store your worlds. Used only if you choose S3.
- **Gumroad** (`api.gumroad.com`): if you enter a Pro license key, WorldHop sends the key and the
  WorldHop product ID to Gumroad to verify it, on activation and about once a week afterwards.
  Gumroad, like any web service, also sees your IP address. Gumroad's own
  [privacy policy](https://gumroad.com/privacy) applies to that and to your purchase.
- **GitHub** (`api.github.com`): about once a day WorldHop
  asks GitHub for the latest WorldHop release to tell you if an update exists. No personal data is
  sent beyond what any web request includes, such as your IP address. Nothing is downloaded or
  installed automatically.

The WorldHop window is a local web page served only on your own computer (`127.0.0.1`) while it is
open. It is not reachable from other devices.

We do not use cookies, advertising IDs, analytics, crash reporting or tracking of any kind.

## 4. Encryption (Pro)

Pro can encrypt worlds on your computer before upload, so your storage provider only holds
ciphertext. We cannot recover your passphrase. **If you lose it, encrypted data cannot be
recovered.** See [encryption.md](encryption.md) for details.

## 5. Your choices: removing access and deleting data

- **Disconnect in WorldHop:** use the unlink option for your cloud provider. This asks Google to
  revoke WorldHop's access (when it can reach Google) and deletes the stored tokens or keys from
  your keychain. It does not delete data already in the cloud.
- **Revoke Google access:** open your [Google Account permissions](https://myaccount.google.com/permissions),
  choose WorldHop, and select **Delete all connections** (or **Remove access**).
- **Delete the synced data from Google Drive:** in
  [Google Drive settings](https://drive.google.com/drive/settings) go to **Manage apps**, find
  WorldHop and choose **Options > Delete hidden app data**.
- **Delete data in S3:** delete the objects, or the bucket, using your provider's tools.
- **Delete local data:** uninstall WorldHop and delete its `worldhop` folder in your config
  directory (`%AppData%\worldhop` on Windows, `~/Library/Application Support/worldhop` on macOS,
  `~/.config/worldhop` on Linux). Remove the "worldhop" entries from your keychain.
- **Your Minecraft worlds** are never deleted by uninstalling WorldHop.

## 6. Children

WorldHop is not directed at children under 13 and we do not knowingly collect personal information
from anyone. Because we receive no data, there is nothing to collect from a child.

## 7. Security

Credentials are kept in your operating system's keychain, and the local web page accepts only local
connections. No system is perfectly secure, and you are responsible for the security of your own
computer and cloud accounts.

## 8. Changes

If we change this policy we will update the date above and publish the new version in the project
repository. The history is public in the repository.

## 9. Contact

Questions or requests: gugino@gugino.org.

*This document is not legal advice.*
