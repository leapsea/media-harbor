"""只读请求验证：禁用引擎缓存/Cookie 回写，响应内容只读取到内存。"""
import json
import logging
import os
import sys
import time
from urllib.parse import urljoin


def emit(kind, message="", **values):
    print(json.dumps({"kind": kind, "message": message, **values}, default=str), flush=True)


class Logger:
    def debug(self, message):
        if not message.startswith("[debug]"):
            emit("log", message)

    info = debug

    def warning(self, message):
        emit("log", "警告：" + message)

    def error(self, message):
        emit("log", "错误：" + message)


def youtube(payload):
    import yt_dlp
    from yt_dlp.networking import Request

    class ReadOnlyYoutubeDL(yt_dlp.YoutubeDL):
        def save_cookies(self):
            pass

    options = payload["options"]
    need_video = options["video"] or options["extract_audio"]
    parameters = {
        "logger": Logger(), "quiet": True, "simulate": True,
        "cachedir": False, "cookiefile": payload["cookie"] or None,
        "js_runtimes": {"node": {}}, "playlistend": payload["limit"],
        "format": options["format"] if need_video else "bestaudio/best",
        "writesubtitles": options["subtitles"], "writeautomaticsub": options["auto_subtitles"],
        "subtitleslangs": ["all", "-.*"], "getcomments": options["comments"],
        "extractor_args": {"youtube": {"max_comments": ["1"]}},
    }
    with ReadOnlyYoutubeDL(parameters) as ydl:
        def probe(address, headers, label, depth=0):
            started = time.monotonic()
            with ydl.urlopen(Request(address, headers=headers or {})) as response:
                data = response.read(payload["probe_bytes"])
                if not data:
                    raise RuntimeError(label + "返回空响应")
                emit("log", f"请求阶段：{label}；HTTP {response.status}；内存读取 {len(data)} 字节；耗时 {time.monotonic()-started:.2f}s")
            # 播放清单成功不代表媒体分片可访问，再探测清单中的首个地址。
            if data.lstrip().startswith(b"#EXTM3U"):
                if depth >= payload["manifest_depth"]:
                    raise RuntimeError(label + "播放清单超过探测层数，未确认分片响应")
                child = next((line.strip() for line in data.decode("utf-8").splitlines() if line.strip() and not line.startswith("#")), None)
                if not child:
                    raise RuntimeError(label + "播放清单未包含可验证的媒体地址")
                probe(urljoin(address, child), headers, label + " / 首个分片", depth+1)

        emit("log", "解析阶段：读取 YouTube 媒体、字幕及所选评论；评论仅抽样 1 条")
        info = ydl.extract_info(payload["subscription"]["url"], download=False)
        count = 0

        def check(item):
            nonlocal count
            if item.get("entries") is not None:
                for child in item["entries"]:
                    if child and count < payload["limit"]:
                        check(child)
                return
            count += 1
            media_id = item.get("id", "unknown")
            emit("log", f"媒体阶段：{media_id}；发现 {count}；新增 0；重复 0；验证请求")
            emit("item", metadata=ydl.sanitize_info(item))
            selection = json.loads(sys.stdin.readline())
            if need_video or options["extra_audio"]:
                formats = item.get("requested_formats") or [item]
                for index, fmt in enumerate(formats):
                    if fmt.get("url"):
                        probe(fmt["url"], fmt.get("http_headers", item.get("http_headers")), f"{media_id} 媒体流 {index+1}")
                if options["extra_audio"] and not any(f.get("vcodec") == "none" for f in formats):
                    audio = next((f for f in reversed(item.get("formats", [])) if f.get("vcodec") == "none" and f.get("acodec") != "none"), None)
                    if audio:
                        probe(audio["url"], audio.get("http_headers"), f"{media_id} 附加音频")
            languages = selection["subtitle_languages"].split(",") if selection["subtitle_languages"] else []
            for language in languages:
                subtitles = item.get("subtitles", {}) if options["subtitles"] else {}
                tracks = subtitles.get(language) or item.get("automatic_captions", {}).get(language, [])
                if tracks:
                    track = tracks[-1]
                    if track.get("data"):
                        emit("log", f"请求阶段：{media_id} 字幕 {language}；已取得内存字幕")
                    else:
                        probe(track["url"], track.get("http_headers"), f"{media_id} 字幕 {language}")
            if options["subtitles"] or options["auto_subtitles"]:
                emit("log", f"字幕阶段：{media_id}；匹配语言 {','.join(languages) or '无'}")
            if options["thumbnail"] and item.get("thumbnail"):
                probe(item["thumbnail"], {}, f"{media_id} 缩略图")

        check(info)
        if not count:
            raise RuntimeError("平台未返回可验证的媒体")
        emit("log", f"扫描阶段：发现 {count}；新增 0；重复 0；未创建任务")


