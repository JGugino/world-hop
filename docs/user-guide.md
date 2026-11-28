# WorldHop User Guide

WorldHop keeps your Minecraft worlds in sync between your computers. Close the game on one PC, open it on another, and your world is already there. Your worlds are stored in your own cloud account. WorldHop never hosts or sees them.

## 1. Before you begin

You will need:

- A computer running Windows, macOS or Linux, with Minecraft: Java Edition.
- A Google account (for Google Drive storage), or an S3-compatible bucket with WorldHop Pro.
- An internet connection when syncing.

The free version syncs one world. WorldHop Pro syncs unlimited worlds and adds S3 storage and encryption (see section 9).

## 2. Install WorldHop

Install WorldHop on every computer you want to keep in sync.

| System | Steps |
|---|---|
| Windows | Unzip `WorldHop-<version>-windows-amd64.zip` and run `worldhop.exe`. The app is not code-signed yet, so Windows may show a SmartScreen warning. Choose **More info**, then **Run anyway**. |
| macOS | Open `WorldHop-<version>.dmg` and drag **WorldHop** to Applications. The app is not notarized yet, so the first time, right-click it and choose **Open**, then confirm. |
| Linux | Install the `.deb` with `sudo apt install ./worldhop_<version>_amd64.deb`, or unpack the `.tar.gz` and run `worldhop`. The tray icon needs a desktop with AppIndicator support (on GNOME, install the AppIndicator extension). |

## 3. Meet the tray menu

WorldHop has no desktop window. It lives in your system tray (the menu bar on macOS). Click its icon to open the menu:

| Menu item | What it does |
|---|---|
| **Open Web View** | Opens the WorldHop window in your browser. |
| **Force Sync** | Syncs all your worlds right now. |
| **Update available** | Appears only when a newer version exists. Opens the download page. |
| **Exit** | Quits WorldHop. |

Hover over the icon to see the current sync status. The WorldHop window is served only on your own computer and only while it is open.

## 4. Tell WorldHop where your worlds are

1. Open the **Saves** tab.
2. Choose the type: **Saves folder** (every world inside it, including worlds you create later) or **Single world** (just that one folder).
3. Click **Browse...**, pick the folder, then click **Add**.

You can add as many as you like, in any mix. On first run WorldHop adds the standard Minecraft and launcher folders it finds on your computer.

| System | Default Minecraft saves folder |
|---|---|
| Windows | `%APPDATA%\.minecraft\saves` |
| macOS | `~/Library/Application Support/minecraft/saves` |
| Linux | `~/.minecraft/saves` |

Prism, MultiMC, CurseForge and Modrinth keep saves inside their own instance folders. Add those the same way.

**Important:** computers match worlds by folder name. A world called `Survival Island` on one computer must have the same folder name on the other.

To stop syncing a save, click **Remove** on the **Saves** tab, then **Stop syncing**. Your files and the newest cloud copy are kept. (WorldHop does remove old versions of worlds from your cloud storage over time, see section 8.)

## 5. Connect your cloud storage

1. Open the **Cloud account** tab.
2. Under **Google Drive**, click **Connect**.
3. Sign in and approve access in the browser page that opens.

That is all. WorldHop asks only for access to its own private app folder in your Drive, so it cannot see your other files. Your sign-in details are kept in your system keychain.

![Cloud account tab](c-cloud.png)

To disconnect, click **Disconnect** on the same tab. WorldHop also asks Google to end its access, so it disappears from your Google account's permissions. If you are offline at that moment, WorldHop still disconnects on this computer and tells you to remove it at myaccount.google.com/permissions. If Google access is ever revoked, WorldHop asks you to reconnect.

## 6. How syncing works

Once a cloud account is connected, syncing is automatic:

- when WorldHop starts,
- shortly after you close Minecraft (the world is uploaded),
- every 10 minutes (to pick up changes from your other computers).

You can also sync on demand with **Force sync** at the top of the window (or **Force Sync** in the tray menu), or **Sync this world** on any world.

WorldHop never syncs a world while Minecraft has it open. It waits, and syncs the moment you close the game.

The **Worlds** tab shows each world and its state:

| Status | Meaning |
|---|---|
| Up to date | This computer and the cloud match. |
| Changes to upload | This computer has newer changes. |
| Newer version in the cloud | Another computer uploaded newer changes. |
| Conflict | Two computers changed the world separately. |
| Cloud data missing | Your cloud no longer holds something this computer had seen (deleted or rolled back). Nothing is synced or changed until you choose. See section 8. |

