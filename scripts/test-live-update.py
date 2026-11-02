#!/usr/bin/env python3
"""Run live-update fault injection only on the verified Medium_Phone emulator."""
import argparse
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import uuid

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--serial", required=True)
    parser.add_argument("--su", choices=("stock", "manager"), default="stock")
    parser.add_argument("--case", choices=("rollback-new", "rollback-old"), help="run one cleanup regression")
    args = parser.parse_args()
    emulator = ["python3", str(ROOT / "scripts/emulator.py"), "--serial", args.serial]

    def adb(*command, **kwargs):
        return subprocess.run(emulator + list(command), check=True, **kwargs)

    def root_shell(script, **kwargs):
        prefix = ["su", "0", "/system/bin/sh", "-c"] if args.su == "stock" else ["su", "-c"]
        kwargs.setdefault("timeout", 180)
        return adb("shell", shlex.join(prefix + [script]), **kwargs)

    # The guarded wrapper verifies AVD identity before this and every other adb operation.
    abi = adb("shell", "getprop ro.product.cpu.abi", capture_output=True, text=True).stdout.strip()
    arch = {"arm64-v8a": "arm64", "armeabi-v7a": "arm", "x86_64": "amd64", "x86": "386"}.get(abi)
    if not arch:
        raise SystemExit("unsupported emulator ABI")
    root_shell('[ "$(id -u)" = 0 ] && [ "$(getenforce)" = Enforcing ]')

    common_dir = Path(subprocess.check_output(
        ["git", "rev-parse", "--git-common-dir"], cwd=ROOT, text=True).strip())
    if not common_dir.is_absolute():
        common_dir = (ROOT / common_dir).resolve()
    cache = common_dir.parent / ".work/cache"
    work = ROOT / ".work/live-update-fixture"
    if work.exists():
        shutil.rmtree(work)
    work.mkdir(parents=True)
    env = os.environ.copy()
    env.update(GOOS="android", GOARCH=arch, CGO_ENABLED="0", GOTOOLCHAIN="local")
    env.setdefault("GOCACHE", str(cache / "go-build"))
    env.setdefault("GOMODCACHE", str(cache / "gomod"))
    subprocess.run(["go", "build", "-o", str(work / "fixture"),
                    str(ROOT / "tests/live-update/fixture.go")], env=env, check=True)

    common = (ROOT / "module/common.sh").read_text()
    ctl = (ROOT / "module/ctl.sh").read_text()
    replacements = {
        "SC_MODDIR=/data/adb/modules/shellscaled": "SC_MODDIR=$TEST_LIVE",
        "/data/adb/modules /data/adb/modules_update": '"$TEST_ROOT" "$TEST_ROOT"',
        '[ "$SC_MODDIR" = /data/adb/modules_update/shellscaled ]': '[ "$SC_MODDIR" = "$TEST_STAGE" ]',
    }
    for before, after in replacements.items():
        if ctl.count(before) != 1:
            raise SystemExit("live-update path isolation needs updating")
        ctl = ctl.replace(before, after)
    if common.count("SC_DATA=/data/adb/shellscaled") != 1:
        raise SystemExit("live-update data isolation needs updating")
    common = common.replace("SC_DATA=/data/adb/shellscaled", "SC_DATA=$TEST_ROOT/data")
    common += '\n. "$TEST_BASE/hooks.sh"\n'
    (work / "common.sh").write_text(common)
    (work / "ctl.sh").write_text(ctl)
    for name in ("hooks.sh", "old-supervisor.sh", "run.sh", "cleanup.sh"):
        shutil.copy2(ROOT / "tests/live-update" / name, work / name)
    remote = "/data/local/tmp/sc-lu-" + uuid.uuid4().hex[:12]
    adb("push", str(work), remote)
    try:
        selected = " --case " + shlex.quote(args.case) if args.case else ""
        root_shell("export TEST_BASE=" + shlex.quote(remote) +
                   '; chmod 0700 "$TEST_BASE/fixture"; /system/bin/sh "$TEST_BASE/run.sh"' + selected)
    finally:
        # The remote copy is removed by cleanup, so stream the local script.
        cleanup = (ROOT / "tests/live-update/cleanup.sh").read_text()
        root_shell("/system/bin/sh -c " + shlex.quote(cleanup) + " cleanup " + shlex.quote(remote), timeout=30)


if __name__ == "__main__":
    main()
