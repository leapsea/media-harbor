"""薄 SDK 适配：HTTP 许可和逐条确认由 Go 决定，提取与下载仍使用 yt-dlp。"""
import sys
import yt_dlp
from yt_dlp.networking.common import RequestDirector

# 原方法属于当前 SDK 进程，平台时钟由跨进程 Go 实例共享。
original_send = RequestDirector.send
original_stdout = yt_dlp.YoutubeDL.to_stdout


def send(self, request):
    lease = control({"Kind": "acquire", "Media": os.environ.get("MEDIA_HARBOR_STAGE") == "media"})["lease"]
    response = None
    try:
        response = original_send(self, request)
        return response
    except yt_dlp.networking.exceptions.HTTPError as error:
        response = error.response
        raise
    finally:
        # 传输层仅报告原始状态，窗口、重试冷却及取消由共享 Go 实例处理。
        control({"Kind": "release", "Lease": lease,
                 "Status": response.status if response is not None else 0,
                 "RetryAfter": response.headers.get("Retry-After", "") if response is not None else ""})


def to_stdout(self, message, *args, **kwargs):
    original_stdout(self, message, *args, **kwargs)
    if os.environ.get("MEDIA_HARBOR_STREAM_ACK") == "1" and message.startswith("{"):
        # lazy-playlist 配合逐条确认，背压时不会继续获取下一条或下一页。
        sys.stdout.flush()
        if sys.stdin.readline() != "continue\n":
            raise SystemExit(0)


RequestDirector.send = send
yt_dlp.YoutubeDL.to_stdout = to_stdout
yt_dlp.main(sys.argv[1:])
