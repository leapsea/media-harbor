"""验证本地构建的镜像；素材在测试容器内生成，不访问真实媒体平台。"""
import json
import os
from pathlib import Path
import subprocess
import time
import urllib.error
import urllib.request

RELEASE = json.loads((Path(__file__).resolve().parents[1] / "internal/config/release.json").read_text())
APP_VERSION = os.environ.get("APP_VERSION", RELEASE["version"])
IMAGE = os.environ.get("SMOKE_IMAGE", "media-harbor:" + APP_VERSION)
ROOT = Path(__file__).resolve().parents[1]
RUN_ID = str(time.time_ns())
RUN_ROOT = ROOT / ".test-runtime" / "container" / RUN_ID
PORT = int(os.environ.get("SMOKE_PORT", "18882"))
PLATFORM = os.environ.get("SMOKE_PLATFORM", "")
GALLERY_VERSION = os.environ.get("GALLERY_DL_VERSION", RELEASE["gallery_dl"])
YTDLP_VERSION = os.environ.get("YTDLP_VERSION", RELEASE["yt_dlp"])
FFMPEG_VERSION = os.environ.get("FFMPEG_VERSION", RELEASE["ffmpeg"]).split("-")[0]
BASE = f"http://127.0.0.1:{PORT}/api"


def docker(*args):
    return subprocess.check_output(["docker", *args], text=True).strip()


def request(path, method="GET", data=None):
    body = None if data is None else json.dumps(data).encode()
    req = urllib.request.Request(BASE + path, body, {"Content-Type": "application/json"}, method=method)
    with urllib.request.urlopen(req, timeout=10) as response:
        return json.load(response)


def wait_until(read, check, timeout=30):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            value = read()
            if check(value):
                return value
        except (urllib.error.URLError, ConnectionError):
            pass
        time.sleep(0.25)
    raise AssertionError("等待状态超时")


def run_case(name, mounted, readonly_download=False):
    container = f"media-harbor-smoke-{RUN_ID}-{name}"
    source = None
    args = ["run", "-d", "--name", container, "-p", f"127.0.0.1:{PORT}:8080"]
    if PLATFORM:
        args += ["--platform", PLATFORM]
    if mounted:
        # 仅挂载项目内专属测试目录，避免接触用户已有媒体。
        for directory in ("data", "logs", "downloads", "archive"):
            host = RUN_ROOT / name / directory
            host.mkdir(parents=True, exist_ok=True)
            host.chmod(0o777)
            mode = ":ro" if readonly_download and directory == "downloads" else ""
            args += ["-v", f"{host}:/{directory}{mode}"]
    args.append(IMAGE)
    docker(*args)
    try:
        dashboard = wait_until(lambda: request("/dashboard"), lambda d: d["version"] == APP_VERSION)
        dirs = dashboard["directories"]
        assert all(d["uid"] == 1000 and d["gid"] == 1000 for d in dirs), dirs
        expected = "mounted" if mounted else "unmounted"
        assert all(d["mounted"] == expected for d in dirs), dirs
        assert dirs[1]["writable"] != readonly_download, dirs
        usage = request("/storage")
        assert usage["logs"]["path"] == "/logs", usage
        versions = request("/engines")["versions"]
        assert versions["gallery-dl"] == GALLERY_VERSION, versions
        assert tuple(map(int, versions["yt-dlp"].split("."))) == tuple(map(int, YTDLP_VERSION.split("."))), versions
        assert versions["ffmpeg"].startswith("ffmpeg version " + FFMPEG_VERSION + " "), versions

        # 源服务器与应用都运行在同一个临时容器，仅提供一秒的测试视频。
        docker("exec", container, "mkdir", "-p", "/data/smoke-source")
        docker("exec", container, "ffmpeg", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=blue:s=160x120:d=1", "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-shortest", "-c:v", "mpeg4", "-c:a", "aac", "/data/smoke-source/fixture.mp4")
        source = subprocess.Popen(["docker", "exec", container, "python3", "-m", "http.server", "8001", "--bind", "127.0.0.1", "--directory", "/data/smoke-source"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        # 循环确认源服务器，不使用外网或真实平台账户。
        for _ in range(30):
            try:
                docker("exec", container, "python3", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8001/fixture.mp4').close()")
                break
            except subprocess.CalledProcessError:
                time.sleep(0.1)
        settings = request("/settings")
        options = settings["youtube"]
        options.update(format="b", extra_audio=True, extract_audio=True, metadata=True, comments=False, subtitles=False, auto_subtitles=False, thumbnail=False)
        payload = dict(platform="youtube", url="http://127.0.0.1:8001/fixture.mp4", display_name="fixture", enabled=False, schedule="", template="", cookie=False, archive=mounted and not readonly_download, youtube=options)
        sub = request("/subscriptions", "POST", payload)
        request(f"/subscriptions/{sub['id']}/scan?kind=full", "POST")
        wait_until(lambda: request("/tasks"), lambda ts: any(t["kind"] == "full" and t["status"] == "completed" for t in ts))
        if readonly_download:
            tasks = request("/tasks")
            assert any(t["kind"] == "download" and t["status"] == "queued" for t in tasks), tasks
            assert not any(t["kind"] == "download" and t["status"] == "running" for t in tasks), tasks
        else:
            items = wait_until(lambda: request("/contents"), lambda cs: len(cs) == 1 and cs[0]["status"] == "completed" and (not mounted or cs[0]["archived"]))
            paths = items[0]["paths"]
            for suffix in (".mp4", ".audio.m4a", ".extracted.m4a", ".info.json"):
                assert any(p.endswith(suffix) for p in paths), paths
            assert all(p.startswith("/archive/" if mounted else "/downloads/") for p in paths), paths
            request(f"/subscriptions/{sub['id']}/scan", "POST")
            wait_until(lambda: request("/tasks"), lambda ts: len(ts) == 3 and all(t["status"] == "completed" for t in ts))
            assert len(request("/contents")) == 1
            if mounted:
                # 同一个镜像重启后，数据库和挂载媒体仍然存在。
                docker("restart", container)
                wait_until(lambda: request("/subscriptions"), lambda ss: len(ss) == 1)
                persisted = request("/contents")[0]
                assert persisted["paths"] == paths and persisted["archived"]
                for path in paths:
                    docker("exec", container, "test", "-f", path)
        print(f"PASS {name}: UID/GID、目录状态、引擎版本、队列与文件流程")
    except Exception:
        print(docker("logs", container))
        raise
    finally:
        if source is not None:
            source.terminate()
            source.wait(timeout=10)
        # 测试容器不保留；项目内测试挂载数据保留用于复核。
        docker("rm", "-f", container)


if __name__ == "__main__":
    run_case("unmounted", False)
    run_case("mounted", True)
    run_case("readonly-download", True, True)
