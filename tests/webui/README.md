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
preservation of dirty fields across language switches and device refreshes,
masked saves alongside an external preference change, text-only command output,
all four tabs in three languages at 320/360 CSS-pixel viewport widths, and removal
of compatibility/developer copy. The device fixture includes online/offline,
unnamed, addressless, long-name, dual-stack and HTML-shaped synthetic examples.
Search, per-field copying, localized control-only feedback, Clipboard API mocks,
DOM copy fallback/failure, and the fixed console RPC are exercised. No mock
opens a browser or reads/writes Android's clipboard. System language cases
override `navigator.languages` only inside this test WebView, not Android itself.
Deferred clipboard promises also exercise pagehide/pageshow: an old login-link
copy cannot mark a new link as copied, invoke fallback while away, or clear a
newer operation's pending/disabled state. All synthetic login URLs use
`https://example.test/`; screenshot mode keeps the login URL empty.
Binary checks and updates use only the fixture's memory: newer/current versions,
unsupported manifests, check/update errors, and deliberately held busy responses
exercise explicit checks, button gating, card-local feedback, and preservation
of unsaved settings and synthetic identity. Switching tabs or languages does not
start a binary check, and the fake update cannot fetch or replace executables.
Stable/preview selection uses the fake native channel RPC, clears previous check
results, keeps unsaved edits, and restores the saved selection after an error.
Changing channel never checks or updates binaries, and does not add localStorage
keys. A status refresh also preserves an available update with the same Tailscale
version, as a newer module revision may contain a different binary.

This fixture supplements actual KernelSU bridge and native helper acceptance;
it does not assert that a root manager, Tailscale or networking works.

For documentation screenshots, pass a tab (`overview`, `devices`, `settings`, or `command`)
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
Repeat with `devices`, `settings` and `command` for the other tabs. The device
page uses three harmless documentation examples, excluding the adversarial test
strings. This mode still has no
root, networking, real module access, or test overlay. Omit `screenshot` to run
the normal twenty-one regression cases again.

The separate `ClipboardProbeActivity` is the only optional component that reads
the real clipboard. After copying in the real KernelSU WebUI, explicitly launch
it with the expected text's SHA256 (UTF-8, no added newline):

```sh
python3 scripts/emulator.py shell am start -S -n dev.shellscaled.webuitest/.ClipboardProbeActivity --es expectedSha256 EXPECTED_HEX
```

The probe requires the shell's `DUMP` permission, has no intent filter, waits for
foreground focus, and retries for at most one second. It compares literal text
in memory and logs only `PASS clipboard-match` or `FAIL clipboard-match`. It
does not display, save or log the clipboard contents or hash, coerce URI/Intent
items, or write to the clipboard. It is never run by the ordinary fixture suite;
remove the test APK after manual acceptance.

`scripts/test-webui.py` validates the real native devices projection in memory,
checks that ordinary status omits peers, and rejects devices/console requests
with command, URL, argv, environment, settings or component overrides. It does
not print or save the actual inventory and never invokes a valid console action;
the real external console launch is a separate manual acceptance step. The
native fixed target is `https://console.tailscale.com/admin/machines`. The native
runner also performs a read-only `binary-check`, checks its version/flag projection,
and rejects binary action requests containing arbitrary source, version, path,
checksum, architecture, or command fields. It never sends a valid `binary-update`;
real updates are a separate integration acceptance step. It saves both supported
channels, checks status reflects the preference without changing account/settings,
rejects invalid channels and mixed action fields, and restores the original
channel in `finally`, including when another native assertion fails.