![Worlds tab](c-worlds.png)

When something needs your attention, a message at the top explains what happened in plain words and, where possible, links to the fix.

![Waiting for Minecraft to close](c-wait.png)

## 7. Using a second computer

1. Install WorldHop on the second computer.
2. Add your saves folder (section 4).
3. Connect the **same Google account** (section 5).
4. Worlds that are in the cloud but not on this computer appear under **Cloud worlds**. Click **Download**.

WorldHop creates a new folder for the world and never overwrites an existing one. When there is exactly one saves folder, new cloud worlds can also be fetched automatically.

From then on, the everyday routine is simple: play, close Minecraft, move to your other computer, open Minecraft.

## 8. Conflicts and snapshots

### If two computers changed the same world

This happens when you play on two computers without syncing in between. WorldHop never overwrites silently. The world shows **Conflict**; choose what to keep:

| Choice | Result |
|---|---|
| **Keep local** | Use this computer's version and upload it. |
| **Keep remote** | Use the cloud version. A snapshot of yours is saved first. |
| **Keep newest** | Use whichever was changed most recently. |
| **Keep both** | Copy this version to a new folder named `<world> (conflict copy <date>)`, then use the cloud version. |

### If your cloud lost data

If something in your cloud was deleted, or your cloud was rolled back to an older state, WorldHop notices (on any computer that had already seen what is missing). That world shows **Cloud data missing** and is left alone: WorldHop changes nothing, here or in the cloud, until you choose.

| Choice | Result |
|---|---|
| **Use the cloud as it is now** | Forget the missing history and go by what the cloud holds. If this computer's version differs from what is left, you get the usual conflict choices, and nothing is replaced until you pick. If the world is gone from the cloud entirely, the next sync uploads this computer's copy. |
| **Upload this computer's copy** | Upload this computer's world as a new version, rebuilding what the cloud lost. This is the only way to repair missing data, and needs a computer that has the world. |

This warning also appears if you connected a different cloud account, or cleared WorldHop's data in your cloud yourself. Choosing **Use the cloud as it is now** settles it. A brand-new computer can't tell, because it has never seen the old versions.

### Snapshots

Before a sync replaces a world on your computer, WorldHop saves the old version as a snapshot. The newest five per world are kept on this computer.

1. Close Minecraft.
2. On the **Worlds** tab, click **Snapshots** on the world.
3. Click **Restore** next to the version you want, then confirm.

Restoring saves your current version as a snapshot first, so a restore can be undone. The restored world is uploaded on the next sync.

### Snapshots in the cloud

Every upload is also kept in your cloud storage as a version. Under **Snapshots** on a world, the **In the cloud** list shows them; **Restore** puts one on this computer (your current world is saved as a snapshot first, and the next sync uploads the restored copy as a new version). On the **Settings** tab you choose how many of the newest versions of each world the cloud keeps: 5 by default, at least 2. Older ones are removed to free space (at most once a day), but anything uploaded in the last 14 days is always kept.

## 9. WorldHop Pro

| | Free | Pro |
|---|---|---|
| Worlds synced | 1 | Unlimited |
| Google Drive | Yes | Yes |
| Your own S3-compatible storage | No | Yes |
| Encryption | No | Yes |
| Snapshots, restore, conflict tools | Yes | Yes |

Pro is a one-time purchase from store.gugino.org.

### Activate Pro

1. After purchase, find your license key in the receipt email.
2. Open the **License** tab, paste the key, and click **Activate**.

Activation needs an internet connection. WorldHop re-checks the key about once a week; if you are offline, Pro keeps working for up to 30 days. A refunded purchase returns WorldHop to the free version.

On the free version, the first world to sync is the free world. You can change it on the **License** tab.

### Your own storage (S3)

1. Open **Cloud account** and click **Set up** under S3-compatible storage.
2. Enter the bucket details and your access key. Leave **Endpoint** empty for AWS S3. For other services, enter their endpoint (for example `https://<account>.r2.cloudflarestorage.com`).
3. Click **Test connection**, then **Save**.

Works with AWS S3, Cloudflare R2, Backblaze B2, Wasabi, MinIO and other S3-compatible services. The key needs list, read, write and delete permission on the bucket (delete is how WorldHop removes old versions; without it your storage still syncs, but old versions are not cleaned up). Your secret key is stored in the system keychain, not in a file.

