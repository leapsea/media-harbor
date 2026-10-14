"""用正式镜像与可控离线引擎验证平台串行、跨平台独立和重启续跑。"""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import time

# 复用 Instagram 验收素材与 HTTP/API 工具，不接触本地真实任务或远端平台。
spec = importlib.util.spec_from_file_location("instagram_smoke", Path(__file__).with_name("instagram-container-smoke.py"))
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)
fixture.RUN = fixture.ROOT / ".test-runtime" / "platform-queue" / str(time.time_ns())
fixture.NAME = "media-harbor-platform-queue-" + fixture.RUN.name
request, wait = fixture.request, fixture.wait

YOUTUBE = r'''#!/opt/engines/bin/python3
import json,os,sys,time
from pathlib import Path
from urllib.parse import urlparse,parse_qs
args=sys.argv[1:]
if '--version' in args:
    print('fixture'); sys.exit(0)
if '--load-info-json' in args:
    data=json.loads(Path(args[args.index('--load-info-json')+1]).read_text())
    code=data['id'].removesuffix('-video')
    while code=='yt-a' and not os.path.exists('/data/release-yt-a'): time.sleep(.03)
    path=args[args.index('-o')+1] % {'title':data['title'],'id':data['id'],'ext':'mp4'}
    Path(path).parent.mkdir(parents=True,exist_ok=True)
    Path(path).write_bytes(b'offline-video-fixture')
    sys.exit(0)
url=urlparse(args[-1])
code=parse_qs(url.query).get('v',[''])[0].removesuffix('-video') or url.path.strip('/@')
print(json.dumps({'id':code+'-video','title':code,'channel':code,'channel_id':'UC'+code,
    'webpage_url':'https://www.youtube.com/watch?v='+code+'-video',
    'formats':[{'format_id':'fixture','ext':'mp4','vcodec':'h264','acodec':'none','filesize':21}]}))
'''


def tasks_for(sub):
    return [task for task in request("/tasks") if task["subscription_id"] == sub["id"]]


def running_downloads(sub):
    return [task for task in tasks_for(sub) if task["content_id"] and task["status"] == "running"]


def setup():
    for name in ("data", "downloads", "logs", "archive", "fixture/bin"):
        (fixture.RUN / name).mkdir(parents=True)
    # Instagram A 的 HTTP 响应等待释放文件，其余素材正常下载。
    source = fixture.SOURCE.replace("import json,threading", "import json,threading,os,time")
    source = source.replace("body=b'GIF89a'", "while self.path.startswith('/media/ig-a/') and not os.path.exists('/data/release-ig-a'): time.sleep(.03)\n        body=b'GIF89a'")
    (fixture.RUN / "fixture/source.py").write_text(source)
    # 创建不同作者时头像身份也独立，避免素材提取器产生人为的身份冲突。
    wrapper = fixture.WRAPPER.replace("code=sys.argv[3]; sys.argv=['gallery-dl']+sys.argv[4:]; exec(code)", "code=sys.argv[3];args=sys.argv[4:];\n    if 'import yt_dlp' in code:\n        import os;os.execv('/fixture/bin/yt-dlp',['yt-dlp']+args)\n    sys.argv=['gallery-dl']+args;exec(code)")
    wrapper = wrapper.replace("code='avatar'", "code=self.url.rstrip('/').split('/')[-2]+'-avatar'")
    for name, content in (("python3", wrapper), ("gallery-dl", wrapper), ("yt-dlp", YOUTUBE)):
        path = fixture.RUN / "fixture/bin" / name
        path.write_text(content)
        path.chmod(0o755)
    args = ["docker", "run", "-d", "--rm", "--name", fixture.NAME, "--user", f"{os.getuid()}:{os.getgid()}",
            "-e", "INSTAGRAM_REQUEST_INTERVAL_SECONDS=0.02", "-e", "INSTAGRAM_429_COOLDOWN_SECONDS=0.04",
            "-p", f"127.0.0.1:{fixture.PORT}:8080", "-e", "PATH=/fixture/bin:/opt/engines/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"]
    for name in ("data", "downloads", "logs", "archive", "fixture"):
        args += ["-v", f"{fixture.RUN / name}:/{name}"]
    args += ["--entrypoint", "/bin/sh", fixture.IMAGE, "-c", "/opt/engines/bin/python3 /fixture/source.py & exec /app/server"]
    subprocess.check_output(args)


