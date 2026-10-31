# Taildrop

Shellscaled exposes a small receive workflow in the KernelSU WebUI. The WebUI
stores a receive directory under Android shared storage, defaults to
`/storage/emulated/0/Download/Tailscale`, creates that directory on first use,
and invokes the module's restricted `tailscale file get --conflict=rename`
command. The `/sdcard/Download/Tailscale` spelling is accepted for devices that
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

The receive button performs one bounded operation and reports the command result
in the WebUI. Files that `file get` already moved into staging remain there when
publishing fails, while files left in the inbox remain available for a later
retry.

Incoming files first land in the private module staging directory. Shellscaled
then copies each regular, non-symlink file into the selected directory with
exclusive creation, syncs and closes it, and removes the staged source only
after the copy succeeds. Name conflicts receive a numbered name. The shared
directory is created with mode `0775` and files with mode `0664`. Shellscaled
verifies the corresponding `/data/media/0` backing path, sets only the new
default directory and newly created files to `media_rw` (`1023:1023`), verifies
the resulting owner, and restores the fixed Android storage label. It never
recursively changes an existing user directory. A failed backing-path or label
check leaves the staged source in place and reports the permission failure.

## Sending from Android

The module WebUI only receives files. KernelSU's WebUI API provides no media
picker, so sending from Android is not supported.