def instagram(payload):
    from gallery_dl import config, exception, extractor, job

    if os.path.isfile(payload["gallery_config"]):
        config.load((payload["gallery_config"],), strict=True)
    # 根级配置优先于提取器子级，显式阻断所有持久化与用户配置动作。
    config.set(("cache",), "file", ":memory:")
    for key, value in {"cookies-update": False, "archive": None, "postprocessors": [],
                       "actions": None, "async": False, "cookies": payload["cookie"] or None,
                       "user-agent": payload["user_agent"], "headers": payload["headers"]}.items():
        config.set((), key, value)
    logging.basicConfig(stream=sys.stderr, level=logging.INFO)
    count = [0]
    author = [""]

    class RequestJob(job.Job):
        avatar = False
        def handle_directory(self, metadata):
            pass

        def handle_url(self, address, metadata):
            if not self.avatar and count[0] >= payload["limit"]:
                raise exception.StopExtraction()
            if not self.avatar:
                count[0] += 1
                author[0] = metadata.get("username") or author[0]
            media_id = str(metadata.get("post_id", metadata.get("media_id", "unknown")))
            started = time.monotonic()
            with self.extractor.session.get(address, stream=True) as response:
                response.raise_for_status()
                data = next(response.iter_content(payload["probe_bytes"]), b"")
                if not data:
                    raise RuntimeError("媒体返回空响应")
                label = "头像" if self.avatar else "媒体"
                emit("log", f"请求阶段：{label} {media_id}；HTTP {response.status_code}；内存读取 {len(data)} 字节；发现 {count[0]}；新增 0；重复 0；耗时 {time.monotonic()-started:.2f}s")

        def handle_queue(self, address, metadata):
            if count[0] >= payload["limit"]:
                raise exception.StopExtraction()
            cls = metadata.get("_extractor")
            child = RequestJob(cls.from_url(address) if cls else address, self)
            if child.run():
                raise RuntimeError("Instagram 子流程请求失败")

    emit("log", "解析阶段：读取 Instagram 媒体，使用内存缓存和只读 Cookie")
    request_job = RequestJob(payload["subscription"]["url"])
    if request_job.run():
        raise RuntimeError("Instagram 请求验证失败，详情见引擎日志")
    if not count[0]:
        raise RuntimeError("平台未返回可验证的媒体")
    if author[0]:
        emit("log", f"头像阶段：验证博主 {author[0]} 的头像请求")
        avatar_job = RequestJob(f"https://www.instagram.com/{author[0]}/avatar/")
        avatar_job.avatar = True
        if avatar_job.run():
            raise RuntimeError("Instagram 头像请求验证失败")
    emit("log", f"扫描阶段：发现 {count[0]}；新增 0；重复 0；未创建任务")


try:
    payload = json.loads(sys.stdin.readline())
    youtube(payload) if payload["subscription"]["platform"] == "youtube" else instagram(payload)
except Exception as error:
    emit("error", str(error))
    sys.exit(1)
