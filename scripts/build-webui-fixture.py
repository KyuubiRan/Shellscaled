#!/usr/bin/env python3
"""Package the real WebUI and emulator-only JS regressions without Gradle or host JS execution."""
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import zipfile

ROOT = Path(__file__).resolve().parents[1]
WORK = ROOT / ".work/webui-fixture"


def run(args):
    subprocess.run([str(value) for value in args], check=True)


def main():
    sdk_path = os.environ.get("ANDROID_HOME") or os.environ.get("ANDROID_SDK_ROOT")
    if not sdk_path:
        adb = shutil.which("adb")
        if not adb:
            raise SystemExit("Set ANDROID_HOME to an installed SDK")
        sdk_path = Path(adb).resolve().parents[1]
    sdk = Path(sdk_path)
    platform = sdk / "platforms/android-36/android.jar"
    tools = sdk / "build-tools/36.0.0"
    if not platform.exists() or not tools.exists():
        raise SystemExit("Fixture requires installed Android SDK platform 36 and build-tools 36.0.0")
    WORK.mkdir(parents=True, exist_ok=True)
    classes, dex = WORK / "classes", WORK / "dex"
    for directory in (classes, dex):
        if directory.exists():
            shutil.rmtree(directory)
        directory.mkdir()
    sources = sorted((ROOT / "tests/webui/src").rglob("*.java"))
    run(["javac", "--release", "17", "-classpath", platform, "-d", classes, *sources])
    run([tools / "d8", "--lib", platform, "--min-api", "26", "--output", dex, *sorted(classes.rglob("*.class"))])
    unsigned = WORK / "unsigned.apk"
    run([tools / "aapt2", "link", "-I", platform, "--manifest", ROOT / "tests/webui/AndroidManifest.xml", "-o", unsigned])
    assets = {name: (ROOT / "module/webroot" / name).read_bytes()
              for name in ("index.html", "app.js", "i18n.js", "styles.css")}
    # Parse a data literal for the runtime completeness assertion; never execute JS here.
    match = re.search(r"\bconst STRINGS\s*=\s*(\{.*?\})\s*;\s*\n\s*function loadPreference", assets["i18n.js"].decode(), re.S)
    if not match:
        raise SystemExit("Cannot locate the i18n data literal for fixture packaging")
    translations = json.loads(match.group(1))
    snapshot = {name: hashlib.sha256(data).hexdigest() for name, data in assets.items()}
    with zipfile.ZipFile(unsigned, "a", compression=zipfile.ZIP_DEFLATED) as archive:
        archive.write(dex / "classes.dex", "classes.dex")
        for name, data in assets.items():
            archive.writestr("assets/webroot/" + name, data)
        archive.write(ROOT / "tests/webui/tests.js", "assets/fixture/tests.js")
        archive.write(ROOT / "tests/webui/screenshot.js", "assets/fixture/screenshot.js")
        archive.writestr("assets/fixture/translations.json", json.dumps(translations, ensure_ascii=False))
        archive.writestr("assets/fixture/snapshot.json", json.dumps(snapshot, indent=2))
    aligned = WORK / "aligned.apk"
    run([tools / "zipalign", "-f", "4", unsigned, aligned])
    key = WORK / "emulator-only.jks"
    if not key.exists():
        run(["keytool", "-genkeypair", "-keystore", key, "-alias", "fixture", "-storepass", "android",
             "-keypass", "android", "-dname", "CN=Emulator Test", "-keyalg", "RSA", "-validity", "3650"])
    apk = WORK / "webui-fixture.apk"
    run([tools / "apksigner", "sign", "--ks", key, "--ks-key-alias", "fixture", "--ks-pass", "pass:android", "--out", apk, aligned])
    (WORK / "snapshot.json").write_text(json.dumps(snapshot, indent=2) + "\n")
    unsigned.unlink()
    aligned.unlink()
    print(apk)
    print("Build only. Run the JS regressions inside Medium_Phone; see tests/webui/README.md.")


if __name__ == "__main__":
    main()
