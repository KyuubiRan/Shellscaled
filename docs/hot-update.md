# Live module update

## Overview

Installing a module zip from the manager makes the new module live within seconds, without a reboot. `customize.sh` starts a worker that swaps the staged and live module trees and replaces the supervisor. The running `tailscaled` and MagicDNS proxy are adopted when their binaries are unchanged, so in that case the tailnet, and any session running over it, is not interrupted.

If any precondition fails before the swap, nothing is changed and the manager activates the update at the next reboot, as for any other module.

KernelSU's native hot install is not requested (`MODULE_HOT_INSTALL_REQUEST=false`). When ksud renames the live tree, the old supervisor's check of `/proc/PID/exe` against `$SC_MODDIR/bin/tailscaled` fails, and on its next pass it would start a second `tailscaled`. Only the module's own code can retire the old supervisor before the rename, so the module performs the promotion itself on every manager.

## Sequence

### `customize.sh`

It runs in the installer's mount namespace, where `/system` may be unavailable, so it uses only shell builtins and the installer's busybox, and takes no locks.

1. Verify the payload, architecture and permissions.
2. If the live tree has a `disable` or `remove` flag, print that the update activates at reboot and stop. The flags are read here because the installer clears them after the script returns.
3. Otherwise write a token (the installer shell's pid and start time) to `$MODPATH/.sc-upgrade` and start `ctl.sh upgrade <token> <ppid>` from the staged tree through `busybox setsid busybox nsenter -t 1 -m`, so the worker runs detached, in its own session and in init's mount namespace.
4. The installer continues: it writes `update` and a copy of `module.prop` into the old live tree (which the worker later moves to trash), deletes the staged `customize.sh` and exits.

A fresh install uses the same worker; there is then no live tree to move and no supervisor to retire.

### Worker: `ctl.sh upgrade`

1. **Paths.** The worker runs only from `/data/adb/modules_update/shellscaled`. It sets `SC_STAGED` to that directory and `SC_MODDIR` to `/data/adb/modules/shellscaled`, so the functions sourced from the staged `common.sh` act on the live path. Output goes to `service.log`. The worker moves itself into the root cgroup (best effort), so it does not die with the manager app's cgroup.
2. **Wait.** Poll every 0.2 s, for at most 120 s, until the installer token's process has exited, and also the parent process if its executable is `ksud`, `apd` or `magisk*`.
3. **Lock.** Take `control.lock`, the lock used by the lifecycle commands and the binary updater. If it is busy, exit.
4. **Checks.** Exit unless all of these hold:
   - the staged `.sc-upgrade` equals the token, so the staged tree belongs to this install and not to a newer one that is still extracting;
   - the staged `customize.sh` is gone, so the installer finished;
   - `/data/adb/modules`, `/data/adb/modules_update` and `/data/adb/shellscaled` are on one filesystem, so `mv` is a rename and never a copy.
5. **Freeze.** Clear `/data/adb/shellscaled/trash`. If the supervisor is running, wait for it to reach a safe point and stop it with SIGSTOP (see "Supervisor handover").
6. **Swap.** `mv` the live tree to `trash/<timestamp>`, then `mv` the staged tree to the live path. If the second rename fails, the first is undone. On any rename failure the supervisor is resumed with SIGCONT and the worker exits.
7. **Retire.** SIGKILL the old supervisor. SIGKILL runs no EXIT trap, so its cleanup does not run and the daemon, the proxy and the DNS jumps stay in place. Remove `supervisor.pid`, send SIGTERM to the old automatic Taildrop receiver, and remove `.sc-upgrade`.
8. **Flags.** A `disable` or `remove` flag found in the old tree was set after the installer finished, so it is copied into the new tree.
9. **Start.** Run `sc_cli_install` (idempotent). If a supervisor was running, or this is a fresh install, run `sc_start`. If `sc_start` refuses because of a flag, stop the proxy and the daemon. Refresh the module description, log `Live update to <version> complete.` and release the lock.

### Adoption in `sc_start`

- **Daemon.** It is kept only when it is verified running, its socket and `defaults-initialized` exist, `cmp -s /proc/PID/exe` matches the new `bin/tailscaled`, and its command line equals the new binary path followed by `SC_DAEMON_ARGS`. Otherwise it gets SIGTERM (polled every 0.1 s, SIGKILL after 15 s) and the new supervisor starts the new binary. A daemon whose initialization was cut short is therefore never adopted.
- **Proxy.** It is kept only when it is verified running, `cmp -s` matches the new `bin/shellscaled-web`, and both OUTPUT jumps exist (on kernels without IPv6 NAT, the IPv6 jump is the `filter` table jump to `shellscaled_dns6_fallback`). The jump check covers a supervisor frozen inside `sc_magicdns_stop` after the jumps were removed. Otherwise it is terminated: it flushes its chain, the jumps stay, and the supervisor starts the new proxy. While the chain is empty, DNS uses the system path, so it is never blackholed.
- **Supervisor.** When the daemon and proxy are both running at startup, the new supervisor records the daemon's identity, so it does not restart an adopted proxy. When the daemon identity changes, the supervisor uses `sc_magicdns_term`, not `sc_magicdns_stop`, so the OUTPUT jumps are never removed and re-added. The automatic Taildrop receiver is always restarted.

Binaries are compared by content, not by inode: every extraction creates new inodes, and `/proc/PID/exe` still opens the running image after a rename or unlink.

After a successful daemon start the supervisor skips its 3 s loop sleep, so the proxy follows on the next pass. Supervisor registration is polled every 0.1 s and daemon readiness every 0.2 s. toybox and busybox `sleep` both accept fractions; supervisors always run in init's mount namespace, where `/system/bin/sleep` is toybox.

## Supervisor handover

**Safe point.** The supervisor is identified by pid, start time and command line (`/system/bin/sh /data/adb/modules/shellscaled/ctl.sh supervise`); the command line is argv text, so a rename does not change it. Android's `/system/bin/sh` is mksh, which has no builtin `sleep`, so a `sleep` child means the supervisor is in one of its waits: registration, the readiness loop in `sc_start_daemon`, the 5 s back-off, the wait in `sc_magicdns_term`, or the main-loop sleep. No `sleep` runs between spawning a process and `sc_record_pid`; comments next to `sc_start_daemon` and `sc_magicdns_start` state this invariant. The worker polls `pgrep -P <supervisor> -x sleep` every 0.2 s for at most 10 s, then sends SIGSTOP. If the poll times out, it exits before anything has changed. While the supervisor is frozen, the worker records its automatic Taildrop receiver child, because that link is lost once the supervisor dies.

**Identity across renames.** The pid files keep their format (pid and start time), so a supervisor from upstream v1.104.4-r3 or earlier still reads them until it is retired; no cooperation from the old release is needed. `sc_daemon_running` and `sc_magicdns_running` strip a trailing ` (deleted)` from `/proc/PID/exe` and accept both the live path and `/data/adb/shellscaled/trash/*/bin/<binary>`. The pattern also holds after the trash is deleted and after the binary updater renames over `tailscaled`. The helper applies the same rule before it signals the proxy with SIGUSR1, so an adopted proxy keeps receiving refresh requests.

## Failure behaviour

| Failure | State | Result |
|---|---|---|
| Wait, lock, check or safe-point poll fails | nothing changed | The manager activates the update at reboot. |
| A rename fails | old tree restored, supervisor resumed | No downtime; the update activates at reboot. |
| Crash between the two renames | no live tree, staged tree intact | The manager installs the staged tree at boot. |
| Worker killed while the supervisor is frozen | daemon still serving, supervisor stopped | Supervision resumes at reboot or with `kill -CONT`. |
| The new `sc_start` fails after the swap | new tree live | The supervisor retries 5 times. There is no automatic rollback: if the new release cannot start, the tailnet stays down until local recovery. |

The old tree stays in `/data/adb/shellscaled/trash/<timestamp>` until the next boot (`service.sh` removes the trash) or the next live update. To recover manually, move the trees back and run `ctl.sh start`.

## Reboot, disable and uninstall

- **Reboot after a live update.** `modules_update/shellscaled` no longer exists, so the manager has nothing to promote; the `update` flag and the copied `module.prop` went to trash with the old tree. The manager may show "reboot required" until it refreshes. `service.sh` starts normally.
- **Reboot after the worker gave up.** The manager promotes the staged tree in post-fs-data, before `service.sh` runs.
- **Flag set before the install.** The worker is not started and the update activates at reboot. The installer's usual "an update re-enables the module" behaviour is kept.
- **Flag set during the update.** The flag is carried into the new tree, `sc_start` refuses, and the worker stops the daemon and proxy: the same end state the supervisor reaches when the module is disabled.
- **Uninstall.** `uninstall.sh` runs `ctl.sh stop`, which uses the same identity checks, and removing `/data/adb/shellscaled` also removes the trash.
- **Binary updater.** It holds `control.lock` across stop, replace and start, so it never overlaps a live update.
- **WebUI.** The bridge binary has a fixed path; it is missing only between the two renames, and an RPC that is already running keeps its own inode. Reopen the WebUI after an update.
- **SELinux.** No new policy. `nsenter` changes only the mount namespace, and the staged tree is renamed, never copied, so the installer's labels and permissions are kept.

## Lifecycle commands

`ctl.sh start|stop|restart` re-executes itself in init's mount namespace when `[ /proc/$$/ns/mnt -ef /proc/1/ns/mnt ]` fails (`-ef` is a mksh builtin, so it works where `/system/bin` is unavailable), using the first available `/data/adb/{ksu/bin,magisk,ap/bin}/busybox nsenter -t 1 -m`. `sc_start` also moves itself into the root cgroup before starting the supervisor. Supervisors started from the WebUI or the module action button therefore always run in init's mount namespace and cgroup, where `/system/bin` is available to their cleanup. busybox `nsenter` without `-p` does not fork, so the PID is preserved and the binary updater's parent check still identifies the helper.

## Verifying

Run this read-only check before an update, right after it, and 30 s later. It prints no addresses or account names.

```sh
su -c 'R=/data/adb/shellscaled/run; M=/data/adb/modules/shellscaled; grep -E "^version(Code)?=" $M/module.prop; ls -d /data/adb/modules_update/shellscaled $M/update $M/disable /data/adb/shellscaled/trash/* $R/control.lock 2>&1; echo "init mnt=$(readlink /proc/1/ns/mnt) cg=$(head -1 /proc/1/cgroup)"; for f in supervisor daemon magicdns; do set -- $(cat $R/$f.pid 2>/dev/null); [ -n "$1" ] || { echo "$f: none"; continue; }; echo "$f pid=$1 rec=$2 now=$(sed "s/.*) //" /proc/$1/stat 2>/dev/null | cut -d" " -f20) st=$(sed "s/.*) //" /proc/$1/stat 2>/dev/null | cut -d" " -f1) exe=$(readlink /proc/$1/exe) mnt=$(readlink /proc/$1/ns/mnt) cg=$(head -1 /proc/$1/cgroup)"; done; set -- $(cat $R/daemon.pid 2>/dev/null); cmp -s /proc/$1/exe $M/bin/tailscaled && echo "cmp: same" || echo "cmp: differs"; echo "jumps v4=$(iptables -w -t nat -S OUTPUT | grep -c shellscaled_dns) v6=$(ip6tables -w -t nat -S OUTPUT | grep -c shellscaled_dns)"; echo "taildrop-auto=$(ps -A -o ARGS | grep -c "[t]aildrop-auto")"; ls -Z $M/webroot/index.html $M/bin/tailscaled; $M/bin/tailscale status --json 2>/dev/null | grep -m1 BackendState; tail -n 15 /data/adb/shellscaled/logs/service.log'
```

Expected:

- **Binary unchanged:** the daemon's pid and start time are identical and the check prints `cmp: same`.
- **Binary changed:** only the daemon line changes.
- **Always:** `modules_update/shellscaled` and the `update` flag are absent; there is exactly one supervisor, one proxy and one automatic Taildrop receiver; their mount namespace and cgroup equal init's; the jump count is 1 for each family; and the SELinux labels are unchanged.

## Device results

On a Pixel running KernelSU Next:

- With an unchanged `tailscaled`, the daemon keeps its pid and the tailnet is not interrupted, including when the old tree was already removed from the trash.
- With a changed `tailscaled`, the tailnet is lost for about 8 s, mostly control and DERP reconnection.
- When the takeover fails, the old daemon keeps serving.

Not yet tested: Magisk, APatch, and a reboot after a live update. The upstream emulator test suite has not been run against this feature.
