#!/usr/bin/env python3
"""Orchestrate native WebUI tests entirely in the verified Android emulator."""
import base64
import argparse
import json
import ipaddress
from datetime import datetime
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
    assert result.get("ok"), "Native action failed; private response omitted"
    return result


def check_devices(result):
    # Validate in memory only. Never print or save names, addresses, IDs or last-seen times.
    shared_fields = {"ok", "settings", "daemonRunning", "exitNodeConfigured", "exitCode", "devices", "truncated"}
    assert set(result) <= shared_fields, "Unexpected device response fields"
    values = result.get("devices")
    assert isinstance(values, list) and len(values) <= 1000, "Invalid device collection"
    assert "truncated" not in result or type(result["truncated"]) is bool, "Invalid truncation flag"
    required = {"id", "name", "dnsName", "ips", "online", "os", "self"}
    for device in values:
        assert isinstance(device, dict) and required <= set(device) <= required | {"lastSeen"}, "Unexpected device fields"
        assert all(isinstance(device[key], str) for key in ("id", "name", "dnsName", "os")), "Invalid device text"
        assert type(device["online"]) is bool and type(device["self"]) is bool, "Invalid device flags"
        assert isinstance(device["ips"], list), "Invalid address collection"
        for address in device["ips"]:
            try:
                assert isinstance(address, str)
                ipaddress.ip_address(address)
            except (AssertionError, ValueError):
                raise AssertionError("Invalid projected address; value omitted") from None
        if "lastSeen" in device:
            try:
                assert isinstance(device["lastSeen"], str)
                timestamp = datetime.fromisoformat(device["lastSeen"].replace("Z", "+00:00"))
                assert timestamp.tzinfo is not None
            except (AssertionError, ValueError):
                raise AssertionError("Invalid last-seen timestamp; value omitted") from None
    assert sum(device["self"] for device in values) <= 1, "Duplicate local-device projection"


def check_channel(value):
    assert isinstance(value, str) and value in ("stable", "preview"), "Invalid update channel"


