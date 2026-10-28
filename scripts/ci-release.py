#!/usr/bin/env python3
"""Link a verified CLI release to a module revision, then prepare an offline build.

sync is offline and commits only the source gitlink and module/version.json.
prepare downloads only from the fixed public CLI repository. Neither command
pushes, publishes, executes Android binaries, or runs host tests.
"""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
import struct
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parents[1]
METADATA = ROOT / "module/version.json"
CLI_REPO = "KyuubiRan/tailscale-android-cli"
MODULE_REPO = "KyuubiRan/Shellscaled"
CLI_URL = f"https://github.com/{CLI_REPO}.git"
API = "https://api.github.com"
VERSION = r"([0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6})"
TAG = re.compile(rf"android-v{VERSION}-(dev\.|r)([1-9][0-9]{{0,9}})\Z")
ASSET = re.compile(rf"Tailscale-{VERSION}-r([1-9][0-9]{{0,9}})-arm64\.binary\.json\Z")
MODULE_TAG = re.compile(rf"v{VERSION}-r([0-9]{{1,2}})\Z")
SHA = re.compile(r"[0-9a-f]{64}\Z")
COMMIT = re.compile(r"[0-9a-f]{40}\Z")
MAX_ZIP = 64 << 20
MAX_JSON = 16 << 10
ALLOWED_STAGE = {"tailscale", "module/version.json"}


def fail(message):
    raise ValueError(message)


def git(*args, cwd=ROOT, capture=True, env=None):
    result = subprocess.run(["git", *args], cwd=cwd, check=True, text=True,
                            stdout=subprocess.PIPE if capture else None, env=env)
    return result.stdout.strip() if capture else ""


def branch_channel(branch):
    if branch not in ("main", "dev"):
        fail("Release branch must be main or dev")
    return "stable" if branch == "main" else "prerelease"


def read_json(path, limit=MAX_JSON):
    if path.stat().st_size > limit:
        fail("JSON metadata exceeds its size limit")
    return json.loads(path.read_text(encoding="utf-8"))


