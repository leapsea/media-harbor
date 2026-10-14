"""正式镜像与真实 yt-dlp：验证分辨率优先、画质上限和不可用清单刷新。"""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("source", ROOT / "scripts/incremental-container-smoke.py")
source = importlib.util.module_from_spec(spec)
spec.loader.exec_module(source)
api = source.fixture
RUN = ROOT / ".test-runtime/youtube-format" / str(time.time_ns())
NAME = "media-harbor-youtube-format-" + RUN.name
api.BASE = "http://127.0.0.1:18883/api"

# 保留实际 HTTP、yt-dlp 选流、音视频合并和 Go 调度，仅用本地素材替换 YouTube 提取器。
HTTP = source.SOURCE.replace("body=b'{}'", "body=json.dumps({'count':counts[self.path]}).encode()")
HTTP = HTTP.replace("elif self.path.endswith('.mp4'):", "elif self.path.endswith(('.mp4','.m4a','.webm')):")
HTTP = HTTP.replace("Path('/fixture/media.mp4').read_bytes()",
                    "(Path('/fixture') / self.path.split('/')[-1]).read_bytes()")

SDK = source.YOUTUBE_SDK.replace(
    "self._download_json('http://127.0.0.1:8001/post/youtube/'+code, code)",
    "count=self._download_json('http://127.0.0.1:8001/post/youtube/'+code, code)['count']",
)
OLD_FORMATS = """'formats':[{'format_id':'fixture','url':'http://127.0.0.1:8001/media/youtube/'+code+'.mp4',
                            'ext':'mp4','vcodec':'h264','acodec':'aac'}]"""
NEW_FORMATS = """'formats': ([{'format_id':'sb','url':'http://127.0.0.1:8001/storyboard',
                             'ext':'mhtml','protocol':'mhtml','vcodec':'none','acodec':'none'}]
                    if code=='never' or (code=='refresh' and count==1) else [
                    {'format_id':'low','url':'http://127.0.0.1:8001/media/youtube/low.mp4',
                     'ext':'mp4','vcodec':'h264','acodec':'none','width':320,'height':180,'preference':1000},
                    {'format_id':'high','url':'http://127.0.0.1:8001/media/youtube/high.mp4',
                     'ext':'mp4','vcodec':'h264','acodec':'none','width':640,'height':360,'preference':-1000},
                    {'format_id':'audio','url':'http://127.0.0.1:8001/media/youtube/audio.m4a',
                     'ext':'m4a','vcodec':'none','acodec':'aac','quality':2},
                    {'format_id':'opus','url':'http://127.0.0.1:8001/media/youtube/audio.webm',
                     'ext':'webm','vcodec':'none','acodec':'opus','quality':4}])"""
assert OLD_FORMATS in SDK
SDK = SDK.replace(OLD_FORMATS, NEW_FORMATS)
# 各场景使用独立频道身份，避免测试订阅被同一作者的唯一约束合并。
SDK = SDK.replace("self.playlist_result(entries(),'author','author',channel='author',channel_id='author')",
                  "self.playlist_result(entries(),self._match_id(url),self._match_id(url),channel=self._match_id(url),channel_id=self._match_id(url))")
SDK = SDK.replace("'channel_id':'author'", "'channel_id':code")
# 适配器入口同时覆盖普通 CLI 和 Go 使用的 Python SDK 控制通道。
WRAPPER = "#!/opt/engines/bin/python3\n" + SDK + """
if sys.argv[1:3]==['-u','-c']:
    code=sys.argv[3];sys.argv=['yt-dlp']+sys.argv[4:];exec(code)
else:
    yt_dlp.main(sys.argv[1:])
"""


