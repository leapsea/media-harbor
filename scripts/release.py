"""读取固定构建版本，检查上游并生成补丁版本候选；不发布或修改远程仓库。"""
import argparse
import copy
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "internal/config/release.json"


def number(version):
    """比较稳定版本；Alpine 软件包修订号独立于上游版本。"""
    base, _, revision = version.partition("-r")
    return tuple(map(int, base.split("."))), int(revision or 0)


def pypi_version(package):
    with urllib.request.urlopen(f"https://pypi.org/pypi/{package}/json", timeout=30) as response:
        data = json.load(response)
    stable = [v for v, files in data["releases"].items()
              if re.fullmatch(r"\d+(?:\.\d+)+", v) and any(not f.get("yanked", False) for f in files)]
    return max(stable, key=number)


def apk_versions(raw):
    """APKINDEX 是 Alpine 的实际安装索引，不以官网发布替代可安装版本。"""
    with tarfile.open(fileobj=io.BytesIO(raw), mode="r:gz") as archive:
        text = archive.extractfile("APKINDEX").read().decode()
    versions = set()
    for entry in text.split("\n\n"):
        fields = dict(line.split(":", 1) for line in entry.splitlines() if ":" in line)
        if fields.get("P") == "ffmpeg":
            versions.add(fields["V"])
    return versions


def ffmpeg_version(branch):
    available = []
    for arch in ("x86_64", "aarch64"):
        url = f"https://dl-cdn.alpinelinux.org/alpine/v{branch}/community/{arch}/APKINDEX.tar.gz"
        with urllib.request.urlopen(url, timeout=30) as response:
            available.append(apk_versions(response.read()))
    # 同时支持两个目标架构才进入多架构发布。
    return max(available[0] & available[1], key=number)


def next_version(current, tags):
    major, minor, patch = map(int, current.split("."))
    pattern = re.compile(rf"v{major}\.{minor}\.(\d+)$")
    patches = [patch] + [int(match[1]) for tag in tags if (match := pattern.fullmatch(tag))]
    return f"{major}.{minor}.{max(patches) + 1}"


def candidate(current, latest, tags):
    result = copy.deepcopy(current)
    changed = []
    for key in ("gallery_dl", "yt_dlp", "ffmpeg"):
        if number(latest[key]) > number(current[key]):
            result[key] = latest[key]
            changed.append(key)
    if changed:
        result["version"] = next_version(current["version"], tags)
    return result, changed


def github_output(values, output):
    if output:
        with open(output, "a") as file:
            for key, value in values.items():
                file.write(f"{key}={value}\n")


def resolve(args):
    current = json.loads(MANIFEST.read_text())
    result, changed = current, []
    if args.check:
        latest = {"gallery_dl": pypi_version("gallery-dl"), "yt_dlp": pypi_version("yt-dlp"),
                  "ffmpeg": ffmpeg_version(current["alpine"])}
        tags = subprocess.check_output(["git", "tag", "--list"], text=True).splitlines()
        result, changed = candidate(current, latest, tags)
        print(json.dumps({"current": current, "available": latest, "changed": changed}, ensure_ascii=False))
    else:
        for key in ("gallery_dl", "yt_dlp", "ffmpeg"):
            override = os.environ.get(key.upper())
            if override:
                result[key] = override
        if args.tag:
            result["version"] = args.tag.removeprefix("v")
    values = {**result, "automatic": str(bool(changed)).lower(),
              "build": str(not args.check or bool(changed)).lower()}
    github_output(values, args.output)
    print(json.dumps(values, ensure_ascii=False))
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with open(summary, "a") as file:
            file.write("## Dependency check\n\n" + ("Version: " + result["version"] if changed else "No dependency update" if args.check else "Version: " + result["version"]) + "\n\n")
            for key in ("gallery_dl", "yt_dlp", "ffmpeg"):
                file.write(f"- {key}: {current[key]} → {result[key]}\n")


def apply(args):
    current = json.loads(MANIFEST.read_text())
    for key in ("version", "gallery_dl", "yt_dlp", "ffmpeg"):
        current[key] = os.environ[key.upper()]
    MANIFEST.write_text(json.dumps(current, indent=2) + "\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest="command", required=True)
    check = commands.add_parser("resolve")
    check.add_argument("--check", action="store_true")
    check.add_argument("--tag", default="")
    check.add_argument("--output")
    check.set_defaults(run=resolve)
    commands.add_parser("apply").set_defaults(run=apply)
    args = parser.parse_args()
    args.run(args)