def check_waiting(sub):
    tasks = tasks_for(sub)
    assert len(tasks) == 1 and tasks[0]["status"] == "queued", tasks
    log = request(f"/subscriptions/{sub['id']}/log")["log"]
    assert "平台排队" in log, log


def run():
    setup()
    try:
        settings = wait(lambda: request("/settings"), bool)
        settings["debug"]["test_item_limit"] = 10
        settings["instagram_concurrent"] = settings["youtube_concurrent"] = 4
        settings["youtube"] = {"video": True, "format": "best", "fragments": 1}
        request("/settings", "PUT", settings)
        subs = {}
        for code in ("ig-a", "ig-b", "yt-a", "yt-b"):
            platform = "instagram" if code.startswith("ig") else "youtube"
            url = f"https://www.instagram.com/{code}/" if platform == "instagram" else f"https://www.youtube.com/@{code}"
            subs[code] = request("/subscriptions", "POST", {"platform": platform, "url": url, "display_name": code, "enabled": False})
        request(f"/subscriptions/{subs['ig-a']['id']}/scan?kind=full", "POST")
        wait(lambda: running_downloads(subs["ig-a"]), lambda ts: len(ts) == 2)
        # Instagram 有两个空闲下载线程时 B 也必须等待；YouTube A 可以独立启动。
        for code in ("ig-b", "yt-a", "yt-b"):
            request(f"/subscriptions/{subs[code]['id']}/scan?kind=full", "POST")
        wait(lambda: running_downloads(subs["yt-a"]), bool)
        check_waiting(subs["ig-b"])
        check_waiting(subs["yt-b"])
        print("PASS 两个平台各运行 A，B 排队；平台并发设为 4 且仍有空余线程", flush=True)
        # 可选停留供界面验收；最多一分钟，普通自动检查不等待。
        if os.environ.get("SMOKE_UI_HOLD") == "1":
            print("UI_HOLD " + str(fixture.RUN), flush=True)
            deadline = time.monotonic() + 60
            while not (fixture.RUN / "continue-ui").exists() and time.monotonic() < deadline:
                time.sleep(.1)
        # 清空所有进程内状态，恢复必须依赖数据库中的当前平台批次。
        subprocess.check_output(["docker", "restart", fixture.NAME])
        wait(lambda: running_downloads(subs["ig-a"]), lambda ts: len(ts) == 2)
        wait(lambda: running_downloads(subs["yt-a"]), bool)
        check_waiting(subs["ig-b"])
        check_waiting(subs["yt-b"])
        print("PASS 重启后继续两个 A，两个 B 保持排队", flush=True)
        (fixture.RUN / "data/release-yt-a").touch()
        wait(lambda: tasks_for(subs["yt-b"]), lambda ts: any(t["content_id"] and t["status"] == "completed" for t in ts))
        check_waiting(subs["ig-b"])
        print("PASS YouTube 完成并交接 B，不等待 Instagram A", flush=True)
        (fixture.RUN / "data/release-ig-a").touch()
        wait(lambda: tasks_for(subs["ig-b"]), lambda ts: len(ts) == 3 and all(t["status"] == "completed" for t in ts))
        first, second = tasks_for(subs["ig-a"]), tasks_for(subs["ig-b"])
        assert all(t["status"] == "completed" for t in first), first
        # 以实际持久化时间验证交接发生在 A 的最后一条下载结束之后。
        ended = max(t["finished_at"] for t in first)
        started = next(t["started_at"] for t in second if not t["content_id"])
        assert started >= ended, (started, ended)
        report = {"platform_concurrency": 4, "same_platform_serial": True, "cross_platform_independent": True,
                  "restart_resumes_current_author": True, "instagram_a_finished_at": ended, "instagram_b_started_at": started}
        (fixture.RUN / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2))
        print("PASS Instagram A 下载全部结束后才开始 B；验收结果：" + str(fixture.RUN / "report.json"), flush=True)
    except Exception:
        print(subprocess.check_output(["docker", "logs", fixture.NAME], text=True))
        raise
    finally:
        subprocess.run(["docker", "rm", "-f", fixture.NAME], stdout=subprocess.DEVNULL, check=False)


if __name__ == "__main__":
    run()
