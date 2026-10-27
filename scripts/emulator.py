#!/usr/bin/env python3
"""Run adb operations only after verifying the selected emulator's AVD name."""
import argparse
import os
import subprocess
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--serial", default=os.environ.get("SHELLSCALED_EMULATOR", "emulator-5554"))
    parser.add_argument("args", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if not args.serial.startswith("emulator-"):
        parser.error("physical/network devices are forbidden")
    prefix = ["adb", "-s", args.serial]
    avd = subprocess.check_output(prefix + ["emu", "avd", "name"], text=True).splitlines()
    if not avd or avd[0].strip() != "Medium_Phone":
        parser.error("selected emulator is not Medium_Phone")
    command = args.args
    if command and command[0] == "--":
        command = command[1:]
    if not command:
        parser.error("provide an adb command")
    if command[0] not in {"shell", "push", "pull", "install", "uninstall", "root", "wait-for-device", "reboot", "logcat"}:
        parser.error("unsupported adb command")
    sys.exit(subprocess.run(prefix + command).returncode)


if __name__ == "__main__":
    main()
