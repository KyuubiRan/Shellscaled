# Android WebView regressions

This standalone fixture packages the current `module/webroot` files unchanged and
loads them in a real Android WebView on the expected KernelSU asset origin. Its
fake `ksu.exec` decodes the fixed RPC envelope and changes only synthetic state
inside the test APK. It never runs a shell, needs no root or network permission,
and cannot read the module socket, account, filesystem, or device inventory.
All other URLs and navigations are blocked. Only language is stored in the
fixture's separate WebView storage. Logcat contains test names and counts only.

Build with the installed SDK platform 36 / build-tools 36.0.0; no Gradle, npm,
downloads, or host JavaScript runtime is used:

```sh
python3 scripts/build-webui-fixture.py
```

Run only on the guarded `Medium_Phone` emulator:

```sh
python3 scripts/emulator.py install -r .work/webui-fixture/webui-fixture.apk
python3 scripts/emulator.py shell am start -S -n dev.shellscaled.webuitest/.MainActivity
python3 scripts/emulator.py logcat -d -s ShellscaledUiTest:I '*:S'
```

Inspect the latest `START` through `DONE pass=N fail=0` block. Tests run
automatically after page load. Rebuild after any WebUI edit; ignored
`.work/webui-fixture/snapshot.json` records the exact packaged asset hashes.
No fixture files enter the module package.

Coverage includes all translation keys and static DOM text/aria/placeholders in
Simplified Chinese, Traditional Chinese and English, system-locale propagation,
language-only storage, one connection button, cancellation while connecting,
preservation of dirty fields across language switches, masked saves alongside
an external preference change, text-only command output, English layout in all
three tabs, and removal of compatibility/developer copy. System language cases
override `navigator.languages` only inside this test WebView, not Android itself.

This fixture supplements actual KernelSU bridge and native helper acceptance;
it does not assert that a root manager, Tailscale or networking works.

For documentation screenshots, pass a tab (`overview`, `settings`, or `command`)
and language (`zh-CN`, `zh-TW`, or `en`, default `zh-CN`). This skips all automatic
tests and prepares the real UI through its language and tab controls. Data stays
synthetic: version `1.104.2`, state `Running`, and example addresses
`100.64.0.10` / `fd7a:115c:a1e0::10`. The command tab shows a simulated
`tailscale version` result, without test injection strings or debug copy.

```sh
python3 scripts/emulator.py shell am start -S -n dev.shellscaled.webuitest/.MainActivity --es screenshot overview --es locale zh-CN
python3 scripts/emulator.py logcat -d -s ShellscaledUiTest:I '*:S'
```

Wait for the matching `SCREENSHOT READY overview zh-CN` log before capturing.
Repeat with `settings` and `command` for the other tabs. This mode still has no
root, networking, real module access, or test overlay. Omit `screenshot` to run
the normal eleven regression cases again.
