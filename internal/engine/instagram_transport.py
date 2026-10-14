"""SDK 控制适配：真实请求仍由原会话执行，调度与响应策略由 Go 决定。"""
import json
import os
import urllib.request


def control(event):
    request = urllib.request.Request(os.environ["MEDIA_HARBOR_CONTROL"], json.dumps(event).encode(),
                                    {"X-Media-Harbor-Token": os.environ["MEDIA_HARBOR_TOKEN"], "Content-Type": "application/json"})
    # 本机控制通道不使用平台代理，令牌不会发送到用户配置的 HTTP 代理。
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(request) as response:
        return json.load(response)


def install_instagram_transport():
    if not os.environ.get("MEDIA_HARBOR_CONTROL"):
        return
    from requests.adapters import HTTPAdapter
    original = HTTPAdapter.send

    def send(self, request, *args, **kwargs):
        # 下载描述已准备好，媒体会话的 CDN 请求不占解析窗口。
        lease = control({"Kind": "acquire", "Media": os.environ.get("MEDIA_HARBOR_STAGE") == "media"})["lease"]
        response = None
        try:
            response = original(self, request, *args, **kwargs)
            if os.environ.get("MEDIA_HARBOR_STAGE") == "media" and not os.environ.get("MEDIA_HARBOR_PROBE") and response.status_code in (200, 206):
                # SDK 字节读取只报告原始计数，百分比、阶段与日志节流由 Go 处理。
                read = response.iter_content
                total = response.headers.get("Content-Range", "/"+response.headers.get("Content-Length", "0")).rsplit("/", 1)[-1]
                offset = request.headers.get("Range", "bytes=0-").split("=", 1)[-1].split("-", 1)[0]
                def chunks(*args, **kwargs):
                    done = int(offset) if offset.isdecimal() else 0
                    for chunk in read(*args, **kwargs):
                        done += len(chunk)
                        print("@instagram-bytes " + json.dumps({"done": done, "total": int(total) if total.isdecimal() else 0}), flush=True)
                        yield chunk
                response.iter_content = chunks
            return response
        finally:
            headers = response.headers if response is not None else {}
            offset = request.headers.get("Range", "bytes=0-").split("=", 1)[-1].split("-", 1)[0]
            total = headers.get("Content-Range", "/"+headers.get("Content-Length", "0")).rsplit("/", 1)[-1]
            control({"Kind": "release", "Lease": lease, "Status": response.status_code if response is not None else 0,
                     "RetryAfter": headers.get("Retry-After", ""), "Offset": int(offset) if offset.isdecimal() else 0,
                     "Total": int(total) if total.isdecimal() else 0})

    HTTPAdapter.send = send
