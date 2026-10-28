#!/usr/bin/env python3
"""Build an offline module locally. Never downloads an SDK, NDK, or toolchain."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import zipfile

ROOT = Path(__file__).resolve().parents[1]
WORK = ROOT / ".work"
SOURCE = ROOT / "tailscale"


def run(args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)


def output(args, cwd=ROOT):
    return subprocess.check_output(args, cwd=cwd, text=True).strip()


def version():
    value = (SOURCE / "VERSION.txt").read_text().strip()
    if not re.fullmatch(r"\d+\.\d+\.\d+", value):
        raise SystemExit("Expected a stable three-part Tailscale VERSION.txt")
    return value


def compiler(arch):
    explicit = os.environ.get("ANDROID_NDK_HOME") or os.environ.get("ANDROID_NDK_ROOT")
    sdk = os.environ.get("ANDROID_HOME") or os.environ.get("ANDROID_SDK_ROOT")
    candidates = [Path(explicit)] if explicit else []
    if sdk:
        candidates += sorted((Path(sdk) / "ndk").glob("*"), reverse=True)
    if not candidates:
        adb = shutil.which("adb")
        if adb:
            candidates += sorted((Path(adb).resolve().parents[1] / "ndk").glob("*"), reverse=True)
    names = {"arm64": "aarch64-linux-android23-clang", "arm": "armv7a-linux-androideabi23-clang", "amd64": "x86_64-linux-android23-clang"}
    for ndk in candidates:
        for tool in (ndk / "toolchains/llvm/prebuilt").glob(f"*/bin/{names[arch]}"):
            return tool
    raise SystemExit("Install an Android NDK or set ANDROID_NDK_HOME; no automatic SDK download is performed")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--arch", choices=["arm64", "arm", "amd64"], default="arm64")
    parser.add_argument("--revision", type=int, default=0, help="module revision within the same Tailscale version (0..99)")
    parser.add_argument("--package-only", action="store_true", help="reuse this source revision's existing binary")
    args = parser.parse_args()
    if not 0 <= args.revision < 100:
        parser.error("revision must be 0..99")
    ver = version()
    major, minor, patch = map(int, ver.split("."))
    if minor >= 1000 or patch >= 1000:
        parser.error("version fields exceed versionCode encoding")
    code = major * 100_000_000 + minor * 100_000 + patch * 100 + args.revision
    if code > 2_147_483_647:
        parser.error("versionCode exceeds Android signed integer range")
    source_sha = output(["git", "rev-parse", "HEAD"], cwd=SOURCE)
    if output(["git", "status", "--porcelain"], cwd=SOURCE):
        raise SystemExit("Commit the Tailscale source changes before building")
    cache = WORK / "cache"
    binary = WORK / "bin" / args.arch / "tailscaled"
    binary.parent.mkdir(parents=True, exist_ok=True)
    metadata_file = binary.with_suffix(".json")
    metadata = {"tailscaleVersion": ver, "sourceCommit": source_sha, "architecture": args.arch}
    if not args.package_only:
        env = os.environ.copy()
        env.update({"GOOS": "android", "GOARCH": args.arch, "CGO_ENABLED": "1", "CC": str(compiler(args.arch)), "GOCACHE": str(cache / "go-build"), "GOMODCACHE": str(cache / "gomod"), "GOTOOLCHAIN": "local"})
        if args.arch == "arm":
            env["GOARM"] = "7"
        # Keep Android DNS resolution in bionic. Disable remote logging and unused host integrations.
        tags = "ts_include_cli,ts_omit_aws,ts_omit_kube,ts_omit_bird,ts_omit_tap,ts_omit_systray,ts_omit_syspolicy,ts_omit_logtail,ts_omit_netlog,ts_omit_tpm,ts_omit_ssh,ts_omit_clientupdate,ts_omit_linuxdnsfight"
        ldflags = f"-s -w -X tailscale.com/version.shortStamp={ver} -X tailscale.com/version.longStamp={ver}-shellscaled-{source_sha[:12]} -X tailscale.com/version.gitCommitStamp={source_sha} -extldflags=-Wl,-z,max-page-size=16384"
        run([os.environ.get("SHELLSCALED_GO", "go"), "build", "-trimpath", "-buildvcs=false", "-buildmode=pie", f"-tags={tags}", f"-ldflags={ldflags}", "-o", str(binary), "./cmd/tailscaled"], cwd=SOURCE, env=env)
        metadata_file.write_text(json.dumps(metadata, indent=2) + "\n")
    elif not binary.exists() or not metadata_file.exists() or json.loads(metadata_file.read_text()) != metadata:
        raise SystemExit("No matching binary for this source commit; run a full build first")
    helper = binary.parent / "shellscaled-web"
    helper_env = os.environ.copy()
    helper_env.update({"GOOS": "android", "GOARCH": args.arch, "CGO_ENABLED": "1", "CC": str(compiler(args.arch)), "GOCACHE": str(cache / "go-build"), "GOMODCACHE": str(cache / "gomod"), "GOTOOLCHAIN": "local"})
    if args.arch == "arm":
        helper_env["GOARM"] = "7"
    run([os.environ.get("SHELLSCALED_GO", "go"), "build", "-trimpath", "-buildvcs=false", "-buildmode=pie", "-ldflags=-s -w -extldflags=-Wl,-z,max-page-size=16384", "-o", str(helper), "."], cwd=ROOT / "helper", env=helper_env)
    stage = WORK / "package" / args.arch
    if stage.exists():
        shutil.rmtree(stage)
    shutil.copytree(ROOT / "module", stage)
    template = stage / "module.prop.in"
    props = template.read_text().replace("@VERSION@", ver).replace("@VERSION_CODE@", str(code)).replace("@ARCH@", args.arch)
    (stage / "module.prop").write_text(props)
    template.unlink()
    (stage / "bin").mkdir(exist_ok=True)
    shutil.copy2(binary, stage / "bin/tailscaled")
    shutil.copy2(helper, stage / "bin/shellscaled-web")
    (stage / "licenses").mkdir(exist_ok=True)
    for filename in ["LICENSE", "PATENTS"]:
        shutil.copyfile(SOURCE / filename, stage / "licenses" / f"tailscale-{filename}")
    shutil.copyfile(ROOT / "LICENSE", stage / "licenses/module-LICENSE")
    module_metadata = {"moduleSourceCommit": output(["git", "rev-parse", "HEAD"]), "moduleTreeDirty": bool(output(["git", "status", "--porcelain"]))}
    (stage / "build-info.json").write_text(json.dumps({**metadata, **module_metadata, "moduleRevision": args.revision, "versionCode": code}, indent=2) + "\n")
    checksums = []
    for path in sorted(stage.rglob("*")):
        if path.is_file() and "META-INF" not in path.relative_to(stage).parts:
            checksums.append(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.relative_to(stage).as_posix()}\n")
    (stage / "SHA256SUMS").write_text("".join(checksums))
    dist = ROOT / "dist"
    dist.mkdir(exist_ok=True)
    dest = dist / f"Shellscaled-{ver}-r{args.revision}-{args.arch}.zip"
    with zipfile.ZipFile(dest, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
        for path in sorted(stage.rglob("*")):
            if path.is_file():
                info = zipfile.ZipInfo(path.relative_to(stage).as_posix(), date_time=(2020, 1, 1, 0, 0, 0))
                executable = path.suffix == ".sh" or path.parent.name == "bin" or path.name == "update-binary"
                info.external_attr = (0o100755 if executable else 0o100644) << 16
                archive.writestr(info, path.read_bytes(), compress_type=zipfile.ZIP_DEFLATED, compresslevel=9)
    digest = hashlib.sha256(dest.read_bytes()).hexdigest()
    dest.with_suffix(".zip.sha256").write_text(f"{digest}  {dest.name}\n")
    print(f"Built {dest.name}: {dest.stat().st_size // 1024 // 1024} MiB, sha256 {digest}")


if __name__ == "__main__":
    main()