def run():
    for directory in ("data", "downloads", "archive", "logs", "fixture/bin"):
        (RUN / directory).mkdir(parents=True)
    (RUN / "fixture/source.py").write_text(HTTP)
    for entry in ("python3", "yt-dlp"):
        path = RUN / "fixture/bin" / entry
        path.write_text(WRAPPER)
        path.chmod(0o755)
    args = ["docker", "run", "-d", "--rm", "--name", NAME,
            "--user", f"{os.getuid()}:{os.getgid()}", "-p", "127.0.0.1:18883:8080",
            "-e", "PATH=/fixture/bin:/opt/engines/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"]
    for directory in ("data", "downloads", "archive", "logs", "fixture"):
        args += ["-v", f"{RUN / directory}:/{directory}"]
    # 生成两种真实视频尺寸及独立音轨，最终用 ffprobe 校验文件而非只检查参数。
    startup = """set -e
ffmpeg -v error -f lavfi -i color=s=320x180:d=0.2 -c:v libx264 -y /fixture/low.mp4
ffmpeg -v error -f lavfi -i color=s=640x360:d=0.2 -c:v libx264 -y /fixture/high.mp4
ffmpeg -v error -f lavfi -i anullsrc -t 0.2 -c:a aac -y /fixture/audio.m4a
ffmpeg -v error -f lavfi -i sine=frequency=440:duration=0.2 -c:a libopus -y /fixture/audio.webm
/opt/engines/bin/python3 /fixture/source.py &
exec /app/server
"""
    subprocess.check_output(args + ["--entrypoint", "/bin/sh", api.IMAGE, "-c", startup])
    try:
        settings = api.wait(lambda: api.request("/settings"), bool)
        settings["debug"]["test_item_limit"] = 10
        settings["pipeline"]["youtube"].update(min_interval_seconds=.01, max_requests=1000)
        settings["ui"]["task_log_lines"] = 1000
        settings["youtube"].update(video=True, metadata=False, comments=False, subtitles=False,
                                   auto_subtitles=False, thumbnail=False, extra_audio=True, extract_audio=False)
        api.request("/settings", "PUT", settings)
        report = {}
        for code, height in (("highest", 360), ("capped", 180), ("refresh", 360), ("never", None)):
            # 每次只扫描一个素材条目，独立配置便于核对刷新数量及失败范围。
            (RUN / "data/fixture-items.json").write_text(json.dumps([code]))
            options = dict(settings["youtube"], format="bestvideo+bestaudio/best")
            # 原始默认值覆盖本地提取；旧 m4a 设置也不能强制转换额外音频。
            options.update(audio_format="best" if code == "highest" else "m4a",
                           extract_audio=code == "highest")
            if code == "capped":
                options["format"] = "bestvideo[height<=180]+bestaudio/best[height<=180]"
            sub = api.request("/subscriptions", "POST", {"platform": "youtube",
                              "url": "https://www.youtube.com/@" + code, "display_name": code,
                              "enabled": False, "youtube": options})
            api.request(f"/subscriptions/{sub['id']}/scan?kind=full", "POST")
            tasks = api.wait(lambda: [t for t in api.request("/tasks") if t["subscription_id"] == sub["id"]],
                             lambda ts: any(t["content_id"] for t in ts)
                             and all(t["status"] not in ("queued", "running") for t in ts))
            media = next(t for t in tasks if t["content_id"])
            log = api.request(f"/subscriptions/{sub['id']}/log")["log"]
            requests = json.loads((RUN / "data/requests.json").read_text())
            assert requests["/post/youtube/" + code] == (2 if code in ("refresh", "never") else 1), requests
            if height is None:
                assert media["status"] == "failed" and "重新解析后仍无" in media["error"], media
                assert "可用视频流 0" in log and "附属任务 [" not in log, log
            else:
                assert media["status"] == "completed", media
                content = next(c for c in api.request("/contents") if c["subscription_id"] == sub["id"])
                path = next(p for p in content["paths"] if p.endswith(".mp4"))
                streams = json.loads(subprocess.check_output(["docker", "exec", NAME, "ffprobe", "-v", "error",
                                     "-show_streams", "-of", "json", path]))["streams"]
                assert next(s for s in streams if s["codec_type"] == "video")["height"] == height, streams
                assert any(s["codec_type"] == "audio" for s in streams), streams
                # 校验实际编码与编码数据哈希，证明最佳 Opus 音轨没有被重编码。
                audio = next(p for p in content["paths"] if p.endswith(".audio.opus"))
                def audio_hash(path):
                    return subprocess.check_output(["docker", "exec", NAME, "ffmpeg", "-v", "error",
                           "-i", path, "-map", "0:a:0", "-c:a", "copy", "-f", "hash", "-hash", "sha256", "-"])
                original_hash = audio_hash("/fixture/audio.webm")
                assert audio_hash(audio) == original_hash, audio
                assert not any(p.endswith(".audio.m4a") for p in content["paths"]), content
                assert "不指定转码格式或码率" in log and "libfdk_aac" not in log, log
                if code == "highest":
                    extracted = next(p for p in content["paths"] if p.endswith(".extracted.opus"))
                    assert audio_hash(extracted) == original_hash, extracted
                report[code + "-audio"] = {"native_codec": "opus", "packet_hash_unchanged": True}
                if code == "refresh":
                    assert "重新解析完整格式清单" in log, log
            report[code] = {"status": media["status"], "height": height,
                            "metadata_requests": requests["/post/youtube/" + code]}
            print("PASS " + code + ": " + json.dumps(report[code], ensure_ascii=False), flush=True)
        (RUN / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2))
        print("容器验收结果：" + str(RUN / "report.json"), flush=True)
    except Exception:
        print(subprocess.check_output(["docker", "logs", NAME], text=True))
        raise
    finally:
        subprocess.run(["docker", "rm", "-f", NAME], stdout=subprocess.DEVNULL, check=False)


if __name__ == "__main__":
    run()
