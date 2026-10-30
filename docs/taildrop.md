# Taildrop

Shellscaled exposes a small receive workflow in the KernelSU WebUI. The WebUI
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

The receive button performs one operation bounded to 25 seconds; the automatic
receiver allows one hour. Each waiting file listed by `GET /localapi/v0/files/`
is downloaded into a private partial directory, synced, and checked against the
size the daemon reports. Only a complete download is linked into the private
staging directory (never replacing an entry already there), and only then is it
deleted from the daemon with `DELETE /localapi/v0/files/<name>`. The delete is
not cut short by the receive deadline, and a failed delete is checked against
the file list: the staged link is removed only if the daemon still has the
file, so a file is never left in neither place. An
interrupted, timed-out or short download is discarded and stops the batch; the
file stays in the daemon for the next attempt and the result reports
`ok: false`, with `taildropReceived` true only if earlier files were published.
The partial directory is emptied before each receive. Files already in staging
are published before new ones are fetched; while publishing fails, new files
stay in the daemon.

Shellscaled then publishes staging: it copies each regular, non-symlink file into the selected directory with
exclusive creation, syncs and closes it, and removes the staged source only
after the copy succeeds. Name conflicts receive a numbered name. The shared
directory is created with mode `0775` and files with mode `0664`. Shellscaled
verifies the corresponding `/data/media/0` backing path, sets only the new
default directory and newly created files to `media_rw` (`1023:1023`), verifies
the resulting owner, and restores the fixed Android storage label. It never
recursively changes an existing user directory. A failed backing-path or label
check leaves the staged source in place and reports the permission failure.

## Daemon direct delivery

The default folder matches the Android tailscaled's own direct file root
(`setPlatformDefaultDirectFileRoot`). When the daemon can create it at startup,
it writes received files there itself and they never enter the waiting queue;
otherwise the receiver above handles the queue. Either way files land in the
same place unless another folder is chosen.

## Sending from Android

The module WebUI only receives files. KernelSU's WebUI API provides no media
picker, so sending from Android is not supported.