def write_json(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".new")
    temporary.write_text(json.dumps(data, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def version_tuple(value):
    if not isinstance(value, str) or re.fullmatch(VERSION, value) is None:
        fail("Expected a stable three-part Tailscale version")
    return tuple(map(int, value.split(".")))


def version_code(version, revision):
    major, minor, patch = version_tuple(version)
    if not 0 <= revision <= 99 or minor >= 1000 or patch >= 1000:
        fail("Module revision/version exceeds versionCode encoding (revision 0..99)")
    result = major * 100_000_000 + minor * 100_000 + patch * 100 + revision
    if result > 2_147_483_647:
        fail("versionCode exceeds Android's signed integer limit")
    return result


def validate_manifest(data, tag):
    parsed = TAG.fullmatch(tag)
    if not parsed or not isinstance(data, dict):
        fail("Invalid CLI release tag or manifest")
    version, kind, run = parsed.groups()
    channel = "prerelease" if kind == "dev." else "stable"
    required = {"schema", "compatibility", "releaseChannel", "tailscaleVersion",
                "sourceCommit", "architecture", "binarySHA256", "packageName", "packageSHA256"}
    if set(data) != required:
        fail("Unexpected CLI manifest schema")
    if (type(data["schema"]) is not int or data["schema"] != 1
            or type(data["compatibility"]) is not int or data["compatibility"] != 1
            or data["architecture"] != "arm64" or data["tailscaleVersion"] != version
            or data["releaseChannel"] != channel
            or data["packageName"] != f"Tailscale-{version}-r{run}-arm64.zip"):
        fail("CLI manifest/tag/version/ABI/channel mismatch")
    if (not isinstance(data["sourceCommit"], str) or not COMMIT.fullmatch(data["sourceCommit"])
            or any(not isinstance(data[key], str) or not SHA.fullmatch(data[key])
                   for key in ("binarySHA256", "packageSHA256"))):
        fail("Invalid CLI source commit or digest")
    version_code(version, 0)
    return data


def clean_branch(branch):
    branch_channel(branch)
    if git("branch", "--show-current") != branch:
        fail("Checkout the requested release branch before linking a release")
    if git("status", "--porcelain", "--untracked-files=all"):
        fail("Release preparation requires a clean checkout")
    url = git("config", "-f", ".gitmodules", "submodule.tailscale.url")
    if url != CLI_URL:
        fail("The source submodule must use the fixed public CLI repository")


def occupied_revision(version):
    revisions = [int(match[2]) for tag in git("tag", "--list", f"v{version}-r*").splitlines()
                 if (match := MODULE_TAG.fullmatch(tag)) and match[1] == version]
    return max(revisions, default=-1)


def next_metadata(current, manifest, tag, branch, pending, consume=False):
    version = manifest["tailscaleVersion"]
    old_version = current["tailscaleVersion"]
    if version_tuple(version) < version_tuple(old_version):
        fail("Refusing a Tailscale version downgrade")
    same_source = (current.get("cliReleaseTag") == tag
                   and current.get("sourceCommit") == manifest["sourceCommit"]
                   and old_version == version)
    if consume and current.get("pending") is True and same_source:
        revision = current["moduleRevision"]
    elif version == old_version:
        revision = current["moduleRevision"] + 1
    else:
        revision = 0
    # main/dev share immutable vX-rN module tags. A release on another branch
    # may have consumed a pending reservation; never overwrite that release.
    revision = max(revision, occupied_revision(version) + 1)
    return {"tailscaleVersion": version, "moduleRevision": revision,
            "versionCode": version_code(version, revision),
            "sourceCommit": manifest["sourceCommit"], "cliReleaseTag": tag,
            "releaseChannel": branch_channel(branch), "pending": pending}


def stage_gitlink(source):
    # Updating an uninitialized gitlink avoids cloning a second copy during
    # cross-repository synchronization. prepare checks out the pinned commit.
    git("update-index", "--add", "--cacheinfo", f"160000,{source},tailscale")
    git("add", "--", "module/version.json")


def commit_metadata(message):
    staged = set(git("diff", "--cached", "--name-only").splitlines())
    if not staged or not staged <= ALLOWED_STAGE:
        fail("Only the source gitlink and version metadata may be committed")
    name, email = os.environ.get("RELEASE_AUTHOR_NAME", ""), os.environ.get("RELEASE_AUTHOR_EMAIL", "")
    if not name or not email or any(c in name + email for c in "\r\n\x00"):
        fail("Configure RELEASE_AUTHOR_NAME and RELEASE_AUTHOR_EMAIL repository variables")
    identity = os.environ.copy()
    identity.update(GIT_AUTHOR_NAME=name, GIT_AUTHOR_EMAIL=email,
                    GIT_COMMITTER_NAME=name, GIT_COMMITTER_EMAIL=email)
    git("-c", f"user.name={name}", "-c", f"user.email={email}", "-c", "commit.gpgsign=false",
        "commit", "-m", message, capture=False, env=identity)


def cli_tag_key(tag):
    match = TAG.fullmatch(tag)
    if not match:
        fail("Invalid linked CLI tag")
    return (*version_tuple(match[1]), int(match[3]), match[2] == "r")


def reject_older_cli(current, tag, skip=False):
    previous = current.get("cliReleaseTag", "")
    if previous and cli_tag_key(tag) < cli_tag_key(previous):
        if skip:
            print("A newer CLI release is already linked; stale synchronization skipped")
            return True
        fail("Refusing an older CLI version/run than the currently linked core")
    return False


def sync(args):
    clean_branch(args.branch)
    manifest = validate_manifest(read_json(Path(args.manifest)), args.cli_tag)
    if manifest["releaseChannel"] != branch_channel(args.branch):
        fail("Cross-repository synchronization must use the same release branch")
    current = read_json(METADATA)
    if current.get("cliReleaseTag") == args.cli_tag:
        if (current.get("sourceCommit") != manifest["sourceCommit"]
                or current.get("tailscaleVersion") != manifest["tailscaleVersion"]):
            fail("An immutable CLI tag was reused with different source metadata")
        print("CLI release already linked; no new module revision")
        return
    if reject_older_cli(current, args.cli_tag, skip=True):
        return
    metadata = next_metadata(current, manifest, args.cli_tag, args.branch, pending=True)
    write_json(METADATA, metadata)
    stage_gitlink(metadata["sourceCommit"])
    commit_metadata(f"Link CLI {args.cli_tag} for module v{metadata['tailscaleVersion']}-r{metadata['moduleRevision']}")


def allowed_url(value):
    parsed = urllib.parse.urlsplit(value)
    host = (parsed.hostname or "").lower()
    return (parsed.scheme == "https" and not parsed.username and not parsed.password
            and not parsed.fragment and parsed.port in (None, 443)
            and (host in ("github.com", "api.github.com") or host.endswith(".githubusercontent.com")))


class SafeRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if not allowed_url(newurl):
            fail("Release download redirected outside GitHub HTTPS")
        redirected = super().redirect_request(req, fp, code, msg, headers, newurl)
        if redirected:
            redirected.remove_header("Authorization")
        return redirected


def fetch(url, limit):
    if not allowed_url(url):
        fail("Invalid fixed release URL")
    headers = {"Accept": "application/vnd.github+json", "User-Agent": "Shellscaled-release-linker"}
    token = os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN")
    if token and url.startswith(API + "/repos/"):
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(url, headers=headers)
    # Default ProxyHandler inherits the runner/owner's proxy environment.
    opener = urllib.request.build_opener(SafeRedirect())
    with opener.open(request, timeout=60) as response:
        if response.status != 200 or int(response.headers.get("Content-Length", "0")) > limit:
            fail("Invalid or oversized GitHub release response")
        result = response.read(limit + 1)
        if len(result) > limit:
            fail("GitHub release response exceeds its size limit")
        return result


def release_key(release, branch):
    if not isinstance(release, dict):
        return None
    match = TAG.fullmatch(release.get("tag_name", ""))
    if (not match or not isinstance(release.get("published_at"), str) or not release["published_at"]
            or release.get("draft") is not False
            or type(release.get("prerelease")) is not bool
            or (match[2] == "dev.") != release["prerelease"]
            or (branch == "main" and release["prerelease"])):
        return None
    return (*version_tuple(match[1]), int(match[3]), not release["prerelease"], release.get("published_at") or "")


def find_release(branch, requested):
    if requested:
        if not TAG.fullmatch(requested):
            fail("Manual CLI tag has an invalid format")
        release = json.loads(fetch(f"{API}/repos/{CLI_REPO}/releases/tags/{requested}", 1 << 20))
        if release_key(release, branch) is None:
            fail("Requested CLI release is incompatible with this branch")
        return release
    releases = json.loads(fetch(f"{API}/repos/{CLI_REPO}/releases?per_page=20", 1 << 20))
    if not isinstance(releases, list) or len(releases) > 20:
        fail("Unexpected CLI release list")
    # A high stable version must not disappear behind many more recently
    # published previews of older versions. The main publisher marks --latest.
    try:
        stable = json.loads(fetch(f"{API}/repos/{CLI_REPO}/releases/latest", 1 << 20))
    except urllib.error.HTTPError as error:
        if error.code != 404:
            raise
    else:
        if not isinstance(stable, dict) or stable.get("prerelease") is not False or stable.get("draft") is not False:
            fail("Unexpected latest stable CLI release response")
        releases = [item for item in releases if item.get("tag_name") != stable.get("tag_name")]
        releases.append(stable)
    eligible = [(key, release) for release in releases if (key := release_key(release, branch)) is not None]
    return max(eligible, key=lambda item: item[0])[1] if eligible else None


def verify_asset(asset, tag, limit):
    name = asset.get("name", "")
    if (asset.get("browser_download_url") != f"https://github.com/{CLI_REPO}/releases/download/{tag}/{name}"
            or not isinstance(asset.get("size"), int) or not 0 < asset["size"] <= limit
            or not isinstance(asset.get("digest"), str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", asset["digest"])):
        fail("Invalid GitHub asset URL, size, or API digest")
    data = fetch(asset["browser_download_url"], limit)
    if len(data) != asset["size"] or "sha256:" + hashlib.sha256(data).hexdigest() != asset["digest"]:
        fail("Downloaded asset does not match GitHub's API digest")
    return data


def load_release(release):
    tag = release["tag_name"]
    assets = release.get("assets", [])
    manifests = [item for item in assets if ASSET.fullmatch(item.get("name", ""))]
    if not manifests:
        return None
    if len(manifests) != 1:
        fail("CLI release must have exactly one matching arm64 manifest")
    data = verify_asset(manifests[0], tag, MAX_JSON)
    manifest = validate_manifest(json.loads(data), tag)
    if manifest["releaseChannel"] != ("prerelease" if release["prerelease"] else "stable"):
        fail("CLI manifest and GitHub release channels disagree")
    expected_name = manifest["packageName"].removesuffix(".zip") + ".binary.json"
    if manifests[0]["name"] != expected_name:
        fail("CLI manifest asset name does not match its payload")
    packages = [item for item in assets if item.get("name") == manifest["packageName"]]
    if len(packages) != 1 or packages[0].get("digest") != "sha256:" + manifest["packageSHA256"]:
        fail("CLI package is missing or has a different API digest")
    return manifest, verify_asset(packages[0], tag, MAX_ZIP)


def android_elf(binary):
    if len(binary) < 64 or binary[:7] != b"\x7fELF\x02\x01\x01":
        fail("Expected Android arm64 ELF")
    kind, machine = struct.unpack_from("<HH", binary, 16)
    entry, phoff = struct.unpack_from("<QQ", binary, 24)
    phsize, count = struct.unpack_from("<HH", binary, 54)
    if kind != 3 or machine != 183 or not entry or phsize != 56 or not 0 < count <= 4096 or phoff + count * phsize > len(binary):
        fail("Invalid Android PIE ELF header")
    interpreters, loads = 0, 0
    for index in range(count):
        ptype, _, offset, address, _, size, _, alignment = struct.unpack_from("<IIQQQQQQ", binary, phoff + index * phsize)
        if offset + size > len(binary):
            fail("ELF segment exceeds the binary")
        if ptype == 3:
            if binary[offset:offset + size] != b"/system/bin/linker64\0":
                fail("Expected Android linker64 interpreter")
            interpreters += 1
        if ptype == 1:
            if alignment < 16384 or alignment & (alignment - 1) or offset % alignment != address % alignment:
                fail("ELF PT_LOAD is not 16 KiB compatible")
            loads += 1
    if interpreters != 1 or not loads:
        fail("Android ELF interpreter/load segment missing")


def install_binary(manifest, package):
    with zipfile.ZipFile(io.BytesIO(package)) as archive:
        if len(archive.infolist()) > 256:
            fail("Unexpected CLI ZIP entry count")
        found = {}
        for name, limit in (("bin/tailscaled", MAX_ZIP), ("build-info.json", MAX_JSON)):
            matches = [item for item in archive.infolist() if item.filename == name]
            if len(matches) != 1 or not 0 < matches[0].file_size <= limit or ((matches[0].external_attr >> 16) & 0o170000) not in (0, 0o100000):
                fail("CLI ZIP has missing/duplicate/unsafe core metadata")
            with archive.open(matches[0]) as reader:
                found[name] = reader.read(limit + 1)
            if len(found[name]) > limit:
                fail("CLI ZIP entry exceeds its limit")
    info = json.loads(found["build-info.json"])
    expected = {key: manifest[key] for key in ("tailscaleVersion", "sourceCommit", "architecture", "binarySHA256", "releaseChannel")}
    expected["binaryCompatibility"] = manifest["compatibility"]
    if any(info.get(key) != value for key, value in expected.items()):
        fail("CLI ZIP build-info disagrees with the release manifest")
    binary = found["bin/tailscaled"]
    if hashlib.sha256(binary).hexdigest() != manifest["binarySHA256"]:
        fail("CLI core digest mismatch")
    android_elf(binary)
    directory = ROOT / ".work/bin/arm64"
    directory.mkdir(parents=True, exist_ok=True)
    path = directory / "tailscaled"
    path.write_bytes(binary)
    path.chmod(0o755)
    # This is the build cache's source identity, not installation metadata.
    write_json(directory / "tailscaled.json", {key: manifest[key] for key in ("tailscaleVersion", "sourceCommit", "architecture")})


def checkout_source(manifest):
    git("submodule", "update", "--init", "--depth=1", "--", "tailscale", capture=False)
    source = ROOT / "tailscale"
    git("fetch", "--depth=1", CLI_URL, manifest["sourceCommit"], cwd=source, capture=False)
    git("checkout", "--detach", manifest["sourceCommit"], cwd=source, capture=False)
    if git("rev-parse", "HEAD", cwd=source) != manifest["sourceCommit"] or (source / "VERSION.txt").read_text().strip() != manifest["tailscaleVersion"]:
        fail("Pinned CLI source commit/version mismatch")
    if git("status", "--porcelain", cwd=source):
        fail("CLI source checkout is dirty")


def outputs(**values):
    destination = os.environ.get("GITHUB_OUTPUT")
    if destination:
        with open(destination, "a", encoding="utf-8") as file:
            for key, value in values.items():
                text = str(value)
                if "\n" in text or "\r" in text:
                    fail("Invalid workflow output")
                file.write(f"{key}={text}\n")
    print(json.dumps(values, sort_keys=True))


def retry_reservation(current, expected):
    if not COMMIT.fullmatch(expected) or current.get("pending") is not False:
        return False
    head = git("rev-parse", "HEAD")
    if git("rev-list", "--parents", "-n", "1", "HEAD").split() != [head, expected]:
        return False
    changed = set(git("diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD").splitlines())
    tag = f"v{current['tailscaleVersion']}-r{current['moduleRevision']}"
    subject = f"Prepare module {tag} from CLI {current.get('cliReleaseTag', '')}"
    return ("module/version.json" in changed and changed <= ALLOWED_STAGE
            and git("log", "-1", "--format=%s") == subject
            and current.get("versionCode") == version_code(current["tailscaleVersion"], current["moduleRevision"])
            and bool(TAG.fullmatch(current.get("cliReleaseTag", "")))
            and git("rev-parse", "HEAD:tailscale") == current.get("sourceCommit"))


def module_already_published(tag):
    try:
        release = json.loads(fetch(f"{API}/repos/{MODULE_REPO}/releases/tags/{tag}", 1 << 20))
    except urllib.error.HTTPError as error:
        if error.code == 404:  # Drafts are handled later through their numeric ID.
            return False
        raise
    return release.get("draft") is False


def prepare(args):
    clean_branch(args.branch)
    current = read_json(METADATA)
    retry = False
    if args.expected_head and git("rev-parse", "HEAD") != args.expected_head:
        retry = retry_reservation(current, args.expected_head)
        if not retry:
            outputs(ready="false", reason="A newer push superseded this run")
            return
        if current["releaseChannel"] != branch_channel(args.branch):
            fail("Retry reservation belongs to a different release channel")
        tag = f"v{current['tailscaleVersion']}-r{current['moduleRevision']}"
        if module_already_published(tag):
            outputs(ready="false", reason="This immutable module release is already published")
            return
    requested = args.cli_tag or (current.get("cliReleaseTag", "") if retry or current.get("pending") else "")
    release = find_release(args.branch, requested)
    loaded = load_release(release) if release else None
    if loaded is None:
        outputs(ready="false", reason="No compatible CLI release is available yet")
        return
    manifest, package = loaded
    reject_older_cli(current, release["tag_name"])
    if retry:
        if (manifest["sourceCommit"] != current["sourceCommit"]
                or manifest["tailscaleVersion"] != current["tailscaleVersion"]
                or release["tag_name"] != current["cliReleaseTag"]):
            fail("An immutable retry reservation has different CLI source metadata")
        metadata = current
    else:
        metadata = next_metadata(current, manifest, release["tag_name"], args.branch, pending=False, consume=True)
    install_binary(manifest, package)
    checkout_source(manifest)
    tag = f"v{metadata['tailscaleVersion']}-r{metadata['moduleRevision']}"
    if not retry:
        write_json(METADATA, metadata)
        stage_gitlink(metadata["sourceCommit"])
        commit_metadata(f"Prepare module {tag} from CLI {release['tag_name']}")
    if git("status", "--porcelain", "--untracked-files=all"):
        fail("Module build checkout must be clean after its metadata commit")
    outputs(ready="true", version=metadata["tailscaleVersion"], revision=metadata["moduleRevision"],
            tag=tag, channel=metadata["releaseChannel"], source=metadata["sourceCommit"],
            commit=git("rev-parse", "HEAD"), cli_tag=metadata["cliReleaseTag"])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    link = sub.add_parser("sync")
    link.add_argument("--manifest", required=True)
    link.add_argument("--cli-tag", required=True)
    link.add_argument("--branch", required=True, choices=("main", "dev"))
    build = sub.add_parser("prepare")
    build.add_argument("--cli-tag", default="")
    build.add_argument("--branch", required=True, choices=("main", "dev"))
    build.add_argument("--expected-head", default="")
    args = parser.parse_args()
    try:
        (sync if args.command == "sync" else prepare)(args)
    except (ValueError, KeyError, TypeError, OSError, subprocess.CalledProcessError, zipfile.BadZipFile) as error:
        # Do not dump request headers, tokens, environment, or downloaded JSON.
        if isinstance(error, urllib.error.HTTPError):
            print(f"Release preparation failed: GitHub HTTP {error.code}", file=sys.stderr)
        elif isinstance(error, urllib.error.URLError):
            print("Release preparation failed: HTTPS request unavailable", file=sys.stderr)
        elif isinstance(error, subprocess.CalledProcessError):
            print("Release preparation failed: Git command failed", file=sys.stderr)
        else:
            print(f"Release preparation failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