def check_binary(result, expected_channel):
    # The check is read-only; never invoke a valid binary-update from this runner.
    assert set(result) <= {"ok", "settings", "daemonRunning", "exitNodeConfigured", "exitCode", "binary", "updateChannel"}, "Unexpected binary-check response fields"
    binary = result.get("binary")
    required = {"currentVersion", "latestVersion", "updateAvailable", "checked", "supported"}
    assert isinstance(binary, dict) and required <= set(binary) <= required | {"reason", "channel"}, "Invalid binary-check projection"
    assert all(isinstance(binary[key], str) and len(binary[key]) <= 128
               for key in ("currentVersion", "latestVersion")), "Invalid binary version text"
    assert all(type(binary[key]) is bool for key in ("updateAvailable", "checked", "supported")), "Invalid binary-check flags"
    assert "reason" not in binary or isinstance(binary["reason"], str) and len(binary["reason"]) <= 80, "Invalid binary-check reason"
    for payload, key in ((result, "updateChannel"), (binary, "channel")):
        if key in payload:
            check_channel(payload[key])
            assert payload[key] == expected_channel, "Binary check used a different update channel"
    if not binary["checked"]:
        assert not binary["supported"] and not binary["updateAvailable"], "Unchecked update unexpectedly enabled"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manager", choices=["ksu", "magisk"], default="ksu")
    args = parser.parse_args()
    original = success("status")
    assert "devices" not in original, "Ordinary status unexpectedly returned the peer inventory"
    assert original["state"] == "Running", "Complete login before authenticated WebUI tests"
    saved = original["settings"]
    saved_channel = original.get("updateChannel")
    check_channel(saved_channel)
    # Never store account/IP inventory, credentials, or raw CLI status in test reports.
    baseline_ips = original.get("ips", [])
    try:
        check_devices(success("devices"))
        assert "devices" not in success("status"), "Peer inventory leaked into ordinary status"
        print("PASS: devices returns only the bounded projected fields; ordinary status stays inventory-free")
        for channel in ("stable", "preview"):
            changed = success("binary-channel", channel=channel)
            assert changed.get("updateChannel") == channel and "binary" not in changed, "Channel save unexpectedly checked updates"
            status = success("status")
            assert status.get("updateChannel") == channel, "Saved update channel not reflected in status"
            assert status["settings"] == saved and status.get("ips", []) == baseline_ips, "Channel save changed settings or identity"
        success("binary-channel", channel=saved_channel)
        for channel in ("", "prerelease", "nightly", "../../other", True, 1, None, []):
            assert not rpc("binary-channel", channel=channel).get("ok"), "Unsupported update channel accepted"
        assert not rpc("binary-channel").get("ok"), "Missing update channel accepted"
        assert success("status").get("updateChannel") == saved_channel, "Invalid input changed update channel"
        print("PASS: stable/preview channel saves preserve settings and identity; invalid channels rejected")
        check_binary(success("binary-check"), saved_channel)
        print("PASS: binary-check returns bounded version text and typed update flags without inventory")
        for action in ("devices", "console", "about", "login", "binary-check", "binary-update", "binary-channel"):
            for fields in ({"command": "id"}, {"settings": {"shieldsUp": True}},
                           {"url": "https://example.invalid/"}, {"args": ["--user", "0"]},
                           {"env": {"PATH": "/other"}}, {"component": "example.invalid/.Activity"},
                           {"version": "0.0.1"}, {"path": "/data/local/tmp/example"},
                           {"sha256": "0" * 64}, {"architecture": "example"}):
                arguments = {"channel": saved_channel} if action == "binary-channel" else {}
                assert not rpc(action, **arguments, **fields).get("ok"), "Mixed or arbitrary action arguments accepted"
        for action in ("devices", "console", "about", "login", "binary-check", "binary-update", "status"):
            assert not rpc(action, channel="preview").get("ok"), "Channel override accepted outside channel action"
        for action in ("about", "login"):
            for fields in ({"command": ""}, {"command": None}, {"settings": None}, {"channel": None},
                           {"url": "https://github.com/KyuubiRan/Shellscaled"}):
                assert not rpc(action, **fields).get("ok"), "Fixed browser action accepted an extra field"
        # Valid login is covered only by synthetic LocalAPI/browser hooks in the
        # native tests. Never initiate authentication against the real account.
        # Valid console/about/update actions are likewise manual-only here.
        print("PASS: devices/console/about/login/binary actions reject mixed channel, command, settings, URL, argv, environment and source overrides")
        for command in ["tailscale version", "tailscaled --version", "tailscale status --json --peers=false", "tailscale ping --help"]:
            assert success("command", command=command).get("exitCode") == 0
        print("PASS: allowed Tailscale and daemon diagnostics execute")
        attacks = ["id", "sh -c id", "tailscalex status", "tailscale status; touch /data/local/tmp/shellscaled-escape", "tailscale status | id", "tailscale status && id", "tailscale status > /data/local/tmp/shellscaled-escape", "tailscale status\nid", "tailscale status $(id)", "tailscale status `id`", "tailscale --socket=/other status", "tailscale status --socket=/other", "tailscale ssh localhost id", "tailscale debug local-creds", "tailscale file get /data/adb", "tailscale update", "tailscale set --auth-key=file:/data/adb/key", "tailscaled --cleanup", "tailscaled --state=/other", "tailscale console", "tailscale devices", "tailscale about", "am start -a android.intent.action.VIEW -d https://example.invalid/"]
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
        try:
            restored = success("settings", settings={"acceptRoutes": saved["acceptRoutes"], "shieldsUp": saved["shieldsUp"]})
            if restored["exitNodeConfigured"]:
                success("settings", settings={"exitNodeAllowLanAccess": saved["exitNodeAllowLanAccess"]})
            success("up")
        finally:
            assert success("binary-channel", channel=saved_channel).get("updateChannel") == saved_channel
    print("Completed emulator WebUI backend checks; original settings and channel restored")


if __name__ == "__main__":
    main()
