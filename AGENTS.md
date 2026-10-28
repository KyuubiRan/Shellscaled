# Shellscaled development

Shellscaled runs Tailscale on rooted Android through a native TUN, without using Android VpnService. Support Magisk and KernelSU with the same offline module format.

## Source and branches

- `main` is the release branch; develop on `dev` in both repositories.
- `tailscale/` is the source dependency, tracked as a submodule of the official Tailscale fork. Commit and push its source before publishing a parent commit that references it. Keep the submodule's tracked release branch as `main` and retain upstream ancestry with separate Android patches.
- Read the module version from `tailscale/VERSION.txt`. Module revisions affect `versionCode` without inventing an unrelated Tailscale version.
- Preserve upstream BSD notices and Tailscale's license and patents notice in distributed packages.
- Use `KyuubiRan` for the module author. Keep Git author, committer, and signing identity inherited from the owner's local Git configuration; do not anonymize or replace them with the module label.

## Build and storage

- Compile locally with `python3 scripts/build.py`. Reuse an installed Android NDK; do not automatically download an SDK or NDK.
- Both repositories are open source. The owner authorized their dedicated CI pipelines: `dev` publishes Pre-releases and `main` publishes Releases. CLI releases synchronize the matching module branch, source reference and tracked module version metadata; both pipelines also support manual runs. Disable unrelated inherited workflows. CI builds and static checks are allowed; all runtime tests remain emulator-only.
- Test and RC releases must be GitHub Pre-releases. The owner authorized automatic stable publication from `main`; do not promote development work to `main` merely to trigger a stable release. Default local builds to the prerelease update channel and verify download links and badges include prereleases.
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
- Keep the WebUI command panel limited to the native helper's Tailscale program, subcommand, and flag allowlists. Never pass user input to a shell, expose an unrestricted executor, or load remote scripts in the root WebView.
- Fetch device details only for the device page, project the selected display fields, and keep device data out of storage, logs and published screenshots. Documentation screenshots must use synthetic devices.
- Open the console and project homepage only through the helper's fixed external-browser actions and their fixed HTTPS destinations. Never accept arbitrary URLs or add browser launch commands to the command panel.
- Binary updates require an explicit user action, a compatible Android payload from the CLI repository's public release, and verified GitHub asset, manifest and binary hashes. Validate before stopping the service, replace atomically, restore the previous binary on failure, preserve account/preferences and remove transaction files. Never substitute upstream Linux binaries or enable Tailscale's unrestricted self-updater.
- Preserve the user's stable/preview update-channel preference across module updates. Preview includes newer stable releases. Compare versions before publication dates and never let an older preview override a newer stable version.
- Update only edited settings through masked LocalAPI preferences. Preserve other settings and unsaved edits across status refreshes.
- Keep product copy focused on user actions and status. Put implementation and validation details in development documentation, not the WebUI.
- Localize the interface and native application messages in Simplified Chinese, Traditional Chinese, and English. Follow system language by default, allow a language choice, and keep connection control as one state-dependent button.
- Refresh the module's own KernelSU description override and module.prop fallback. Do not infer the active manager from hidden kernel sysfs entries or optional environment variables.
- Document unsupported or unverified features explicitly. Do not claim VPN coexistence, end-to-end connectivity, or reboot persistence from a build-only check.
