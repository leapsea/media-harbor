"""薄 SDK 适配：HTTP 许可和逐条确认由 Go 决定，提取与下载仍使用 yt-dlp。"""
import sys
import yt_dlp
from yt_dlp.networking.common import RequestDirector

# 原方法属于当前 SDK 进程，平台时钟由跨进程 Go 实例共享。
original_send = RequestDirector.send
original_stdout = yt_dlp.YoutubeDL.to_stdout

# 原始条目只在评论阶段上报；不读取评论内容，不改变 SDK 分页及数量限制。
if os.environ.get("MEDIA_HARBOR_STAGE") == "comments":
    from yt_dlp.extractor.youtube import YoutubeIE
    original_comments = YoutubeIE._get_comments
    def get_comments(self, *args, **kwargs):
        limit = int(self._configuration_arg("max_comments", ["0"])[0])
        control({"Kind": "comment-progress", "Total": limit, "Offset": 0})
        for comment in original_comments(self, *args, **kwargs):
            control({"Kind": "comment-progress", "Total": limit, "Offset": 1})
            yield comment
    YoutubeIE._get_comments = get_comments


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
                 "RetryAfter": response.headers.get("Retry-After", "") if response is not None else "",
                 "Host": urllib.parse.urlparse(request.url).hostname or "", "Authenticated": bool(request.headers.get("Cookie")),
                 "SetCookies": response.headers.get_all("Set-Cookie", []) if response is not None else [],
                 "AuthFailure": ""})


def to_stdout(self, message, *args, **kwargs):
    original_stdout(self, message, *args, **kwargs)
    if os.environ.get("MEDIA_HARBOR_STREAM_ACK") == "1" and message.startswith("{"):
        # lazy-playlist 配合逐条确认，背压时不会继续获取下一条或下一页。
        sys.stdout.flush()
        if sys.stdin.readline() != "continue\n":
            raise SystemExit(0)


# 共享Cookie仅由Go按版本保存，旧SDK结束时不能覆写新上传的凭据。
if os.environ.get("MEDIA_HARBOR_COOKIE_UPDATE_GO") == "1":
    original_warning = yt_dlp.YoutubeDL.report_warning
    def report_warning(self, message, *args, **kwargs):
        original_warning(self, message, *args, **kwargs)
        try:
            control({"Kind": "sdk-error", "AuthFailure": message})
        except Exception:
            pass
    yt_dlp.YoutubeDL.report_warning = report_warning
    def save_cookies(self):
        pass
    yt_dlp.YoutubeDL.save_cookies = save_cookies

RequestDirector.send = send
yt_dlp.YoutubeDL.to_stdout = to_stdout
yt_dlp.main(sys.argv[1:])
