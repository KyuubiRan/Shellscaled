#!/usr/bin/env python3
"""Render a local version badge from an authenticated GitHub Release lookup."""
import argparse
import html
import json
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--repo", default="KyuubiRan/Shellscaled")
parser.add_argument("--tag", help="release tag, including an authenticated draft; default is latest stable")
args = parser.parse_args()
if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", args.repo):
    parser.error("invalid repository name")
if args.tag and not re.fullmatch(r"[A-Za-z0-9_.+-]{1,64}", args.tag):
    parser.error("invalid release tag")
endpoint = f"repos/{args.repo}/releases/" + ("tags/" + args.tag if args.tag else "latest")
result = subprocess.run(["gh", "api", endpoint], capture_output=True, text=True)
if result.returncode and args.tag:
    # A draft without a Git tag has an untagged URL and is not returned by
    # releases/tags. The authenticated release list still contains its tag_name.
    candidates = json.loads(subprocess.check_output(["gh", "api", f"repos/{args.repo}/releases?per_page=100"], text=True))
    release = next((item for item in candidates if item["tag_name"] == args.tag), None)
    if release is None:
        raise SystemExit("No accessible Release matches that tag")
elif result.returncode:
    raise SystemExit("No accessible stable Release; specify --tag for a draft")
else:
    release = json.loads(result.stdout)
version = release["tag_name"]
if not re.fullmatch(r"[A-Za-z0-9_.+-]{1,64}", version):
    raise SystemExit("Unexpected release tag")
label = html.escape(version, quote=True)
right = max(90, len(version) * 7 + 18)
width = 76 + right
svg = f'''<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="28" role="img" aria-label="Release: {label}">
<title>Release: {label}</title><defs><clipPath id="r"><rect width="{width}" height="28" rx="5"/></clipPath></defs>
<g clip-path="url(#r)"><path fill="#35423b" d="M0 0h76v28H0z"/><path fill="#207a4a" d="M76 0h{right}v28H76z"/></g>
<path fill="none" stroke="#fff" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" d="M11 8v8m-3-3 3 3 3-3M7 18v2h8v-2"/>
<g fill="#fff" text-anchor="middle" font-family="Verdana,DejaVu Sans,sans-serif" font-size="11"><text x="45" y="18">release</text><text x="{76+right/2:g}" y="18">{label}</text></g></svg>
'''
target = ROOT / "docs/images/release.svg"
target.parent.mkdir(parents=True, exist_ok=True)
target.write_text(svg)
print("Updated release badge from GitHub: " + version)
