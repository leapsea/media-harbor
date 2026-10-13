"""媒体响应的真实字节数独立输出，不将签名地址写入进度事件。"""
import json
import time
from urllib.parse import urlsplit


def install_instagram_progress():
    from gallery_dl.downloader.http import HttpDownloader
    original = HttpDownloader.download

    def download(self, url, pathfmt):
        request = self.session.request

        started = time.monotonic()
        attempts = 0

        def tracked(*args, **kwargs):
            nonlocal attempts
            attempts += 1
            at = time.monotonic()
            # 只记录服务器域名，不把带签名的媒体地址和 Cookie 写入日志。
            print(f"[instagram][info] 媒体 HTTP 请求开始；服务器 {urlsplit(url).hostname}；请求次数 {attempts}", flush=True)
            response = request(*args, **kwargs)
            print(f"[instagram][info] 媒体 HTTP 响应；状态 {response.status_code}；等待响应耗时 {time.monotonic()-at:.3f}s", flush=True)
            if response.status_code in (401, 403, 404, 410):
                print("@instagram-source-rejected " + json.dumps({"status": response.status_code}), flush=True)
            if response.status_code not in (200, 206):
                if response.status_code == 429:
                    print("@transfer " + json.dumps({"kind": "media", "status": "running", "phase": "媒体服务器返回 429，等待引擎退避重试"}), flush=True)
                return response
            offset = pathfmt.part_size() if response.status_code == 206 else 0
            size = response.headers.get("Content-Length") or "0"
            if response.status_code == 206:
                size = response.headers.get("Content-Range", "/0").rsplit("/", 1)[-1]
            total = int(size) if size.isdecimal() else 0
            print("@transfer " + json.dumps({"kind": "media", "status": "running", "phase": "传输媒体", "bytes_total": total, "bytes_done": offset}), flush=True)
            read = response.iter_content
            done = offset
            last = 0

            def chunks(*args, **kwargs):
                nonlocal done, last
                for chunk in read(*args, **kwargs):
                    done += len(chunk)
                    now = time.monotonic()
                    if now-last >= 1:
                        print("@transfer " + json.dumps({"kind": "media", "status": "running",
                            "phase": "传输媒体",
                            "bytes_total": total, "bytes_done": done,
                            "percent": min(99.9, done*100/total) if total else 0}), flush=True)
                        last = now
                    yield chunk

            response.iter_content = chunks
            return response

        # 同一个媒体下载期间追踪流读取，结束后还原会话，避免影响后续解析。
        self.session.request = tracked
        try:
            return original(self, url, pathfmt)
        finally:
            self.session.request = request
            print(f"[instagram][info] 媒体 HTTP 下载结束；请求次数 {attempts}；耗时 {time.monotonic()-started:.3f}s", flush=True)

    HttpDownloader.download = download
