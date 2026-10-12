#!/bin/sh
set -e

# 本地镜像独立命名，挂载目录沿用 Compose 配置，使用宿主机当前用户写入。
cd "$(dirname "$0")/.."
export IMAGE=media-harbor:local
export PORT=8080
export BIND_ADDRESS=127.0.0.1
export PUID="${PUID:-$(id -u)}"
export PGID="${PGID:-$(id -g)}"

docker compose build media-harbor
docker compose up -d --no-build --force-recreate media-harbor
