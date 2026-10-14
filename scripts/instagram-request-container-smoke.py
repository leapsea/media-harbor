"""正式容器：跨 SDK 进程请求间隔、共享 Retry-After、取消和只读验证。"""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import threading
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("incremental", ROOT / "scripts/incremental-container-smoke.py")
source = importlib.util.module_from_spec(spec)
spec.loader.exec_module(source)
api = source.fixture
RUN = ROOT / ".test-runtime/instagram-requests" / str(time.time_ns())
NAME = "media-harbor-instagram-requests-" + RUN.name
api.BASE = "http://127.0.0.1:18883/api"

# 上游记录实际请求时间；仅第一次帖子请求触发 429，响应头要求等待两秒。
HTTP = source.SOURCE.replace("import json,threading", "import json,threading,time")
HTTP = HTTP.replace("counts=Counter();", "events=[]; counts=Counter();")
HTTP = HTTP.replace("counts[self.path]+=1", "counts[self.path]+=1\n            events.append({'path':self.path,'at':time.monotonic()})\n            Path('/data/events.json').write_text(json.dumps(events))")
HTTP = HTTP.replace("if self.path.startswith('/profile/'):", "if self.path=='/post/instagram/new2' and counts[self.path]==1:\n            self.send_response(429);self.send_header('Retry-After','2');self.end_headers();return\n        if self.path.startswith('/profile/'):")


def wait_scan(task):
    tasks = api.wait(lambda: api.request("/tasks"), lambda ts: next(t for t in ts if t["id"] == task["id"])["status"] in ("completed", "failed", "cancelled"))
    current = next(t for t in tasks if t["id"] == task["id"])
    assert current["status"] == "completed", current


