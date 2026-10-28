# Development

Develop on `dev` in both repositories; `main` contains releases. The root
submodule tracks the source repository's `main` branch. Push the source first,
then the parent reference. Preserve the owner's configured Git identity/signing.

## Source ancestry and updates

The official GitHub fork retains the real Tailscale history through
`v1.104.2` (`e26db7a5797683ec99ad1516c40b628c7f2dfd71`). The Android changes
are separate commits directly above that release, rather than a disconnected
snapshot. The original r1 source is retained by `shellscaled-v1.104.2-r1` so
existing parent commits remain reproducible.

Configure `upstream` as `https://github.com/tailscale/tailscale.git` when working
from a fresh clone. `android-upstream` may reference the original
`anasfanani/tailscale-android-cli` port. Fetch the intended official release and
rebase the Android patch series onto it; review actual conflicts instead of
copying the upstream tree into a new root commit. Check the resulting ancestry
with `git merge-base --is-ancestor <upstream-release> dev`. Never rewrite an
already published release tag. After an approved development-history rewrite,
use an explicit lease bound to the previously fetched remote revision.

## Local build

Requires Python 3, the Go version in `tailscale/go.mod` or newer, and an installed
Android NDK. `ANDROID_NDK_HOME` and `SHELLSCALED_GO` can select existing tools.
No SDK/NDK is downloaded automatically.

```sh
git submodule update --init
git -C tailscale switch dev
python3 scripts/build.py --arch arm64
```

Version comes from `tailscale/VERSION.txt`; the default revision and channel come
from `module/version.json`. `--revision` and `--release-channel` allow explicit
local overrides. The module revision is 0..99:
`versionCode = major * 100000000 + minor * 100000 + patch * 100 + revision`.
`--package-only` reuses only a matching Tailscale binary and rebuilds the helper.
Before release, build from clean committed trees and verify `build-info.json`.

Caches, staging and emulator overrides belong in ignored `.work/`; reference
clones belong in ignored `.reference/`; final ZIP/checksum files belong in
ignored `dist/`. Reuse them, clean obsolete products, and never clone AOSP.

## Release and documentation

Repositories are public. Both dedicated workflows run on branch pushes or manual
workflow dispatch: `dev` publishes GitHub Pre-releases; `main` publishes stable
Releases. Promote reviewed work to `main` deliberately. Initial workflow setup
uses `[skip ci]` so infrastructure does not itself publish a stable release.
CI compiles and checks package structure; all runtime tests use the guarded
Android emulator. Inherited upstream workflows remain disabled.

The CLI workflow publishes an Android ZIP, SHA256 and `.binary.json`. A dedicated
write deploy key, scoped only to this module repository, then checks out the
matching module branch and runs `scripts/ci-release.py sync`. It commits only
`tailscale` and `module/version.json`, without cloning another source tree.
Configure `SHELLSCALED_SYNC_KEY` in the CLI repository and `RELEASE_AUTHOR_NAME` /
`RELEASE_AUTHOR_EMAIL` in both repositories. Never export a personal token or
signing key for this integration.

The module workflow verifies the fixed CLI release's GitHub digests, manifest,
ZIP, core binary and Android ELF before using `--package-only`. It builds the
native helper, commits the source pointer/version metadata with its own token,
and targets that clean commit when releasing the offline ZIP. Metadata pushes
with the workflow token do not recursively start another build; CLI deploy-key
pushes intentionally do. No existing release, asset or tag is overwritten.
Shared module tags reserve increasing revisions across both branches, and a
serialized queue preserves pending runs from either branch. Repeating a failed
run resumes its own unmodified revision; an older CLI run cannot roll back the
source reference. New manual runs can select an existing CLI tag or use the
highest eligible version. Missing CLI releases skip initial bootstrap cleanly.

`module/version.json` records Tailscale version, module revision, versionCode,
source commit, CLI release tag, release channel and pending status. Module
revisions are separate from the CLI workflow's numeric run. Compatibility
metadata binds each Android binary to its source and package hashes; missing or
incompatible manifests do not qualify for in-app binary updates.

README files contain user instructions and screenshots, without development
workflow or owner-specific notes. Screenshots use the real Android WebView with
synthetic documentation data from [the isolated fixture](../tests/webui/README.md).
They never contain a real account or Tailnet address.

The README version badge includes public GitHub Pre-releases and links to the
Release list, since GitHub’s `/releases/latest` excludes Pre-releases. No token is
embedded in Markdown or its image URL; the badge reads public release metadata.

## Device view and external console

Ordinary status polls continue to request `peers=false`. The explicit `devices`
action reads the current client's visible devices and projects only names,
stable IDs, Tailscale addresses, online status, OS, self and offline last-seen
time. Peer map keys, user/account records, endpoints and credentials never enter
the response. The list is bounded by both device count and encoded bytes and is
kept only in page memory. Fixture screenshots use synthetic names and addresses.

The `console` action launches a fixed Android browser Intent for
`https://console.tailscale.com/admin/machines`, the destination linked by the
[official device management guide](https://tailscale.com/docs/features/access-control/device-management/how-to/filter).
It accepts no URL or command arguments and discards ActivityManager output.
The homepage `about` action similarly opens only
`https://github.com/KyuubiRan/Shellscaled` in the system browser, with no caller
URL or component. The WebView keeps its navigation and script restrictions; the command panel
retains its existing Tailscale-only allowlist.

Copying is initiated by a user click, with feedback on that control. Device
names, addresses, search text and clipboard contents are never persisted.

## Binary-only updates

The settings card checks only after a user request. Its source is the newest eligible
public release of [the Android CLI fork](https://github.com/KyuubiRan/tailscale-android-cli),
using GitHub asset digests and the generated manifest. Users choose stable or preview in Settings, with the choice saved in
private module data. Preview accepts non-draft prerelease and stable builds;
stable accepts only stable builds. Merge the fixed latest-stable endpoint with
the bounded release list, so preview traffic cannot hide a higher stable release.
Compare Tailscale version and numeric CLI run
before release date, preferring stable on an exact tie. A newer stable version
must outrank an older preview even if the preview was published later. The
installation channel supplies only the initial default; runtime binary metadata
cannot silently change the user's choice. Downloaded ZIP and binary hashes, source/version/architecture metadata,
Android PIE interpreter and binary compatibility must agree before service
interruption. The runtime download is distinct from the offline installer.

Keep `binaryCompatibility` aligned with the module's runtime contract. Bump it
when a binary needs new module routing, lifecycle or other supporting behavior;
older modules must request a module upgrade instead of accepting that binary.
Do not advance the installed module's `versionCode` during a binary-only update.
The original build metadata remains its installation provenance; runtime binary
metadata is trusted only when its hash matches the current file.

An update holds a process lock, stages a verified binary on the destination
filesystem, and only then stops the owned service. Replacement is atomic, with
a single temporary rollback link. Restore the prior service state after success
or rollback and remove all transaction files. Account state and preferences are
never copied, reset or deleted by the updater.
