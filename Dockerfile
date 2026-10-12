# 构建环境可覆盖，运行镜像与引擎版本独立固定。
ARG WEB_BUILD_IMAGE=node:24-alpine3.23
ARG GO_BUILD_IMAGE=golang:1.26.5-alpine3.23
# 前端构建产物由 Go 提供，无需额外 Web 容器。
FROM --platform=$BUILDPLATFORM ${WEB_BUILD_IMAGE} AS web
WORKDIR /src/web
COPY web/package*.json ./
# 锁文件变化时复用包下载缓存，安装结果仍由 npm ci 决定。
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# 使用纯 Go SQLite，支持 amd64 与 arm64 交叉编译。
FROM --platform=$BUILDPLATFORM ${GO_BUILD_IMAGE} AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# 依赖层不引入架构和版本参数，双架构共享下载结果后才分支编译。
ARG TARGETOS
ARG TARGETARCH
ARG APP_VERSION
# 本地连续构建保留包编译缓存；CI 跨运行继续使用 GHA 层缓存。
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags "-X media-harbor/internal/config.BuildVersion=${APP_VERSION}" -o /server ./cmd/server

# 引擎与 FFmpeg 独立固定版本；启动时不自动升级。
FROM alpine:3.23
ARG GALLERY_DL_VERSION
ARG YTDLP_VERSION
ARG FFMPEG_VERSION
COPY internal/config/release.json /tmp/build-release.json
RUN apk add --no-cache python3 py3-pip nodejs ca-certificates tzdata
# 未传构建参数时读取同一版本清单，本地构建和 CI 使用一致的固定依赖。
RUN GALLERY_DL_VERSION="${GALLERY_DL_VERSION:-$(python3 -c 'import json; print(json.load(open("/tmp/build-release.json"))["gallery_dl"])')}" \
 && YTDLP_VERSION="${YTDLP_VERSION:-$(python3 -c 'import json; print(json.load(open("/tmp/build-release.json"))["yt_dlp"])')}" \
 && FFMPEG_VERSION="${FFMPEG_VERSION:-$(python3 -c 'import json; print(json.load(open("/tmp/build-release.json"))["ffmpeg"])')}" \
 && apk add --no-cache "ffmpeg=${FFMPEG_VERSION}" \
 && python3 -m venv /opt/engines \
 && /opt/engines/bin/pip install --no-cache-dir "gallery-dl==${GALLERY_DL_VERSION}" "yt-dlp[default,curl-cffi]==${YTDLP_VERSION}" \
 && rm /tmp/build-release.json \
 && mkdir -p /app/web /data /logs /downloads /archive \
 && chown -R 1000:1000 /data /logs /downloads /archive
ENV PATH="/opt/engines/bin:${PATH}" ADDRESS=:8080 DATA_DIR=/data LOG_DIR=/logs DOWNLOAD_DIR=/downloads ARCHIVE_DIR=/archive WEB_DIR=/app/web HOME=/data TZ=Asia/Singapore
WORKDIR /app
COPY --from=backend /server /app/server
COPY --from=web /src/web/dist /app/web
USER 1000:1000
EXPOSE 8080
CMD ["/app/server"]
