# Shellscaled development

Shellscaled runs Tailscale on rooted Android through a native TUN, without using Android VpnService. Support Magisk and KernelSU with the same offline module format.

## Source and branches

- `main` is the release branch; develop on `dev` in both repositories.
- `tailscale/` is the source dependency, tracked as a private submodule. Commit and push its source before publishing a parent commit that references it. Keep the submodule's tracked release branch as `main`.
- Read the module version from `tailscale/VERSION.txt`. Module revisions affect `versionCode` without inventing an unrelated Tailscale version.
- Preserve upstream BSD notices and Tailscale's license and patents notice in distributed packages.

## Build and storage

- Compile locally with `python3 scripts/build.py`. Reuse an installed Android NDK; do not automatically download an SDK or NDK.
- GitHub repositories must remain private. GitHub Actions are disabled. Do not enable or dispatch hosted builds without explicit owner authorization.
- Clone reference projects only into ignored `.reference/`. Never clone AOSP; inspect official documentation and necessary individual source files instead.
- Keep caches, emulator preparation, transient downloads, and package staging inside ignored `.work/`. Keep final packages in ignored `dist/`. Reuse these locations and remove obsolete task-owned products rather than accumulating backups.
- Inherit the user's exported proxy variables for network commands. Do not print credentials or add hard-coded proxy settings.

## Device safety and verification

- All runtime tests, installs, root changes, and networking experiments must target the `Medium_Phone` Android emulator. Use `scripts/emulator.py` to verify the AVD before issuing adb operations. Never choose an attached physical device implicitly.
- Keep SELinux enforcing. Diagnose and implement narrowly scoped policies instead of disabling enforcement.
- Do not overwrite shared SDK images or collect full device backups. Keep any required emulator boot overrides in `.work/`.
- Notify the owner when Tailscale account login becomes necessary. If the owner has not replied, pause the goal and wait; do not retrieve stored credentials or authenticate another way.
- Treat login URLs, state files, node keys, account data, and complete network inventories as private. Never commit them or include them in public diagnostics.

## Runtime design

- Installation is offline and validates its payload. Never download mutable `latest` binaries during installation.
- Use module-local boot scripts, without general `service.d` entries or mandatory system overlays/metamodules.
- Preserve persistent account state across updates. Use bounded logs and verified process ownership for service control.
- Default to `accept-dns=false` and `netfilter-mode=off`. Never silently replace the active Android VPN or take over DNS.
- Clean up only module-owned processes, interfaces, and policy rules. Avoid global firewall flushes and broad process-name matching.
- Document unsupported or unverified features explicitly. Do not claim VPN coexistence, end-to-end connectivity, or reboot persistence from a build-only check.
