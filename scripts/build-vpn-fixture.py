#!/usr/bin/env python3
"""Compile the emulator VPN fixture with an installed SDK; no Gradle downloads."""
import os
from pathlib import Path
import shutil
import subprocess
import zipfile

ROOT = Path(__file__).resolve().parents[1]
WORK = ROOT / ".work/vpn-fixture"


def run(args):
    subprocess.run([str(x) for x in args], check=True)


def main():
    sdk_path = os.environ.get("ANDROID_HOME") or os.environ.get("ANDROID_SDK_ROOT")
    if not sdk_path:
        adb = shutil.which("adb")
        if not adb:
            raise SystemExit("Set ANDROID_HOME to an installed SDK")
        sdk_path = Path(adb).resolve().parents[1]
    sdk = Path(sdk_path)
    platform = sdk / "platforms/android-36/android.jar"
    build_tools = sdk / "build-tools/36.0.0"
    if not platform.exists() or not build_tools.exists():
        raise SystemExit("Fixture requires installed Android SDK platform 36 and build-tools 36.0.0")
    WORK.mkdir(parents=True, exist_ok=True)
    classes = WORK / "classes"
    dex = WORK / "dex"
    classes.mkdir(exist_ok=True)
    dex.mkdir(exist_ok=True)
    java = sorted((ROOT / "tests/vpn/src").rglob("*.java"))
    run(["javac", "--release", "17", "-classpath", platform, "-d", classes, *java])
    run([build_tools / "d8", "--lib", platform, "--min-api", "26", "--output", dex, *sorted(classes.rglob("*.class"))])
    unsigned = WORK / "unsigned.apk"
    run([build_tools / "aapt2", "link", "-I", platform, "--manifest", ROOT / "tests/vpn/AndroidManifest.xml", "-o", unsigned])
    with zipfile.ZipFile(unsigned, "a", compression=zipfile.ZIP_DEFLATED) as archive:
        archive.write(dex / "classes.dex", "classes.dex")
    aligned = WORK / "aligned.apk"
    run([build_tools / "zipalign", "-f", "4", unsigned, aligned])
    key = WORK / "emulator-only.jks"
    if not key.exists():
        run(["keytool", "-genkeypair", "-keystore", key, "-alias", "fixture", "-storepass", "android", "-keypass", "android", "-dname", "CN=Emulator Test", "-keyalg", "RSA", "-validity", "3650"])
    apk = WORK / "vpn-fixture.apk"
    run([build_tools / "apksigner", "sign", "--ks", key, "--ks-key-alias", "fixture", "--ks-pass", "pass:android", "--out", apk, aligned])
    print(apk)


if __name__ == "__main__":
    main()
