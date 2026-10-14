"""只读 SDK 适配：输出元数据，并按 Go 的命令发起单次内存请求。"""
import base64
import json
import logging
import os
import sys


def emit(kind, **values):
    print(json.dumps({"kind": kind, **values}, default=str), flush=True)


class Logger:
    def debug(self, message):
        if not message.startswith("[debug]"):
            emit("log", message=message)
    info = debug
    warning = debug
    error = debug


def exchange(metadata, request, role="media"):
    emit("item", metadata=metadata, role=role)
    while True:
        command = json.loads(sys.stdin.readline())
        if command["kind"] == "next":
            return
        probe = command["probe"]
        # 内存探测是媒体消费者；还原阶段后下一条解析继续使用生产窗口。
        stage = os.environ.get("MEDIA_HARBOR_STAGE", "dryrun")
        os.environ["MEDIA_HARBOR_STAGE"] = "media"
        os.environ["MEDIA_HARBOR_PROBE"] = "1"
        try:
            status, body = request(probe["url"], probe.get("headers") or {})
        finally:
            os.environ["MEDIA_HARBOR_STAGE"] = stage
            os.environ.pop("MEDIA_HARBOR_PROBE", None)
        emit("probe", status=status, body=base64.b64encode(body).decode())


def youtube(payload):
    import yt_dlp
    from yt_dlp.networking import Request
    class ReadOnlyYoutubeDL(yt_dlp.YoutubeDL):
        def save_cookies(self): pass
    options = payload["options"]
    params = {"logger": Logger(), "quiet": True, "simulate": True, "cachedir": False,
              "extract_flat": "in_playlist", "lazy_playlist": True,
              "cookiefile": payload["cookie"] or None, "js_runtimes": {"node": {}}, "playlistend": payload["limit"],
              "format": options["format"] if options["video"] or options["extract_audio"] else "bestaudio/best",
              "writesubtitles": options["subtitles"], "writeautomaticsub": options["auto_subtitles"],
              "subtitleslangs": ["all", "-.*"], "getcomments": options["comments"],
              "extractor_args": {"youtube": {"max_comments": ["1"]}}}
    if payload.get("language"):
        params["extractor_args"]["youtube"]["lang"] = [payload["language"]]
    with ReadOnlyYoutubeDL(params) as ydl:
        def request(address, headers):
            with ydl.urlopen(Request(address, headers=headers)) as response:
                return response.status, response.read(payload["probe_bytes"])
        count = 0
        def deliver(item):
            nonlocal count
            if item.get("entries") is not None:
                for child in item["entries"]:
                    if child and count < payload["limit"]: deliver(child)
            elif count < payload["limit"]:
                if item.get("_type") in ("url", "url_transparent"):
                    item = ydl.extract_info(item["url"], download=False)
                count += 1
                exchange(ydl.sanitize_info(item), request)
        deliver(ydl.extract_info(payload["subscription"]["url"], download=False))


def instagram(payload):
    from gallery_dl import config, exception, job
    if os.path.isfile(payload["gallery_config"]): config.load((payload["gallery_config"],), strict=True)
    config.set(("cache",), "file", ":memory:")
    for key, value in {"cookies-update": False, "archive": None, "postprocessors": [], "actions": None,
                       "sleep-request": 0, "sleep-429": 0.001, "sleep-retries": 0.001,
                       "async": False, "cookies": payload["cookie"] or None,
                       "user-agent": payload["user_agent"], "headers": payload["headers"]}.items():
        config.set((), key, value)
    # Go 的窗口和冷却也覆盖只读探测，SDK 不另外叠加默认429等待。
    config.set(("extractor", "instagram"), "sleep-request", 0)
    config.set(("extractor", "instagram"), "sleep-429", 0.001)
    config.set(("extractor", "instagram"), "sleep-retries", 0.001)
    logging.basicConfig(stream=sys.stderr, level=logging.INFO)
    count = [0]; author = [""]
    class ReadOnlyJob(job.Job):
        avatar = False
        def handle_directory(self, metadata): pass
        def handle_url(self, address, metadata):
            if not self.avatar and count[0] >= payload["limit"]: raise exception.StopExtraction()
            if not self.avatar:
                count[0] += 1
                author[0] = metadata.get("username") or author[0]
            def request(url, headers):
                with self.extractor.session.get(url, headers=headers, stream=True) as response:
                    response.raise_for_status()
                    return response.status_code, next(response.iter_content(payload["probe_bytes"]), b"")
            exchange({"id": str(metadata.get("post_id", "avatar")), "url": address,
                      "http_headers": metadata.get("_http_headers", {})}, request, "avatar" if self.avatar else "media")
        def handle_queue(self, address, metadata):
            if count[0] >= payload["limit"]: raise exception.StopExtraction()
            cls = metadata.get("_extractor")
            if ReadOnlyJob(cls.from_url(address) if cls else address, self).run(): raise RuntimeError("Instagram 子流程请求失败")
    if ReadOnlyJob(payload["subscription"]["url"]).run(): raise RuntimeError("Instagram 请求验证失败")
    if author[0]:
        emit("log", message="头像阶段：验证博主 " + author[0] + " 的头像请求")
        avatar = ReadOnlyJob(f"https://www.instagram.com/{author[0]}/avatar/"); avatar.avatar = True
        if avatar.run(): raise RuntimeError("Instagram 头像请求验证失败")


try:
    payload = json.loads(sys.stdin.readline())
    youtube(payload) if payload["subscription"]["platform"] == "youtube" else instagram(payload)
except Exception as error:
    emit("error", message=str(error))
    sys.exit(1)
