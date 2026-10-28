# Development

Develop on `dev` in both repositories; `main` contains releases. The root
submodule tracks the source repository's `main` branch. Push the source first,
then the parent reference. Preserve the owner's configured Git identity/signing.

## Source ancestry and updates

The private source mirror retains the real official Tailscale history through
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
python3 scripts/build.py --arch arm64 --revision 2
```

Version comes from `tailscale/VERSION.txt`. The module revision is 0..99:
`versionCode = major * 100000000 + minor * 100000 + patch * 100 + revision`.
`--package-only` reuses only a matching Tailscale binary and rebuilds the helper.
Before release, build from clean committed trees and verify `build-info.json`.

Caches, staging and emulator overrides belong in ignored `.work/`; reference
clones belong in ignored `.reference/`; final ZIP/checksum files belong in
ignored `dist/`. Reuse them, clean obsolete products, and never clone AOSP.

## Release and documentation

Repositories remain private and Actions disabled; builds and emulator tests run
locally. See [emulator instructions](emulator.md) and [validation](validation.md).
Tag only the validated commit and upload the offline ZIP plus its checksum.

README files contain user instructions and screenshots, without development
workflow or owner-specific notes. Screenshots use the real Android WebView with
synthetic documentation data from [the isolated fixture](../tests/webui/README.md).
They never contain a real account or Tailnet address.

The version badge is a local SVG so it also works for a private repository.
Generate it from the authenticated Release API, including a draft when preparing
a release, then verify it against the published Release:

```sh
python3 scripts/update-release-badge.py --tag v1.104.2-r2
python3 scripts/update-release-badge.py
```

Only the tag is written into the SVG; authentication stays inside `gh`, with no
token in Markdown, the image, or its URL. No hosted job is used to update it.
