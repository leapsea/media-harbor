"""媒体响应的真实字节数独立输出，不将签名地址写入进度事件。"""
import json
import time


def install_instagram_progress():
    from gallery_dl.downloader.http import HttpDownloader
    original = HttpDownloader.download

    def download(self, url, pathfmt):
        request = self.session.request

        def tracked(*args, **kwargs):
            response = request(*args, **kwargs)
            if response.status_code not in (200, 206):
                return response
            offset = pathfmt.part_size() if response.status_code == 206 else 0
            size = response.headers.get("Content-Length") or "0"
            if response.status_code == 206:
                size = response.headers.get("Content-Range", "/0").rsplit("/", 1)[-1]
            total = int(size) if size.isdecimal() else 0
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

    HttpDownloader.download = download