When you disconnect S3, WorldHop forgets the key on this computer, but it cannot cancel the key with your storage service: it stays valid until you delete or rotate it in the service's console. WorldHop shows the access key's id so you can find it.

### Encryption

Encryption scrambles your worlds on your computer before they are uploaded, so your cloud provider only stores unreadable data.

1. Open the **Encryption** tab.
2. Choose a passphrase and type it twice.
3. Tick the box confirming you understand the passphrase cannot be recovered, then turn encryption on.
4. On every other computer, turn encryption on and enter the **same passphrase**.

Optionally tick **Remember on this computer** to store the key in your system keychain so you are not asked again. Otherwise WorldHop asks for the passphrase after each restart, and syncing pauses until you enter it. Use **Lock now** to lock it again.

**If you lose your passphrase, your encrypted worlds cannot be recovered. Nobody, including us, can reset it.**

To change it, open the **Encryption** tab and choose **Change passphrase**. Enter the current passphrase and the new one twice. WorldHop then re-encrypts all your worlds with the new passphrase and shows its progress; syncing on this computer pauses until it finishes, and stopping it midway changes nothing. Try not to sync from your other computers meanwhile. Afterwards, each other computer asks for the **new** passphrase. The previous encrypted copy stays in your cloud account and can still be opened with the old passphrase; WorldHop cannot delete it, so remove it with your cloud provider if the old passphrase was exposed.

Turning encryption on uploads encrypted copies beside your existing ones. Once a world's encrypted copy is in place, WorldHop deletes that world's old unencrypted copy from your cloud storage by itself (after a sync, within the hour). A world that has no encrypted copy yet keeps its unencrypted copy until a computer that has it syncs. To remove everything at once, delete the hidden app data in your cloud account (Google Drive: Settings, Manage apps, WorldHop, Delete hidden app data) and sync again.

## 10. Updates

When a newer version is available, WorldHop shows a notice with a link to the download page and adds an **Update available** item to the tray menu. It checks about once a day. Nothing is downloaded or installed without you.

## 11. Troubleshooting

| Problem | What to do |
|---|---|
| No tray icon on Linux | Install AppIndicator support for your desktop. |
| A world is not listed | Check that its folder, or its parent saves folder, is added on the **Saves** tab. It must contain a `level.dat` file. |
| "Waiting for Minecraft to close" | Close the world in Minecraft. WorldHop syncs it automatically. |
| Google sign-in did nothing | Make sure your browser did not block the page, then click **Connect** again. |
| The browser page after sign-in says something went wrong | It names the problem and what to do (declined, a permission left unticked, no internet, keychain locked, and so on). Fix that, go back to WorldHop and click **Connect** again. Nothing is connected until the page says so. |
| "No cloud account" message | Connect Google Drive or S3 on the **Cloud account** tab. |
| Only one world syncs | You are on the free version. Pick the free world, or upgrade, on the **License** tab. |
| Pro stopped working | Open **License**. If you were offline for a long time, connect to the internet so WorldHop can re-check. |
| "Encryption is on but locked" | Enter your passphrase on the **Encryption** tab. If it says the passphrase was changed on another computer, enter the new one. |
| "Your encryption passphrase is being changed" | A passphrase change is running on this computer. Syncing resumes when it finishes; watch the **Encryption** tab. |
| "Cloud data missing" on a world | Something your cloud held is gone or rolled back. Read the explanation under the world and choose (section 8). Nothing changes until you do. |
| "Encryption key looks rolled back" | The key in your cloud is older than the one this computer used. On the **Encryption** tab, put the key back from this computer, or go by what the cloud shows. |
| Two computers show a conflict | Choose a resolution (section 8). Nothing is overwritten until you do. |
| A world looks wrong after a sync | Close Minecraft and restore an earlier snapshot (section 8). |

## 12. Privacy and safety

- Your worlds go only to your own cloud account. WorldHop has no servers that store them.
- On Google Drive, WorldHop can access only its own hidden app folder.
- WorldHop's web page runs only on your own computer, and only while it is open.
- Every download is verified before it replaces anything, and replaced worlds are kept as snapshots.

WorldHop is not an official Minecraft product and is not affiliated with Mojang or Microsoft.
