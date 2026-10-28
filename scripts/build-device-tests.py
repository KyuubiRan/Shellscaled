#!/usr/bin/env python3
"""Cross-compile tests; execute the resulting programs only on the emulator."""
import os
import subprocess
import argparse
from build import ROOT, SOURCE, compiler

env = os.environ.copy()
env.update(GOOS="android", GOARCH="arm64", CGO_ENABLED="1", CC=str(compiler("arm64")), GOCACHE=str(ROOT / ".work/cache/go-build"), GOMODCACHE=str(ROOT / ".work/cache/gomod"), GOTOOLCHAIN="local")
out = ROOT / ".work/tests"
out.mkdir(parents=True, exist_ok=True)
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--web-only", action="store_true")
args = parser.parse_args()
if not args.web_only:
    for package, name in [("./wgengine/router/osrouter", "router.test"), ("./util/osuser", "osuser.test"), ("./net/netmon", "netmon.test"), ("./derp/derphttp", "derphttp.test")]:
        subprocess.run([os.environ.get("SHELLSCALED_GO", "go"), "test", "-c", "-tags=ts_omit_ssh,ts_omit_clientupdate,ts_omit_linuxdnsfight", "-o", str(out / name), package], cwd=SOURCE, env=env, check=True)
subprocess.run([os.environ.get("SHELLSCALED_GO", "go"), "test", "-c", "-o", str(out / "web-control.test"), "."], cwd=ROOT / "helper", env=env, check=True)
print("Cross-compiled. Run these tests on the verified emulator; do not execute them on the host.")
