#!/usr/bin/env python3
"""Orchestrate native WebUI tests entirely in the verified Android emulator."""
import base64
import argparse
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]
GUARD = ["python3", str(ROOT / "scripts/emulator.py"), "shell"]
HELPER = "/data/adb/modules/shellscaled/bin/shellscaled-web"


def shell(command):
    return subprocess.run(GUARD + [command], text=True, capture_output=True, timeout=65)


def rpc(action, **fields):
    encoded = base64.b64encode(json.dumps({"action": action, **fields}).encode()).decode()
    proc = shell(HELPER + " rpc " + encoded)
    try:
        return json.loads(proc.stdout)
    except ValueError:
        raise AssertionError("Native helper did not return JSON; no private response printed") from None


def success(action, **fields):
    result = rpc(action, **fields)
    assert result.get("ok"), result.get("error", "native action failed")
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manager", choices=["ksu", "magisk"], default="ksu")
    args = parser.parse_args()
    original = success("status")
    assert original["state"] == "Running", "Complete login before authenticated WebUI tests"
    saved = original["settings"]
    # Never store account/IP inventory, credentials, or raw CLI status in test reports.
    baseline_ips = original.get("ips", [])
    try:
        for command in ["tailscale version", "tailscaled --version", "tailscale status --json --peers=false", "tailscale ping --help"]:
            assert success("command", command=command).get("exitCode") == 0
        print("PASS: allowed Tailscale and daemon diagnostics execute")
        attacks = ["id", "sh -c id", "tailscalex status", "tailscale status; touch /data/local/tmp/shellscaled-escape", "tailscale status | id", "tailscale status && id", "tailscale status > /data/local/tmp/shellscaled-escape", "tailscale status\nid", "tailscale status $(id)", "tailscale status `id`", "tailscale --socket=/other status", "tailscale status --socket=/other", "tailscale ssh localhost id", "tailscale debug local-creds", "tailscale file get /data/adb", "tailscale update", "tailscale set --auth-key=file:/data/adb/key", "tailscaled --cleanup", "tailscaled --state=/other"]
        assert shell("test ! -e /data/local/tmp/shellscaled-escape").returncode == 0
        for command in attacks:
            assert not rpc("command", command=command).get("ok"), "unsafe input was accepted"
        assert shell("test ! -e /data/local/tmp/shellscaled-escape").returncode == 0
        print(f"PASS: {len(attacks)} command escape attempts rejected without side effects")
        assert not rpc("settings", settings={"acceptDNS": True}).get("ok")
        assert not rpc("exec", command="id").get("ok")
        print("PASS: unknown settings and actions rejected")
        changed = success("settings", settings={"acceptRoutes": not saved["acceptRoutes"]})
        assert changed["settings"]["acceptRoutes"] != saved["acceptRoutes"]
        assert changed["settings"]["shieldsUp"] == saved["shieldsUp"]
        for action, state in [("down", "Stopped"), ("up", "Running")]:
            changed = success(action)
            assert changed["state"] == state
            assert changed["settings"]["acceptRoutes"] != saved["acceptRoutes"]
            if args.manager == "ksu":
                desc = shell("KSU_MODULE=shellscaled /data/adb/ksud module config get override.description")
            else:
                desc = shell("cat /data/adb/modules/shellscaled/module.prop")
            assert desc.returncode == 0
            labels = ("已连接", "已連線", "Connected") if state == "Running" else ("已断开", "已中斷連線", "Disconnected")
            assert any(label in desc.stdout for label in labels)
        print("PASS: up/down preserves preferences and immediately updates module description")
        for expected in ["Stopped", "Running"]:
            assert shell("/data/adb/modules/shellscaled/action.sh").returncode == 0
            assert success("status")["state"] == expected
        print("PASS: module card action toggles down/up")
        assert success("status").get("ips", []) == baseline_ips
        print("PASS: account identity preserved")
        original_locale = shell("cat /data/adb/shellscaled/ui.locale 2>/dev/null").stdout.strip() or "system"
        try:
            for locale, expected in [("zh-CN", "请选择"), ("zh-TW", "請選擇"), ("en", "Choose")]:
                assert expected in rpc("settings", settings={}, locale=locale).get("error", "")
                assert shell("cat /data/adb/shellscaled/ui.locale").stdout.strip() == locale
            assert not rpc("status", locale="../../other").get("ok")
        finally:
            success("status", locale=original_locale)
        print("PASS: all three native locales and language preference restoration")
    finally:
        restored = success("settings", settings={"acceptRoutes": saved["acceptRoutes"], "shieldsUp": saved["shieldsUp"]})
        if restored["exitNodeConfigured"]:
            success("settings", settings={"exitNodeAllowLanAccess": saved["exitNodeAllowLanAccess"]})
        success("up")
    print("Completed emulator WebUI backend checks; original settings restored")


if __name__ == "__main__":
    main()
