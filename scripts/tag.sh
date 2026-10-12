#!/bin/sh
set -e

cd "$(dirname "$0")/.."
version=$(python3 -c 'import json; print(json.load(open("internal/config/release.json"))["version"])')
git tag -a "v$version" -m "Media Harbor $version"
git push origin "v$version"
