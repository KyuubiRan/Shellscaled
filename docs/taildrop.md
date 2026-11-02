# Taildrop

Shellscaled exposes a receive workflow in the KernelSU WebUI's **Taildrop** tab. The WebUI
stores a receive directory under Android shared storage, defaults to
`/storage/emulated/0/Download/Taildrop`, creates that directory on first use,
and receives the daemon's waiting files through LocalAPI. The `/sdcard/Download/Taildrop` spelling is accepted for devices that
expose shared storage through that alias. Custom directories stay inside the
primary shared-storage tree, including `Download`, `Documents`, `Pictures`,
`Music`, and user-created folders. Android app-private subtrees such as
`Android/data`, `Android/obb`, and `Android/sandbox` are excluded.

The directory browser follows the interaction pattern used by
[Material Files](https://github.com/zhanghai/MaterialFiles): a visible current
path, an explicit parent action, and directory rows that open the next level.
Shellscaled only borrows this interaction model; it does not copy code or add
the file manager as a dependency. Directory enumeration stays in the root
helper and uses directory APIs with bounded results.
Choosing **Choose this directory** saves the directory immediately; there is no separate
save action. The path field displays the saved directory. Automatic status
refresh pauses in this tab, and unrelated updates retain directory rows and
scroll position. A failed save leaves the browser open so the choice can be retried.
Success feedback disappears after three seconds and clears immediately when
leaving the tab, including when a pending save finishes after navigation.

The service supervisor starts an automatic receiver which waits on the daemon's
`GET /localapi/v0/files/?waitsec=3600` endpoint, the same blocking mechanism used
by upstream `AwaitWaitingFiles` and `tailscale file get --wait`. File arrival
wakes the request immediately. An idle wait renews after one hour; connection
or publication failures retry after 30 seconds. The receiver works with the
WebUI closed and uses the saved directory and the same staging and ownership
workflow as manual receiving. Stopping the service cancels the wait.

The official Android app registers `androidFileOps` with the Taildrop extension
and streams received data through `ShareFileHelper` into Android SAF storage.
Its `Notifier` observes file state on the IPN bus. The separate root daemon in
Shellscaled uses the supported blocking LocalAPI instead. References inspected:
[`fileops.go`](https://github.com/tailscale/tailscale-android/blob/f1bb6045ae3756c4549c58c12ea09f4c0a86d8df/libtailscale/fileops.go),
[`backend.go`](https://github.com/tailscale/tailscale-android/blob/f1bb6045ae3756c4549c58c12ea09f4c0a86d8df/libtailscale/backend.go),
and [`Notifier.kt`](https://github.com/tailscale/tailscale-android/blob/f1bb6045ae3756c4549c58c12ea09f4c0a86d8df/android/src/main/java/com/tailscale/ipn/ui/notifier/Notifier.kt).

The receive button bounds downloading to 25 seconds; the automatic receiver
allows one hour. A lock serializes manual and automatic receives. Each waiting
file is downloaded into a private partial directory, synced, and checked against
the daemon's listed size and Content-Length. Only a complete download is linked
into private staging. Staging's directory is synced before requesting deletion
from the daemon. Interrupted or short transfers stay in the daemon; their
partial files are never published and are cleared before another batch.

A DELETE acknowledgement can be lost after the daemon has removed a file. Its
basename can immediately be reused by another upload, including one of the
same size. The receiver therefore never discards verified staged bytes merely
because a failed DELETE is followed by a same-name entry. An ambiguous delete
may produce a complete duplicate, and reports the operation's failure; retaining
both copies is preferable to losing the only original. Files already staged are
published before new ones are fetched. A destination failure keeps staging and
leaves new files in the daemon.

Publication uses an exclusively created hidden temporary file in the selected
shared directory. Copying, file sync, media_rw ownership (1023:1023), mode 0664,
and restorecon all complete before a final name appears. Linux renameat2 with
RENAME_NOREPLACE publishes atomically and tries numbered names on a conflict.
Unsupported atomic rename or a cross-filesystem error preserves staging; it
never falls back to an overwriting rename. The directory is synced before the
staged source is removed. A crash after publication but before source cleanup
can retain a complete duplicate, never an unfinished final file.

A private publication journal records one exact temporary name plus the parent
and file backing-device/inode/type identities. Recovery removes only that
recorded regular file after matching those identities; it never scans or deletes
public files by prefix. Copying starts only after the file identity is durable.
A crash in the tiny creation-before-identity window can leave an unknown empty
hidden entry: recovery leaves that entry untouched rather than guessing its
ownership. Directory or inode replacement likewise fails closed and preserves
staging. Existing user files and the contents of existing directories are never
recursively changed.

The receive destination is captured for each batch. Saving a new destination
updates later batches without restarting the daemon; an already active batch
finishes in its previous destination. Saved paths are written atomically.

## Daemon mode and binary-only updates

A module with this receiver starts its bundled daemon with the fixed boolean
`TS_ANDROID_TAILDROP_INBOX=1`. The matching CLI uses its existing profile-scoped
private inbox under `--statedir/files/`; completed uploads become available
through LocalAPI. Both default Download/Taildrop and custom destinations pass
through the same receiver and publication transaction. Android applications
supplying explicit SAF FileOps retain their direct-delivery behavior.

Without this opt-in the CLI retains the legacy fixed direct destination,
`/sdcard/Download/Taildrop`. Binary-only updates of older modules therefore keep
working even though those modules have no receiver. New modules verify the
mode of an existing module-owned daemon and restart it when needed; live-update
adoption must apply the same check. Existing files in the former direct folder
remain untouched. This module change requires the matching CLI opt-in support.

## Verification

The synthetic native suite covers interrupted downloads, cancellation,
lost DELETE acknowledgements with equal-size and different-size replacement
files, conflicting publications, owner/label failures, journal identity
replacement, and unsupported atomic rename. WebView fixtures cover directory
navigation, deferred device refresh, pending controls and draft preservation,
receive feedback, and stale page completions. None uses a real account.

`TestTaildropAndroidSharedStorage` is disabled by default. Run it only through
`scripts/emulator.py` on Medium_Phone with `SHELLSCALED_STORAGE_TEST=1`. It also
requires the emulator's qemu property and SELinux Enforcing. It creates only a
random task-owned directory inside the existing Download folder, verifies real
FUSE atomic publication, non-overwrite, backing ownership/label, and precise
journal recovery, then removes only that directory after checking its identity.
It never touches actual Taildrop files. These checks do not claim authenticated
cross-device delivery or compatibility with every storage implementation.

## Sending from Android

The module WebUI only receives files. KernelSU's WebUI API provides no media
picker, so sending from Android is not supported.
