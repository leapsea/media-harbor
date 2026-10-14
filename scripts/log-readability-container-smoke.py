"""正式镜像与真实 yt-dlp 控制通道，验证中文日志、请求汇总和警告合并。"""
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
RUN = ROOT / ".test-runtime/log-readability" / str(time.time_ns())
NAME = "media-harbor-log-readability-" + RUN.name
api.BASE = "http://127.0.0.1:18885/api"

# 只替换平台素材；网络许可、格式解析、真实下载和日志持久化仍走正式实现。
SDK = source.YOUTUBE_SDK.replace(
    "code=self._match_id(url)",
    "code=self._match_id(url)\n        "
    "self.report_warning('Preferring \"zh-CN\" translated fields. Note that some metadata extraction may fail or be incorrect.')\n        "
    "self.report_warning('Preferring \"zh-CN\" translated fields. Note that some metadata extraction may fail or be incorrect.')",
)
SDK = SDK.replace("class Video(InfoExtractor):", "class Video(InfoExtractor):\n    IE_NAME='youtube'")
SDK = SDK.replace("class Profile(InfoExtractor):", "class Profile(InfoExtractor):\n    IE_NAME='youtube:tab'")
SDK = SDK.replace("posts=self._download_json", "self.report_warning('Preferring \"zh-CN\" translated fields. Note that some metadata extraction may fail or be incorrect.')\n        posts=self._download_json")
WRAPPER = "#!/opt/engines/bin/python3\n" + SDK + """
if sys.argv[1:3]==['-u','-c']:
    code=sys.argv[3];sys.argv=['yt-dlp']+sys.argv[4:];exec(code)
else:
    yt_dlp.main(sys.argv[1:])
"""


def run():
    for directory in ("data", "downloads", "archive", "logs", "fixture/bin"):
        (RUN / directory).mkdir(parents=True)
    (RUN / "fixture/source.py").write_text(source.SOURCE)
    (RUN / "data/fixture-items.json").write_text('["new3","new2","new1"]')
    for entry in ("python3", "yt-dlp"):
        path = RUN / "fixture/bin" / entry
        path.write_text(WRAPPER)
        path.chmod(0o755)
    args = ["docker", "run", "-d", "--rm", "--name", NAME,
            "--user", f"{os.getuid()}:{os.getgid()}", "-p", "127.0.0.1:18885:8080",
            "-e", "PATH=/fixture/bin:/opt/engines/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"]
    for directory in ("data", "downloads", "archive", "logs", "fixture"):
        args += ["-v", f"{RUN / directory}:/{directory}"]
    startup = """set -e
ffmpeg -v error -f lavfi -i color=s=32x32:d=0.2 -f lavfi -i anullsrc -shortest -c:v libx264 -c:a aac -y /fixture/media.mp4
/opt/engines/bin/python3 /fixture/source.py &
exec /app/server
"""
    subprocess.check_output(args + ["--entrypoint", "/bin/sh", api.IMAGE, "-c", startup])
    try:
        settings = api.wait(lambda: api.request("/settings"), bool)
        settings["debug"]["test_item_limit"] = 10
        settings["pipeline"]["youtube"].update(min_interval_seconds=1.5, max_requests=1000)
        settings["ui"]["task_log_lines"] = 1000
        settings["youtube"].update(video=True, format="b", metadata=True, comments=False,
                                   subtitles=False, auto_subtitles=False, thumbnail=False,
                                   extra_audio=False, extract_audio=False)
        api.request("/settings", "PUT", settings)
        sub = api.request("/subscriptions", "POST", {"platform": "youtube",
                          "url": "https://www.youtube.com/@author", "display_name": "日志可读性验收",
                          "enabled": False})
        scan = api.request(f"/subscriptions/{sub['id']}/scan?kind=full", "POST")
        tasks = api.wait(lambda: [t for t in api.request("/tasks") if t["subscription_id"] == sub["id"]],
                         lambda ts: len(ts) >= 4 and all(t["status"] == "completed" for t in ts))
        log = api.request(f"/subscriptions/{sub['id']}/log")["log"]
        (RUN / "task.log").write_text(log)
        scan_lines = [line for line in log.splitlines() if f"[任务 #{scan['id']}]" in line]
        warning = "[引擎警告] YouTube 使用 zh-CN 翻译的元数据"
        assert sum(warning in line for line in scan_lines) == 1, scan_lines
        assert any("[警告汇总] 同一提示重复 6 次" in line for line in scan_lines), scan_lines
        assert "[请求等待] YouTube · 视频信息解析 · 请求间隔" in log, log
        assert "[请求汇总] YouTube · 视频信息解析 · 媒体 new3" in log, log
        assert "[扫描汇总] 发现 3 / 新增 3" in log, log
        assert "[视频] new3" in log and "标题：new3" in log, log
        for old in ("HTTP 请求获准", "许可 ", "媒体传输 false", "资源序号 0", "Preferring", "等待平台返回媒体"):
            assert old not in log, (old, log)
        report = {"completed_media": sum(bool(t["content_id"]) for t in tasks),
                  "scan_warning_lines": 1, "coalesced_warning_repeats": 6,
                  "request_summary": True, "request_interval_unchanged": 1.5,
                  "task_log": str(RUN / "task.log")}
        (RUN / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2))
        print("PASS 日志可读性：" + json.dumps(report, ensure_ascii=False), flush=True)
        if os.environ.get("SMOKE_UI_HOLD") == "1":
            # 界面检查结束后写入继续标记；最长保留三分钟，防止测试容器遗留。
            print("UI_HOLD " + str(RUN), flush=True)
            deadline = time.monotonic() + 180
            while time.monotonic() < deadline and not (RUN / "continue-ui").exists():
                time.sleep(.2)
    except Exception:
        print(subprocess.check_output(["docker", "logs", NAME], text=True))
        raise
    finally:
        subprocess.run(["docker", "rm", "-f", NAME], stdout=subprocess.DEVNULL, check=False)


if __name__ == "__main__":
    run()