def run():
    for name in ("data", "downloads", "logs", "archive", "fixture/bin"):
        (RUN / name).mkdir(parents=True)
    (RUN / "fixture/source.py").write_text(HTTP)
    (RUN / "data/fixture-items.json").write_text('["new2","new1"]')
    # 素材不设置 sleep-429，确认默认 SDK 等待确实由正式 Go 入口接管。
    (RUN / "data/gallery-dl.json").write_text(json.dumps({"extractor": {"sleep-retries": 0.001}}))
    instagram = source.INSTAGRAM.replace("def items(self):", "def items(self):\n        owner='other' if '/other/' in self.url else 'author'")
    instagram = instagram.replace("'owner_id':'author'", "'owner_id':owner").replace("'username':'author'", "'username':owner")
    # 只读适配也直接导入 yt-dlp SDK，使用相同的离线提取器而不是访问真实平台。
    youtube_sdk = source.YOUTUBE.split("\n", 1)[1].replace("yt_dlp.main(sys.argv[1:])", "")
    # source.INSTAGRAM 已统一注册真实 SDK 素材，不能重复覆盖安装函数形成递归。
    # 历史入口：instagram.replace("exec(code)", "exec(youtube_sdk);exec(code)")。
    for name, script in (("python3", instagram), ("gallery-dl", instagram), ("yt-dlp", source.YOUTUBE)):
        path = RUN / "fixture/bin" / name
        path.write_text(script); path.chmod(0o755)
    args = ["docker", "run", "-d", "--rm", "--name", NAME, "--user", f"{os.getuid()}:{os.getgid()}",
            "-p", "127.0.0.1:18883:8080", "-e", "INSTAGRAM_REQUEST_INTERVAL_SECONDS=0.08",
            "-e", "INSTAGRAM_429_COOLDOWN_SECONDS=0.1",
            "-e", "PATH=/fixture/bin:/opt/engines/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"]
    for name in ("data", "downloads", "logs", "archive", "fixture"):
        args += ["-v", f"{RUN / name}:/{name}"]
    args += ["--entrypoint", "/bin/sh", api.IMAGE, "-c",
             "ffmpeg -v error -f lavfi -i color=s=32x32:d=0.2 -f lavfi -i anullsrc -shortest -c:v libx264 -c:a aac -y /fixture/media.mp4 && ffmpeg -v error -i /fixture/media.mp4 -frames:v 1 -threads 1 -y /fixture/media.png && /opt/engines/bin/python3 /fixture/source.py & exec /app/server"]
    subprocess.check_output(args)
    try:
        settings = api.wait(lambda: api.request("/settings"), bool)
        settings["debug"]["test_item_limit"] = 10
        settings["pipeline"]["youtube"]["min_interval_seconds"] = .02
        settings["pipeline"]["youtube"]["max_requests"] = 1000
        settings["ui"]["task_log_lines"] = 1000
        settings["instagram_concurrent"] = 1
        settings["youtube"].update(video=True, format="best", metadata=True, comments=False,
                                   subtitles=False, auto_subtitles=False, thumbnail=False, extra_audio=False, extract_audio=False)
        api.request("/settings", "PUT", settings)
        sub = api.request("/subscriptions", "POST", {"platform": "instagram", "url": "https://www.instagram.com/author/", "display_name": "IG 请求调度", "enabled": False})
        task = api.request(f"/subscriptions/{sub['id']}/scan?kind=full", "POST")
        api.wait(lambda: json.loads((RUN / "data/requests.json").read_text()), lambda counts: counts.get('/post/instagram/new2', 0) >= 1)
        # 429 已在请求中返回，此时再发起另一进程的头像解析及独立 YouTube 任务。
        created = []
        def create_avatar():
            try: created.append(api.request("/subscriptions", "POST", {"platform": "instagram", "url": "https://www.instagram.com/other/", "display_name": "IG 冷却中的头像", "enabled": False}))
            except Exception as error: created.append(error)
        thread = threading.Thread(target=create_avatar)
        thread.start()
        yt = api.request("/subscriptions", "POST", {"platform": "youtube", "url": "https://www.youtube.com/@author", "display_name": "YT 独立执行", "enabled": False})
        yt_task = api.request(f"/subscriptions/{yt['id']}/scan?kind=full", "POST")
        wait_scan(task); wait_scan(yt_task)
        thread.join(timeout=10)
        assert created and isinstance(created[0], dict), created
        api.wait(lambda: api.request("/tasks"), lambda ts: not any(t["status"] in ("queued", "running") for t in ts))
        events = json.loads((RUN / "data/events.json").read_text())
        limited = next(e for e in events if e["path"] == "/post/instagram/new2")
        ig = [e for e in events if "/instagram/" in e["path"] and not e["path"].startswith("/media/")]
        after = [e for e in ig if e["at"] > limited["at"]]
        gap = min(e["at"] for e in after) - limited["at"]
        assert gap >= 1.9, (gap, after)
        assert any("youtube" in e["path"] and limited["at"] < e["at"] < limited["at"]+2 for e in events), events
        gaps = [right["at"]-left["at"] for left, right in zip(ig, ig[1:])]
        assert min(gaps) >= .065, gaps
        log = api.request(f"/subscriptions/{sub['id']}/log")["log"]
        assert "所有扫描、帖子解析与头像请求统一冷却" in log and "媒体 HTTP 响应" in log, log
        print(f"PASS 跨进程请求最小间隔 {min(gaps):.3f}s；Retry-After 全平台暂停 {gap:.3f}s；YouTube 冷却期间继续", flush=True)
        # 试运行只读：配置与任务数量不变；Cookie 的内容和时间不变。
        before_tasks = len(api.request("/tasks")); before_subs = len(api.request("/subscriptions"))
        cookie = RUN / "data/cookies/instagram.txt"
        cookie.parent.mkdir(exist_ok=True)
        cookie.write_text("# Netscape HTTP Cookie File\n.example.test\tTRUE\t/\tFALSE\t0\told\tvalue\n")
        before_cookie = (cookie.read_bytes(), cookie.stat().st_mtime_ns)
        for platform, url in (("instagram", "https://www.instagram.com/author/"), ("youtube", "https://www.youtube.com/@author")):
            request = urllib.request.Request(api.BASE+"/subscriptions/dryrun", json.dumps({"platform": platform, "url": url}).encode(), {"Content-Type": "application/json"})
            with urllib.request.urlopen(request, timeout=30) as response:
                result = [json.loads(line) for line in response]
            assert result[-1].get("ok"), result
            assert any("HTTP 200" in r.get("message", "") for r in result), result
        assert len(api.request("/tasks")) == before_tasks and len(api.request("/subscriptions")) == before_subs
        assert before_cookie == (cookie.read_bytes(), cookie.stat().st_mtime_ns)
        print("PASS Go 试运行验证媒体和头像；两平台均未创建任务、配置或回写 Cookie", flush=True)
        report = {"minimum_request_interval_seconds": min(gaps), "retry_after_cooldown_seconds": gap,
                  "youtube_runs_during_instagram_cooldown": True, "dryrun_read_only": True}
        (RUN / "report.json").write_text(json.dumps(report, indent=2))
        print("容器验收结果：" + str(RUN / "report.json"), flush=True)
        if os.environ.get("SMOKE_UI_HOLD") == "1":
            print("UI_HOLD " + str(RUN), flush=True)
            deadline = time.monotonic()+60
            while time.monotonic()<deadline and not (RUN / "continue-ui").exists(): time.sleep(.2)
    except Exception:
        print(subprocess.check_output(["docker", "logs", NAME], text=True)); raise
    finally:
        subprocess.run(["docker", "rm", "-f", NAME], stdout=subprocess.DEVNULL, check=False)


if __name__ == "__main__":
    run()
